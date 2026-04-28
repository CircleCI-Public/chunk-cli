package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

const (
	providerCircleCI  = "circleci"
	providerAnthropic = "anthropic"
	providerGitHub    = "github"
)

const configFilePermHint = "Check file permissions on the chunk config file."

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication",
	}
	cmd.AddCommand(newAuthSetCmd())
	cmd.AddCommand(newAuthStatusCmd())
	cmd.AddCommand(newAuthRemoveCmd())
	return cmd
}

func newAuthSetCmd() *cobra.Command {
	var insecureStorage bool
	cmd := &cobra.Command{
		Use:       "set <provider>",
		Short:     "Store credentials for a provider",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"circleci", "anthropic", "github"},
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, _ := config.Resolve("", "")
			provider := args[0]
			io := iostream.FromCmd(cmd)
			switch provider {
			case providerCircleCI:
				return authSetCircleCI(cmd.Context(), io, rc, insecureStorage)
			case providerAnthropic:
				return authSetAnthropic(cmd.Context(), io, rc, insecureStorage)
			case providerGitHub:
				return authSetGitHub(cmd.Context(), io, rc, insecureStorage)
			default:
				return &userError{
					msg:    fmt.Sprintf("Unknown provider %q.", provider),
					detail: "Valid providers: circleci, anthropic, github.",
					errMsg: fmt.Sprintf("unknown provider %q", provider),
				}
			}
		},
	}
	cmd.Flags().BoolVar(&insecureStorage, "insecure-storage", false, "Save credentials to config file instead of system keychain")
	return cmd
}

func authSetCircleCI(ctx context.Context, io iostream.Streams, rc config.ResolvedConfig, insecureStorage bool) error {
	envSet := strings.HasPrefix(rc.CircleCITokenSource, "Environment")
	io.Println("")
	io.Println(ui.Bold("Chunk CLI - CircleCI Token Setup"))
	io.Println("")
	io.Println("Create a CircleCI token at https://app.circleci.com/settings/user/tokens")
	printSaveHint(io, "Token", insecureStorage)
	io.Println("")

	if envSet {
		io.Println(ui.Warning("A CircleCI token is set in environment variables (" + config.EnvCircleToken + "/" + config.EnvCircleCIToken + ")."))
		io.Println(ui.Dim("Environment variables take precedence over stored config."))
		io.Println("")
	}

	if rc.CircleCIToken != "" && !envSet {
		io.Printf("A CircleCI token is already stored.\n")
		replace, err := tui.Confirm("Do you want to replace it?", false)
		if err != nil {
			io.Println("Cancelled.")
			return nil
		}
		if !replace {
			io.Println("Keeping existing token.")
			return nil
		}
	}

	token, err := tui.PromptHidden("CircleCI Token")
	if err != nil {
		return nil
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return &userError{
			msg:        "Token cannot be empty.",
			suggestion: "Create a token at https://app.circleci.com/settings/user/tokens",
			errMsg:     "empty circleci token",
		}
	}

	return saveCircleCIToken(ctx, token, io, rc.CircleCIBaseURL, insecureStorage)
}

func authSetAnthropic(ctx context.Context, io iostream.Streams, rc config.ResolvedConfig, insecureStorage bool) error {
	envSet := strings.HasPrefix(rc.AnthropicAPIKeySource, "Environment")
	io.Println("")
	io.Println(ui.Bold("Chunk CLI - Anthropic API Key Setup"))
	io.Println("")
	io.Println("Enter your Anthropic API key (starts with sk-ant-).")
	printSaveHint(io, "Key", insecureStorage)
	io.Println("")
	if envSet {
		io.Println(ui.Warning("An Anthropic API key is set in environment variables (" + config.EnvAnthropicAPIKey + ")."))
		io.Println(ui.Dim("Environment variables take precedence over stored config."))
		io.Println("")
	}

	if rc.AnthropicAPIKey != "" && !envSet {
		io.Printf("An Anthropic API key is already stored.\n")
		replace, err := tui.Confirm("Do you want to replace it?", false)
		if err != nil {
			return nil
		}
		if !replace {
			io.Println("Keeping existing API key.")
			return nil
		}
	}

	key, err := tui.PromptHidden("API Key")
	if err != nil {
		return nil
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return &userError{
			msg:        "API key cannot be empty.",
			suggestion: "Get an API key from https://console.anthropic.com/",
			errMsg:     "empty anthropic key",
		}
	}

	if !strings.HasPrefix(key, "sk-ant-") {
		return &userError{
			msg:        "Invalid API key format.",
			detail:     "Keys should start with \"sk-ant-\".",
			suggestion: "Get a valid API key from https://console.anthropic.com/",
			errMsg:     "invalid anthropic key format",
		}
	}

	io.ErrPrintln(ui.Dim("Validating API key..."))
	if err := authprompt.ValidateAPIKey(ctx, key, rc.AnthropicBaseURL); err != nil {
		return &userError{
			msg:        "API key validation failed.",
			suggestion: "Check that your key is correct and has not been revoked.",
			err:        err,
		}
	}

	savedToKeychain, err := authprompt.SaveAnthropicKey(key, rc.AnthropicBaseURL, insecureStorage)
	if err != nil {
		return &userError{msg: "Could not save credentials.", suggestion: configFilePermHint, err: err}
	}

	io.Println("")
	printSaved(io, "Anthropic API key", savedToKeychain)
	io.Println(ui.Dim("You can now run code reviews with: chunk build-prompt"))
	return nil
}

