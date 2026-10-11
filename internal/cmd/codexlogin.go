package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/codexauth"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/codex"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/keyring"
	"github.com/smeltdev/smelt/internal/ui"
)

// Provider names for Codex credentials.
const providerCodex = "codex"

// codexHost is the codex CLI on this machine. A var so tests can point it at a
// fake binary.
var codexHost = codexauth.Host{}

const hintCodexLogin = "Run 'smelt auth login codex' when you're ready."

// codexOnPath reports whether the codex CLI is on this machine.
func codexOnPath() bool {
	_, err := exec.LookPath(codexHost.BinaryPath())
	return err == nil
}

// codexLogin signs in to Codex in a private CODEX_HOME and keeps the resulting
// auth.json in the keychain. The user's ~/.codex is never touched. A running
// daemon is stopped so that its next launch sees the new login.
func codexLogin(ctx context.Context, streams iostream.Streams, deviceAuth bool) error {
	if !isInteractive() {
		return newUserError("Cannot log in to Codex without an interactive terminal.").
			withCode("auth.codex_login_required").
			withSuggestion("Run 'smelt auth login codex' in a terminal, or set " + codexEnvHint + ".").
			withExitCode(ExitAuthError).
			wrap(ui.ErrNoTTY)
	}
	if !codexOnPath() {
		return &userError{
			msg:        "The Codex CLI is not installed on this machine, so smelt cannot log in to it.",
			suggestion: "Install it from https://github.com/openai/codex, or store an API key with 'smelt auth set codex'.",
			exitCode:   ExitAuthError,
			errMsg:     "codex not installed",
		}
	}
	streams.Println("")
	streams.Println(ui.Bold("Smelt CLI - Codex Login"))
	streams.Println(ui.Dim("Signing in with ChatGPT in a private directory; ~/.codex is not touched."))
	streams.Println("")

	raw, err := codexHost.Login(ctx, codexauth.LoginOptions{
		DeviceAuth: deviceAuth, Stdin: os.Stdin, Stdout: streams.Out, Stderr: streams.Err,
	})
	if err != nil {
		return &userError{msg: "Codex login failed.", suggestion: hintCodexLogin, exitCode: ExitAuthError, err: err}
	}
	if err := codexauth.StoreChatGPT(raw); err != nil {
		return &userError{msg: msgCouldNotSaveCredentials, err: err}
	}
	stopDaemonForCredentialChange()
	printSaved(streams, "Codex login", false)
	if exp, err := codexauth.Expiry(raw); err == nil {
		streams.Println(ui.Dim("It lasts until " + exp.Format("2006-01-02") + "; then run 'smelt refresh codex'."))
	}
	return nil
}

// codexEnvHint names the variables that stand in for a stored Codex login.
const codexEnvHint = "SMELT_CODEX_AUTH_JSON (the base64 of auth.json) or CODEX_API_KEY"

// authSetCodex stores an OpenAI API key for Codex.
func authSetCodex(streams iostream.Streams, force bool) error {
	streams.Println("")
	streams.Println(ui.Bold("Smelt CLI - Codex API Key Setup"))
	streams.Println("")
	streams.Println("Enter an OpenAI API key. Use a key on a shared or unattended runner; on")
	streams.Println("your own machine, 'smelt auth login codex' uses your ChatGPT subscription.")
	printSaveHint(streams, "Key", false)
	streams.Println("")
	if _, err := keyring.Get(keyring.ServiceCodexAPIKey); err == nil && !force {
		if nonInteractive() {
			return errNoForce("replace the Codex API key")
		}
		replace, err := ui.Confirm("A Codex API key is already stored. Replace it?", false)
		if errors.Is(err, ui.ErrNoTTY) {
			return errNoForce("replace the Codex API key")
		}
		if err != nil || !replace {
			streams.Println("Keeping existing API key.")
			return nil
		}
	}
	key, err := ui.PromptHidden("API Key")
	if errors.Is(err, ui.ErrNoTTY) {
		return newUserError("Cannot prompt for a Codex API key without an interactive terminal.").
			withCode("auth.codex_key_required").
			withSuggestion("Set " + codex.EnvAPIKey + " to configure it non-interactively.").
			withExitCode(ExitAuthError).
			wrap(err)
	}
	if err != nil {
		return nil
	}
	if err := codexauth.StoreAPIKey(key); err != nil {
		return &userError{msg: msgCouldNotSaveCredentials, err: err}
	}
	stopDaemonForCredentialChange()
	printSaved(streams, "Codex API key", false)
	return nil
}

