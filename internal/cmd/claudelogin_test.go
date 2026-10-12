package cmd

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	gokeyring "github.com/zalando/go-keyring"
	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/claudeauth"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/keyring"
	"github.com/smeltdev/smelt/internal/ui"
)

func TestDecideInitAuth(t *testing.T) {
	for _, has := range []bool{false, true} {
		for _, interactive := range []bool{false, true} {
			for _, insecure := range []bool{false, true} {
				for _, skip := range []bool{false, true} {
					got := decideInitAuth(initAuthState{
						HasCredential: has, Interactive: interactive, InsecureStorage: insecure, Skip: skip,
					})
					switch {
					case skip:
						assert.Equal(t, got, skipped)
					case has:
						assert.Equal(t, got, reportExisting)
					case insecure:
						assert.Equal(t, got, hintInsecure)
					case !interactive:
						assert.Equal(t, got, hintNonInteractive)
					default:
						assert.Equal(t, got, promptLogin)
					}
					if has || !interactive {
						assert.Assert(t, got != promptLogin)
					}
				}
			}
		}
	}
}

func TestParseRefreshProvider(t *testing.T) {
	for _, in := range []string{"", "claude", "anthropic-oauth"} {
		p, err := parseRefreshProvider(in)
		assert.NilError(t, err)
		assert.Equal(t, p, providerClaude)
	}
	p, err := parseRefreshProvider("codex")
	assert.NilError(t, err)
	assert.Equal(t, p, providerCodex)
	_, err = parseRefreshProvider("gemini")
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.Detail(), "claude, anthropic-oauth, codex"), ue.Detail())
}

func stubMint(t *testing.T, token string, err error) *int {
	t.Helper()
	gokeyring.MockInit()
	stops := 0
	origMint, origStop := mintClaudeToken, stopDaemonForCredentialChange
	mintClaudeToken = func(context.Context, iostream.Streams, func(string) (string, error)) (string, error) {
		return token, err
	}
	stopDaemonForCredentialChange = func() bool { stops++; return true }
	t.Cleanup(func() { mintClaudeToken, stopDaemonForCredentialChange = origMint, origStop })
	return &stops
}

func TestMintAndStoreClaudeToken(t *testing.T) {
	const secret = "sk-ant-oat01-supersecret"
	var out, errb bytes.Buffer
	streams := iostream.Streams{Out: &out, Err: &errb}

	t.Run("saves and stops daemon", func(t *testing.T) {
		isolateConfig(t)
		stops := stubMint(t, "  "+secret+"\n", nil)
		saved, restarted, err := mintAndStoreClaudeToken(context.Background(), streams, "https://example.invalid")
		assert.NilError(t, err)
		assert.Assert(t, saved)
		assert.Assert(t, restarted, "the stub reports a stopped daemon")
		assert.Equal(t, *stops, 1)
		got, err := keyring.Get(keyring.ServiceAnthropicOAuth("https://example.invalid"))
		assert.NilError(t, err)
		assert.Equal(t, got, secret)
		assert.Assert(t, !strings.Contains(out.String()+errb.String(), secret))
	})

	for name, mintErr := range map[string]error{
		"not installed": claudeauth.ErrNotInstalled,
		"no tty":        ui.ErrNoTTY,
		"cancelled":     ui.ErrCancelled,
		"empty":         nil,
	} {
		t.Run(name, func(t *testing.T) {
			isolateConfig(t)
			token := ""
			stops := stubMint(t, " \n\t", nil)
			if mintErr != nil {
				stops = stubMint(t, token, mintErr)
			}
			saved, restarted, err := mintAndStoreClaudeToken(context.Background(), streams, "https://example.invalid")
			assert.Assert(t, !saved)
			assert.Assert(t, !restarted)
			assert.Equal(t, *stops, 0)
			if name == "cancelled" {
				assert.NilError(t, err)
			} else {
				var ue *userError
				assert.Assert(t, errors.As(err, &ue))
			}
			_, getErr := keyring.Get(keyring.ServiceAnthropicOAuth("https://example.invalid"))
			assert.Assert(t, getErr != nil)
		})
	}
}

