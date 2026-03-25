package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/httpcl"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

const apiKeySourceEnvVar = "Environment variable"

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication",
	}
	cmd.AddCommand(newAuthLoginCmd())
	cmd.AddCommand(newAuthStatusCmd())
	cmd.AddCommand(newAuthLogoutCmd())
	return cmd
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

			io.Printf("\n%s\n", ui.Success(fmt.Sprintf("API key validated and saved to %s", config.Path())))
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
				io.Printf("%s Config file (%s)\n", ui.Label("API key source:", w), config.Path())
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
			io.Printf("This will remove your stored API key from %s\n", config.Path())
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
				io.ErrPrintln(ui.FormatError(
					"Failed to remove API key.",
					"An error occurred while trying to remove the API key from the config file.",
					fmt.Sprintf("Check file permissions on %s", config.Path()),
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
