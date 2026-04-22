package authprompt_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
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

func TestResolveCircleCIClient_TokenInEnv(t *testing.T) {
	isolateConfig(t)

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	t.Setenv(config.EnvCircleToken, randToken("cci-"))
	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)

	rc, _ := config.Resolve("", "")
	client, err := authprompt.ResolveCircleCIClient(rc)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

func TestResolveCircleCIClient_TokenInConfig(t *testing.T) {
	isolateConfig(t)

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	cfg, err := config.Load()
	assert.NilError(t, err)
	cfg.CircleCIToken = randToken("cci-")
	assert.NilError(t, config.Save(cfg))

	rc, _ := config.Resolve("", "")
	client, err := authprompt.ResolveCircleCIClient(rc)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

func TestResolveCircleCIClient_NeedsAuth(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	rc, _ := config.Resolve("", "")
	_, err := authprompt.ResolveCircleCIClient(rc)
	assert.Assert(t, errors.Is(err, authprompt.ErrNeedsAuth))
}

func TestResolveAnthropicClient_KeyInEnv(t *testing.T) {
	isolateConfig(t)

	ant := fakes.NewFakeAnthropic("ok")
	srv := httptest.NewServer(ant)
	defer srv.Close()

	t.Setenv(config.EnvAnthropicAPIKey, randToken("sk-ant-"))
	t.Setenv(config.EnvAnthropicBaseURL, srv.URL)

	rc, _ := config.Resolve("", "")
	client, err := authprompt.ResolveAnthropicClient(rc)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

func TestResolveAnthropicClient_KeyInConfig(t *testing.T) {
	isolateConfig(t)

	ant := fakes.NewFakeAnthropic("ok")
	srv := httptest.NewServer(ant)
	defer srv.Close()

	t.Setenv(config.EnvAnthropicBaseURL, srv.URL)
	t.Setenv(config.EnvAnthropicAPIKey, "")

	cfg, err := config.Load()
	assert.NilError(t, err)
	cfg.AnthropicAPIKey = randToken("sk-ant-")
	assert.NilError(t, config.Save(cfg))

	rc, _ := config.Resolve("", "")
	client, err := authprompt.ResolveAnthropicClient(rc)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

func TestResolveAnthropicClient_NeedsAuth(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvAnthropicAPIKey, "")

	rc, _ := config.Resolve("", "")
	_, err := authprompt.ResolveAnthropicClient(rc)
	assert.Assert(t, errors.Is(err, authprompt.ErrNeedsAuth))
}

func TestResolveGitHubClient_TokenInEnv(t *testing.T) {
	isolateConfig(t)

	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()

	t.Setenv(config.EnvGitHubToken, randToken("ghp_"))
	t.Setenv(config.EnvGitHubAPIURL, srv.URL)

	rc, _ := config.Resolve("", "")
	client, err := authprompt.ResolveGitHubClient(rc, nil)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

func TestResolveGitHubClient_TokenInConfig(t *testing.T) {
	isolateConfig(t)

	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()

	t.Setenv(config.EnvGitHubAPIURL, srv.URL)
	t.Setenv(config.EnvGitHubToken, "")

	cfg, err := config.Load()
	assert.NilError(t, err)
	cfg.GitHubToken = randToken("ghp_")
	assert.NilError(t, config.Save(cfg))

	rc, _ := config.Resolve("", "")
	client, err := authprompt.ResolveGitHubClient(rc, nil)
	assert.NilError(t, err)
	assert.Assert(t, client != nil)
}

func TestResolveGitHubClient_NeedsAuth(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvGitHubToken, "")

	rc, _ := config.Resolve("", "")
	_, err := authprompt.ResolveGitHubClient(rc, nil)
	assert.Assert(t, errors.Is(err, authprompt.ErrNeedsAuth))
}

func TestSaveCircleCIToken(t *testing.T) {
	isolateConfig(t)

	token := randToken("cci-")
	err := authprompt.SaveCircleCIToken(token)
	assert.NilError(t, err)

	cfg, err := config.Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.CircleCIToken, token)
}

func TestSaveAnthropicKey(t *testing.T) {
	isolateConfig(t)

	key := randToken("sk-ant-")
	err := authprompt.SaveAnthropicKey(key)
	assert.NilError(t, err)

	cfg, err := config.Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.AnthropicAPIKey, key)
}

func TestSaveGitHubToken(t *testing.T) {
	isolateConfig(t)

	token := randToken("ghp_")
	err := authprompt.SaveGitHubToken(token)
	assert.NilError(t, err)

	cfg, err := config.Load()
	assert.NilError(t, err)
	assert.Equal(t, cfg.GitHubToken, token)
}
