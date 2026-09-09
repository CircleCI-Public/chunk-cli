package authprompt

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

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

// ValidateCircleCIToken calls GET /api/v2/me to confirm the token is accepted
// and returns the authenticated user's UUID. A 429 response is treated as
// valid (rate-limited but authenticated); uuid.Nil is returned in that case.
func ValidateCircleCIToken(ctx context.Context, token, baseURL string) (uuid.UUID, error) {
	if baseURL == "" {
		baseURL = "https://circleci.com"
	}
	cl := hc.New(hc.Config{
		BaseURL:    baseURL,
		AuthToken:  token,
		AuthHeader: "Circle-Token",
		UserAgent:  version.UserAgent(),
	})
	var me struct {
		ID string `json:"id"`
	}
	_, err := cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v2/me", hc.JSONDecoder(&me)))
	if err != nil {
		if hc.HasStatusCode(err, http.StatusTooManyRequests) {
			return uuid.Nil, nil
		}
		return uuid.Nil, err
	}
	id, parseErr := uuid.Parse(me.ID)
	if parseErr != nil {
		return uuid.Nil, nil
	}
	return id, nil
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
// Returns ErrNeedsAuth when the caller must prompt. onWarn is called with a
// plain-text deprecation message when the server signals endpoint removal; pass
// nil to silence warnings.
func ResolveCircleCIClient(rc config.ResolvedConfig, onWarn func(string)) (*circleci.Client, error) {
	if rc.CircleCIToken == "" {
		return nil, ErrNeedsAuth
	}
	return circleci.NewClient(circleci.Config{
		Token:   rc.CircleCIToken,
		BaseURL: rc.CircleCIBaseURL,
		OnWarn:  onWarn,
		// A 401 is the only signal that the token in hand has gone stale, and
		// re-resolving is the only way it can improve — there is no refresh
		// grant. This matters for processes that outlive a login: the watch
		// daemon builds one client at startup and keeps it for its whole life.
		ReloadToken: func() (string, error) {
			reloaded, err := config.ResolveCircleCI(false)
			if err != nil {
				return "", err
			}
			return reloaded.CircleCIToken, nil
		},
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

// SaveCircleCIToken persists a CircleCI token. When insecureStorage is false it uses
// the system keychain; when true it falls back to the config file.
func SaveCircleCIToken(token, baseURL string, insecureStorage bool) error {
	if !insecureStorage {
		return keyring.Set(keyring.ServiceCircleCI(baseURL), token)
	}
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

// SaveAnthropicKey persists an Anthropic API key. When insecureStorage is false it uses
// the system keychain; when true it falls back to the config file.
func SaveAnthropicKey(key, baseURL string, insecureStorage bool) error {
	if !insecureStorage {
		return keyring.Set(keyring.ServiceAnthropic(baseURL), key)
	}
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

// SaveGitHubToken persists a GitHub token. When insecureStorage is false it uses
// the system keychain; when true it falls back to the config file.
func SaveGitHubToken(token, baseURL string, insecureStorage bool) error {
	if !insecureStorage {
		return keyring.Set(keyring.ServiceGitHub(baseURL), token)
	}
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