func saveCircleCIToken(ctx context.Context, token string, streams iostream.Streams, circleCIBaseURL string, insecureStorage bool) error {
	streams.ErrPrintln(ui.Dim("Validating CircleCI token..."))
	if err := authprompt.ValidateCircleCIToken(ctx, token, circleCIBaseURL); err != nil {
		return &userError{
			msg:        "CircleCI token validation failed.",
			suggestion: "Check that your token is correct.",
			err:        fmt.Errorf("validate token: %w", err),
		}
	}

	savedToKeychain, err := authprompt.SaveCircleCIToken(token, circleCIBaseURL, insecureStorage)
	if err != nil {
		return &userError{
			msg:        "Failed to save CircleCI token.",
			suggestion: "Check that your config file is writable.",
			err:        fmt.Errorf("save token: %w", err),
		}
	}

	streams.ErrPrintln("")
	printSaved(streams, "CircleCI token", savedToKeychain)
	return nil
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check authentication status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)

			io.Println("")
			io.Println(ui.Bold("Chunk CLI - Authentication Status"))
			io.Println("")

			rc, resolveErr := config.Resolve("", "")
			if resolveErr != nil {
				io.ErrPrintln(ui.Warning(fmt.Sprintf("Could not load config: %v", resolveErr)))
			}

			hasFailure := false

			// CircleCI section
			io.Println(ui.Bold("CircleCI"))
			if rc.CircleCIToken == "" {
				io.Println("  Not set")
			} else {
				io.Printf("  Source: %s\n", rc.CircleCITokenSource)
				io.Printf("  Token:  %s\n", config.MaskKey(rc.CircleCIToken))
				io.ErrPrintln(ui.Dim("Validating CircleCI token..."))
				if err := authprompt.ValidateCircleCIToken(cmd.Context(), rc.CircleCIToken, rc.CircleCIBaseURL); err != nil {
					io.ErrPrintln(ui.FormatError(
						"CircleCI token validation failed.",
						"",
						"Run `chunk auth set circleci` to set a new token.",
					))
					hasFailure = true
				} else {
					io.Println(ui.Success("Valid"))
				}
			}
			io.Println("")

			// Anthropic section
			io.Println(ui.Bold("Anthropic"))
			if rc.AnthropicAPIKey == "" {
				io.Println("  Not set")
			} else {
				io.Printf("  Source: %s\n", rc.AnthropicAPIKeySource)
				io.Printf("  Key:    %s\n", config.MaskKey(rc.AnthropicAPIKey))
				io.ErrPrintln(ui.Dim("Validating API key..."))
				if err := authprompt.ValidateAPIKey(cmd.Context(), rc.AnthropicAPIKey, rc.AnthropicBaseURL); err != nil {
					io.ErrPrintln(ui.FormatError(
						"API key validation failed.",
						"The API key could not be validated with the Anthropic API.",
						"Run `chunk auth set anthropic` to set a new key.",
					))
					hasFailure = true
				} else {
					io.Println(ui.Success("Valid"))
				}
			}
			io.Println("")

			// GitHub section
			io.Println(ui.Bold("GitHub"))
			if rc.GitHubToken == "" {
				io.Println("  Not set")
				io.Println(ui.Dim("  Run `chunk auth set github` to configure."))
			} else {
				io.Printf("  Source: %s\n", rc.GitHubTokenSource)
				io.Printf("  Token:  %s\n", config.MaskKey(rc.GitHubToken))
				io.ErrPrintln(ui.Dim("Validating GitHub token..."))
				if err := authprompt.ValidateGitHubToken(cmd.Context(), rc.GitHubToken, rc.GitHubAPIURL); err != nil {
					io.ErrPrintln(ui.FormatError(
						"GitHub token validation failed.",
						"",
						"Run `chunk auth set github` to set a new token.",
					))
					hasFailure = true
				} else {
					io.Println(ui.Success("Valid"))
				}
			}
			io.Println("")

			if hasFailure {
				return &userError{msg: "One or more credential checks failed.", errMsg: "auth status: validation failures"}
			}
			return nil
		},
	}
}

func newAuthRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "remove <provider>",
		Short:     "Remove stored credentials",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"circleci", "anthropic", "github"},
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, _ := config.Resolve("", "")
			provider := args[0]
			io := iostream.FromCmd(cmd)
			switch provider {
			case providerCircleCI:
				envSet := strings.HasPrefix(rc.CircleCITokenSource, "Environment")
				return authRemoveCircleCI(io, envSet)
			case providerAnthropic:
				envSet := strings.HasPrefix(rc.AnthropicAPIKeySource, "Environment")
				return authRemoveAnthropic(io, envSet)
			case providerGitHub:
				envSet := strings.HasPrefix(rc.GitHubTokenSource, "Environment")
				return authRemoveGitHub(io, envSet)
			default:
				return &userError{
					msg:    fmt.Sprintf("Unknown provider %q.", provider),
					detail: "Valid providers: circleci, anthropic, github.",
					errMsg: fmt.Sprintf("unknown provider %q", provider),
				}
			}
		},
	}
}

func authRemoveCircleCI(io iostream.Streams, envSet bool) error {
	cfg, err := config.Load()
	if err != nil {
		return &userError{msg: "Could not load configuration.", suggestion: configFilePermHint, err: err}
	}
	if cfg.CircleCIToken == "" {
		io.Println(ui.Warning("No CircleCI token stored in config file."))
		if envSet {
			io.Println("Note: A CircleCI token is set in environment variables.")
			io.Println("To remove it, unset the environment variable.")
			io.Println("")
		}
		return nil
	}

	io.Println("")
	cfgPath, err := config.Path()
	if err != nil {
		return &userError{msg: "Could not access configuration.", err: err}
	}
	io.Printf("This will remove your stored CircleCI token from %s\n", cfgPath)
	confirmed, err := tui.Confirm("Are you sure?", false)
	if err != nil || !confirmed {
		io.Println("")
		io.Println("Cancelled.")
		io.Println("")
		return nil
	}

	if err := config.Clear("circleCIToken"); err != nil {
		hint := configFilePermHint
		if errPath, pathErr := config.Path(); pathErr == nil {
			hint = fmt.Sprintf("Check file permissions on %s.", errPath)
		}
		return &userError{
			msg:        "Failed to remove CircleCI token.",
			detail:     "An error occurred while trying to remove the token from the config file.",
			suggestion: hint,
			err:        err,
		}
	}

	io.Println(ui.Success("CircleCI token removed successfully."))
	if envSet {
		io.Println(ui.Warning("Note: " + config.EnvCircleToken + "/" + config.EnvCircleCIToken + " is still set in your environment variables."))
	}
	return nil
}

func authRemoveAnthropic(io iostream.Streams, envSet bool) error {
	cfg, err := config.Load()
	if err != nil {
		return &userError{msg: "Could not load configuration.", suggestion: configFilePermHint, err: err}
	}
	if cfg.AnthropicAPIKey == "" {
		io.Println(ui.Warning("No API key stored in config file."))
		if envSet {
			io.Println("Note: " + config.EnvAnthropicAPIKey + " is set in your environment variables.")
			io.Println("To remove it, unset the environment variable.")
			io.Println("")
		}
		return nil
	}

	io.Println("")
	cfgPath, err := config.Path()
	if err != nil {
		return &userError{msg: "Could not access configuration.", err: err}
	}
	io.Printf("This will remove your stored API key from %s\n", cfgPath)
	confirmed, err := tui.Confirm("Are you sure?", false)
	if err != nil || !confirmed {
		io.Println("")
		io.Println("Cancelled.")
		io.Println("")
		return nil
	}

	if err := config.Clear("anthropicAPIKey"); err != nil {
		hint := configFilePermHint
		if errPath, pathErr := config.Path(); pathErr == nil {
			hint = fmt.Sprintf("Check file permissions on %s.", errPath)
		}
		return &userError{
			msg:        "Failed to remove API key.",
			detail:     "An error occurred while trying to remove the API key from the config file.",
			suggestion: hint,
			err:        err,
		}
	}

	io.Println(ui.Success("API key removed successfully."))
	if envSet {
		io.Println(ui.Warning("Note: " + config.EnvAnthropicAPIKey + " is still set in your environment variables."))
	}
	return nil
}