// authRemoveCodex deletes the stored Codex credentials.
func authRemoveCodex(streams iostream.Streams, force bool) error {
	_, chatErr := keyring.Get(keyring.ServiceCodexChatGPT)
	_, keyErr := keyring.Get(keyring.ServiceCodexAPIKey)
	if chatErr != nil && keyErr != nil {
		streams.Println(ui.Warning("No Codex credential stored."))
		return nil
	}
	if !force {
		if nonInteractive() {
			return errNoForce("remove the Codex credential")
		}
		confirmed, err := ui.Confirm("Remove the stored Codex login and API key?", false)
		if errors.Is(err, ui.ErrNoTTY) {
			return errNoForce("remove the Codex credential")
		}
		if err != nil || !confirmed {
			streams.Println("Keeping stored credentials.")
			return nil
		}
	}
	if err := codexauth.Remove(); err != nil {
		return &userError{msg: "Could not remove the stored Codex credential.", err: err}
	}
	stopDaemonForCredentialChange()
	streams.Println(ui.Success("Codex credential removed."))
	return nil
}

// refreshCodex logs in to Codex again and replaces the stored login: the
// one-minute browser login that `smelt factory build` asks for when the stored
// access token is about to expire.
func refreshCodex(ctx context.Context, streams iostream.Streams, deviceAuth bool) error {
	if l, err := codexauth.Find(); err == nil && l.Kind == codexauth.KindChatGPT {
		if exp, err := codexauth.Expiry(l.Auth); err == nil {
			streams.Println("Current Codex login expires " + exp.Format(time.RFC3339) + ".")
		}
	}
	return codexLogin(ctx, streams, deviceAuth)
}

// codexStatus prints the Codex section of `smelt auth status`: the kind of
// credential, the account's plan and whether it still works. Nothing secret is
// printed.
func codexStatus(ctx context.Context, streams iostream.Streams) {
	streams.Println(ui.Bold("Codex"))
	l, err := codexauth.Find()
	if err != nil {
		streams.Println("  Not set")
		streams.Println(ui.Dim("  Run `smelt auth login codex` (ChatGPT) or `smelt auth set codex` (API key)."))
		streams.Println("")
		return
	}
	if l.Kind == codexauth.KindAPIKey {
		streams.Println("  Kind:   API key")
		streams.Println("")
		return
	}
	streams.Println("  Kind:   ChatGPT login")
	if exp, err := codexauth.Expiry(l.Auth); err == nil {
		streams.Printf("  Access token expires: %s\n", exp.Format("2006-01-02 15:04"))
	}
	if codexOnPath() {
		acct := codexHost.Probe(ctx, l.Auth, time.Now())
		streams.Printf("  Plan:   %s\n", orUnknown(acct.Plan))
		streams.Printf("  Works:  %s\n", acct.Auth)
	}
	streams.Println("")
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// codexAuthStep is the init step that offers to log in to Codex. Never fatal.
func codexAuthStep(ctx context.Context, streams iostream.Streams, confirm confirmFunc) {
	if _, err := codexauth.Find(); err == nil {
		streams.ErrPrintln("Codex credential: found")
		return
	}
	if !isInteractive() {
		streams.ErrPrintln(ui.Dim("No Codex credential. " + hintCodexLogin))
		return
	}
	ok, err := confirm("Log in to Codex with your ChatGPT account now?", true)
	if err != nil || !ok {
		streams.ErrPrintln(ui.Dim(hintCodexLogin))
		return
	}
	if err := codexLogin(ctx, streams, false); err != nil {
		warnUserError(streams, "Codex login failed: ", err)
		streams.ErrPrintln(ui.Dim(hintCodexLogin))
	}
}

// codexDaemonAuth is the daemon's Codex setup: a login is looked for once, at
// startup, but a ChatGPT login is checked and renewed at the start of each
// run, since a daemon can outlive a login's access token.
func codexDaemonAuth(host codexauth.Host) (refresh func(context.Context) (harness.Credential, error), authError string) {
	if _, err := codexauth.Find(); err != nil {
		return nil, "no Codex credential — factory runs with codex unavailable (run: smelt auth login codex)"
	}
	return func(ctx context.Context) (harness.Credential, error) {
		cred, err := codexauth.ForRun(ctx, host, time.Now())
		if err != nil {
			return harness.Credential{}, fmt.Errorf("codex: %s", strings.TrimPrefix(err.Error(), "codex: "))
		}
		return cred, nil
	}, ""
}
