package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

const (
	providerCircleCI  = "circleci"
	providerAnthropic = "anthropic"
)

// ErrSilent is returned when a command has already reported its error to the user
// and wants to exit non-zero without printing anything further.
var ErrSilent = errors.New("silent")

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
		ValidArgs: []string{"circleci", "anthropic"},
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
			default:
				return fmt.Errorf("unknown provider %q: valid providers are circleci, anthropic", provider)
			}
		},
	}
}

func authSetCircleCI(ctx context.Context, io iostream.Streams, circleCIBaseURL, circleTokenEnv string) error {
	io.Println("")
	io.Println(ui.Bold("Chunk CLI - CircleCI Token Setup"))
	io.Println("")
	io.Println("Create a CircleCI token at https://app.circleci.com/settings/user/tokens")
	io.Println("")

	if circleTokenEnv != "" {
		io.Println(ui.Warning("A CircleCI token is set in environment variables (CIRCLE_TOKEN/CIRCLECI_TOKEN)."))
		io.Println(ui.Dim("Environment variables take precedence over stored config."))
		io.Println("")
	}

	cfg, err := config.Load()
	if err != nil {
		io.ErrPrintln(ui.Warning(fmt.Sprintf("Could not load config: %v", err)))
		return fmt.Errorf("load config: %w", err)
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
		io.ErrPrintln(ui.FormatError("Token cannot be empty.", "", "Create a token at https://app.circleci.com/settings/user/tokens"))
		return ErrSilent
	}

	if err := saveCircleCIToken(ctx, token, io, circleCIBaseURL); err != nil {
		return ErrSilent
	}
	return nil
}

func authSetAnthropic(ctx context.Context, io iostream.Streams, anthropicBaseURL, anthropicKeyEnv string) error {
	io.Println("")
	io.Println(ui.Bold("Chunk CLI - Anthropic API Key Setup"))
	io.Println("")
	io.Println("Enter your Anthropic API key (starts with sk-ant-).")
	io.Println(ui.Dim("The key will be stored securely and never displayed."))
	io.Println("")
	if anthropicKeyEnv != "" {
		io.Println(ui.Warning("An Anthropic API key is set in environment variables (ANTHROPIC_API_KEY)."))
		io.Println(ui.Dim("Environment variables take precedence over stored config."))
		io.Println("")
	}

	cfg, err := config.Load()
	if err != nil {
		io.ErrPrintln(ui.Warning(fmt.Sprintf("Could not load config: %v", err)))
		return fmt.Errorf("load config: %w", err)
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
		io.ErrPrintln(ui.FormatError("API key cannot be empty.", "", "Get an API key from https://console.anthropic.com/"))
		return ErrSilent
	}

	if !strings.HasPrefix(key, "sk-ant-") {
		io.ErrPrintln(ui.FormatError("Invalid API key format.", "Keys should start with \"sk-ant-\".", "Get a valid API key from https://console.anthropic.com/"))
		return ErrSilent
	}

	io.ErrPrintln(ui.Dim("Validating API key..."))
	if err := validateAPIKey(ctx, key, anthropicBaseURL); err != nil {
		io.ErrPrintln(ui.FormatError("API key validation failed.", "", "Check that your key is correct and has not been revoked."))
		return ErrSilent
	}

	cfg, err = config.Load()
	if err != nil {
		return fmt.Errorf("load config before save: %w", err)
	}
	cfg.AnthropicAPIKey = key
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save API key: %w", err)
	}

	cfgPath, err := config.Path()
	if err != nil {
		return err
	}
	io.Printf("\n%s\n", ui.Success(fmt.Sprintf("API key validated and saved to %s", cfgPath)))
	io.Println(ui.Dim("You can now run code reviews with: chunk build-prompt"))
	return nil
}

// saveCircleCIToken validates and saves a CircleCI token to user config.
// It prints status messages to streams and returns an error if anything fails.
func saveCircleCIToken(ctx context.Context, token string, streams iostream.Streams, circleCIBaseURL string) error {
	streams.ErrPrintln(ui.Dim("Validating CircleCI token..."))
	if err := validateCircleCIToken(ctx, token, circleCIBaseURL); err != nil {
		streams.ErrPrintln(ui.FormatError("CircleCI token validation failed.", "", "Check that your token is correct."))
		return fmt.Errorf("validate token: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		streams.ErrPrintln(ui.Warning(fmt.Sprintf("Could not load config: %v", err)))
		return fmt.Errorf("load config: %w", err)
	}
	cfg.CircleCIToken = token
	if err := config.Save(cfg); err != nil {
		streams.ErrPrintln(ui.FormatError("Failed to save CircleCI token.", "", "Check that your config file is writable."))
		return fmt.Errorf("save token: %w", err)
	}

	cfgPath, err := config.Path()
	if err != nil {
		return err
	}
	streams.ErrPrintf("\n%s\n", ui.Success(fmt.Sprintf("CircleCI token validated and saved to %s", cfgPath)))
	return nil
}

func validateCircleCIToken(ctx context.Context, token, baseURL string) error {
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

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check authentication status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			circleCIBaseURL := os.Getenv("CIRCLECI_BASE_URL")
			anthropicBaseURL := os.Getenv("ANTHROPIC_BASE_URL")

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
				if err := validateCircleCIToken(cmd.Context(), rc.CircleCIToken, circleCIBaseURL); err != nil {
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
				if err := validateAPIKey(cmd.Context(), rc.AnthropicAPIKey, anthropicBaseURL); err != nil {
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

			// GitHub section (env-var only, no auth set/remove support)
			io.Println(ui.Bold("GitHub"))
			if rc.GitHubToken != "" {
				io.Println("  Set (via GITHUB_TOKEN)")
			} else {
				io.Println("  Not set")
				io.Println(ui.Dim("  Set the GITHUB_TOKEN environment variable to configure."))
			}
			io.Println("")

			if hasFailure {
				return ErrSilent
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
		ValidArgs: []string{"circleci", "anthropic"},
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
			default:
				return fmt.Errorf("unknown provider %q: valid providers are circleci, anthropic", provider)
			}
		},
	}
}

func authRemoveCircleCI(io iostream.Streams, circleTokenEnv string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
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
		return err
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
		hint := "Check file permissions on the chunk config file"
		if errPath, pathErr := config.Path(); pathErr == nil {
			hint = fmt.Sprintf("Check file permissions on %s", errPath)
		}
		io.ErrPrintln(ui.FormatError("Failed to remove CircleCI token.", "An error occurred while trying to remove the token from the config file.", hint))
		return ErrSilent
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
		return err
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
		return err
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
		hint := "Check file permissions on the chunk config file"
		if errPath, pathErr := config.Path(); pathErr == nil {
			hint = fmt.Sprintf("Check file permissions on %s", errPath)
		}
		io.ErrPrintln(ui.FormatError(
			"Failed to remove API key.",
			"An error occurred while trying to remove the API key from the config file.",
			hint,
		))
		return ErrSilent
	}

	io.Println(ui.Success("API key removed successfully."))
	if anthropicKeyEnv != "" {
		io.Println(ui.Warning("Note: ANTHROPIC_API_KEY is still set in your environment variables."))
	}
	return nil
}

func validateAPIKey(ctx context.Context, apiKey, baseURL string) error {
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