func authSetGitHub(ctx context.Context, io iostream.Streams, rc config.ResolvedConfig, insecureStorage bool) error {
	envSet := strings.HasPrefix(rc.GitHubTokenSource, "Environment")
	io.Println("")
	io.Println(ui.Bold("Chunk CLI - GitHub Token Setup"))
	io.Println("")
	io.Println("Create a token at https://github.com/settings/tokens")
	printSaveHint(io, "Token", insecureStorage)
	io.Println("")

	if envSet {
		io.Println(ui.Warning("A GitHub token is set in environment variables (" + config.EnvGitHubToken + ")."))
		io.Println(ui.Dim("Environment variables take precedence over stored config."))
		io.Println("")
	}

	if rc.GitHubToken != "" && !envSet {
		io.Printf("A GitHub token is already stored.\n")
		replace, err := tui.Confirm("Do you want to replace it?", false)
		if err != nil {
			io.Println("Cancelled.")
			return nil
		}
		if !replace {
			io.Println("Keeping existing token.")
			return nil
		}
	}

	token, err := tui.PromptHidden("GitHub Token")
	if err != nil {
		return nil
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return &userError{
			msg:        "Token cannot be empty.",
			suggestion: "Create a token at https://github.com/settings/tokens",
			errMsg:     "empty github token",
		}
	}

	io.ErrPrintln(ui.Dim("Validating GitHub token..."))
	if err := authprompt.ValidateGitHubToken(ctx, token, rc.GitHubAPIURL); err != nil {
		return &userError{
			msg:        "GitHub token validation failed.",
			suggestion: "Check that your token is correct and has not been revoked.",
			err:        err,
		}
	}

	savedToKeychain, err := authprompt.SaveGitHubToken(token, rc.GitHubAPIURL, insecureStorage)
	if err != nil {
		return &userError{msg: "Could not save credentials.", suggestion: configFilePermHint, err: err}
	}

	io.Println("")
	printSaved(io, "GitHub token", savedToKeychain)
	return nil
}

func authRemoveGitHub(io iostream.Streams, envSet bool) error {
	cfg, err := config.Load()
	if err != nil {
		return &userError{msg: "Could not load configuration.", suggestion: configFilePermHint, err: err}
	}
	if cfg.GitHubToken == "" {
		io.Println(ui.Warning("No GitHub token stored in config file."))
		if envSet {
			io.Println("Note: A GitHub token is set in environment variables.")
			io.Println("To remove it, unset the environment variable.")
			io.Println("")
		}
		return nil
	}

	io.Println("")
	cfgPath, err := config.Path()
	if err != nil {
		return &userError{msg: "Could not access configuration.", err: err}
	}
	io.Printf("This will remove your stored GitHub token from %s\n", cfgPath)
	confirmed, err := tui.Confirm("Are you sure?", false)
	if err != nil || !confirmed {
		io.Println("")
		io.Println("Cancelled.")
		io.Println("")
		return nil
	}

	if err := config.Clear("gitHubToken"); err != nil {
		hint := configFilePermHint
		if errPath, pathErr := config.Path(); pathErr == nil {
			hint = fmt.Sprintf("Check file permissions on %s.", errPath)
		}
		return &userError{
			msg:        "Failed to remove GitHub token.",
			detail:     "An error occurred while trying to remove the token from the config file.",
			suggestion: hint,
			err:        err,
		}
	}

	io.Println(ui.Success("GitHub token removed successfully."))
	if envSet {
		io.Println(ui.Warning("Note: " + config.EnvGitHubToken + " is still set in your environment variables."))
	}
	return nil
}
