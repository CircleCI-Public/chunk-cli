package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
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
	var sidecarID, identityFile, workdir string
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

			var remoteAll bool
			var resolveErr error
			sidecarID, remoteAll, resolveErr = resolveSidecarForValidate(cmd.Context(), sidecarID, remote, hook, workDir, cfg, name, inlineCmd, streams, statusFn)
			if resolveErr != nil {
				return resolveErr
			}

			execErr := runValidate(cmd.Context(), workDir, name, inlineCmd, save, sidecarID, identityFile, workdir, remoteAll, cfg, statusFn, streams)

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

	cmd.Flags().BoolVar(&remote, "remote", false, "Run on active sidecar (reads .chunk/sidecar.json)")
	cmd.Flags().StringVar(&sidecarID, "sidecar-id", "", "Sidecar ID for remote execution")
	cmd.Flags().StringVar(&identityFile, "identity-file", "", "SSH identity file (uses ssh-agent or ~/.ssh/chunk_ai when omitted)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory on sidecar (reads from sidecar.json, defaults to ./workspace)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show commands without executing")
	cmd.Flags().BoolVar(&list, "list", false, "List all configured commands")
	cmd.Flags().StringVar(&inlineCmd, "cmd", "", "Run an inline command instead of config")
	cmd.Flags().BoolVar(&save, "save", false, "Save --cmd to .chunk/config.json")
	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")

	return cmd
}

