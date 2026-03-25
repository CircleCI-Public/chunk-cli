package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/hook"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
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
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), ui.Warning(`"chunk validate run" is no longer a subcommand. Use "chunk validate" or "chunk validate <name>".`))
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
					streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", cmdName)))
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
				if cfg.FindCommand(name) == nil {
					isTTY := term.IsTerminal(int(os.Stdin.Fd()))
					if !isTTY {
						return fmt.Errorf("command %q is not configured\nAdd %q to .chunk/config.json", name, name)
					}
					// Interactive setup: prompt for command
					streams.ErrPrintf("Command %s is not configured yet.\n\n", ui.Bold(name))
					streams.ErrPrintf("What command should %s run? ", ui.Bold(name))
					scanner := bufio.NewScanner(os.Stdin)
					if !scanner.Scan() {
						return fmt.Errorf("no input received")
					}
					input := strings.TrimSpace(scanner.Text())
					if input == "" {
						streams.ErrPrintln(ui.Dim("No command entered, aborting."))
						return fmt.Errorf("no command entered")
					}
					if err := validate.SaveCommand(workDir, name, input); err != nil {
						return fmt.Errorf("save command: %w", err)
					}
					streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", name)))
					// Reload config after save
					cfg, err = validate.LoadProjectConfig(workDir)
					if err != nil {
						return err
					}
				}
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
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), ui.Warning(`"chunk validate run" is no longer a subcommand. Use "chunk validate" or "chunk validate <name>".`))
			os.Exit(2)
		},
	}
}

func newValidateInitCmd() *cobra.Command {
	var profile string
	var force, skipEnv bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize hook config files and detect install/test commands",
		RunE: func(cmd *cobra.Command, args []string) error {
			workDir, err := os.Getwd()
			if err != nil {
				return err
			}

			gitCmd := exec.Command("git", "rev-parse", "--git-dir")
			gitCmd.Dir = workDir
			if err := gitCmd.Run(); err != nil {
				return fmt.Errorf("not a git repository, run this command from inside a git repo")
			}

			if err := hook.ValidateProfile(profile); err != nil {
				return err
			}

			streams := iostream.FromCmd(cmd)
			configPath := filepath.Join(workDir, ".chunk", "config.json")
			if _, err := os.Stat(configPath); err == nil && !force {
				cfg, loadErr := validate.LoadProjectConfig(workDir)
				if loadErr == nil && cfg.HasCommands() {
					streams.ErrPrintf("Config already exists at %s\n", configPath)
					streams.ErrPrintln(ui.Dim("To re-detect and overwrite: chunk validate init --force"))
					return nil
				}
			}

			// Phase 1: hook setup (repo init + shell env)
			if err := hook.RunSetup(workDir, profile, force, skipEnv, "", streams); err != nil {
				return fmt.Errorf("hook setup: %w", err)
			}

			// Phase 2: detect commands
			claude, err := anthropic.New()
			if err != nil {
				return err
			}

			testCmd, err := detectTestCommand(cmd.Context(), claude, workDir)
			if err != nil {
				return fmt.Errorf("detect test command: %w", err)
			}

			streams.ErrPrintf("Detected test command: %s\n", ui.Bold(testCmd))

			commands := []validate.Command{}
			pm := detectPackageManager(workDir)
			if pm != nil {
				streams.ErrPrintf("Detected package manager: %s\n", ui.Bold(pm.name))
				commands = append(commands, validate.Command{Name: "install", Run: pm.installCommand})
			}
			commands = append(commands, validate.Command{Name: "test", Run: testCmd})

			cfg := &validate.ProjectConfig{Commands: commands}
			if err := validate.SaveProjectConfig(workDir, cfg); err != nil {
				return fmt.Errorf("write config: %w", err)
			}

			streams.ErrPrintf("Wrote %s\n", configPath)
			streams.ErrPrintln(ui.Success("Validation config initialized"))
			return nil
		},
	}

	cmd.Flags().StringVar(&profile, "profile", "enable",
		fmt.Sprintf("Shell environment profile (%s)", strings.Join(hook.ValidProfiles, ", ")))
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing files and config")
	cmd.Flags().BoolVar(&skipEnv, "skip-env", false, "Skip shell environment update")

	return cmd
}

