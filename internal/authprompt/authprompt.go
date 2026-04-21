package authprompt

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// ErrNeedsAuth is returned by Resolve* functions when no credentials are
// available in env vars or config, indicating that the caller should prompt
// the user interactively.
var ErrNeedsAuth = errors.New("authentication required")

// ValidateCircleCIToken calls GET /api/v2/me to confirm the token is accepted.
// A 429 response is treated as valid (rate-limited but authenticated).
func ValidateCircleCIToken(ctx context.Context, token, baseURL string) error {
	if baseURL == "" {
		baseURL = "https://circleci.com"
	}
	cl := httpcl.New(httpcl.Config{
		BaseURL:    baseURL,
		AuthToken:  token,
		AuthHeader: "Circle-Token",
	})
	_, err := cl.Call(ctx, httpcl.NewRequest(http.MethodGet, "/api/v2/me"))
	if err != nil {
		if httpcl.HasStatusCode(err, http.StatusTooManyRequests) {
			return nil
		}
		return err
	}
	return nil
}

// ValidateAPIKey calls POST /v1/messages/count_tokens to confirm the Anthropic
// key is accepted. A 429 response is treated as valid.
func ValidateAPIKey(ctx context.Context, apiKey, baseURL string) error {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	cl := httpcl.New(httpcl.Config{
		BaseURL:    baseURL,
		AuthToken:  apiKey,
		AuthHeader: "x-api-key",
	})
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	body := struct {
		Model    string `json:"model"`
		Messages []msg  `json:"messages"`
	}{
		Model:    config.ValidationModel,
		Messages: []msg{{Role: "user", Content: "auth test"}},
	}
	_, err := cl.Call(ctx, httpcl.NewRequest(http.MethodPost, "/v1/messages/count_tokens",
		httpcl.Body(body),
		httpcl.Header("anthropic-version", "2023-06-01"),
	))
	if err != nil {
		if httpcl.HasStatusCode(err, http.StatusTooManyRequests) {
			return nil
		}
		return err
	}
	return nil
}

// ResolveCircleCIClient returns a CircleCI client if credentials are available
// in env vars or config. Returns ErrNeedsAuth when the caller must prompt.
func ResolveCircleCIClient() (*circleci.Client, error) {
	rc, err := config.Resolve("", "")
	if rc.CircleCIToken != "" {
		return circleci.NewClient()
	}
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}
	return nil, ErrNeedsAuth
}

// ResolveAnthropicClient returns an Anthropic client if credentials are
// available in env vars or config. Returns ErrNeedsAuth when the caller must
// prompt.
func ResolveAnthropicClient() (*anthropic.Client, error) {
	rc, err := config.Resolve("", "")
	if rc.AnthropicAPIKey != "" {
		return anthropic.New()
	}
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}
	return nil, ErrNeedsAuth
}

// ResolveGitHubClient returns a GitHub client if credentials are available in
// env vars or config. Returns ErrNeedsAuth when the caller must prompt.
func ResolveGitHubClient() (*github.Client, error) {
	rc, err := config.Resolve("", "")
	if rc.GitHubToken != "" {
		return github.New()
	}
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}
	return nil, ErrNeedsAuth
}

// SaveCircleCIToken persists a CircleCI token to the config file.
func SaveCircleCIToken(token string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.CircleCIToken = token
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	return nil
}

// SaveAnthropicKey persists an Anthropic API key to the config file.
func SaveAnthropicKey(key string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.AnthropicAPIKey = key
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save API key: %w", err)
	}
	return nil
}

// SaveGitHubToken persists a GitHub token to the config file.
func SaveGitHubToken(token string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.GitHubToken = token
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	return nil
}

// ValidateGitHubToken calls GET /user to confirm the token is accepted.
// A 429 response is treated as valid (rate-limited but authenticated).
func ValidateGitHubToken(ctx context.Context, token, baseURL string) error {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	cl := httpcl.New(httpcl.Config{
		BaseURL:    baseURL,
		AuthToken:  "token " + token,
		AuthHeader: "Authorization",
		UserAgent:  "chunk-cli",
	})
	_, err := cl.Call(ctx, httpcl.NewRequest(http.MethodGet, "/user"))
	if err != nil {
		if httpcl.HasStatusCode(err, http.StatusTooManyRequests) {
			return nil
		}
		return err
	}
	return nil
}

// CircleCIBaseURL returns the configured CircleCI base URL from the environment.
func CircleCIBaseURL() string {
	return os.Getenv("CIRCLECI_BASE_URL")
}

// AnthropicBaseURL returns the configured Anthropic base URL from the environment.
func AnthropicBaseURL() string {
	return os.Getenv("ANTHROPIC_BASE_URL")
}

// GitHubBaseURL returns the configured GitHub API URL from the environment.
func GitHubBaseURL() string {
	return os.Getenv("GITHUB_API_URL")
}
