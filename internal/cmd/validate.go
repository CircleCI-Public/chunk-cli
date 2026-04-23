package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sandbox"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

func newStatusFunc(streams iostream.Streams) iostream.StatusFunc {
	return func(level iostream.Level, msg string) {
		switch level {
		case iostream.LevelStep:
			streams.ErrPrintln(ui.Bold(msg))
		case iostream.LevelInfo:
			streams.ErrPrintf("  %s\n", ui.Dim(msg))
		case iostream.LevelWarn:
			streams.ErrPrintf("  %s\n", ui.Warning(msg))
		case iostream.LevelDone:
			streams.ErrPrintf("  %s\n", ui.Success(msg))
		}
	}
}

// hookContext holds the Claude Code Stop hook payload fields.
type hookContext struct {
	sessionID      string
	stopHookActive bool
}

// detectHook reads the Claude Code hook JSON payload from r when r is not a
// terminal. Returns nil if not running as a Stop hook.
func detectHook(r io.Reader) *hookContext {
	if f, ok := r.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return nil
	}
	var p struct {
		SessionID      string `json:"session_id"`
		StopHookActive bool   `json:"stop_hook_active"`
	}
	_ = json.NewDecoder(r).Decode(&p)
	if p.SessionID == "" {
		return nil
	}
	return &hookContext{sessionID: p.SessionID, stopHookActive: p.StopHookActive}
}

func newValidateCmd() *cobra.Command {
	var sandboxID, identityFile, remoteWorkdir string
	var dryRun, list, save, remote bool
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

			hook := detectHook(cmd.InOrStdin())
			if hook != nil {
				if !hook.stopHookActive {
					validate.ResetAttempts(hook.sessionID)
				}
				// Route stdout to stderr so all output appears in the Stop
				// hook feedback block that Claude Code shows the agent.
				streams = iostream.Streams{Out: streams.Err, Err: streams.Err}
			}
			statusFn := newStatusFunc(streams)

			// Hook: skip entirely when the working tree is clean.
			if hook != nil && !validate.HasGitChanges(workDir) {
				return nil
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
				return validate.List(cfg, statusFn)
			}

			cfg, err := config.LoadProjectConfig(workDir)
			if hook != nil && (err != nil || !cfg.HasCommands()) && inlineCmd == "" {
				return nil // no config in hook context: skip silently
			}
			if (err != nil || !cfg.HasCommands()) && inlineCmd == "" {
				return &userError{
					msg:        "No validate commands configured.",
					suggestion: "Run 'chunk init' first.",
					errMsg:     "no validate commands configured",
				}
			}

			if dryRun {
				if inlineCmd != "" {
					cmdName := name
					if cmdName == "" {
						cmdName = "custom"
					}
					statusFn(iostream.LevelInfo, fmt.Sprintf("%s: %s", cmdName, inlineCmd))
					return nil
				}
				return mapValidateError(validate.RunDryRun(cfg, name, statusFn))
			}

			if remote {
				if err := resolveSandboxID(&sandboxID); err != nil {
					return err
				}
			}

			execErr := runValidate(cmd.Context(), workDir, name, inlineCmd, save, sandboxID, identityFile, remoteWorkdir, cfg, statusFn, streams)

			if hook != nil {
				maxAttempts := cfg.StopHookMaxAttempts
				if maxAttempts <= 0 {
					maxAttempts = validate.DefaultMaxAttempts
				}
				return validate.WrapHookResult(hook.sessionID, execErr, maxAttempts, streams.Err)
			}
			return execErr
		},
	}

	cmd.Flags().BoolVar(&remote, "remote", false, "Run on active sandbox (reads .chunk/sandbox.json)")
	cmd.Flags().StringVar(&sandboxID, "sandbox-id", "", "Sandbox ID for remote execution")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file (uses ssh-agent or ~/.ssh/chunk_ai when omitted)")
	cmd.Flags().StringVar(&remoteWorkdir, "workdir", "", "Working directory on sandbox (reads from sandbox.json, defaults to ./workspace)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show commands without executing")
	cmd.Flags().BoolVar(&list, "list", false, "List all configured commands")
	cmd.Flags().StringVar(&inlineCmd, "cmd", "", "Run an inline command instead of config")
	cmd.Flags().BoolVar(&save, "save", false, "Save --cmd to .chunk/config.json")
	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")

	return cmd
}