func detectTestCommand(ctx context.Context, claude *anthropic.Client, workDir string) (string, error) {
	entries, _ := os.ReadDir(workDir)
	var files []string
	for _, e := range entries {
		files = append(files, e.Name())
	}

	// Check well-known files deterministically before asking Claude.
	if cmd := detectTestCommandFromFiles(files); cmd != "" {
		return cmd, nil
	}

	context := gatherRepoContext(workDir, files)
	pm := detectPackageManager(workDir)

	var pmHint string
	if pm != nil {
		pmHint = fmt.Sprintf("Detected package manager: %s. Use %s to run tests (e.g. `%s test`).\n\n", pm.name, pm.name, pm.name)
	}

	prompt := fmt.Sprintf(
		"You are analyzing a software repository to determine how tests are run.\n\n"+
			"%s%s\n\n"+
			"Based on the above, output ONLY the shell command used to run the test suite — "+
			"nothing else. No explanation, no markdown. Just the command string.",
		pmHint, context,
	)

	resp, err := claude.Ask(ctx, config.ValidationModel, 64, prompt)
	if err != nil {
		return "", fmt.Errorf("detect test command: %w", err)
	}

	result := strings.TrimSpace(resp)
	if result == "" {
		return "npm test", nil
	}
	return result, nil
}

func detectTestCommandFromFiles(files []string) string {
	has := make(map[string]bool, len(files))
	for _, f := range files {
		has[f] = true
	}

	switch {
	case has["Taskfile.yml"] || has["Taskfile.yaml"]:
		return "task test"
	case has["Makefile"]:
		return "make test"
	case has["go.mod"]:
		return "go test ./..."
	case has["Cargo.toml"]:
		return "cargo test"
	case has["pyproject.toml"]:
		return "pytest"
	case has["package.json"]:
		return "npm test"
	default:
		return ""
	}
}

// gatherRepoContext builds a rich context string with the root file listing
// and contents of key config files, mirroring the TS implementation.
func gatherRepoContext(workDir string, rootFiles []string) string {
	var parts []string
	parts = append(parts, "Root files:\n"+strings.Join(rootFiles, "\n"))

	candidates := []string{
		"package.json",
		"Makefile",
		"go.mod",
		"pom.xml",
		"build.gradle",
		"build.gradle.kts",
		"pyproject.toml",
		"setup.py",
		"pytest.ini",
		"Cargo.toml",
		"Taskfile.yml",
		"Taskfile.yaml",
		".chunk/hook/config.yml",
		".npmrc",
		".yarnrc",
		".yarnrc.yml",
		"requirements.txt",
		"requirements-dev.txt",
		"requirements-test.txt",
		"Pipfile",
		"Gemfile",
		"go.sum",
		"project.clj",
		"deps.edn",
	}

	const maxBytes = 4000
	for _, rel := range candidates {
		full := filepath.Join(workDir, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > maxBytes {
			content = content[:maxBytes]
		}
		parts = append(parts, fmt.Sprintf("\n--- %s ---\n%s", rel, content))
	}

	return strings.Join(parts, "\n")
}

type packageManager struct {
	name           string
	installCommand string
}

// detectPackageManager returns the package manager and its CI-safe install command.
func detectPackageManager(workDir string) *packageManager {
	lockfiles := []struct {
		file string
		pm   packageManager
	}{
		{"pnpm-lock.yaml", packageManager{"pnpm", "pnpm install --frozen-lockfile"}},
		{"yarn.lock", packageManager{"yarn", "yarn install --frozen-lockfile"}},
		{"bun.lock", packageManager{"bun", "bun install --frozen-lockfile"}},
		{"bun.lockb", packageManager{"bun", "bun install --frozen-lockfile"}},
		{"package-lock.json", packageManager{"npm", "npm ci"}},
	}

	for _, lf := range lockfiles {
		if _, err := os.Stat(filepath.Join(workDir, lf.file)); err == nil {
			return &lf.pm
		}
	}
	return nil
}
