package authprompt_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/usererr"
)

// isolateConfig sets up a temp HOME so config.Load/Save use an isolated file.
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

func TestValidateCircleCIToken_OK(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	err := authprompt.ValidateCircleCIToken(context.Background(), randToken("cci-"), srv.URL)
	assert.NilError(t, err)
}

func TestValidateAPIKey_OK(t *testing.T) {
	ant := fakes.NewFakeAnthropic()
	srv := httptest.NewServer(ant)
	defer srv.Close()

	err := authprompt.ValidateAPIKey(context.Background(), randToken("sk-ant-"), srv.URL)
	assert.NilError(t, err)
}

func TestValidateGitHubToken_OK(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()

	err := authprompt.ValidateGitHubToken(context.Background(), randToken("ghp_"), srv.URL)
	assert.NilError(t, err)
}

func TestEnsureCircleCIClient_TokenInEnv(t *testing.T) {
	isolateConfig(t)

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	t.Setenv("CIRCLE_TOKEN", randToken("cci-"))
	t.Setenv("CIRCLECI_BASE_URL", srv.URL)

	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	client, err := authprompt.EnsureCircleCIClient(context.Background(), streams, tui.PromptHidden)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

func TestEnsureCircleCIClient_TokenInConfig(t *testing.T) {
	isolateConfig(t)

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	t.Setenv("CIRCLECI_BASE_URL", srv.URL)
	// Ensure env vars are clear so config file wins
	t.Setenv("CIRCLE_TOKEN", "")
	t.Setenv("CIRCLECI_TOKEN", "")

	cfg, err := config.Load()
	assert.NilError(t, err)
	cfg.CircleCIToken = randToken("cci-")
	assert.NilError(t, config.Save(cfg))

	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	client, err := authprompt.EnsureCircleCIClient(context.Background(), streams, tui.PromptHidden)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

func TestEnsureAnthropicClient_KeyInEnv(t *testing.T) {
	isolateConfig(t)

	ant := fakes.NewFakeAnthropic("ok")
	srv := httptest.NewServer(ant)
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", randToken("sk-ant-"))
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	client, err := authprompt.EnsureAnthropicClient(context.Background(), streams, tui.PromptHidden)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

func TestEnsureAnthropicClient_KeyInConfig(t *testing.T) {
	isolateConfig(t)

	ant := fakes.NewFakeAnthropic("ok")
	srv := httptest.NewServer(ant)
	defer srv.Close()

	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := config.Load()
	assert.NilError(t, err)
	cfg.AnthropicAPIKey = randToken("sk-ant-")
	assert.NilError(t, config.Save(cfg))

	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	client, err := authprompt.EnsureAnthropicClient(context.Background(), streams, tui.PromptHidden)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

func TestEnsureGitHubClient_TokenInEnv(t *testing.T) {
	isolateConfig(t)

	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", randToken("ghp_"))
	t.Setenv("GITHUB_API_URL", srv.URL)

	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	client, err := authprompt.EnsureGitHubClient(context.Background(), streams, tui.PromptHidden)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

func TestEnsureGitHubClient_TokenInConfig(t *testing.T) {
	isolateConfig(t)

	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()

	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GITHUB_TOKEN", "")

	cfg, err := config.Load()
	assert.NilError(t, err)
	cfg.GitHubToken = randToken("ghp_")
	assert.NilError(t, config.Save(cfg))

	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	client, err := authprompt.EnsureGitHubClient(context.Background(), streams, tui.PromptHidden)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

// --- prompt-and-save path tests (via injected prompter) ---

func assertNoTTYError(t *testing.T, err error, wantSubstr string) {
	t.Helper()
	assert.Assert(t, err != nil)
	assert.Assert(t, errors.Is(err, tui.ErrNoTTY), "expected ErrNoTTY in chain, got: %v", err)
	var ue *usererr.Error
	assert.Assert(t, errors.As(err, &ue), "expected *usererr.Error, got: %T %v", err, err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), wantSubstr),
		"expected %q in user message %q", wantSubstr, ue.UserMessage())
}

func TestEnsureCircleCIClient_NoTTY(t *testing.T) {
	isolateConfig(t)
	t.Setenv("CIRCLE_TOKEN", "")
	t.Setenv("CIRCLECI_TOKEN", "")

	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	prompter := func(string) (string, error) { return "", tui.ErrNoTTY }
	_, err := authprompt.EnsureCircleCIClient(context.Background(), streams, prompter)
	assertNoTTYError(t, err, "CIRCLE_TOKEN")
}

func TestEnsureAnthropicClient_NoTTY(t *testing.T) {
	isolateConfig(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	prompter := func(string) (string, error) { return "", tui.ErrNoTTY }
	_, err := authprompt.EnsureAnthropicClient(context.Background(), streams, prompter)
	assertNoTTYError(t, err, "ANTHROPIC_API_KEY")
}

func TestEnsureGitHubClient_NoTTY(t *testing.T) {
	isolateConfig(t)
	t.Setenv("GITHUB_TOKEN", "")

	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	prompter := func(string) (string, error) { return "", tui.ErrNoTTY }
	_, err := authprompt.EnsureGitHubClient(context.Background(), streams, prompter)
	assertNoTTYError(t, err, "GITHUB_TOKEN")
}

func TestEnsureGitHubClient_PromptAndSave(t *testing.T) {
	isolateConfig(t)

	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()

	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GITHUB_TOKEN", "")

	token := randToken("ghp_")
	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	prompter := func(string) (string, error) { return token, nil }
	client, err := authprompt.EnsureGitHubClient(context.Background(), streams, prompter)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)

	// Token should have been persisted
	cfg, err := config.Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.GitHubToken, token)
}

func TestEnsureAnthropicClient_PromptAndSave(t *testing.T) {
	isolateConfig(t)

	ant := fakes.NewFakeAnthropic("ok")
	srv := httptest.NewServer(ant)
	defer srv.Close()

	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "")

	key := randToken("sk-ant-")
	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	prompter := func(string) (string, error) { return key, nil }
	client, err := authprompt.EnsureAnthropicClient(context.Background(), streams, prompter)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)

	cfg, err := config.Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.AnthropicAPIKey, key)
}

func TestEnsureCircleCIClient_PromptAndSave(t *testing.T) {
	isolateConfig(t)

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	t.Setenv("CIRCLECI_BASE_URL", srv.URL)
	t.Setenv("CIRCLE_TOKEN", "")
	t.Setenv("CIRCLECI_TOKEN", "")

	token := randToken("cci-")
	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	prompter := func(string) (string, error) { return token, nil }
	client, err := authprompt.EnsureCircleCIClient(context.Background(), streams, prompter)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)

	cfg, err := config.Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.CircleCIToken, token)
}

func TestEnsureGitHubClient_InvalidToken(t *testing.T) {
	isolateConfig(t)
	t.Setenv("GITHUB_TOKEN", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	t.Setenv("GITHUB_API_URL", srv.URL)

	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	prompter := func(string) (string, error) { return randToken("ghp_"), nil }
	_, err := authprompt.EnsureGitHubClient(context.Background(), streams, prompter)
	assert.Assert(t, err != nil)
}
