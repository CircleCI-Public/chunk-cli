package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvXDGConfigHome, filepath.Join(home, ".config"))
	t.Setenv(config.EnvChunkSessionID, "")
	t.Setenv(session.EnvClaudeSessionID, "")
	// Keychain service keys are derived from these base URLs, which otherwise
	// default to production. Point them at an unroutable host so a test that
	// reaches the keychain cannot read the developer's own credentials. Tests
	// needing a real endpoint override these after calling isolateConfig.
	t.Setenv(config.EnvCircleCIBaseURL, unroutableBaseURL)
	t.Setenv(config.EnvAnthropicBaseURL, unroutableBaseURL)
	t.Setenv(config.EnvGitHubAPIURL, unroutableBaseURL)
}

// unroutableBaseURL is in the RFC 5737 TEST-NET-1 range: never a real service,
// and never a keychain service key holding a real credential.
const unroutableBaseURL = "http://192.0.2.1"

// insecureStorageCmd registers the root's --insecure-storage flag on a detached
// subcommand and turns it on. Tests construct subcommands without a root, so
// the persistent flag does not exist and insecureStorageFlag would report
// false, sending credential resolution to the real keychain.
func insecureStorageCmd(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().Bool("insecure-storage", false, "")
	_ = cmd.Flags().Set("insecure-storage", "true")
	return cmd
}

// newTestRootCmd builds the real root command with --insecure-storage turned
// on, so credential resolution uses the config file rather than the developer's
// keychain. Set it rather than prepending to each test's args: the flag then
// cannot be forgotten at a call site, and every test keeps the args it means to
// exercise. Tests must use this in place of NewRootCmd.
func newTestRootCmd() *cobra.Command {
	root := NewRootCmd("test")
	_ = root.PersistentFlags().Set("insecure-storage", "true")
	return root
}

func randToken(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func discardStreams() iostream.Streams {
	return iostream.Streams{Out: io.Discard, Err: io.Discard}
}

func noTTYPrompter(_ string) (string, error) {
	return "", ui.ErrNoTTY
}

func testCmd() *cobra.Command {
	return insecureStorageCmd(&cobra.Command{})
}

func TestEnsureCircleCIClient_NoTTY(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	rc, _ := config.Resolve("", "", true)
	_, err := ensureCircleCIClient(context.Background(), testCmd(), rc, discardStreams(), noTTYPrompter)
	assert.Assert(t, err != nil)
	assert.Assert(t, errors.Is(err, ui.ErrNoTTY))

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.Suggestion(), "CIRCLE_TOKEN"))
}

func TestEnsureAnthropicClient_NoTTY(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvAnthropicAPIKey, "")

	rc, _ := config.Resolve("", "", true)
	_, err := ensureAnthropicClient(context.Background(), testCmd(), rc, discardStreams(), noTTYPrompter)
	assert.Assert(t, err != nil)
	assert.Assert(t, errors.Is(err, ui.ErrNoTTY))

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.Suggestion(), "ANTHROPIC_API_KEY"))
}

func TestEnsureGitHubClient_NoTTY(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvGitHubToken, "")

	rc, _ := config.Resolve("", "", true)
	_, err := ensureGitHubClient(context.Background(), testCmd(), rc, discardStreams(), noTTYPrompter)
	assert.Assert(t, err != nil)
	assert.Assert(t, errors.Is(err, ui.ErrNoTTY))

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.Suggestion(), "GITHUB_TOKEN"))
}

func TestEnsureGitHubClient_PromptAndSave(t *testing.T) {
	isolateConfig(t)

	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()

	t.Setenv(config.EnvGitHubToken, "")
	t.Setenv(config.EnvGitHubAPIURL, srv.URL)

	token := randToken("ghp_")
	prompter := func(_ string) (string, error) { return token, nil }

	rc, _ := config.Resolve("", "", true)
	client, err := ensureGitHubClient(context.Background(), testCmd(), rc, discardStreams(), prompter)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)

	cfg, err := config.Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.GitHubToken, token)
}

func TestEnsureAnthropicClient_PromptAndSave(t *testing.T) {
	isolateConfig(t)

	ant := fakes.NewFakeAnthropic("ok")
	srv := httptest.NewServer(ant)
	defer srv.Close()

	t.Setenv(config.EnvAnthropicAPIKey, "")
	t.Setenv(config.EnvAnthropicBaseURL, srv.URL)

	key := randToken("sk-ant-")
	prompter := func(_ string) (string, error) { return key, nil }

	rc, _ := config.Resolve("", "", true)
	client, err := ensureAnthropicClient(context.Background(), testCmd(), rc, discardStreams(), prompter)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)

	cfg, err := config.Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.AnthropicAPIKey, key)
}

func TestEnsureAnthropicClient_InvalidPrefix(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvAnthropicAPIKey, "")

	prompter := func(_ string) (string, error) { return "bad-key", nil }

	rc, _ := config.Resolve("", "", true)
	_, err := ensureAnthropicClient(context.Background(), testCmd(), rc, discardStreams(), prompter)
	assert.Assert(t, err != nil)

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.Detail(), "sk-ant-"), "expected detail about invalid key format, got: %s", ue.Detail())
}

func TestEnsureCircleCIClient_EmptyToken(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	prompter := func(_ string) (string, error) { return "", nil }

	rc, _ := config.Resolve("", "", true)
	_, err := ensureCircleCIClient(context.Background(), testCmd(), rc, discardStreams(), prompter)
	assert.Assert(t, err != nil)

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.Suggestion(), "CIRCLE_TOKEN"), "expected suggestion about CIRCLE_TOKEN, got: %s", ue.Suggestion())
}

// A subcommand built without a root has no --insecure-storage flag, so
// insecureStorageFlag reports false and credential resolution falls through to
// the keychain. insecureStorageCmd is what keeps tests off it.
func TestInsecureStorageFlagRequiresRegistration(t *testing.T) {
	assert.Equal(t, insecureStorageFlag(&cobra.Command{}), false)
	assert.Equal(t, insecureStorageFlag(insecureStorageCmd(&cobra.Command{})), true)
}

// Registering the flag without turning it on is the trap org_test.go fell into:
// the flag exists, cobra accepts it, and resolution still uses the keychain.
func TestInsecureStorageFlagRegisteredButUnsetIsFalse(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("insecure-storage", false, "")
	assert.Equal(t, insecureStorageFlag(cmd), false)
}
