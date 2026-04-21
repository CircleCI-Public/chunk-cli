package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/usererr"
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
	return &cobra.Command{
		Use:       "set <provider>",
		Short:     "Store credentials for a provider",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"circleci", "anthropic", "github"},
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := args[0]
			io := iostream.FromCmd(cmd)
			switch provider {
			case providerCircleCI:
				circleCIBaseURL := os.Getenv("CIRCLECI_BASE_URL")
				circleTokenEnv := os.Getenv("CIRCLE_TOKEN")
				if circleTokenEnv == "" {
					circleTokenEnv = os.Getenv("CIRCLECI_TOKEN")
				}
				return authSetCircleCI(cmd.Context(), io, circleCIBaseURL, circleTokenEnv)
			case providerAnthropic:
				anthropicBaseURL := os.Getenv("ANTHROPIC_BASE_URL")
				anthropicKeyEnv := os.Getenv("ANTHROPIC_API_KEY")
				return authSetAnthropic(cmd.Context(), io, anthropicBaseURL, anthropicKeyEnv)
			case providerGitHub:
				githubBaseURL := os.Getenv("GITHUB_API_URL")
				githubTokenEnv := os.Getenv("GITHUB_TOKEN")
				return authSetGitHub(cmd.Context(), io, githubBaseURL, githubTokenEnv)
			default:
				return usererr.Newf(
					fmt.Sprintf("Unknown provider %q. Valid providers: circleci, anthropic, github.", provider),
					"unknown provider %q", provider,
				)
			}
		},
	}
}

func authSetCircleCI(ctx context.Context, io iostream.Streams, circleCIBaseURL, circleTokenEnv string) error {
	io.Println("")
	io.Println(ui.Bold("Chunk CLI - CircleCI Token Setup"))
	io.Println("")
	io.Println("Create a CircleCI token at https://app.circleci.com/settings/user/tokens")
	printSaveHint(io, "Token")
	io.Println("")

	if circleTokenEnv != "" {
		io.Println(ui.Warning("A CircleCI token is set in environment variables (CIRCLE_TOKEN/CIRCLECI_TOKEN)."))
		io.Println(ui.Dim("Environment variables take precedence over stored config."))
		io.Println("")
	}

	cfg, err := config.Load()
	if err != nil {
		return usererr.New("Could not load configuration. "+configFilePermHint, err)
	}
	if cfg.CircleCIToken != "" {
		io.Printf("A CircleCI token is already stored in config.\n")
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
		return usererr.New(
			ui.FormatError("Token cannot be empty.", "", "Create a token at https://app.circleci.com/settings/user/tokens"),
			fmt.Errorf("empty circleci token"),
		)
	}

	return saveCircleCIToken(ctx, token, io, circleCIBaseURL)
}

func authSetAnthropic(ctx context.Context, io iostream.Streams, anthropicBaseURL, anthropicKeyEnv string) error {
	io.Println("")
	io.Println(ui.Bold("Chunk CLI - Anthropic API Key Setup"))
	io.Println("")
	io.Println("Enter your Anthropic API key (starts with sk-ant-).")
	printSaveHint(io, "Key")
	io.Println("")
	if anthropicKeyEnv != "" {
		io.Println(ui.Warning("An Anthropic API key is set in environment variables (ANTHROPIC_API_KEY)."))
		io.Println(ui.Dim("Environment variables take precedence over stored config."))
		io.Println("")
	}

	cfg, err := config.Load()
	if err != nil {
		return usererr.New("Could not load configuration. "+configFilePermHint, err)
	}
	if cfg.AnthropicAPIKey != "" {
		io.Printf("An Anthropic API key is already stored in config.\n")
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
		return usererr.New(
			ui.FormatError("API key cannot be empty.", "", "Get an API key from https://console.anthropic.com/"),
			fmt.Errorf("empty anthropic key"),
		)
	}

	if !strings.HasPrefix(key, "sk-ant-") {
		return usererr.New(
			ui.FormatError("Invalid API key format.", "Keys should start with \"sk-ant-\".", "Get a valid API key from https://console.anthropic.com/"),
			fmt.Errorf("invalid anthropic key format"),
		)
	}

	io.ErrPrintln(ui.Dim("Validating API key..."))
	if err := authprompt.ValidateAPIKey(ctx, key, anthropicBaseURL); err != nil {
		return usererr.New(
			ui.FormatError("API key validation failed.", "", "Check that your key is correct and has not been revoked."),
			err,
		)
	}

	cfg, err = config.Load()
	if err != nil {
		return usererr.New("Could not load configuration. "+configFilePermHint, err)
	}
	cfg.AnthropicAPIKey = key
	if err := config.Save(cfg); err != nil {
		return usererr.New("Could not save credentials. "+configFilePermHint, err)
	}

	io.Println("")
	printSaved(io, "Anthropic API key")
	io.Println(ui.Dim("You can now run code reviews with: chunk build-prompt"))
	return nil
}

