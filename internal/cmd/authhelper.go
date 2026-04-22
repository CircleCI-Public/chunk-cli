package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

const (
	suggestionCircleCIAuth  = "Set " + config.EnvCircleToken + " or run 'chunk auth set circleci'."
	suggestionAnthropicAuth = "Set " + config.EnvAnthropicAPIKey + " or run 'chunk auth set anthropic'."
	suggestionGitHubAuth    = "Set " + config.EnvGitHubToken + " or run 'chunk auth set github'."
)

func printSaveHint(streams iostream.Streams, label string) {
	if cfgPath, err := config.Path(); err == nil {
		streams.ErrPrintln(ui.Dim(fmt.Sprintf("%s will be saved to user config (%s, mode 0600)", label, cfgPath)))
	}
}

func printSaved(streams iostream.Streams, label string) {
	msg := label + " saved"
	if cfgPath, err := config.Path(); err == nil {
		msg = fmt.Sprintf("%s saved to user config (%s)", label, cfgPath)
	}
	streams.ErrPrintln(ui.Success(msg))
}

func ensureCircleCIClient(ctx context.Context, streams iostream.Streams, prompter func(string) (string, error)) (*circleci.Client, error) {
	rc, _ := config.Resolve("", "")
	client, err := authprompt.ResolveCircleCIClient(rc)
	if err == nil {
		return client, nil
	}
	if !errors.Is(err, authprompt.ErrNeedsAuth) {
		return nil, err
	}

	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("CircleCI token required"))
	streams.ErrPrintln("Create a token at https://app.circleci.com/settings/user/tokens")
	printSaveHint(streams, "Token")
	streams.ErrPrintln("")

	token, err := prompter("CircleCI Token")
	if err != nil {
		if errors.Is(err, tui.ErrNoTTY) {
			return nil, &userError{
				msg:        "CircleCI token required.",
				suggestion: suggestionCircleCIAuth,
				err:        err,
			}
		}
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, &userError{
			msg:        "CircleCI token required.",
			suggestion: suggestionCircleCIAuth,
			errMsg:     "empty token entered",
		}
	}

	streams.ErrPrintln(ui.Dim("Validating CircleCI token..."))
	if err := authprompt.ValidateCircleCIToken(ctx, token, rc.CircleCIBaseURL); err != nil {
		return nil, fmt.Errorf("invalid CircleCI token: %w", err)
	}

	if err := authprompt.SaveCircleCIToken(token); err != nil {
		return nil, err
	}
	printSaved(streams, "CircleCI token")
	return circleci.NewClient(circleci.Config{
		Token:   token,
		BaseURL: rc.CircleCIBaseURL,
	})
}

func ensureAnthropicClient(ctx context.Context, streams iostream.Streams, prompter func(string) (string, error)) (*anthropic.Client, error) {
	rc, _ := config.Resolve("", "")
	client, err := authprompt.ResolveAnthropicClient(rc)
	if err == nil {
		return client, nil
	}
	if !errors.Is(err, authprompt.ErrNeedsAuth) {
		return nil, err
	}

	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("Anthropic API key required"))
	streams.ErrPrintln("Get a key at https://console.anthropic.com/")
	printSaveHint(streams, "Key")
	streams.ErrPrintln("")

	key, err := prompter("API Key")
	if err != nil {
		if errors.Is(err, tui.ErrNoTTY) {
			return nil, &userError{
				msg:        "Anthropic API key required.",
				suggestion: suggestionAnthropicAuth,
				err:        err,
			}
		}
		return nil, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, &userError{
			msg:        "Anthropic API key required.",
			suggestion: suggestionAnthropicAuth,
			errMsg:     "empty key entered",
		}
	}
	if !strings.HasPrefix(key, "sk-ant-") {
		return nil, &userError{
			msg:        "Invalid API key format.",
			detail:     "Keys should start with \"sk-ant-\".",
			suggestion: "Get a valid key from https://console.anthropic.com/",
			errMsg:     "invalid key prefix",
		}
	}

	streams.ErrPrintln(ui.Dim("Validating API key..."))
	if err := authprompt.ValidateAPIKey(ctx, key, rc.AnthropicBaseURL); err != nil {
		return nil, fmt.Errorf("invalid Anthropic API key: %w", err)
	}

	if err := authprompt.SaveAnthropicKey(key); err != nil {
		return nil, err
	}
	printSaved(streams, "Anthropic API key")
	return anthropic.New(anthropic.Config{
		APIKey:  key,
		BaseURL: rc.AnthropicBaseURL,
	})
}

func ensureGitHubClient(ctx context.Context, streams iostream.Streams, prompter func(string) (string, error)) (*github.Client, error) {
	rc, _ := config.Resolve("", "")
	logStatus := func(msg string) { streams.ErrPrintln("  " + msg) }
	client, err := authprompt.ResolveGitHubClient(rc, logStatus)
	if err == nil {
		return client, nil
	}
	if !errors.Is(err, authprompt.ErrNeedsAuth) {
		return nil, err
	}

	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("GitHub token required"))
	streams.ErrPrintln("Create a token at https://github.com/settings/tokens")
	printSaveHint(streams, "Token")
	streams.ErrPrintln("")

	token, err := prompter("GitHub Token")
	if err != nil {
		if errors.Is(err, tui.ErrNoTTY) {
			return nil, &userError{
				msg:        "GitHub token required.",
				suggestion: suggestionGitHubAuth,
				err:        err,
			}
		}
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, &userError{
			msg:        "GitHub token required.",
			suggestion: suggestionGitHubAuth,
			errMsg:     "empty token entered",
		}
	}

	streams.ErrPrintln(ui.Dim("Validating GitHub token..."))
	if err := authprompt.ValidateGitHubToken(ctx, token, rc.GitHubAPIURL); err != nil {
		return nil, fmt.Errorf("invalid GitHub token: %w", err)
	}

	if err := authprompt.SaveGitHubToken(token); err != nil {
		return nil, err
	}
	printSaved(streams, "GitHub token")
	return github.New(github.Config{
		Token:     token,
		BaseURL:   rc.GitHubAPIURL,
		LogStatus: logStatus,
	})
}
