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
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/usererr"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

func newValidateCmd() *cobra.Command {
	var sandboxID, orgID string
	var dryRun, list, save, forceRun, status bool
	var inlineCmd, projectDir string

	cmd := &cobra.Command{
		Use:   "validate [name]",
		Short: "Run validation commands",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			streams := iostream.FromCmd(cmd)

			workDir := projectDir
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return err
				}
			}

			var name string
			if len(args) == 1 {
				name = args[0]
			}

			// Guard: deprecated "validate run" subcommand
			if name == "run" {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), `"chunk validate run" is no longer a subcommand. Use "chunk validate" or "chunk validate <name>".`)
				os.Exit(2)
			}

			// --list: show configured commands
			if list {
				cfg, err := validate.LoadProjectConfig(workDir)
				if err != nil {
					cfg = &validate.ProjectConfig{}
				}
				return validate.List(cfg, streams)
			}

			// --status: check cache only
			if status {
				cfg, err := validate.LoadProjectConfig(workDir)
				if err != nil {
					cfg = &validate.ProjectConfig{}
				}
				return validate.Status(workDir, name, cfg, streams)
			}

			// --cmd: run inline command
			if inlineCmd != "" {
				cmdName := name
				if cmdName == "" {
					cmdName = "custom"
				}
				if save {
					if err := validate.SaveCommand(workDir, cmdName, inlineCmd); err != nil {
						return fmt.Errorf("save command: %w", err)
					}
					streams.ErrPrintf("Saved %s to .chunk/config.json\n", cmdName)
				}
				return validate.RunInline(cmd.Context(), workDir, cmdName, inlineCmd, forceRun, streams)
			}

			cfg, err := validate.LoadProjectConfig(workDir)
			if err != nil || !cfg.HasCommands() {
				return fmt.Errorf("no validate commands configured, run validate init first")
			}

			if dryRun {
				return validate.RunDryRun(cfg, name, streams)
			}

			if sandboxID != "" {
				if orgID == "" {
					return fmt.Errorf("--org-id is required when using --sandbox-id")
				}
				client, err := circleci.NewClient()
				if err != nil {
					return err
				}
				return validate.RunRemote(cmd.Context(), client, cfg, sandboxID, orgID, streams)
			}

			// Named command
			if name != "" {
				return validate.RunNamed(cmd.Context(), workDir, name, forceRun, cfg, streams)
			}

			// Run all
			return validate.RunAll(cmd.Context(), workDir, forceRun, cfg, streams)
		},
	}

	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID for remote execution")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID (required with --sandbox-id)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show commands without executing")
	cmd.Flags().BoolVar(&list, "list", false, "List all configured commands")
	cmd.Flags().StringVar(&inlineCmd, "cmd", "", "Run an inline command instead of config")
	cmd.Flags().BoolVar(&save, "save", false, "Save --cmd to .chunk/config.json")
	cmd.Flags().BoolVar(&forceRun, "force-run", false, "Ignore cache, always run")
	cmd.Flags().BoolVar(&status, "status", false, "Check cache only, don't execute")
	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")

	cmd.AddCommand(newValidateInitCmd())
	cmd.AddCommand(newValidateRunCmd())

	return cmd
}

func newValidateRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "run",
		Short:  "Deprecated: use 'chunk validate' directly",
		Hidden: true,
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), `"chunk validate run" is no longer a subcommand. Use "chunk validate" or "chunk validate <name>".`)
			os.Exit(2)
		},
	}
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
					return usererr.New(
					fmt.Sprintf("Invalid profile %q. Valid profiles: %s", profile, strings.Join(names, ", ")),
					fmt.Errorf("invalid profile %q", profile),
				)
				}
			}

			io := iostream.FromCmd(cmd)
			configPath := filepath.Join(workDir, ".chunk", "config.json")
			if _, err := os.Stat(configPath); err == nil && !force {
				io.ErrPrintln("Config already exists. Use --force to overwrite.")
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

			config := map[string]interface{}{
				"commands": []map[string]string{
					{"name": "test", "run": testCmd},
				},
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

			io.ErrPrintln("Validation config initialized")
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
