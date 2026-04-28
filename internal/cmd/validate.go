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
	var sidecarID, identityFile, workdir, orgID string
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

			// allRemote is true when the caller explicitly targets the sidecar
			// (--remote or --sidecar-id), meaning every command runs there.
			// Per-command routing only applies when the sidecar is resolved implicitly.
			allRemote := remote || sidecarID != ""

			image := resolveImage(name, cfg)

			if remote {
				// --remote: force all commands to sidecar, creating one if needed.
				if err := resolveOrCreateSidecarID(cmd.Context(), &sidecarID, orgID, image, workDir, streams); err != nil {
					return err
				}
				statusFn(iostream.LevelInfo, fmt.Sprintf("running all commands on sidecar %s", sidecarID))
			} else if cfg.HasRemoteCommands() {
				// Per-command remote: use active sidecar if available.
				if active, err := sidecar.LoadActive(); err == nil && active != nil {
					sidecarID = active.SidecarID
					statusFn(iostream.LevelInfo, fmt.Sprintf("using sidecar %s for remote commands", sidecarID))
				} else if hook != nil {
					// In Stop hook context: auto-create a sandbox if possible.
					if err := resolveOrCreateSidecarID(cmd.Context(), &sidecarID, orgID, image, workDir, streams); err != nil {
						streams.ErrPrintf("warning: no sandbox available (%v); run 'chunk config set orgID <id>' to enable remote validation, running locally instead\n", err)
					}
				} else {
					statusFn(iostream.LevelWarn, "no active sidecar found — remote commands will run locally")
				}
			}

			execErr := runValidate(cmd.Context(), workDir, name, inlineCmd, save, sidecarID, identityFile, workdir, allRemote, cfg, statusFn, streams)

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

	cmd.Flags().BoolVar(&remote, "remote", false, "Run on active sidecar, or create one if none is set")
	cmd.Flags().StringVar(&sidecarID, "sidecar-id", "", "Sidecar ID for remote execution")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID (used when creating a new sidecar)")
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
// allRemote is true when --remote is passed explicitly (all commands run on the
// sidecar); false means only commands with Remote:true are routed to the sidecar.
func runValidate(ctx context.Context, workDir, name, inlineCmd string, save bool, sidecarID, identityFile, workdir string, allRemote bool, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	// --cmd: inline command (always local in per-command mode)
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
		if sidecarID != "" && allRemote {
			execFn, dest, err := openSSHSession(ctx, sidecarID, identityFile, workdir, streams)
			if err != nil {
				return err
			}
			return validate.RunRemoteInline(ctx, execFn, cmdName, inlineCmd, dest, statusFn, streams)
		}
		return validate.RunInline(ctx, workDir, cmdName, inlineCmd, statusFn, streams)
	}

	// All-remote execution (--remote flag): send everything to the sidecar.
	if sidecarID != "" && allRemote {
		execFn, dest, err := openSSHSession(ctx, sidecarID, identityFile, workdir, streams)
		if err != nil {
			return err
		}
		return validate.RunRemote(ctx, execFn, cfg, name, dest, statusFn, streams)
	}

	// Per-command remote routing: commands with Remote:true go to the sidecar,
	// the rest run locally.
	if sidecarID != "" {
		if name != "" {
			if cmd := cfg.FindCommand(name); cmd != nil && cmd.Remote {
				statusFn(iostream.LevelInfo, fmt.Sprintf("running %s on sidecar %s", name, sidecarID))
				execFn, dest, err := openSSHSession(ctx, sidecarID, identityFile, workdir, streams)
				if err != nil {
					return err
				}
				return validate.RunRemote(ctx, execFn, cfg, name, dest, statusFn, streams)
			}
			statusFn(iostream.LevelInfo, fmt.Sprintf("running %s locally (not marked remote)", name))
			// Named command is not marked remote; fall through to local execution.
		} else {
			remoteCfg, localCfg := splitByRemote(cfg)
			if len(remoteCfg.Commands) > 0 {
				names := commandNames(remoteCfg.Commands)
				statusFn(iostream.LevelInfo, fmt.Sprintf("running on sidecar %s: %s", sidecarID, names))
			}
			if len(localCfg.Commands) > 0 {
				statusFn(iostream.LevelInfo, fmt.Sprintf("running locally: %s", commandNames(localCfg.Commands)))
			}
			var runErr error
			if len(remoteCfg.Commands) > 0 {
				execFn, dest, err := openSSHSession(ctx, sidecarID, identityFile, workdir, streams)
				if err != nil {
					streams.ErrPrintf("warning: could not reach sidecar (%v); running %s locally instead\n", err, commandNames(remoteCfg.Commands))
					localCfg.Commands = append(remoteCfg.Commands, localCfg.Commands...)
				} else {
					runErr = validate.RunRemote(ctx, execFn, remoteCfg, "", dest, statusFn, streams)
				}
			}
			if len(localCfg.Commands) > 0 {
				if err := mapValidateError(validate.RunAll(ctx, workDir, localCfg, statusFn, streams)); err != nil {
					runErr = errors.Join(runErr, err)
				}
			}
			return runErr
		}
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

// splitByRemote partitions cfg.Commands into two configs: one containing only
// commands with Remote:true, and one containing the rest.
func splitByRemote(cfg *config.ProjectConfig) (remote, local *config.ProjectConfig) {
	remote = &config.ProjectConfig{}
	local = &config.ProjectConfig{}
	for _, cmd := range cfg.Commands {
		if cmd.Remote {
			remote.Commands = append(remote.Commands, cmd)
		} else {
			local.Commands = append(local.Commands, cmd)
		}
	}
	return remote, local
}

// commandNames returns a comma-separated list of command names.
func commandNames(cmds []config.Command) string {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

// resolveImage returns the sidecar image to use for sandbox creation.
// A per-command sidecarImage takes precedence over the project-level default.
func resolveImage(name string, cfg *config.ProjectConfig) string {
	if name != "" && cfg != nil {
		if cmd := cfg.FindCommand(name); cmd != nil && cmd.SidecarImage != "" {
			return cmd.SidecarImage
		}
	}
	if cfg != nil && cfg.Validation != nil {
		return cfg.Validation.SidecarImage
	}
	return ""
}

// resolveOrCreateSidecarID fills sidecarID from the active sidecar, or creates
// a new sandbox when none is configured.
func resolveOrCreateSidecarID(ctx context.Context, sidecarID *string, orgID, image, workDir string, streams iostream.Streams) error {
	if *sidecarID != "" {
		return nil
	}
	active, err := sidecar.LoadActive()
	if err != nil {
		return &userError{msg: "Could not load the active sidecar.", suggestion: configFilePermHint, err: err}
	}
	if active != nil {
		*sidecarID = active.SidecarID
		return nil
	}
	streams.ErrPrintf("No active sidecar found, creating a new sandbox...\n")
	client, err := ensureCircleCIClient(ctx, streams, tui.PromptHidden)
	if err != nil {
		return err
	}
	// Fallback: read org ID from project config if not provided via flag or env.
	if orgID == "" {
		if projCfg, loadErr := config.LoadProjectConfig(workDir); loadErr == nil && projCfg.OrgID != "" {
			orgID = projCfg.OrgID
		}
	}
	resolvedOrgID, err := resolveOrgID(orgID, orgPicker(ctx, client))
	if err != nil {
		return err
	}
	provider := os.Getenv(config.EnvSidecarProvider)
	if provider == "" {
		provider = defaultProvider
	}
	sandboxName := filepath.Base(workDir) + "-validate"
	sc, err := sidecar.Create(ctx, client, resolvedOrgID, sandboxName, provider, image)
	if err != nil {
		if authErr := notAuthorized("create sidecars", err); authErr != nil {
			return authErr
		}
		return &userError{
			msg:        "Could not create a sandbox.",
			suggestion: "Check your network connection or run 'chunk sidecar create' manually.",
			err:        err,
		}
	}
	if saveErr := sidecar.SaveActive(sidecar.ActiveSidecar{SidecarID: sc.ID, Name: sc.Name}); saveErr != nil {
		streams.ErrPrintf("warning: could not save active sidecar: %v\n", saveErr)
	}
	// Persist the org ID so future sandbox creation skips the picker.
	projCfg, loadErr := config.LoadProjectConfig(workDir)
	if loadErr != nil {
		projCfg = &config.ProjectConfig{}
	}
	if projCfg.OrgID == "" {
		projCfg.OrgID = resolvedOrgID
		if saveErr := config.SaveProjectConfig(workDir, projCfg); saveErr != nil {
			streams.ErrPrintf("warning: could not save org ID to project config: %v\n", saveErr)
		}
	}
	streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Created sandbox %s (%s)", sc.Name, sc.ID)))
	*sidecarID = sc.ID
	return nil
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
