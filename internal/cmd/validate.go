package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sandbox"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/usererr"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

func newValidateCmd() *cobra.Command {
	var sandboxID, identityFile, workdir string
	var dryRun, list, save, ifChanged bool
	var inlineCmd, projectDir string

	cmd := &cobra.Command{
		Use:          "validate [name]",
		Short:        "Run validation commands",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
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

			// --if-changed: hook-friendly mode. Skip all validation when the
			// working tree is clean or the project has no commands configured.
			// Intended for Stop hook usage — never errors, always exits 0.
			if ifChanged {
				// CLAUDE_WORKING_DIR is set by Claude Code to the session's
				// actual working directory, which for worktrees is the worktree
				// root rather than the main repo root (CLAUDE_PROJECT_DIR).
				if projectDir == "" {
					if wd := os.Getenv("CLAUDE_WORKING_DIR"); wd != "" {
						workDir = wd
					}
				}
				return runIfChanged(cmd.Context(), workDir, streams)
			}

			var name string
			if len(args) == 1 {
				name = args[0]
			}

			// --list: show configured commands
			if list {
				cfg, err := config.LoadProjectConfig(workDir)
				if err != nil {
					cfg = &config.ProjectConfig{}
				}
				return validate.List(cfg, streams)
			}

			// --cmd: run inline command
			if inlineCmd != "" {
				cmdName := name
				if cmdName == "" {
					cmdName = "custom"
				}
				if dryRun {
					streams.Printf("%s: %s\n", ui.Bold(cmdName), ui.Gray(inlineCmd))
					return nil
				}
				if save {
					if err := config.SaveCommand(workDir, cmdName, inlineCmd); err != nil {
						return fmt.Errorf("save command: %w", err)
					}
					streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", cmdName)))
				}
				return validate.RunInline(cmd.Context(), workDir, cmdName, inlineCmd, streams)
			}

			cfg, err := config.LoadProjectConfig(workDir)
			if err != nil || !cfg.HasCommands() {
				return fmt.Errorf("no validate commands configured, run 'chunk init' first")
			}

			if dryRun {
				return validate.RunDryRun(cfg, name, streams)
			}

			if sandboxID != "" {
				client, err := authprompt.EnsureCircleCIClient(cmd.Context(), streams, tui.PromptHidden)
				if err != nil {
					return err
				}
				authSock := os.Getenv("SSH_AUTH_SOCK")
				session, err := sandbox.OpenSession(cmd.Context(), client, sandboxID, identityFile, authSock)
				if err != nil {
					return fmt.Errorf("open session: %w", err)
				}
				dest := workdir
				if dest == "" {
					dest = "/workspace"
				}
				return validate.RunRemote(cmd.Context(), func(ctx context.Context, script string) (string, string, int, error) {
					result, err := sandbox.ExecOverSSH(ctx, session, "sh -c "+sandbox.ShellEscape(script), nil, nil)
					if err != nil {
						return "", "", 0, err
					}
					return result.Stdout, result.Stderr, result.ExitCode, nil
				}, cfg, dest, streams)
			}

			// Named command
			if name != "" {
				cfg, err = ensureCommandConfigured(workDir, name, cfg, streams)
				if err != nil {
					return err
				}
				return validate.RunNamed(cmd.Context(), workDir, name, cfg, streams)
			}

			// Run all
			return validate.RunAll(cmd.Context(), workDir, cfg, streams)
		},
	}

	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID for remote execution")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file (uses ssh-agent or ~/.ssh/chunk_ai when omitted)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory on sandbox (auto-detected as /workspace/<repo> when omitted)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show commands without executing")
	cmd.Flags().BoolVar(&list, "list", false, "List all configured commands")
	cmd.Flags().StringVar(&inlineCmd, "cmd", "", "Run an inline command instead of config")
	cmd.Flags().BoolVar(&save, "save", false, "Save --cmd to .chunk/config.json")
	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")
	cmd.Flags().BoolVar(&ifChanged, "if-changed", false, "Skip validation if there are no uncommitted changes (for Stop hook use); respects CLAUDE_WORKING_DIR for worktree detection")

	return cmd
}

// ensureCommandConfigured returns a config with the named command present,
// prompting interactively to configure it if missing (TTY only).
func ensureCommandConfigured(workDir, name string, cfg *config.ProjectConfig, streams iostream.Streams) (*config.ProjectConfig, error) {
	if cfg.FindCommand(name) != nil {
		return cfg, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("command %q is not configured\nAdd %q to .chunk/config.json", name, name)
	}
	streams.ErrPrintf("Command %s is not configured yet.\n\n", ui.Bold(name))
	streams.ErrPrintf("What command should %s run? ", ui.Bold(name))
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return nil, fmt.Errorf("no input received")
	}
	input := strings.TrimSpace(scanner.Text())
	if input == "" {
		streams.ErrPrintln(ui.Dim("No command entered, aborting."))
		return nil, fmt.Errorf("no command entered")
	}
	if err := config.SaveCommand(workDir, name, input); err != nil {
		return nil, fmt.Errorf("save command: %w", err)
	}
	streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", name)))
	return config.LoadProjectConfig(workDir)
}

// runIfChanged implements --if-changed hook mode: skip when the working tree
// is clean, enforce max-attempt limiting, and exit 2 to re-signal the agent.
func runIfChanged(ctx context.Context, workDir string, streams iostream.Streams) error {
	hasChanges, _ := validate.HasUncommittedChanges(workDir)
	if !hasChanges {
		streams.ErrPrintln(ui.Dim("chunk validate: no changes, skipping"))
		return nil
	}

	cfg, err := config.LoadProjectConfig(workDir)
	if err != nil || !cfg.HasCommands() {
		return nil
	}

	// Acquire a per-directory advisory lock to prevent concurrent Stop hook
	// invocations (e.g. two sessions sharing a worktree) from running
	// expensive commands simultaneously.
	release, acquired := validate.TryLock(workDir, streams.Err)
	if !acquired {
		streams.ErrPrintln(ui.Dim("chunk validate: another validate is running, skipping"))
		return nil
	}
	defer release()

	// Compute the content hash before running validation so that
	// TrackFailedAttempt uses a stable snapshot. A concurrent commit between
	// RunAll and the attempt counter update would otherwise spuriously reset
	// the consecutive-failure count.
	contentHash := validate.ComputeContentHash(workDir)

	// Route stdout to stderr so all command output appears in the Stop hook
	// feedback block that Claude Code shows the agent.
	hookStreams := iostream.Streams{Out: streams.Err, Err: streams.Err}

	if err := validate.RunAll(ctx, workDir, cfg, hookStreams); err != nil {
		// When the force-validate sentinel is present, always re-signal the
		// agent (exit 2) regardless of attempt count — useful for debugging
		// loops where the developer wants unlimited retries.
		if !validate.ForceHookFileExists(workDir) {
			maxAttempts := cfg.StopHookMaxAttempts
			if maxAttempts <= 0 {
				maxAttempts = validate.DefaultMaxAttempts
			}
			n := validate.TrackFailedAttempt(workDir, contentHash)
			if n >= maxAttempts {
				streams.ErrPrintf("chunk validate: validation has failed %d time(s) with the same uncommitted changes.\n", n)
				streams.ErrPrintf("The failures above do not appear to be resolving automatically.\n")
				streams.ErrPrintf("Stop attempting to fix this and ask the user for guidance instead.\n")
				return &usererr.ExitError{Code: 2}
			}
		}
		return &usererr.ExitError{Code: 2}
	}
	validate.ResetAttempts(workDir)
	return nil
}
