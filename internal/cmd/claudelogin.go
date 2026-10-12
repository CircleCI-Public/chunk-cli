package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/smeltdev/smelt/internal/authprompt"
	"github.com/smeltdev/smelt/internal/claudeauth"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/smeltd"
	"github.com/smeltdev/smelt/internal/ui"
)

// Replaceable from tests so none of them runs the real claude binary.
var (
	mintClaudeToken = claudeauth.SetupToken
	// stopDaemonForCredentialChange reports whether a running daemon was stopped.
	stopDaemonForCredentialChange = smeltd.StopForCredentialChange
)

const hintClaudeLogin = "Run 'smelt auth refresh' when you're ready."

// mintAndStoreClaudeToken runs `claude setup-token` and stores the result in the
// keychain, then stops a running daemon so its next launch reads the new token.
// saved is false with a nil error when the user cancelled; daemonRestarted
// reports whether a running daemon was stopped to pick the token up.
func mintAndStoreClaudeToken(ctx context.Context, io iostream.Streams, baseURL string) (saved, daemonRestarted bool, err error) {
	token, err := mintClaudeToken(ctx, io, ui.PromptHidden)
	switch {
	case errors.Is(err, claudeauth.ErrNotInstalled):
		return false, false, &userError{
			msg:        "Claude Code is not installed locally, so a token cannot be minted.",
			suggestion: "Install it from https://claude.com/claude-code, or set " + config.EnvClaudeOAuthToken + " instead.",
			exitCode:   ExitAuthError,
			err:        err,
		}
	case errors.Is(err, ui.ErrNoTTY):
		return false, false, newUserError("Cannot mint a Claude token without an interactive terminal.").
			withCode("auth.claude_token_required").
			withSuggestion("Run 'claude setup-token' yourself and set " + config.EnvClaudeOAuthToken + ".").
			withExitCode(ExitAuthError).
			wrap(err)
	case errors.Is(err, ui.ErrCancelled):
		return false, false, nil
	case err != nil:
		return false, false, &userError{msg: "Could not mint a Claude token.", err: err}
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return false, false, &userError{
			msg:        msgEmptyToken,
			suggestion: "Run 'claude setup-token' and check it prints a token.",
			errMsg:     "empty claude token",
		}
	}

	// Not validated before storing, unlike the API key: the count-tokens probe
	// authprompt uses sends x-api-key, which a bearer token is not. A review
	// that Anthropic refuses clears the token and says so.
	if err := authprompt.SaveClaudeOAuthToken(token, baseURL); err != nil {
		return false, false, &userError{msg: msgCouldNotSaveCredentials, err: err}
	}
	// The daemon reads its credential once at startup.
	return true, stopDaemonForCredentialChange(), nil
}

type initAuthAction int

const (
	reportExisting initAuthAction = iota
	promptLogin
	hintNonInteractive
	hintInsecure
	skipped
)

// initAuthState is what the Claude credential step of `smelt init` decides on.
type initAuthState struct {
	HasCredential   bool
	Interactive     bool
	InsecureStorage bool
	Skip            bool
}

// decideInitAuth picks what the Claude credential step of `smelt init` does.
func decideInitAuth(s initAuthState) initAuthAction {
	switch {
	case s.Skip:
		return skipped
	case s.HasCredential:
		return reportExisting
	case s.InsecureStorage:
		return hintInsecure
	case !s.Interactive:
		return hintNonInteractive
	}
	return promptLogin
}

// describeCredential names the kind and source of the credential in effect.
func describeCredential(rc config.ResolvedConfig) (string, bool) {
	cred, source, err := reviewCredential(rc)
	if err != nil {
		return "", false
	}
	kind := "subscription token"
	if cred.EnvVar == config.EnvAnthropicAPIKey {
		kind = "API key"
	}
	return fmt.Sprintf("%s (%s)", kind, source), true
}

// isInteractive is a var so tests can run interactive paths without a TTY.
var isInteractive = func() bool {
	return !nonInteractive() && ui.RequireStdoutTTY() == nil
}

// agentAuthStep makes sure the user has a credential for the project's coding
// agent: the Codex branch for codex, the Claude branch otherwise. Never fatal.
func agentAuthStep(ctx context.Context, streams iostream.Streams, rc config.ResolvedConfig, cfg *config.ProjectConfig, insecureStorage, skip bool, confirm confirmFunc) {
	if cfg != nil && cfg.Factory != nil && cfg.Factory.Harness == config.HarnessCodex {
		if !skip {
			codexAuthStep(ctx, streams, confirm)
		}
		return
	}
	claudeAuthStep(ctx, streams, rc, insecureStorage, skip, confirm)
}

