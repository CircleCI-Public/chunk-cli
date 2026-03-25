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
		RunE: func(cmd *cobra.Command, args []string) error {
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
		Model:    config.DefaultModel,
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
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			rc := config.Resolve("", "")

			if rc.APIKey == "" {
				io.Println(ui.Warning("Not authenticated"))
				return nil
			}

			io.Printf("%s %s %s\n",
				ui.Success("Authenticated:"),
				config.MaskAPIKey(rc.APIKey),
				ui.Dim("("+sourceLabel(rc.APIKeySource)+")"))
			return nil
		},
	}
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear stored API key",
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.APIKey == "" {
				io.Println(ui.Dim("No API key stored in config file"))
				return nil
			}
			if err := config.ClearAPIKey(); err != nil {
				return err
			}
			io.Println(ui.Success("API key removed from config file"))
			return nil
		},
	}
}

func sourceLabel(source string) string {
	switch source {
	case "Config file (user config)":
		return "Config file"
	case "Environment variable":
		return "Environment variable"
	default:
		return source
	}
}