func saveCircleCIToken(ctx context.Context, token string, streams iostream.Streams, circleCIBaseURL string) error {
	streams.ErrPrintln(ui.Dim("Validating CircleCI token..."))
	if err := authprompt.ValidateCircleCIToken(ctx, token, circleCIBaseURL); err != nil {
		return usererr.New(
			ui.FormatError("CircleCI token validation failed.", "", "Check that your token is correct."),
			fmt.Errorf("validate token: %w", err),
		)
	}

	cfg, err := config.Load()
	if err != nil {
		return usererr.New("Could not load configuration. "+configFilePermHint, err)
	}
	cfg.CircleCIToken = token
	if err := config.Save(cfg); err != nil {
		return usererr.New(
			ui.FormatError("Failed to save CircleCI token.", "", "Check that your config file is writable."),
			fmt.Errorf("save token: %w", err),
		)
	}

	streams.ErrPrintln("")
	printSaved(streams, "CircleCI token")
	return nil
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check authentication status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			circleCIBaseURL := os.Getenv("CIRCLECI_BASE_URL")
			anthropicBaseURL := os.Getenv("ANTHROPIC_BASE_URL")
			githubBaseURL := os.Getenv("GITHUB_API_URL")

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
				if err := authprompt.ValidateCircleCIToken(cmd.Context(), rc.CircleCIToken, circleCIBaseURL); err != nil {
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
				if err := authprompt.ValidateAPIKey(cmd.Context(), rc.AnthropicAPIKey, anthropicBaseURL); err != nil {
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
				if err := authprompt.ValidateGitHubToken(cmd.Context(), rc.GitHubToken, githubBaseURL); err != nil {
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
				return usererr.Newf("One or more credential checks failed.", "auth status: validation failures")
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
			provider := args[0]
			io := iostream.FromCmd(cmd)
			switch provider {
			case providerCircleCI:
				circleTokenEnv := os.Getenv("CIRCLE_TOKEN")
				if circleTokenEnv == "" {
					circleTokenEnv = os.Getenv("CIRCLECI_TOKEN")
				}
				return authRemoveCircleCI(io, circleTokenEnv)
			case providerAnthropic:
				anthropicKeyEnv := os.Getenv("ANTHROPIC_API_KEY")
				return authRemoveAnthropic(io, anthropicKeyEnv)
			case providerGitHub:
				githubTokenEnv := os.Getenv("GITHUB_TOKEN")
				return authRemoveGitHub(io, githubTokenEnv)
			default:
				return usererr.Newf(
					fmt.Sprintf("Unknown provider %q. Valid providers: circleci, anthropic, github.", provider),
					"unknown provider %q", provider,
				)
			}
		},
	}
}

func authRemoveCircleCI(io iostream.Streams, circleTokenEnv string) error {
	cfg, err := config.Load()
	if err != nil {
		return usererr.New("Could not load configuration. "+configFilePermHint, err)
	}
	if cfg.CircleCIToken == "" {
		io.Println(ui.Warning("No CircleCI token stored in config file."))
		if circleTokenEnv != "" {
			io.Println("Note: A CircleCI token is set in environment variables.")
			io.Println("To remove it, unset the environment variable.")
			io.Println("")
		}
		return nil
	}

	io.Println("")
	cfgPath, err := config.Path()
	if err != nil {
		return usererr.New("Could not access configuration.", err)
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
		return usererr.New(
			ui.FormatError("Failed to remove CircleCI token.", "An error occurred while trying to remove the token from the config file.", hint),
			err,
		)
	}

	io.Println(ui.Success("CircleCI token removed successfully."))
	if circleTokenEnv != "" {
		io.Println(ui.Warning("Note: CIRCLE_TOKEN/CIRCLECI_TOKEN is still set in your environment variables."))
	}
	return nil
}

func authRemoveAnthropic(io iostream.Streams, anthropicKeyEnv string) error {
	cfg, err := config.Load()
	if err != nil {
		return usererr.New("Could not load configuration. "+configFilePermHint, err)
	}
	if cfg.AnthropicAPIKey == "" {
		io.Println(ui.Warning("No API key stored in config file."))
		if anthropicKeyEnv != "" {
			io.Println("Note: ANTHROPIC_API_KEY is set in your environment variables.")
			io.Println("To remove it, unset the environment variable.")
			io.Println("")
		}
		return nil
	}

	io.Println("")
	cfgPath, err := config.Path()
	if err != nil {
		return usererr.New("Could not access configuration.", err)
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
		return usererr.New(
			ui.FormatError("Failed to remove API key.", "An error occurred while trying to remove the API key from the config file.", hint),
			err,
		)
	}

	io.Println(ui.Success("API key removed successfully."))
	if anthropicKeyEnv != "" {
		io.Println(ui.Warning("Note: ANTHROPIC_API_KEY is still set in your environment variables."))
	}
	return nil
}

func authSetGitHub(ctx context.Context, io iostream.Streams, githubBaseURL, githubTokenEnv string) error {
	io.Println("")
	io.Println(ui.Bold("Chunk CLI - GitHub Token Setup"))
	io.Println("")
	io.Println("Create a token at https://github.com/settings/tokens")
	printSaveHint(io, "Token")
	io.Println("")

	if githubTokenEnv != "" {
		io.Println(ui.Warning("A GitHub token is set in environment variables (GITHUB_TOKEN)."))
		io.Println(ui.Dim("Environment variables take precedence over stored config."))
		io.Println("")
	}

	cfg, err := config.Load()
	if err != nil {
		return usererr.New("Could not load configuration. "+configFilePermHint, err)
	}
	if cfg.GitHubToken != "" {
		io.Printf("A GitHub token is already stored in config.\n")
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
		return usererr.New(
			ui.FormatError("Token cannot be empty.", "", "Create a token at https://github.com/settings/tokens"),
			fmt.Errorf("empty github token"),
		)
	}

	io.ErrPrintln(ui.Dim("Validating GitHub token..."))
	if err := authprompt.ValidateGitHubToken(ctx, token, githubBaseURL); err != nil {
		return usererr.New(
			ui.FormatError("GitHub token validation failed.", "", "Check that your token is correct and has not been revoked."),
			err,
		)
	}

	cfg, err = config.Load()
	if err != nil {
		return usererr.New("Could not load configuration. "+configFilePermHint, err)
	}
	cfg.GitHubToken = token
	if err := config.Save(cfg); err != nil {
		return usererr.New("Could not save credentials. "+configFilePermHint, err)
	}

	io.Println("")
	printSaved(io, "GitHub token")
	return nil
}

func authRemoveGitHub(io iostream.Streams, githubTokenEnv string) error {
	cfg, err := config.Load()
	if err != nil {
		return usererr.New("Could not load configuration. "+configFilePermHint, err)
	}
	if cfg.GitHubToken == "" {
		io.Println(ui.Warning("No GitHub token stored in config file."))
		if githubTokenEnv != "" {
			io.Println("Note: A GitHub token is set in environment variables.")
			io.Println("To remove it, unset the environment variable.")
			io.Println("")
		}
		return nil
	}

	io.Println("")
	cfgPath, err := config.Path()
	if err != nil {
		return usererr.New("Could not access configuration.", err)
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
		return usererr.New(
			ui.FormatError("Failed to remove GitHub token.", "An error occurred while trying to remove the token from the config file.", hint),
			err,
		)
	}

	io.Println(ui.Success("GitHub token removed successfully."))
	if githubTokenEnv != "" {
		io.Println(ui.Warning("Note: GITHUB_TOKEN is still set in your environment variables."))
	}
	return nil
}
