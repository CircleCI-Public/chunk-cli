package authprompt

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/usererr"
)

// ErrCancelled is returned when the user cancels an inline prompt.
var ErrCancelled = tui.ErrCancelled

// Prompter is a function that prompts the user for hidden input.
// tui.PromptHidden is the standard implementation; inject a stub in tests.
type Prompter func(label string) (string, error)

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

func PrintSaveHint(streams iostream.Streams, label string) {
	if cfgPath, err := config.Path(); err == nil {
		streams.ErrPrintln(ui.Dim(fmt.Sprintf("%s will be saved to user config (%s, mode 0600)", label, cfgPath)))
	}
}

func PrintSaved(streams iostream.Streams, label string) {
	msg := label + " saved"
	if cfgPath, err := config.Path(); err == nil {
		msg = fmt.Sprintf("%s saved to user config (%s)", label, cfgPath)
	}
	streams.ErrPrintln(ui.Success(msg))
}

// EnsureCircleCIClient returns a ready-to-use CircleCI client. If a token is
// already available (env var or config file), it uses it directly. Otherwise
// it prompts inline once, validates, saves to config, and returns the client.
func EnsureCircleCIClient(ctx context.Context, streams iostream.Streams, prompter Prompter) (*circleci.Client, error) {
	rc, err := config.Resolve("", "")
	if rc.CircleCIToken != "" {
		return circleci.NewClient()
	}
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}

	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("CircleCI token required"))
	streams.ErrPrintln("Create a token at https://app.circleci.com/settings/user/tokens")
	PrintSaveHint(streams, "Token")
	streams.ErrPrintln("")

	token, err := prompter("CircleCI Token")
	if err != nil {
		if errors.Is(err, tui.ErrNoTTY) {
			return nil, usererr.New("CircleCI token required: set CIRCLE_TOKEN or run 'chunk auth set circleci'", err)
		}
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, usererr.New("CircleCI token required: set CIRCLE_TOKEN or run 'chunk auth set circleci'", fmt.Errorf("empty token entered"))
	}

	streams.ErrPrintln(ui.Dim("Validating CircleCI token..."))
	if err := ValidateCircleCIToken(ctx, token, os.Getenv("CIRCLECI_BASE_URL")); err != nil {
		return nil, fmt.Errorf("invalid CircleCI token: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	cfg.CircleCIToken = token
	if err := config.Save(cfg); err != nil {
		return nil, fmt.Errorf("save token: %w", err)
	}
	PrintSaved(streams, "CircleCI token")
	return circleci.NewClient()
}

// EnsureAnthropicClient returns a ready-to-use Anthropic client. If a key is
// already available (env var or config file), it uses it directly. Otherwise
// it prompts inline once, validates, saves to config, and returns the client.
func EnsureAnthropicClient(ctx context.Context, streams iostream.Streams, prompter Prompter) (*anthropic.Client, error) {
	rc, err := config.Resolve("", "")
	if rc.AnthropicAPIKey != "" {
		return anthropic.New()
	}
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}

	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("Anthropic API key required"))
	streams.ErrPrintln("Get a key at https://console.anthropic.com/")
	PrintSaveHint(streams, "Key")
	streams.ErrPrintln("")

	key, err := prompter("API Key")
	if err != nil {
		if errors.Is(err, tui.ErrNoTTY) {
			return nil, usererr.New("Anthropic API key required: set ANTHROPIC_API_KEY or run 'chunk auth set anthropic'", err)
		}
		return nil, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, usererr.New("Anthropic API key required: set ANTHROPIC_API_KEY or run 'chunk auth set anthropic'", fmt.Errorf("empty key entered"))
	}
	if !strings.HasPrefix(key, "sk-ant-") {
		return nil, usererr.New("Invalid API key format — keys should start with \"sk-ant-\". Get a valid key from https://console.anthropic.com/", fmt.Errorf("invalid key prefix"))
	}

	streams.ErrPrintln(ui.Dim("Validating API key..."))
	if err := ValidateAPIKey(ctx, key, os.Getenv("ANTHROPIC_BASE_URL")); err != nil {
		return nil, fmt.Errorf("invalid Anthropic API key: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	cfg.AnthropicAPIKey = key
	if err := config.Save(cfg); err != nil {
		return nil, fmt.Errorf("save API key: %w", err)
	}
	PrintSaved(streams, "Anthropic API key")
	return anthropic.New()
}

// EnsureGitHubClient returns a ready-to-use GitHub client. If a token is
// already available (env var or config file), it uses it directly. Otherwise
// it prompts inline once, validates the token against GET /user, saves to
// config, and returns the client.
func EnsureGitHubClient(ctx context.Context, streams iostream.Streams, prompter Prompter) (*github.Client, error) {
	rc, err := config.Resolve("", "")
	if rc.GitHubToken != "" {
		return github.New()
	}
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}

	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("GitHub token required"))
	streams.ErrPrintln("Create a token at https://github.com/settings/tokens")
	PrintSaveHint(streams, "Token")
	streams.ErrPrintln("")

	token, err := prompter("GitHub Token")
	if err != nil {
		if errors.Is(err, tui.ErrNoTTY) {
			return nil, usererr.New("GitHub token required: set GITHUB_TOKEN or run 'chunk auth set github'", err)
		}
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, usererr.New("GitHub token required: set GITHUB_TOKEN or run 'chunk auth set github'", fmt.Errorf("empty token entered"))
	}

	streams.ErrPrintln(ui.Dim("Validating GitHub token..."))
	if err := ValidateGitHubToken(ctx, token, os.Getenv("GITHUB_API_URL")); err != nil {
		return nil, fmt.Errorf("invalid GitHub token: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	cfg.GitHubToken = token
	if err := config.Save(cfg); err != nil {
		return nil, fmt.Errorf("save token: %w", err)
	}
	PrintSaved(streams, "GitHub token")
	return github.New()
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
