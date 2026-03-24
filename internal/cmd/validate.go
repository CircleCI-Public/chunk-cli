package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/httpcl"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

func newValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Run validation commands",
	}

	cmd.AddCommand(newValidateRunCmd())
	cmd.AddCommand(newValidateInitCmd())

	return cmd
}

func newValidateRunCmd() *cobra.Command {
	var sandboxID, orgID string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run validation",
		RunE: func(cmd *cobra.Command, args []string) error {
			workDir, err := os.Getwd()
			if err != nil {
				return err
			}

			cfg, err := validate.LoadProjectConfig(workDir)
			if err != nil {
				return fmt.Errorf("No validate commands configured. Run validate init first")
			}

			if dryRun {
				return validate.RunDryRun(cfg)
			}

			if sandboxID != "" {
				if orgID == "" {
					return fmt.Errorf("--org-id is required when using --sandbox-id")
				}
				client, err := circleci.NewClient()
				if err != nil {
					return err
				}
				return validate.RunRemote(cmd.Context(), client, cfg, sandboxID)
			}

			return validate.RunLocally(cfg, workDir)
		},
	}

	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID for remote execution")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print commands without executing")

	return cmd
}

var validProfiles = map[string]bool{
	"node":   true,
	"python": true,
	"go":     true,
	"ruby":   true,
	"java":   true,
	"rust":   true,
}

func newValidateInitCmd() *cobra.Command {
	var profile string
	var force, skipEnv bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize validation config",
		RunE: func(cmd *cobra.Command, args []string) error {
			workDir, err := os.Getwd()
			if err != nil {
				return err
			}

			gitCmd := exec.Command("git", "rev-parse", "--git-dir")
			gitCmd.Dir = workDir
			if err := gitCmd.Run(); err != nil {
				return fmt.Errorf("not a git repository")
			}

			if profile != "" {
				if !validProfiles[profile] {
					names := make([]string, 0, len(validProfiles))
					for k := range validProfiles {
						names = append(names, k)
					}
					return fmt.Errorf("Invalid profile %q. Valid profiles: %s", profile, strings.Join(names, ", "))
				}
			}

			configPath := filepath.Join(workDir, ".chunk", "config.json")
			if _, err := os.Stat(configPath); err == nil && !force {
				fmt.Println("Config already exists. Use --force to overwrite.")
				return nil
			}

			apiKey := os.Getenv("ANTHROPIC_API_KEY")
			if apiKey == "" {
				return fmt.Errorf("ANTHROPIC_API_KEY environment variable is required")
			}

			baseURL := os.Getenv("ANTHROPIC_BASE_URL")
			if baseURL == "" {
				baseURL = "https://api.anthropic.com"
			}

			testCmd, err := detectTestCommand(cmd.Context(), baseURL, apiKey, workDir)
			if err != nil {
				return fmt.Errorf("detect test command: %w", err)
			}

			chunkDir := filepath.Join(workDir, ".chunk")
			if err := os.MkdirAll(chunkDir, 0o755); err != nil {
				return err
			}

			config := map[string]string{
				"testCommand": testCmd,
			}

			data, err := json.MarshalIndent(config, "", "  ")
			if err != nil {
				return err
			}

			if err := os.WriteFile(configPath, data, 0o644); err != nil {
				return err
			}

			hookDir := filepath.Join(chunkDir, "hook")
			if err := os.MkdirAll(hookDir, 0o755); err != nil {
				return err
			}

			hookConfig := "# chunk hook configuration\nversion: 1\n"
			hookConfigPath := filepath.Join(hookDir, "config.yml")
			if err := os.WriteFile(hookConfigPath, []byte(hookConfig), 0o644); err != nil {
				return err
			}

			fmt.Println("Validation config initialized")
			_ = skipEnv
			return nil
		},
	}

	cmd.Flags().StringVar(&profile, "profile", "", "Language profile")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing config")
	cmd.Flags().BoolVar(&skipEnv, "skip-env", false, "Skip environment setup")

	return cmd
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
}

func detectTestCommand(ctx context.Context, baseURL, apiKey, workDir string) (string, error) {
	entries, _ := os.ReadDir(workDir)
	var files []string
	for _, e := range entries {
		files = append(files, e.Name())
	}

	reqBody := anthropicRequest{
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 256,
		Messages: []anthropicMessage{
			{
				Role:    "user",
				Content: fmt.Sprintf("Given a project with these files: %s\nWhat is the test command for this project? Reply with ONLY the command, no explanation.", strings.Join(files, ", ")),
			},
		},
	}

	cl := httpcl.New(httpcl.Config{
		BaseURL:    baseURL,
		AuthToken:  apiKey,
		AuthHeader: "x-api-key",
	})

	var resp anthropicResponse
	_, err := cl.Call(ctx, httpcl.NewRequest(http.MethodPost, "/v1/messages",
		httpcl.Body(reqBody),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return "", fmt.Errorf("anthropic API: %w", err)
	}

	if len(resp.Content) == 0 {
		return "npm test", nil
	}

	return strings.TrimSpace(resp.Content[0].Text), nil
}
