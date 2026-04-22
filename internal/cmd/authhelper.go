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

// ensureCircleCIClient resolves or interactively prompts for a CircleCI token,
// validates it, saves it to config, and returns a ready client.
func ensureCircleCIClient(ctx context.Context, streams iostream.Streams, prompter func(string) (string, error)) (*circleci.Client, error) {
	client, err := authprompt.ResolveCircleCIClient()
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
				suggestion: "Set CIRCLE_TOKEN or run 'chunk auth set circleci'.",
				err:        err,
			}
		}
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, &userError{
			msg:        "CircleCI token required.",
			suggestion: "Set CIRCLE_TOKEN or run 'chunk auth set circleci'.",
			errMsg:     "empty token entered",
		}
	}

	streams.ErrPrintln(ui.Dim("Validating CircleCI token..."))
	if err := authprompt.ValidateCircleCIToken(ctx, token, authprompt.CircleCIBaseURL()); err != nil {
		return nil, fmt.Errorf("invalid CircleCI token: %w", err)
	}

	if err := authprompt.SaveCircleCIToken(token); err != nil {
		return nil, err
	}
	printSaved(streams, "CircleCI token")
	return circleci.NewClient()
}

// ensureAnthropicClient resolves or interactively prompts for an Anthropic API
// key, validates it, saves it to config, and returns a ready client.
func ensureAnthropicClient(ctx context.Context, streams iostream.Streams, prompter func(string) (string, error)) (*anthropic.Client, error) {
	client, err := authprompt.ResolveAnthropicClient()
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
				suggestion: "Set ANTHROPIC_API_KEY or run 'chunk auth set anthropic'.",
				err:        err,
			}
		}
		return nil, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, &userError{
			msg:        "Anthropic API key required.",
			suggestion: "Set ANTHROPIC_API_KEY or run 'chunk auth set anthropic'.",
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
	if err := authprompt.ValidateAPIKey(ctx, key, authprompt.AnthropicBaseURL()); err != nil {
		return nil, fmt.Errorf("invalid Anthropic API key: %w", err)
	}

	if err := authprompt.SaveAnthropicKey(key); err != nil {
		return nil, err
	}
	printSaved(streams, "Anthropic API key")
	return anthropic.New()
}

// ensureGitHubClient resolves or interactively prompts for a GitHub token,
// validates it, saves it to config, and returns a ready client.
func ensureGitHubClient(ctx context.Context, streams iostream.Streams, prompter func(string) (string, error)) (*github.Client, error) {
	logStatus := func(msg string) { streams.ErrPrintln("  " + msg) }
	client, err := authprompt.ResolveGitHubClient(logStatus)
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
				suggestion: "Set GITHUB_TOKEN or run 'chunk auth set github'.",
				err:        err,
			}
		}
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, &userError{
			msg:        "GitHub token required.",
			suggestion: "Set GITHUB_TOKEN or run 'chunk auth set github'.",
			errMsg:     "empty token entered",
		}
	}

	streams.ErrPrintln(ui.Dim("Validating GitHub token..."))
	if err := authprompt.ValidateGitHubToken(ctx, token, authprompt.GitHubBaseURL()); err != nil {
		return nil, fmt.Errorf("invalid GitHub token: %w", err)
	}

	if err := authprompt.SaveGitHubToken(token); err != nil {
		return nil, err
	}
	printSaved(streams, "GitHub token")
	return github.New(logStatus)
}