// runValidate dispatches to the appropriate Run* function based on the
// provided options. It is shared by both direct and hook invocations.
func runValidate(ctx context.Context, workDir, name, inlineCmd string, save bool, sandboxID, identityFile, remoteWorkdir string, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	// --cmd: inline command
	if inlineCmd != "" {
		cmdName := name
		if cmdName == "" {
			cmdName = "custom"
		}
		if save {
			if err := config.SaveCommand(workDir, cmdName, inlineCmd); err != nil {
				return &userError{msg: "Could not save command to .chunk/config.json.", err: err}
			}
			streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", cmdName)))
		}
		if sandboxID != "" {
			execFn, dest, err := openSSHSession(ctx, sandboxID, identityFile, remoteWorkdir, streams)
			if err != nil {
				return err
			}
			return validate.RunRemoteInline(ctx, execFn, cmdName, inlineCmd, dest, streams)
		}
		return validate.RunInline(ctx, workDir, cmdName, inlineCmd, statusFn, streams)
	}

	// Remote execution
	if sandboxID != "" {
		execFn, dest, err := openSSHSession(ctx, sandboxID, identityFile, remoteWorkdir, streams)
		if err != nil {
			return err
		}
		return validate.RunRemote(ctx, execFn, cfg, dest, streams)
	}

	// Named command
	if name != "" {
		if cfg.FindCommand(name) == nil {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return &userError{
					msg:        fmt.Sprintf("Command %q is not configured.", name),
					suggestion: "Add it to .chunk/config.json.",
					errMsg:     fmt.Sprintf("command %q is not configured", name),
				}
			}
			// Interactive setup: prompt for command
			streams.ErrPrintf("Command %s is not configured yet.\n\n", ui.Bold(name))
			streams.ErrPrintf("What command should %s run? ", ui.Bold(name))
			scanner := bufio.NewScanner(os.Stdin)
			if !scanner.Scan() {
				return &userError{msg: "No command entered.", errMsg: "no input received"}
			}
			input := strings.TrimSpace(scanner.Text())
			if input == "" {
				streams.ErrPrintln(ui.Dim("No command entered, aborting."))
				return &userError{msg: "No command entered.", errMsg: "no command entered"}
			}
			if err := config.SaveCommand(workDir, name, input); err != nil {
				return &userError{msg: "Could not save command to .chunk/config.json.", err: err}
			}
			streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", name)))
			var err error
			cfg, err = config.LoadProjectConfig(workDir)
			if err != nil {
				return err
			}
		}
		return mapValidateError(validate.RunNamed(ctx, workDir, name, cfg, statusFn, streams))
	}

	// Run all
	return mapValidateError(validate.RunAll(ctx, workDir, cfg, statusFn, streams))
}

// openSSHSession establishes an SSH session to the sandbox and returns an
// exec function and the resolved remote working directory.
func openSSHSession(ctx context.Context, sandboxID, identityFile, remoteWorkdir string, streams iostream.Streams) (func(context.Context, string) (string, string, int, error), string, error) {
	client, err := ensureCircleCIClient(ctx, streams, tui.PromptHidden)
	if err != nil {
		return nil, "", err
	}
	authSock := os.Getenv(config.EnvSSHAuthSock)
	session, err := sandbox.OpenSession(ctx, client, sandboxID, identityFile, authSock)
	if err != nil {
		return nil, "", &userError{msg: "Could not open SSH session to sandbox.", err: err}
	}
	dest := remoteWorkdir
	if dest == "" {
		if active, err := sandbox.LoadActive(); err == nil && active != nil && active.Workspace != "" {
			dest = active.Workspace
		} else {
			dest = "./workspace"
		}
	}
	execFn := func(ctx context.Context, script string) (string, string, int, error) {
		result, err := sandbox.ExecOverSSH(ctx, session, "sh -c "+sandbox.ShellEscape(script), nil, nil)
		if err != nil {
			return "", "", 0, err
		}
		return result.Stdout, result.Stderr, result.ExitCode, nil
	}
	return execFn, dest, nil
}

func mapValidateError(err error) error {
	if errors.Is(err, validate.ErrNotConfigured) {
		return &userError{
			msg:        "No validate commands configured.",
			suggestion: "Run 'chunk init' first.",
			err:        err,
		}
	}
	return err
}
