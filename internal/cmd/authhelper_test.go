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

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
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

func TestEnsureCircleCIClient_NoTTY(t *testing.T) {
	isolateConfig(t)
	t.Setenv("CIRCLE_TOKEN", "")
	t.Setenv("CIRCLECI_TOKEN", "")

	_, err := ensureCircleCIClient(context.Background(), discardStreams(), noTTYPrompter)
	assert.Assert(t, err != nil)
	assert.Assert(t, errors.Is(err, tui.ErrNoTTY))

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.Suggestion(), "CIRCLE_TOKEN"))
}

func TestEnsureAnthropicClient_NoTTY(t *testing.T) {
	isolateConfig(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	_, err := ensureAnthropicClient(context.Background(), discardStreams(), noTTYPrompter)
	assert.Assert(t, err != nil)
	assert.Assert(t, errors.Is(err, tui.ErrNoTTY))

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.Suggestion(), "ANTHROPIC_API_KEY"))
}

func TestEnsureGitHubClient_NoTTY(t *testing.T) {
	isolateConfig(t)
	t.Setenv("GITHUB_TOKEN", "")

	_, err := ensureGitHubClient(context.Background(), discardStreams(), noTTYPrompter)
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

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_API_URL", srv.URL)

	token := randToken("ghp_")
	prompter := func(_ string) (string, error) { return token, nil }

	client, err := ensureGitHubClient(context.Background(), discardStreams(), prompter)
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

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	key := randToken("sk-ant-")
	prompter := func(_ string) (string, error) { return key, nil }

	client, err := ensureAnthropicClient(context.Background(), discardStreams(), prompter)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)

	cfg, err := config.Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.AnthropicAPIKey, key)
}

func TestEnsureAnthropicClient_InvalidPrefix(t *testing.T) {
	isolateConfig(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	prompter := func(_ string) (string, error) { return "bad-key", nil }

	_, err := ensureAnthropicClient(context.Background(), discardStreams(), prompter)
	assert.Assert(t, err != nil)

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.Detail(), "sk-ant-"), "expected detail about invalid key format, got: %s", ue.Detail())
}

func TestEnsureCircleCIClient_EmptyToken(t *testing.T) {
	isolateConfig(t)
	t.Setenv("CIRCLE_TOKEN", "")
	t.Setenv("CIRCLECI_TOKEN", "")

	prompter := func(_ string) (string, error) { return "", nil }

	_, err := ensureCircleCIClient(context.Background(), discardStreams(), prompter)
	assert.Assert(t, err != nil)

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.Suggestion(), "CIRCLE_TOKEN"), "expected suggestion about CIRCLE_TOKEN, got: %s", ue.Suggestion())
}