func TestClaudeAuthStep_ReportsExistingMasked(t *testing.T) {
	var errb bytes.Buffer
	streams := iostream.Streams{Out: &bytes.Buffer{}, Err: &errb}
	rc := config.ResolvedConfig{ClaudeOAuthToken: "sk-ant-oat01-supersecret", ClaudeOAuthTokenSource: "Keychain"}
	claudeAuthStep(context.Background(), streams, rc, false, false, func(string, bool) (bool, error) {
		t.Fatal("must not prompt when a credential resolves")
		return false, nil
	})
	assert.Assert(t, strings.Contains(errb.String(), "Claude credential: subscription token"))
	assert.Assert(t, !strings.Contains(errb.String(), "supersecret"))
}

func stubInteractive(t *testing.T, v bool) {
	t.Helper()
	orig := isInteractive
	isInteractive = func() bool { return v }
	t.Cleanup(func() { isInteractive = orig })
}

func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newTestRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestRefreshCommands_RefuseWithoutTTYOrWithInsecureStorage(t *testing.T) {
	for _, args := range [][]string{{"refresh"}, {"auth", "refresh", "claude"}} {
		isolateConfig(t)
		stops := stubMint(t, "tok", nil)
		stubInteractive(t, false)
		_, err := runRoot(t, args...)
		var ue *userError
		assert.Assert(t, errors.As(err, &ue), "%v", args)
		assert.Assert(t, strings.Contains(ue.UserMessage(), "interactive terminal") ||
			strings.Contains(ue.UserMessage(), "keychain"), ue.UserMessage())
		assert.Equal(t, *stops, 0)
	}
}

func TestRefreshClaude_WarnsAndReportsRestart(t *testing.T) {
	isolateConfig(t)
	stubInteractive(t, true)
	stops := stubMint(t, "sk-ant-oat01-newsecret", nil)
	rc := config.ResolvedConfig{
		AnthropicAPIKey:        "sk-ant-api-secretkey",
		AnthropicAPIKeySource:  "Keychain",
		ClaudeOAuthToken:       "sk-ant-oat01-oldsecret",
		ClaudeOAuthTokenSource: "Environment variable (" + config.EnvClaudeOAuthToken + ")",
		AnthropicBaseURL:       "https://example.invalid",
	}
	var out bytes.Buffer
	err := refreshClaude(context.Background(), iostream.Streams{Out: &out, Err: &out}, rc, false)
	assert.NilError(t, err)
	assert.Equal(t, *stops, 1)
	got := out.String()
	assert.Assert(t, strings.Contains(got, "overrides the keychain"), got)
	assert.Assert(t, strings.Contains(got, "takes precedence"), got)
	assert.Assert(t, strings.Contains(got, "daemon was restarted"), got)
	for _, secret := range []string{"newsecret", "oldsecret", "secretkey"} {
		assert.Assert(t, !strings.Contains(got, secret), got)
	}
}

func TestRefreshProviderRejectedViaCommand(t *testing.T) {
	isolateConfig(t)
	_, err := runRoot(t, "auth", "refresh", "gemini")
	assert.ErrorContains(t, err, "gemini")
}

func TestAuthSetAndRemoveAnthropicOAuth_StopDaemon(t *testing.T) {
	isolateConfig(t)
	stops := stubMint(t, "sk-ant-oat01-tok", nil)
	var out bytes.Buffer
	streams := iostream.Streams{Out: &out, Err: &out}
	const base = "https://example.invalid"

	assert.NilError(t, authSetAnthropicOAuth(context.Background(), streams, base, false, true, false))
	assert.Equal(t, *stops, 1)
	_, err := keyring.Get(keyring.ServiceAnthropicOAuth(base))
	assert.NilError(t, err)

	assert.NilError(t, authRemoveAnthropicOAuth(streams, base, false, true))
	assert.Equal(t, *stops, 2)
	assert.Assert(t, !strings.Contains(out.String(), "oat01-tok"))
}

