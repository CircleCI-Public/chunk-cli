package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

const (
	apiKeySourceEnvVar = "Environment variable"
	providerCircleCI   = "circleci"
	providerAnthropic  = "anthropic"
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
		Use:       "set [provider]",
		Short:     "Store credentials (default: circleci)",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"circleci", "anthropic"},
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := providerCircleCI
			if len(args) > 0 {
				provider = args[0]
			}
			io := iostream.FromCmd(cmd)
			switch provider {
			case providerCircleCI:
				return authSetCircleCI(cmd.Context(), io)
			case providerAnthropic:
				return authSetAnthropic(cmd.Context(), io)
			default:
				return fmt.Errorf("unknown provider %q: valid providers are circleci, anthropic", provider)
			}
		},
	}
}

func authSetCircleCI(ctx context.Context, io iostream.Streams) error {
	io.Println("")
	io.Println(ui.Bold("Chunk CLI - CircleCI Token Setup"))
	io.Println("")
	io.Println("Create a CircleCI token at https://app.circleci.com/settings/user/tokens")
	io.Println("")

	if os.Getenv("CIRCLE_TOKEN") != "" || os.Getenv("CIRCLECI_TOKEN") != "" {
		io.Println(ui.Warning("A CircleCI token is set in environment variables (CIRCLE_TOKEN/CIRCLECI_TOKEN)."))
		io.Println(ui.Dim("Environment variables take precedence over stored config."))
		io.Println("")
	}

	cfg, _ := config.Load()
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

	if err := saveCircleCIToken(ctx, token, io); err != nil {
		return ErrSilent
	}
	return nil
}

func authSetAnthropic(ctx context.Context, io iostream.Streams) error {
	io.Println("")
	io.Println(ui.Bold("Chunk CLI - Anthropic API Key Setup"))
	io.Println("")
	io.Println("Enter your Anthropic API key (starts with sk-ant-).")
	io.Println(ui.Dim("The key will be stored securely and never displayed."))
	io.Println("")

	rc := config.Resolve("", "")
	if rc.APIKey != "" {
		io.Printf("An API key is already configured (source: %s)\n", sourceLabel(rc.APIKeySource))
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
	if err := validateAPIKey(ctx, key); err != nil {
		io.ErrPrintln(ui.FormatError("API key validation failed.", "", "Check that your key is correct and has not been revoked."))
		return ErrSilent
	}

	cfg, _ := config.Load()
	cfg.APIKey = key
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
func saveCircleCIToken(ctx context.Context, token string, streams iostream.Streams) error {
	streams.ErrPrintln(ui.Dim("Validating CircleCI token..."))
	if err := validateCircleCIToken(ctx, token); err != nil {
		streams.ErrPrintln(ui.FormatError("CircleCI token validation failed.", "", "Check that your token is correct."))
		return fmt.Errorf("validate token: %w", err)
	}

	cfg, _ := config.Load()
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

func validateCircleCIToken(ctx context.Context, token string) error {
	cl, err := circleci.NewClientWithToken(token, os.Getenv("CIRCLECI_BASE_URL"))
	if err != nil {
		return err
	}
	if err := cl.GetCurrentUser(ctx); err != nil {
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

			io.Println("")
			io.Println(ui.Bold("Chunk CLI - Authentication Status"))
			io.Println("")

			rc := config.Resolve("", "")

			hasFailure := false

			// CircleCI section
			io.Println(ui.Bold("CircleCI"))
			if rc.CircleCIToken == "" {
				io.Println("  Not set")
			} else {
				io.Printf("  Source: %s\n", rc.CircleCITokenSource)
				io.Printf("  Token:  %s\n", config.MaskAPIKey(rc.CircleCIToken))
				io.ErrPrintln(ui.Dim("Validating CircleCI token..."))
				if err := validateCircleCIToken(cmd.Context(), rc.CircleCIToken); err != nil {
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
			if rc.APIKey == "" {
				io.Println("  Not set")
			} else {
				io.Printf("  Source: %s\n", sourceLabel(rc.APIKeySource))
				io.Printf("  Key:    %s\n", config.MaskAPIKey(rc.APIKey))
				io.ErrPrintln(ui.Dim("Validating API key..."))
				if err := validateAPIKey(cmd.Context(), rc.APIKey); err != nil {
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
			if os.Getenv("GITHUB_TOKEN") != "" {
				io.Println("  Set (via GITHUB_TOKEN)")
			} else {
				io.Println("  Not set")
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
				return authRemoveCircleCI(io)
			case providerAnthropic:
				return authRemoveAnthropic(io)
			default:
				return fmt.Errorf("unknown provider %q: valid providers are circleci, anthropic", provider)
			}
		},
	}
}

func authRemoveCircleCI(io iostream.Streams) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.CircleCIToken == "" {
		io.Println(ui.Warning("No CircleCI token stored in config file."))
		if os.Getenv("CIRCLE_TOKEN") != "" || os.Getenv("CIRCLECI_TOKEN") != "" {
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

	if err := config.ClearCircleCIToken(); err != nil {
		hint := "Check file permissions on the chunk config file"
		if errPath, pathErr := config.Path(); pathErr == nil {
			hint = fmt.Sprintf("Check file permissions on %s", errPath)
		}
		io.ErrPrintln(ui.FormatError("Failed to remove CircleCI token.", "An error occurred while trying to remove the token from the config file.", hint))
		return ErrSilent
	}

	io.Println(ui.Success("CircleCI token removed successfully."))
	if os.Getenv("CIRCLE_TOKEN") != "" || os.Getenv("CIRCLECI_TOKEN") != "" {
		io.Println(ui.Warning("Note: CIRCLE_TOKEN/CIRCLECI_TOKEN is still set in your environment variables."))
	}
	return nil
}

func authRemoveAnthropic(io iostream.Streams) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.APIKey == "" {
		io.Println(ui.Warning("No API key stored in config file."))
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
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

	if err := config.ClearAPIKey(); err != nil {
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
	return nil
}

func validateAPIKey(ctx context.Context, apiKey string) error {
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
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

func sourceLabel(source string) string {
	switch source {
	case config.SourceConfigFile:
		return "Config file"
	case apiKeySourceEnvVar:
		return apiKeySourceEnvVar
	default:
		return source
	}
}
