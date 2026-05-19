package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

func newStatusFunc(streams iostream.Streams) iostream.StatusFunc {
	return func(level iostream.Level, msg string) {
		switch level {
		case iostream.LevelStep:
			streams.ErrPrintln(ui.ErrBold(msg))
		case iostream.LevelInfo:
			streams.ErrPrintf("  %s\n", ui.ErrDim(msg))
		case iostream.LevelWarn:
			streams.ErrPrintf("  %s\n", ui.ErrWarning(msg))
		case iostream.LevelDone:
			streams.ErrPrintf("  %s\n", ui.ErrSuccess(msg))
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

func runValidateList(workDir string, jsonOut bool, streams iostream.Streams, statusFn iostream.StatusFunc) error {
	cfg, err := config.LoadProjectConfig(workDir)
	if err != nil {
		cfg = &config.ProjectConfig{}
	}
	if jsonOut {
		cmds := cfg.Commands
		if cmds == nil {
			cmds = []config.Command{}
		}
		return iostream.PrintJSON(streams.Out, cmds)
	}
	return validate.List(cfg, statusFn)
}

func newValidateCmd() *cobra.Command {
	var sidecarID, identityFile, workdir, orgID string
	var dryRun, list, save, remote, jsonOut bool
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
			ctx := cmd.Context()
			if hook != nil {
				ctx = session.WithID(ctx, hook.sessionID)
				if !hook.stopHookActive {
					validate.ResetAttempts(hook.sessionID)
				}
				// Route stdout to stderr so all output appears in the Stop
				// hook feedback block that Claude Code shows the agent.
				streams = iostream.Streams{Out: streams.Err, Err: streams.Err}
			}
			statusFn := newStatusFunc(streams)

			// Hook: exit 1 with a message when hooks are disabled.
			envDisabled := os.Getenv(config.EnvChunkHooksDisabled) != ""
			if hook != nil && validate.HooksDisabled(workDir, envDisabled) {
				streams.ErrPrintln("chunk validate: hooks are disabled — skipping validation")
				return validate.NewHookExitError(1)
			}

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
				return runValidateList(workDir, jsonOut, streams, statusFn)
			}
			if jsonOut {
				return fmt.Errorf("--json requires --list")
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
				return runValidateDryRun(cfg, name, inlineCmd, statusFn)
			}

			// Hook: fail early when CircleCI auth is missing and remote commands need it.
			// In non-hook context ensureCircleCIClient prompts interactively; hooks have
			// no TTY so we surface a clear message here instead of a confusing fallback.
			rc, _ := config.Resolve("", "")
			if hook != nil && cfg.HasRemoteCommands() && rc.CircleCIToken == "" {
				streams.ErrPrintln("CircleCI auth is not configured.")
				streams.ErrPrintln("Suggestion: " + suggestionCircleCIAuth)
				return errSilentExit
			}

			// allRemote is true when the caller explicitly targets the sidecar
			// (--remote or --sidecar-id), meaning every command runs there.
			// Per-command routing only applies when the sidecar is resolved implicitly.
			allRemote := remote || sidecarID != ""

			image := resolveImage(name, cfg)

			freshlyCreated := false
			if remote {
				// --remote: force all commands to sidecar, creating one if needed.
				var err error
				freshlyCreated, err = resolveOrCreateSidecarID(ctx, &sidecarID, orgID, image, workDir, streams)
				if err != nil {
					return err
				}
				statusFn(iostream.LevelInfo, fmt.Sprintf("running all commands on sidecar %s", sidecarID))
			} else if cfg.HasRemoteCommands() {
				freshlyCreated = resolveSidecar(ctx, &sidecarID, orgID, image, workDir, hook, streams)
			}

			results, execErr := runValidate(ctx, workDir, name, inlineCmd, save, sidecarID, freshlyCreated, identityFile, workdir, allRemote, cfg, statusFn, streams)
			saveValidateResults(workDir, results)

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
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON (only applies with --list)")
	cmd.Flags().StringVar(&inlineCmd, "cmd", "", "Run an inline command instead of config")
	cmd.Flags().BoolVar(&save, "save", false, "Save --cmd to .chunk/config.json")
	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")

	cmd.AddCommand(newPostCommitCmd())

	return cmd
}

func runValidateDryRun(cfg *config.ProjectConfig, name, inlineCmd string, statusFn iostream.StatusFunc) error {
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

// runValidate dispatches to the appropriate Run* function based on the
// provided options. It is shared by both direct and hook invocations.
// allRemote is true when --remote is passed explicitly (all commands run on the
// sidecar); false means only commands with Remote:true are routed to the sidecar.
// Returns nil results for remote/sidecar runs (no result recording on those paths).
func runValidate(ctx context.Context, workDir, name, inlineCmd string, save bool, sidecarID string, freshlyCreated bool, identityFile, workdir string, allRemote bool, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) ([]validate.CommandResult, error) {
	// --cmd: inline command
	if inlineCmd != "" {
		cmdName := name
		if cmdName == "" {
			cmdName = "custom"
		}
		if save {
			if err := config.SaveCommand(workDir, cmdName, inlineCmd); err != nil {
				return nil, &userError{msg: "Could not save command to .chunk/config.json.", err: err}
			}
			streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", cmdName)))
		}
		if sidecarID != "" && allRemote {
			execFn, dest, err := openSSHSession(ctx, sidecarID, identityFile, workdir, streams)
			if err != nil {
				return nil, err
			}
			return nil, validate.RunRemoteInline(ctx, execFn, cmdName, inlineCmd, dest, statusFn, streams)
		}
		return validate.RunInline(ctx, workDir, cmdName, inlineCmd, statusFn, streams)
	}

	// All-remote execution (--remote flag): send everything to the sidecar.
	if sidecarID != "" && allRemote {
		execFn, dest, err := openSSHSession(ctx, sidecarID, identityFile, workdir, streams)
		if err != nil {
			return nil, err
		}
		return nil, validate.RunRemote(ctx, execFn, cfg, name, dest, statusFn, streams)
	}

	// Per-command remote routing: commands with Remote:true go to the sidecar,
	// the rest run locally.
	if sidecarID != "" {
		if name != "" {
			if cmd := cfg.FindCommand(name); cmd != nil && cmd.Remote {
				statusFn(iostream.LevelInfo, fmt.Sprintf("running %s on sidecar %s", name, sidecarID))
				execFn, dest, err := openSSHSession(ctx, sidecarID, identityFile, workdir, streams)
				if err != nil {
					return nil, err
				}
				return nil, validate.RunRemote(ctx, execFn, cfg, name, dest, statusFn, streams)
			}
			statusFn(iostream.LevelInfo, fmt.Sprintf("running %s locally (not marked remote)", name))
			// Named command is not marked remote; fall through to local execution.
		} else {
			return nil, runSplitCommands(ctx, sidecarID, freshlyCreated, identityFile, workdir, workDir, cfg, statusFn, streams)
		}
	}

	// Named command
	if name != "" {
		if cfg.FindCommand(name) == nil {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return nil, &userError{
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
				return nil, &userError{msg: "No command entered.", errMsg: "no input received"}
			}
			input := strings.TrimSpace(scanner.Text())
			if input == "" {
				streams.ErrPrintln(ui.Dim("No command entered, aborting."))
				return nil, &userError{msg: "No command entered.", errMsg: "no command entered"}
			}
			if err := config.SaveCommand(workDir, name, input); err != nil {
				return nil, &userError{msg: "Could not save command to .chunk/config.json.", err: err}
			}
			streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", name)))
			var err error
			cfg, err = config.LoadProjectConfig(workDir)
			if err != nil {
				return nil, err
			}
		}
		results, err := validate.RunNamed(ctx, workDir, name, cfg, statusFn, streams)
		return results, mapValidateError(err)
	}

	// Run all
	results, err := validate.RunAll(ctx, workDir, cfg, statusFn, streams)
	return results, mapValidateError(err)
}

func newPostCommitCmd() *cobra.Command {
	var projectDir string

	cmd := &cobra.Command{
		Use:          "post-commit",
		Short:        "Report validate results as GitHub commit statuses",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			streams := iostream.FromCmd(cmd)
			ctx := cmd.Context()

			workDir := projectDir
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return err
				}
			}

			projCfg, cfgErr := config.LoadProjectConfig(workDir)
			if cfgErr != nil || projCfg.Validation == nil || !projCfg.Validation.CommitStatus {
				return nil
			}

			treeOut, err := exec.Command("git", "-C", workDir, "rev-parse", "HEAD^{tree}").Output()
			if err != nil {
				return fmt.Errorf("resolve HEAD tree: %w", err)
			}
			treeSHA := strings.TrimSpace(string(treeOut))

			results, found, err := validate.LoadResults(treeSHA)
			if err != nil {
				return fmt.Errorf("load validate results: %w", err)
			}
			if !found {
				return nil
			}

			commitOut, err := exec.Command("git", "-C", workDir, "rev-parse", "HEAD").Output()
			if err != nil {
				return fmt.Errorf("resolve HEAD: %w", err)
			}
			commitSHA := strings.TrimSpace(string(commitOut))

			org, repo, err := gitremote.DetectOrgAndRepo(workDir)
			if err != nil {
				return fmt.Errorf("detect repo: %w", err)
			}

			ghClient, err := ensureGitHubClient(ctx, streams, tui.PromptHidden)
			if err != nil {
				return err
			}

			for _, r := range results {
				state := "success"
				if !r.Passed {
					state = "failure"
				}
				if postErr := ghClient.CreateCommitStatus(ctx, org, repo, commitSHA, state, "chunk/"+r.Name, "chunk validate: "+r.Name); postErr != nil {
					streams.ErrPrintf("  %s\n", ui.Warning(fmt.Sprintf("could not post status for %s: %v", r.Name, postErr)))
					continue
				}
				streams.ErrPrintf("  %s %s/%s → %s\n", ui.Success("posted"), org, repo, r.Name)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")
	return cmd
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
	cwd, _ := os.Getwd()
	_, repo, _ := gitremote.DetectOrgAndRepo(cwd)
	dest := sidecar.ResolveWorkspace(ctx, workdir, repo)
	rc, err := config.Resolve("", "")
	if err != nil {
		return nil, "", &userError{msg: "Could not resolve config.", err: err}
	}
	envVars := hostForwardEnv(rc.CircleCIToken)
	execFn := func(ctx context.Context, script string) (string, string, int, error) {
		result, err := sidecar.ExecOverSSH(ctx, session, "sh -c "+sidecar.ShellEscape(script), nil, envVars)
		if err != nil {
			return "", "", 0, err
		}
		return result.Stdout, result.Stderr, result.ExitCode, nil
	}
	return execFn, dest, nil
}

// hostForwardEnv collects host environment variables that should be forwarded
// into commands running on the sidecar. The resolved CircleCI token (which may
// come from env, the on-disk config, or any future keychain backend) is
// forwarded as CIRCLE_TOKEN so remote validate commands can authenticate to
// CircleCI APIs (e.g. smarter-testing endpoints), mirroring the local behavior
// where the token is picked up from the resolved config.
func hostForwardEnv(token string) map[string]string {
	if token == "" {
		return nil
	}
	return map[string]string{config.EnvCircleToken: token}
}

// runSplitCommands handles per-command remote routing when no specific command
// name is given: remote-tagged commands go to the sidecar, the rest run locally.
// When freshlyCreated is true, SSH failures are hard errors rather than
// silent local fallbacks (a newly provisioned sidecar that can't be reached
// indicates a real problem, not temporary unavailability).
func runSplitCommands(ctx context.Context, sidecarID string, freshlyCreated bool, identityFile, workdir, workDir string, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	remoteCfg, localCfg := splitByRemote(cfg)
	if len(remoteCfg.Commands) > 0 {
		statusFn(iostream.LevelInfo, fmt.Sprintf("running on sidecar %s: %s", sidecarID, commandNames(remoteCfg.Commands)))
	}
	if len(localCfg.Commands) > 0 {
		statusFn(iostream.LevelInfo, fmt.Sprintf("running locally: %s", commandNames(localCfg.Commands)))
	}
	var runErr error
	if len(remoteCfg.Commands) > 0 {
		execFn, dest, err := openSSHSession(ctx, sidecarID, identityFile, workdir, streams)
		if err != nil {
			if freshlyCreated {
				return newUserError(fmt.Sprintf("Could not reach newly created sidecar %s.", sidecarID)).
					withCode("sidecar.unreachable").
					withSuggestion("The sidecar may still be starting. Try again in a moment.").
					withExitCode(ExitAPIError).
					wrap(err)
			}
			streams.ErrPrintf("warning: could not reach sidecar (%v); running %s locally instead\n", err, commandNames(remoteCfg.Commands))
			localCfg.Commands = append(remoteCfg.Commands, localCfg.Commands...)
		} else if wsErr := validate.WorkspaceExists(ctx, execFn, dest); wsErr != nil {
			if freshlyCreated {
				return newUserError(fmt.Sprintf("Workspace not found on newly created sidecar %s.", sidecarID)).
					withCode("sidecar.workspace_missing").
					withSuggestion("Run 'chunk sidecar env build' to prepare the workspace.").
					withExitCode(ExitNotFound).
					wrap(wsErr)
			}
			streams.ErrPrintf("warning: %v (%q); run 'chunk sidecar env build' to set up the workspace; running %s locally instead\n", wsErr, dest, commandNames(remoteCfg.Commands))
			localCfg.Commands = append(remoteCfg.Commands, localCfg.Commands...)
		} else {
			runErr = validate.RunRemote(ctx, execFn, remoteCfg, "", dest, statusFn, streams)
		}
	}
	if len(localCfg.Commands) > 0 {
		_, localErr := validate.RunAll(ctx, workDir, localCfg, statusFn, streams)
		if err := mapValidateError(localErr); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	return runErr
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

// resolveSidecar fills sidecarID for per-command remote routing
// (i.e. when --remote is not set but some commands have Remote:true).
// It uses the active sidecar when available, auto-creates one when a sidecar
// image is configured or the caller is a Stop hook, and warns otherwise.
// Returns true when a brand-new sidecar was provisioned in this call.
func resolveSidecar(ctx context.Context, sidecarID *string, orgID, image, workDir string, hook *hookContext, streams iostream.Streams) bool {
	statusFn := newStatusFunc(streams)
	if active, err := sidecar.LoadActive(ctx); err == nil && active != nil {
		*sidecarID = active.SidecarID
		statusFn(iostream.LevelInfo, fmt.Sprintf("using sidecar %s for remote commands", *sidecarID))
		return false
	}
	if hook != nil || image != "" {
		// In Stop hook context, or when a sidecar image is configured: auto-create
		// from the stored snapshot so remote commands get the prepared environment.
		created, err := resolveOrCreateSidecarID(ctx, sidecarID, orgID, image, workDir, streams)
		if err != nil {
			streams.ErrPrintf("warning: no sandbox available (%v); run 'chunk config set orgID <id>' to enable remote validation, running locally instead\n", err)
		}
		return created
	}
	statusFn(iostream.LevelWarn, "no active sidecar found — remote commands will run locally")
	return false
}

// resolveOrCreateSidecarID fills sidecarID from the active sidecar, or creates
// a new sandbox when none is configured. Returns true when a new sidecar was
// provisioned (as opposed to loaded from the active state file).
func resolveOrCreateSidecarID(ctx context.Context, sidecarID *string, orgID, image, workDir string, streams iostream.Streams) (created bool, err error) {
	if *sidecarID != "" {
		return false, nil
	}
	active, loadErr := sidecar.LoadActive(ctx)
	if loadErr != nil {
		return false, &userError{msg: msgCouldNotLoadSidecar, suggestion: configFilePermHint, err: loadErr}
	}
	if active != nil {
		*sidecarID = active.SidecarID
		return false, nil
	}
	streams.ErrPrintf("No active sidecar found, creating a new sandbox...\n")
	client, err := ensureCircleCIClient(ctx, streams, tui.PromptHidden)
	if err != nil {
		return false, err
	}
	// Fallback: read org ID from project config if not provided via flag or env.
	if orgID == "" {
		if projCfg, cfgErr := config.LoadProjectConfig(workDir); cfgErr == nil && projCfg.OrgID != "" {
			orgID = projCfg.OrgID
		}
	}
	resolvedOrgID, err := resolveOrgID(orgID, orgPicker(ctx, client))
	if err != nil {
		return false, err
	}
	sandboxName := filepath.Base(workDir) + "-validate"
	sc, err := sidecar.Create(ctx, client, resolvedOrgID, sandboxName, image)
	if err != nil {
		if authErr := notAuthorized("create sidecars", err); authErr != nil {
			return false, authErr
		}
		return false, &userError{
			msg:        "Could not create a sandbox.",
			suggestion: "Check your network connection or run 'chunk sidecar create' manually.",
			err:        err,
		}
	}
	if saveErr := sidecar.SaveActive(ctx, sidecar.ActiveSidecar{SidecarID: sc.ID, Name: sc.Name}); saveErr != nil {
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
	return true, nil
}

func saveValidateResults(workDir string, results []validate.CommandResult) {
	if len(results) == 0 {
		return
	}
	cfg, err := config.LoadProjectConfig(workDir)
	if err != nil || cfg.Validation == nil || !cfg.Validation.CommitStatus {
		return
	}
	if treeSHA, err := gitutil.ComputeTreeSHA(workDir); err == nil {
		_ = validate.SaveResults(treeSHA, results)
	}
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