// claudeAuthStep makes sure the user has a Claude credential. Never fatal.
func claudeAuthStep(ctx context.Context, streams iostream.Streams, rc config.ResolvedConfig, insecureStorage, skip bool, confirm confirmFunc) {
	desc, has := describeCredential(rc)
	switch decideInitAuth(initAuthState{
		HasCredential:   has,
		Interactive:     isInteractive(),
		InsecureStorage: insecureStorage,
		Skip:            skip,
	}) {
	case skipped:
	case reportExisting:
		streams.ErrPrintln("Claude credential: " + desc)
	case hintInsecure:
		streams.ErrPrintln(ui.Dim("No Claude credential. A subscription token needs the keychain; set " + config.EnvClaudeOAuthToken + " instead."))
	case hintNonInteractive:
		streams.ErrPrintln(ui.Dim("No Claude credential. " + hintClaudeLogin))
	case promptLogin:
		ok, err := confirm("Log in with your Claude subscription now? (runs 'claude setup-token')", true)
		if err != nil || !ok {
			streams.ErrPrintln(ui.Dim(hintClaudeLogin))
			return
		}
		saved, _, err := mintAndStoreClaudeToken(ctx, streams, rc.AnthropicBaseURL)
		if err != nil {
			warnUserError(streams, "Claude login failed: ", err)
			streams.ErrPrintln(ui.Dim(hintClaudeLogin))
			return
		}
		if !saved {
			streams.ErrPrintln(ui.Dim(hintClaudeLogin))
			return
		}
		printSaved(streams, "Claude subscription token", false)
	}
}

// Refresh providers.
const providerClaude = "claude"

var refreshProviders = []string{providerClaude, providerAnthropicOAuth, providerCodex}

func parseRefreshProvider(arg string) (string, error) {
	switch arg {
	case "", providerClaude, providerAnthropicOAuth:
		return providerClaude, nil
	case providerCodex:
		return providerCodex, nil
	}
	return "", &userError{
		msg:    fmt.Sprintf("Unknown provider %q.", arg),
		detail: "Valid providers: " + strings.Join(refreshProviders, ", ") + ".",
		errMsg: fmt.Sprintf("unknown provider %q", arg),
	}
}

func newAuthRefreshCmd() *cobra.Command {
	return newRefreshCmd("refresh [provider]")
}

func newRefreshCmd(use string) *cobra.Command {
	return &cobra.Command{
		Use:       use,
		Short:     "Log in again and replace the stored credential",
		Long:      "Re-mint the Claude subscription token with 'claude setup-token' and replace the stored one. A running smelt daemon is restarted to pick it up.\n\nWith codex, sign in to Codex again with your ChatGPT account and replace the stored login (a one-minute browser login).",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: refreshProviders,
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			provider, err := parseRefreshProvider(arg)
			if err != nil {
				return err
			}
			io := iostream.FromCmd(cmd)
			insecureStorage := insecureStorageFlag(cmd)
			rc, _ := config.Resolve("", "", insecureStorage)
			if provider == providerCodex {
				return refreshCodex(cmd.Context(), io, codexLoginOptions{insecureStorage: insecureStorage})
			}
			return refreshClaude(cmd.Context(), io, rc, insecureStorage)
		},
	}
}

func refreshClaude(ctx context.Context, io iostream.Streams, rc config.ResolvedConfig, insecureStorage bool) error {
	if insecureStorage {
		return &userError{
			msg:        "A Claude subscription token can only be stored in the system keychain.",
			suggestion: "Drop --insecure-storage, or set " + config.EnvClaudeOAuthToken + " instead.",
			errMsg:     "oauth token needs the keychain",
		}
	}
	if !isInteractive() {
		return newUserError("Cannot mint a Claude token without an interactive terminal.").
			withCode("auth.claude_token_required").
			withSuggestion("Run 'claude setup-token' yourself and set " + config.EnvClaudeOAuthToken + ".").
			withExitCode(ExitAuthError).
			wrap(ui.ErrNoTTY)
	}

	if desc, ok := describeCredential(rc); ok {
		io.Println("Current Claude credential: " + desc)
	} else {
		io.Println("No Claude credential is currently in effect.")
	}
	if strings.HasPrefix(rc.ClaudeOAuthTokenSource, "Environment") {
		io.Println(ui.Warning(config.EnvClaudeOAuthToken + " is set and overrides the keychain; refreshing the stored token won't change what smelt uses until it is unset."))
	}
	if rc.AnthropicAPIKey != "" {
		io.Println(ui.Warning(config.EnvAnthropicAPIKey + " / the stored API key takes precedence over the subscription token."))
	}

	saved, restarted, err := mintAndStoreClaudeToken(ctx, io, rc.AnthropicBaseURL)
	if err != nil || !saved {
		return err
	}
	printSaved(io, "Claude subscription token", false)
	if restarted {
		io.Println(ui.Dim("A running smelt daemon was restarted to pick up the new token."))
	}
	return nil
}
