package authprompt

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
	"github.com/CircleCI-Public/chunk-cli/internal/keyring"
	"github.com/CircleCI-Public/chunk-cli/internal/version"
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
	cl := hc.New(hc.Config{
		BaseURL:    baseURL,
		AuthToken:  token,
		AuthHeader: "Circle-Token",
		UserAgent:  version.UserAgent(),
	})
	_, err := cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v2/me"))
	if err != nil {
		if hc.HasStatusCode(err, http.StatusTooManyRequests) {
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
	cl := hc.New(hc.Config{
		BaseURL:    baseURL,
		AuthToken:  apiKey,
		AuthHeader: "x-api-key",
		UserAgent:  version.UserAgent(),
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
	_, err := cl.Call(ctx, hc.NewRequest(http.MethodPost, "/v1/messages/count_tokens",
		hc.Body(body),
		hc.Header("anthropic-version", "2023-06-01"),
	))
	if err != nil {
		if hc.HasStatusCode(err, http.StatusTooManyRequests) {
			return nil
		}
		return err
	}
	return nil
}

// ResolveCircleCIClient returns a CircleCI client if credentials are available.
// Returns ErrNeedsAuth when the caller must prompt.
func ResolveCircleCIClient(rc config.ResolvedConfig) (*circleci.Client, error) {
	if rc.CircleCIToken == "" {
		return nil, ErrNeedsAuth
	}
	return circleci.NewClient(circleci.Config{
		Token:   rc.CircleCIToken,
		BaseURL: rc.CircleCIBaseURL,
	})
}

// ResolveAnthropicClient returns an Anthropic client if credentials are
// available. Returns ErrNeedsAuth when the caller must prompt.
func ResolveAnthropicClient(rc config.ResolvedConfig) (*anthropic.Client, error) {
	if rc.AnthropicAPIKey == "" {
		return nil, ErrNeedsAuth
	}
	return anthropic.New(anthropic.Config{
		APIKey:  rc.AnthropicAPIKey,
		BaseURL: rc.AnthropicBaseURL,
	})
}

// ResolveGitHubClient returns a GitHub client if credentials are available.
// Returns ErrNeedsAuth when the caller must prompt.
func ResolveGitHubClient(rc config.ResolvedConfig, logStatus func(string)) (*github.Client, error) {
	if rc.GitHubToken == "" {
		return nil, ErrNeedsAuth
	}
	return github.New(github.Config{
		Token:     rc.GitHubToken,
		BaseURL:   rc.GitHubAPIURL,
		LogStatus: logStatus,
	})
}

// SaveCircleCIToken persists a CircleCI token to the system keychain, or to
// the config file when insecureStorage is true or the keychain is unavailable.
// baseURL is used to scope the keychain entry to the CircleCI host.
// Returns true if saved to the keychain.
func SaveCircleCIToken(token, baseURL string, insecureStorage bool) (bool, error) {
	if !insecureStorage {
		if err := keyring.Set(keyring.CircleCITokenKey(baseURL), token); err == nil {
			return true, nil
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return false, fmt.Errorf("load config: %w", err)
	}
	cfg.CircleCIToken = token
	if err := config.Save(cfg); err != nil {
		return false, fmt.Errorf("save token: %w", err)
	}
	return false, nil
}

// SaveAnthropicKey persists an Anthropic API key to the system keychain, or to
// the config file when insecureStorage is true or the keychain is unavailable.
// baseURL is used to scope the keychain entry to the Anthropic host.
// Returns true if saved to the keychain.
func SaveAnthropicKey(key, baseURL string, insecureStorage bool) (bool, error) {
	if !insecureStorage {
		if err := keyring.Set(keyring.AnthropicKeyKey(baseURL), key); err == nil {
			return true, nil
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return false, fmt.Errorf("load config: %w", err)
	}
	cfg.AnthropicAPIKey = key
	if err := config.Save(cfg); err != nil {
		return false, fmt.Errorf("save API key: %w", err)
	}
	return false, nil
}

// SaveGitHubToken persists a GitHub token to the system keychain, or to the
// config file when insecureStorage is true or the keychain is unavailable.
// apiURL is used to scope the keychain entry to the GitHub host.
// Returns true if saved to the keychain.
func SaveGitHubToken(token, apiURL string, insecureStorage bool) (bool, error) {
	if !insecureStorage {
		if err := keyring.Set(keyring.GitHubTokenKey(apiURL), token); err == nil {
			return true, nil
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return false, fmt.Errorf("load config: %w", err)
	}
	cfg.GitHubToken = token
	if err := config.Save(cfg); err != nil {
		return false, fmt.Errorf("save token: %w", err)
	}
	return false, nil
}

// ValidateGitHubToken calls GET /user to confirm the token is accepted.
// A 429 response is treated as valid (rate-limited but authenticated).
func ValidateGitHubToken(ctx context.Context, token, baseURL string) error {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	cl := hc.New(hc.Config{
		BaseURL:    baseURL,
		AuthToken:  "token " + token,
		AuthHeader: "Authorization",
		UserAgent:  version.UserAgent(),
	})
	_, err := cl.Call(ctx, hc.NewRequest(http.MethodGet, "/user"))
	if err != nil {
		if hc.HasStatusCode(err, http.StatusTooManyRequests) {
			return nil
		}
		return err
	}
	return nil
}