// runValidate dispatches to the appropriate Run* function based on the
// provided options. It is shared by both direct and hook invocations.
// When remoteAll is true and sidecarID is non-empty, all commands run on the
// sidecar. When remoteAll is false, only commands with Remote:true are routed
// to the sidecar and the rest run locally.
func runValidate(ctx context.Context, workDir, name, inlineCmd string, save bool, sidecarID, identityFile, workdir string, remoteAll bool, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
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
		if sidecarID != "" {
			execFn, dest, err := openSSHSession(ctx, sidecarID, identityFile, workdir, streams)
			if err != nil {
				return err
			}
			return validate.RunRemoteInline(ctx, execFn, cmdName, inlineCmd, dest, streams)
		}
		return validate.RunInline(ctx, workDir, cmdName, inlineCmd, statusFn, streams)
	}

	// Remote execution
	if sidecarID != "" {
		execFn, dest, err := openSSHSession(ctx, sidecarID, identityFile, workdir, streams)
		if err != nil {
			return err
		}
		if remoteAll {
			return validate.RunRemote(ctx, execFn, cfg, name, dest, streams)
		}
		return validate.RunMixed(ctx, workDir, execFn, cfg, name, dest, statusFn, streams)
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

// openSSHSession establishes an SSH session to the sidecar and returns an
// exec function and the resolved remote working directory.
func openSSHSession(ctx context.Context, sidecarID, identityFile, workdir string, streams iostream.Streams) (func(context.Context, string) (string, string, int, error), string, error) {
	client, err := ensureCircleCIClient(ctx, streams, tui.PromptHidden)
	if err != nil {
		return nil, "", err
	}
	authSock := os.Getenv(config.EnvSSHAuthSock)
	session, err := sidecar.OpenSession(ctx, client, sidecarID, identityFile, authSock)
	if err != nil {
		return nil, "", &userError{msg: "Could not open SSH session to sidecar.", err: err}
	}
	dest := workdir
	if dest == "" {
		if active, err := sidecar.LoadActive(); err == nil && active != nil && active.Workspace != "" {
			dest = active.Workspace
		} else {
			dest = "./workspace"
		}
	}
	execFn := func(ctx context.Context, script string) (string, string, int, error) {
		result, err := sidecar.ExecOverSSH(ctx, session, "sh -c "+sidecar.ShellEscape(script), nil, nil)
		if err != nil {
			return "", "", 0, err
		}
		return result.Stdout, result.Stderr, result.ExitCode, nil
	}
	return execFn, dest, nil
}

// resolveSidecarForValidate determines the sidecarID and remoteAll flag for a
// validate run. remoteAll is true when all commands should run on the sidecar
// (explicit --sidecar-id/--remote flag or an existing active sidecar); false
// when only commands with Remote:true are routed (auto-provisioned sidecar).
func resolveSidecarForValidate(ctx context.Context, sidecarID string, remote bool, hook *hookContext, workDir string, cfg *config.ProjectConfig, name, inlineCmd string, streams iostream.Streams, statusFn iostream.StatusFunc) (id string, remoteAll bool, err error) {
	// Explicit --sidecar-id: route all commands to sidecar.
	if sidecarID != "" {
		return sidecarID, true, nil
	}
	// --remote: resolve from active sidecar file.
	if remote {
		if err := resolveSidecarID(&sidecarID); err != nil {
			return "", false, err
		}
		return sidecarID, true, nil
	}
	// Auto-use the active sidecar for the current session.
	var active *sidecar.ActiveSidecar
	if hook != nil && hook.sessionID != "" {
		active, _ = sidecar.LoadForSession(hook.sessionID)
	} else {
		active, _ = sidecar.LoadActive()
	}
	if active != nil {
		if hook == nil {
			statusFn(iostream.LevelInfo, fmt.Sprintf("using active sidecar %s", active.SidecarID))
		}
		if hasRemoteCommands(cfg, name) {
			// Some commands are marked remote: route only those to the sidecar.
			return active.SidecarID, false, nil
		}
		return active.SidecarID, true, nil
	}
	// Per-command remote: auto-provision when some commands require remote execution.
	if inlineCmd == "" && hasRemoteCommands(cfg, name) {
		id, provisionErr := ensureRemoteSidecar(ctx, workDir, hook, streams, statusFn)
		if provisionErr != nil {
			return "", false, provisionErr
		}
		return id, false, nil // remoteAll=false: only Remote:true commands go to sidecar
	}
	return "", false, nil
}

// hasRemoteCommands reports whether the named command (or any command when
// name is empty) has the Remote flag set.
func hasRemoteCommands(cfg *config.ProjectConfig, name string) bool {
	if name != "" {
		c := cfg.FindCommand(name)
		return c != nil && c.Remote
	}
	for _, c := range cfg.Commands {
		if c.Remote {
			return true
		}
	}
	return false
}

// ensureRemoteSidecar returns a sidecarID for remote command execution.
// It loads the active sidecar when one is already set, otherwise creates a new
// one using CIRCLECI_ORG_ID and a name derived from workDir.
func ensureRemoteSidecar(ctx context.Context, workDir string, hook *hookContext, streams iostream.Streams, statusFn iostream.StatusFunc) (string, error) {
	var active *sidecar.ActiveSidecar
	if hook != nil && hook.sessionID != "" {
		active, _ = sidecar.LoadForSession(hook.sessionID)
	}
	if active == nil {
		active, _ = sidecar.LoadActive()
	}
	if active != nil {
		return active.SidecarID, nil
	}

	// No active sidecar: auto-create one.
	client, err := ensureCircleCIClient(ctx, streams, tui.PromptHidden)
	if err != nil {
		return "", fmt.Errorf("cannot provision sidecar for remote commands: %w", err)
	}
	orgID := os.Getenv(config.EnvCircleCIOrgID)
	if orgID == "" {
		return "", &userError{
			msg:        "No active sidecar and CIRCLECI_ORG_ID is not set.",
			suggestion: "Set CIRCLECI_ORG_ID, or run 'chunk sidecar use <id>' to specify a sidecar.",
			errMsg:     "no active sidecar and CIRCLECI_ORG_ID not set",
		}
	}
	name := filepath.Base(workDir)
	provider := os.Getenv(config.EnvSidecarProvider)
	if provider == "" {
		provider = defaultProvider
	}
	statusFn(iostream.LevelStep, fmt.Sprintf("Creating sidecar %q for remote commands...", name))
	sc, err := sidecar.Create(ctx, client, orgID, name, provider, "")
	if err != nil {
		if authErr := notAuthorized("create sidecars", err); authErr != nil {
			return "", authErr
		}
		return "", &userError{msg: "Could not create sidecar for remote commands.", err: err}
	}
	sessionID := ""
	if hook != nil {
		sessionID = hook.sessionID
	}
	if saveErr := sidecar.SaveActiveForSession(sessionID, sidecar.ActiveSidecar{SidecarID: sc.ID, Name: sc.Name}); saveErr != nil {
		streams.ErrPrintf("warning: could not save active sidecar: %v\n", saveErr)
	}
	statusFn(iostream.LevelDone, fmt.Sprintf("Created sidecar %s (%s)", sc.Name, sc.ID))
	return sc.ID, nil
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
