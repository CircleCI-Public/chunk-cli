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
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvXDGConfigHome, filepath.Join(home, ".config"))
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
	return "", tui.ErrNoTTY
}

func testCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("insecure-storage", false, "")
	// Use insecure (config file) storage in tests to avoid hitting the system keychain.
	_ = cmd.Flags().Set("insecure-storage", "true")
	return cmd
}

func TestEnsureCircleCIClient_NoTTY(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	rc, _ := config.Resolve("", "", true)
	_, err := ensureCircleCIClient(context.Background(), testCmd(), rc, discardStreams(), noTTYPrompter)
	assert.Assert(t, err != nil)
	assert.Assert(t, errors.Is(err, tui.ErrNoTTY))

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
	assert.Assert(t, errors.Is(err, tui.ErrNoTTY))

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
	assert.Assert(t, errors.Is(err, tui.ErrNoTTY))

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
