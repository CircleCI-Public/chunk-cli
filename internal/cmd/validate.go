package cmd

import (
	"bufio"
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

// validateHookFlags groups all hook-mode flag values.
type validateHookFlags struct {
	check, noCheck, task bool
	syncSpecs            []string
	on, trigger, matcher string
	limit                int
	staged, always       bool
	onFail               string
	bail                 bool
	instructions, schema string
	overrideCmd          string
}

// isHookMode reports whether any hook-mode flag is set.
func (f *validateHookFlags) isHookMode() bool {
	return f.check || f.noCheck || f.task || len(f.syncSpecs) > 0
}

// runHookMode dispatches to the appropriate hook handler.
func runHookMode(f *validateHookFlags, name, workDir string) error {
	if len(f.syncSpecs) > 0 {
		specs, err := hook.ParseSpecs(f.syncSpecs)
		if err != nil {
			return err
		}
		cfg := hook.LoadConfig(hook.ResolveProject(workDir))
		return hook.RunSyncCheck(cfg, hook.SyncCheckFlags{
			Specs: specs, On: f.on, Trigger: f.trigger, Matcher: f.matcher,
			Limit: f.limit, Staged: f.staged, Always: f.always,
			OnFail: f.onFail, Bail: f.bail,
		}, readStdinEvent())
	}

	if name == "" {
		flag := "--check"
		if f.noCheck {
			flag = "--no-check"
		} else if f.task {
			flag = "--task"
		}
		return fmt.Errorf("%s requires a command name", flag)
	}

	cfg := hook.LoadConfig(hook.ResolveProject(workDir))

	if f.check {
		return hook.RunExecCheck(cfg, hook.ExecCheckFlags{
			Name: name, Staged: f.staged, Always: f.always,
			On: f.on, Trigger: f.trigger, Limit: f.limit,
			Matcher: f.matcher, Cmd: f.overrideCmd,
		}, readStdinEvent())
	}

	if f.noCheck {
		return hook.RunExecRun(cfg, hook.ExecRunFlags{
			Name: name, Cmd: f.overrideCmd, Staged: f.staged, Always: f.always,
			NoCheck: true, On: f.on, Trigger: f.trigger,
			Limit: f.limit, Matcher: f.matcher,
		})
	}

	return hook.RunTaskCheck(cfg, hook.TaskCheckFlags{
		Name: name, Instructions: f.instructions, Schema: f.schema,
		Always: f.always, Staged: f.staged, On: f.on, Trigger: f.trigger,
		Matcher: f.matcher, Limit: f.limit,
	}, readStdinEvent())
}

func newValidateCmd() *cobra.Command {
	var sandboxID, orgID string
	var dryRun, list, save, forceRun, status bool
	var inlineCmd, projectDir string
	var hf validateHookFlags

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

			// Hook modes: --check, --no-check, --task, --sync
			if hf.isHookMode() {
				return runHookMode(&hf, name, workDir)
			}

			// Guard: deprecated "validate run" subcommand
			if name == "run" {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), ui.Warning(`"chunk validate run" is no longer a subcommand. Use "chunk validate" or "chunk validate <name>".`))
				os.Exit(2)
			}

			// --list: show configured commands
			if list {
				cfg, err := config.LoadProjectConfig(workDir)
				if err != nil {
					cfg = &config.ProjectConfig{}
				}
				return validate.List(cfg, streams)
			}

			// --status: check cache only
			if status {
				cfg, err := config.LoadProjectConfig(workDir)
				if err != nil {
					cfg = &config.ProjectConfig{}
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
					if err := config.SaveCommand(workDir, cmdName, inlineCmd); err != nil {
						return fmt.Errorf("save command: %w", err)
					}
					streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", cmdName)))
				}
				return validate.RunInline(cmd.Context(), workDir, cmdName, inlineCmd, forceRun, streams)
			}

			cfg, err := config.LoadProjectConfig(workDir)
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
					if err := config.SaveCommand(workDir, name, input); err != nil {
						return fmt.Errorf("save command: %w", err)
					}
					streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", name)))
					// Reload config after save
					cfg, err = config.LoadProjectConfig(workDir)
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

	// Original flags
	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID for remote execution")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID (required with --sandbox-id)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show commands without executing")
	cmd.Flags().BoolVar(&list, "list", false, "List all configured commands")
	cmd.Flags().StringVar(&inlineCmd, "cmd", "", "Run an inline command instead of config")
	cmd.Flags().BoolVar(&save, "save", false, "Save --cmd to .chunk/config.json")
	cmd.Flags().BoolVar(&forceRun, "force-run", false, "Ignore cache, always run")
	cmd.Flags().BoolVar(&status, "status", false, "Check cache only, don't execute")
	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")

	// Hook-mode flags (hidden — used by IDE-generated settings, not typed by humans)
	cmd.Flags().BoolVar(&hf.check, "check", false, "Check saved sentinel result")
	cmd.Flags().BoolVar(&hf.noCheck, "no-check", false, "Run and save sentinel without enforcing")
	cmd.Flags().BoolVar(&hf.task, "task", false, "Check subagent task result")
	cmd.Flags().StringSliceVar(&hf.syncSpecs, "sync", nil, "Grouped sequential checks (e.g. exec:tests,task:review)")
	cmd.Flags().StringVar(&hf.on, "on", "", "Trigger group name")
	cmd.Flags().StringVar(&hf.trigger, "trigger", "", "Inline trigger pattern")
	cmd.Flags().StringVar(&hf.matcher, "matcher", "", "Tool-name regex filter")
	cmd.Flags().IntVar(&hf.limit, "limit", 0, "Max consecutive blocks")
	cmd.Flags().BoolVar(&hf.staged, "staged", false, "Only staged files")
	cmd.Flags().BoolVar(&hf.always, "always", false, "Run even without changes")
	cmd.Flags().StringVar(&hf.onFail, "on-fail", "restart", "Sync failure strategy")
	cmd.Flags().BoolVar(&hf.bail, "bail", false, "Stop sync at first failure")
	cmd.Flags().StringVar(&hf.instructions, "instructions", "", "Task instructions file")
	cmd.Flags().StringVar(&hf.schema, "schema", "", "Task result schema file")
	cmd.Flags().StringVar(&hf.overrideCmd, "override-cmd", "", "Override configured command (hook mode)")

	hookFlags := []string{
		"check", "no-check", "task", "sync", "on", "trigger", "matcher",
		"limit", "staged", "always", "on-fail", "bail", "instructions",
		"schema", "override-cmd",
	}
	for _, name := range hookFlags {
		_ = cmd.Flags().MarkHidden(name)
	}

	cmd.AddCommand(newValidateInitCmd())
	cmd.AddCommand(newValidateRunCmd())

	return cmd
}

func newValidateRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "run",
		Short:  "Deprecated: use 'chunk validate' directly",
		Hidden: true,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), ui.Warning(`"chunk validate run" is no longer a subcommand. Use "chunk validate" or "chunk validate <name>".`))
			os.Exit(2)
		},
	}
}

func newValidateInitCmd() *cobra.Command {
	var profile string
	var force, skipEnv bool

	cmd := &cobra.Command{
		Use:        "init",
		Short:      "Deprecated: use 'chunk init' instead",
		Hidden:     true,
		Deprecated: "use 'chunk init' instead",
		RunE: func(cmd *cobra.Command, _ []string) error {
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
				cfg, loadErr := config.LoadProjectConfig(workDir)
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

			testCmd, err := validate.DetectTestCommand(cmd.Context(), claude, workDir)
			if err != nil {
				return fmt.Errorf("detect test command: %w", err)
			}

			streams.ErrPrintf("Detected test command: %s\n", ui.Bold(testCmd))

			commands := []config.Command{}
			pm := validate.DetectPackageManager(workDir)
			if pm != nil {
				streams.ErrPrintf("Detected package manager: %s\n", ui.Bold(pm.Name))
				commands = append(commands, config.Command{Name: "install", Run: pm.InstallCommand})
			}
			commands = append(commands, config.Command{Name: "test", Run: testCmd})

			cfg := &config.ProjectConfig{Commands: commands}
			if err := config.SaveProjectConfig(workDir, cfg); err != nil {
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