func TestInitEarlyExit_RunsClaudeAuthStep(t *testing.T) {
	setup := func(t *testing.T) string {
		isolateConfig(t)
		t.Setenv(config.EnvClaudeOAuthToken, "")
		t.Setenv(config.EnvAnthropicAPIKey, "")
		dir := t.TempDir()
		assert.NilError(t, exec.Command("git", "-C", dir, "init", "-q").Run())
		assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
			Commands: []config.Command{{Name: "test", Run: "true"}},
		}))
		return dir
	}

	t.Run("hint without credential", func(t *testing.T) {
		dir := setup(t)
		out, err := runRoot(t, "init", "--project-dir", dir)
		assert.NilError(t, err)
		assert.Assert(t, strings.Contains(out, "Config already exists"), out)
		assert.Assert(t, strings.Contains(out, "No Claude credential"), out)
	})
	t.Run("reports existing credential", func(t *testing.T) {
		dir := setup(t)
		t.Setenv(config.EnvClaudeOAuthToken, "sk-ant-oat01-supersecret")
		out, err := runRoot(t, "init", "--project-dir", dir)
		assert.NilError(t, err)
		assert.Assert(t, strings.Contains(out, "Claude credential: subscription token"), out)
		assert.Assert(t, !strings.Contains(out, "supersecret"), out)
	})
	t.Run("skip-auth", func(t *testing.T) {
		dir := setup(t)
		out, err := runRoot(t, "init", "--project-dir", dir, "--skip-auth")
		assert.NilError(t, err)
		assert.Assert(t, !strings.Contains(out, "Claude credential"), out)
		assert.Assert(t, !strings.Contains(out, "No Claude credential"), out)
	})
}

func TestInitNormalPath_RunsClaudeAuthStepBeforeSummary(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvClaudeOAuthToken, "sk-ant-oat01-supersecret")
	t.Setenv(config.EnvAnthropicAPIKey, "")
	dir := t.TempDir()
	assert.NilError(t, exec.Command("git", "-C", dir, "init", "-q").Run())

	out, err := runRoot(t, "init", "--project-dir", dir, "--skip-validate", "--skip-hooks",
		"--skip-completions", "--skip-skills", "--skip-org-id")
	assert.NilError(t, err)
	cred := strings.Index(out, "Claude credential: subscription token")
	done := strings.Index(out, "Project initialized")
	assert.Assert(t, cred >= 0 && done > cred, out)
	assert.Assert(t, !strings.Contains(out, "supersecret"), out)
}

func TestClaudeAuthStep_PromptLogin(t *testing.T) {
	const base = "https://example.invalid"
	rc := config.ResolvedConfig{AnthropicBaseURL: base}
	answer := func(v bool) confirmFunc {
		return func(string, bool) (bool, error) { return v, nil }
	}
	run := func(t *testing.T, confirm confirmFunc, token string, mintErr error) (string, int, bool) {
		t.Helper()
		isolateConfig(t)
		stubInteractive(t, true)
		stops := stubMint(t, token, mintErr)
		var out bytes.Buffer
		claudeAuthStep(context.Background(), iostream.Streams{Out: &out, Err: &out}, rc, false, false, confirm)
		_, getErr := keyring.Get(keyring.ServiceAnthropicOAuth(base))
		return out.String(), *stops, getErr == nil
	}

	t.Run("yes logs in", func(t *testing.T) {
		out, stops, stored := run(t, answer(true), "sk-ant-oat01-secret", nil)
		assert.Assert(t, stored)
		assert.Equal(t, stops, 1)
		assert.Assert(t, !strings.Contains(out, "oat01-secret"), out)
	})
	t.Run("no prints hint", func(t *testing.T) {
		out, stops, stored := run(t, answer(false), "sk-ant-oat01-secret", nil)
		assert.Assert(t, !stored)
		assert.Equal(t, stops, 0)
		assert.Assert(t, strings.Contains(out, hintClaudeLogin), out)
	})
	t.Run("mint error is a warning", func(t *testing.T) {
		out, stops, stored := run(t, answer(true), "", claudeauth.ErrNotInstalled)
		assert.Assert(t, !stored)
		assert.Equal(t, stops, 0)
		assert.Assert(t, strings.Contains(out, "not installed"), out)
		assert.Assert(t, strings.Contains(out, hintClaudeLogin), out)
	})
}
