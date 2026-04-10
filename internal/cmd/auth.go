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

const apiKeySourceEnvVar = "Environment variable"

// ErrSilent is returned when a command has already reported its error to the user
// and wants to exit non-zero without printing anything further.
var ErrSilent = errors.New("silent")

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication",
	}
	cmd.AddCommand(newAuthSetCmd())
	cmd.AddCommand(newAuthLoginCmd())
	cmd.AddCommand(newAuthStatusCmd())
	cmd.AddCommand(newAuthLogoutCmd())
	return cmd
}

func newAuthSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "set <provider>",
		Short:     "Store credentials for a provider",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"circleci"},
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := args[0]
			io := iostream.FromCmd(cmd)
			switch provider {
			case "circleci":
				return authSetCircleCI(cmd.Context(), io)
			default:
				return fmt.Errorf("unknown provider %q: valid providers are circleci", provider)
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

// saveCircleCIToken validates and saves a CircleCI token to user config.
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
	baseURL := os.Getenv("CIRCLECI_BASE_URL")
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

func newAuthLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Store API key for authentication",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)

			io.Println("")
			io.Println(ui.Bold("Chunk CLI - API Key Setup"))
			io.Println("")
			io.Println("Enter your Anthropic API key (starts with sk-ant-).")
			io.Println(ui.Dim("The key will be stored securely and never displayed."))
			io.Println("")

			// Check for existing key
			rc := config.Resolve("", "")
			if rc.APIKey != "" {
				io.Printf("An API key is already configured (source: %s)\n",
					sourceLabel(rc.APIKeySource))
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
				os.Exit(2)
			}

			if !strings.HasPrefix(key, "sk-ant-") {
				io.ErrPrintln(ui.FormatError("Invalid API key format.", "Keys should start with \"sk-ant-\".", "Get a valid API key from https://console.anthropic.com/"))
				os.Exit(2)
			}

			io.ErrPrintln(ui.Dim("Validating API key..."))
			if err := validateAPIKey(cmd.Context(), key); err != nil {
				io.ErrPrintln(ui.FormatError("API key validation failed.", "", "Check that your key is correct and has not been revoked."))
				os.Exit(2)
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
		},
	}
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
		// Rate limit means the key is valid
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

			if rc.APIKey == "" {
				io.Println(ui.Warning("Not authenticated"))
				io.Println(ui.Dim("No API key found in config file or environment."))
				io.Println("")
				io.Println("To authenticate, run: chunk auth login")
				io.Println("Or set the ANTHROPIC_API_KEY environment variable.")
				io.Println("")
				return nil
			}

			w := 15 // align to "API key source:"
			switch rc.APIKeySource {
			case "Config file (user config)":
				cfgPath, err := config.Path()
				if err != nil {
					return err
				}
				io.Printf("%s Config file (%s)\n", ui.Label("API key source:", w), cfgPath)
			case apiKeySourceEnvVar:
				io.Printf("%s Environment variable (ANTHROPIC_API_KEY)\n", ui.Label("API key source:", w))
			default:
				io.Printf("%s %s\n", ui.Label("API key source:", w), rc.APIKeySource)
			}
			io.Printf("%s %s\n", ui.Label("API key:", w), config.MaskAPIKey(rc.APIKey))

			io.ErrPrintln(ui.Yellow("Validating API key..."))

			if err := validateAPIKey(cmd.Context(), rc.APIKey); err != nil {
				io.ErrPrintln("")
				io.ErrPrintln(ui.FormatError(
					"API key validation failed.",
					"The API key could not be validated with the Anthropic API.",
					"Run `chunk auth login` to set a new key.",
				))
				os.Exit(1)
			}

			io.Println("")
			io.Println(ui.Success("API key is valid"))
			return nil
		},
	}
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove stored credentials",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
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
			confirmed, err := tui.Confirm("Are you sure you want to logout?", false)
			if err != nil {
				io.Println("")
				io.Println("Logout cancelled.")
				io.Println("")
				return nil
			}
			if !confirmed {
				io.Println("")
				io.Println("Logout cancelled.")
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
				os.Exit(2)
			}

			io.Println(ui.Success("API key removed successfully."))
			return nil
		},
	}
}

func sourceLabel(source string) string {
	switch source {
	case "Config file (user config)":
		return "Config file"
	case apiKeySourceEnvVar:
		return apiKeySourceEnvVar
	default:
		return source
	}
}
