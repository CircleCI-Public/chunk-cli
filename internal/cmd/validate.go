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
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
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

var errHookSkip = errors.New("hook: skip validation")

// initHook applies hook-specific context, stream, and early-exit logic.
// Returns updated ctx and streams. A nil error means proceed with validation;
// errHookSkip means the hook decided to skip (clean tree); any other error
// should be returned directly from the command (e.g. hooks disabled).
func initHook(ctx context.Context, hook *hookContext, workDir string, streams iostream.Streams) (context.Context, iostream.Streams, error) {
	if hook == nil {
		return ctx, streams, nil
	}
	ctx = session.WithID(ctx, hook.sessionID)
	if !hook.stopHookActive {
		validate.ResetAttempts(hook.sessionID)
	}
	// Route stdout to stderr so all output appears in the Stop
	// hook feedback block that Claude Code shows the agent.
	streams = iostream.Streams{Out: streams.Err, Err: streams.Err}
	if validate.HooksDisabled(workDir, os.Getenv(config.EnvChunkHooksDisabled) != "") {
		streams.ErrPrintln("chunk validate: hooks are disabled — skipping validation")
		return ctx, streams, validate.NewHookExitError(1)
	}
	if !validate.HasGitChanges(workDir) {
		return ctx, streams, errHookSkip
	}
	return ctx, streams, nil
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

type validateOpts struct {
	sidecarID    string
	identityFile string
	workdir      string
	orgID        string
	dryRun       bool
	list         bool
	save         bool
	remote       bool
	jsonOut      bool
	inlineCmd    string
	projectDir   string
	envVarsFlag  []string
	envFile      string
}

func newValidateCmd() *cobra.Command {
	var opts validateOpts

	cmd := &cobra.Command{
		Use:          "validate [name]",
		Short:        "Run validation commands",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidateCmdE(cmd, args, &opts)
		},
	}

	cmd.Flags().BoolVar(&opts.remote, "remote", false, "Run on active sidecar, or create one if none is set")
	cmd.Flags().StringVar(&opts.sidecarID, "sidecar-id", "", "Sidecar ID for remote execution")
	cmd.Flags().StringVar(&opts.orgID, "org-id", "", "Organization ID (used when creating a new sidecar)")
	cmd.Flags().StringVar(&opts.identityFile, "identity-file", "", "SSH identity file (uses ssh-agent or ~/.ssh/chunk_ai when omitted)")
	cmd.Flags().StringVar(&opts.workdir, "workdir", "", "Working directory on sidecar (reads from sidecar.json, defaults to ./workspace)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Show commands without executing")
	cmd.Flags().BoolVar(&opts.list, "list", false, "List all configured commands")
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "Output as JSON (only applies with --list)")
	cmd.Flags().StringVar(&opts.inlineCmd, "cmd", "", "Run an inline command instead of config")
	cmd.Flags().BoolVar(&opts.save, "save", false, "Save --cmd to .chunk/config.json")
	cmd.Flags().StringVar(&opts.projectDir, "project", "", "Override project directory")
	cmd.Flags().StringArrayVarP(&opts.envVarsFlag, "env", "e", nil, "KEY=VALUE pairs to set in remote sidecar session (repeatable)")
	cmd.Flags().StringVar(&opts.envFile, "env-file", defaultEnvFile, "Env file to load (default: .env.local; pass a path to override)")

	return cmd
}

func runValidateCmdE(cmd *cobra.Command, args []string, opts *validateOpts) error {
	streams := iostream.FromCmd(cmd)

	workDir := opts.projectDir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return err
		}
	}

	hook := detectHook(cmd.InOrStdin())
	ctx := cmd.Context()

	ctx, streams, hookErr := initHook(ctx, hook, workDir, streams)
	if errors.Is(hookErr, errHookSkip) {
		return nil
	}
	if hookErr != nil {
		return hookErr
	}
	statusFn := newStatusFunc(streams)

	var name string
	if len(args) == 1 {
		name = args[0]
	}

	// --list: show configured commands
	if opts.list {
		return runValidateList(workDir, opts.jsonOut, streams, statusFn)
	}
	if opts.jsonOut {
		return fmt.Errorf("--json requires --list")
	}

	cfg, err := config.LoadProjectConfig(workDir)
	if hook != nil && (err != nil || !cfg.HasCommands()) && opts.inlineCmd == "" {
		return nil // no config in hook context: skip silently
	}
	if (err != nil || !cfg.HasCommands()) && opts.inlineCmd == "" {
		return &userError{
			msg:        "No validate commands configured.",
			suggestion: "Run 'chunk init' first.",
			errMsg:     "no validate commands configured",
		}
	}

	// Validate --env flag syntax before any remote resolution so bad
	// values are caught immediately regardless of execution mode.
	if len(opts.envVarsFlag) > 0 {
		if _, vErr := sidecar.ParseEnvPairs(opts.envVarsFlag); vErr != nil {
			return &userError{msg: fmt.Sprintf("invalid --env value: %s", vErr), err: vErr}
		}
	}

	if opts.dryRun {
		return runValidateDryRun(name, opts.inlineCmd, cfg, statusFn)
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
	allRemote := opts.remote || opts.sidecarID != ""

	image := resolveImage(name, cfg)

	freshlyCreated := false
	if opts.remote {
		// --remote: force all commands to sidecar, creating one if needed.
		freshlyCreated, err = resolveOrCreateSidecarID(ctx, &opts.sidecarID, opts.orgID, image, workDir, streams)
		if err != nil {
			return err
		}
		statusFn(iostream.LevelInfo, fmt.Sprintf("running all commands on sidecar %s", opts.sidecarID))
	} else if cfg.HasRemoteCommands() {
		freshlyCreated = resolveSidecar(ctx, &opts.sidecarID, opts.orgID, image, workDir, hook, streams)
	}

	// Only load env vars and resolve secrets when a sidecar is actually
	// being used — avoids parsing .env.local or hitting secrets APIs on
	// purely local runs.
	var envVars map[string]string
	if opts.sidecarID != "" {
		envVars, err = resolveEnvVars(ctx, workDir, opts.envFile, opts.envVarsFlag)
		if err != nil {
			return err
		}
	}

	var execErr error
	switch {
	case opts.inlineCmd != "":
		if opts.save {
			cmdName := name
			if cmdName == "" {
				cmdName = "custom"
			}
			if err := config.SaveCommand(workDir, cmdName, opts.inlineCmd); err != nil {
				return &userError{msg: "Could not save command to .chunk/config.json.", err: err}
			}
			streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", cmdName)))
		}
		execErr = runInline(ctx, workDir, name, opts.inlineCmd, opts.sidecarID, allRemote, opts.identityFile, opts.workdir, envVars, cfg, statusFn, streams)
	case name != "":
		execErr = runNamed(ctx, workDir, name, opts.sidecarID, allRemote, opts.identityFile, opts.workdir, envVars, cfg, statusFn, streams)
	default:
		execErr = runAll(ctx, workDir, opts, cfg, freshlyCreated, envVars, allRemote, statusFn, streams)
	}

	execErr = mapValidateError(execErr)

	if hook != nil {
		maxAttempts := cfg.StopHookMaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = validate.DefaultMaxAttempts
		}
		return validate.WrapHookResult(hook.sessionID, execErr, maxAttempts, streams.Err)
	}
	return execErr
}

func runValidateDryRun(name, inlineCmd string, cfg *config.ProjectConfig, statusFn iostream.StatusFunc) error {
	if inlineCmd != "" {
		cmdName := name
		if cmdName == "" {
			cmdName = "custom"
		}
		statusFn(iostream.LevelInfo, fmt.Sprintf("%s: %s", cmdName, inlineCmd))
		return nil
	}
	runner := validate.NewRunner(cfg, nil, statusFn, iostream.Streams{})
	return mapValidateError(runner.DryRun(name))
}

// runWithRunner dispatches to the appropriate Runner method.
func runWithRunner(ctx context.Context, workDir, name, inlineCmd string, save bool, cfg *config.ProjectConfig, executor validate.Executor, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	runner := validate.NewRunner(cfg, executor, statusFn, streams)
	if inlineCmd != "" {
		return runner.RunInline(ctx, name, inlineCmd)
	}
	if name != "" {
		return runner.RunNamed(ctx, name)
	}
	return runner.RunAll(ctx)
}

func runInline(ctx context.Context, workDir, name, inlineCmd, sidecarID string, allRemote bool, identityFile, workdir string, envVars map[string]string, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	if sidecarID != "" && allRemote {
		executor, err := openSSHSession(ctx, sidecarID, identityFile, workdir, envVars, streams)
		if err != nil {
			return err
		}
		return runWithRunner(ctx, workDir, name, inlineCmd, false, cfg, executor, statusFn, streams)
	}
	executor := validate.NewLocalExecutor(workDir, streams)
	return runWithRunner(ctx, workDir, name, inlineCmd, false, cfg, executor, statusFn, streams)
}

func runNamed(ctx context.Context, workDir, name, sidecarID string, allRemote bool, identityFile, workdir string, envVars map[string]string, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	// Interactive setup only when not in all-remote mode.
	if !allRemote && cfg.FindCommand(name) == nil {
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
		executor := validate.NewLocalExecutor(workDir, streams)
		return runWithRunner(ctx, workDir, name, "", false, cfg, executor, statusFn, streams)
	}

	// Per-command remote routing: run this specific command on the sidecar
	// if it is marked remote.
	if sidecarID != "" && !allRemote {
		if cmd := cfg.FindCommand(name); cmd != nil && cmd.Remote {
			executor, err := openSSHSession(ctx, sidecarID, identityFile, workdir, envVars, streams)
			if err != nil {
				return err
			}
			statusFn(iostream.LevelInfo, fmt.Sprintf("running %s on sidecar %s", name, sidecarID))
			return runWithRunner(ctx, workDir, name, "", false, cfg, executor, statusFn, streams)
		}
		statusFn(iostream.LevelInfo, fmt.Sprintf("running %s locally (not marked remote)", name))
	}

	var executor validate.Executor
	if sidecarID != "" && allRemote {
		var err error
		executor, err = openSSHSession(ctx, sidecarID, identityFile, workdir, envVars, streams)
		if err != nil {
			return err
		}
	} else {
		executor = validate.NewLocalExecutor(workDir, streams)
	}
	return runWithRunner(ctx, workDir, name, "", false, cfg, executor, statusFn, streams)
}

func runAll(ctx context.Context, workDir string, opts *validateOpts, cfg *config.ProjectConfig, freshlyCreated bool, envVars map[string]string, allRemote bool, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	if opts.sidecarID != "" && !allRemote {
		return runSplit(ctx, workDir, opts, cfg, freshlyCreated, envVars, statusFn, streams)
	}

	var executor validate.Executor
	if opts.sidecarID != "" && allRemote {
		var err error
		executor, err = openSSHSession(ctx, opts.sidecarID, opts.identityFile, opts.workdir, envVars, streams)
		if err != nil {
			return err
		}
	} else {
		executor = validate.NewLocalExecutor(workDir, streams)
	}
	return runWithRunner(ctx, workDir, "", "", false, cfg, executor, statusFn, streams)
}

// openSSHSession establishes an SSH session to the sidecar and returns a
// RemoteExecutor wired to run commands on it.
func openSSHSession(ctx context.Context, sidecarID, identityFile, workdir string, envVars map[string]string, streams iostream.Streams) (*validate.RemoteExecutor, error) {
	client, err := ensureCircleCIClient(ctx, streams, tui.PromptHidden)
	if err != nil {
		return nil, err
	}
	authSock := os.Getenv(config.EnvSSHAuthSock)
	session, err := sidecar.OpenSession(ctx, client, sidecarID, identityFile, authSock)
	if err != nil {
		return nil, &userError{msg: "Could not open SSH session to sidecar.", err: err}
	}
	cwd, _ := os.Getwd()
	_, repo, _ := gitremote.DetectOrgAndRepo(cwd)
	dest := sidecar.ResolveWorkspace(ctx, workdir, repo)
	rc, err := config.Resolve("", "")
	if err != nil {
		return nil, &userError{msg: "Could not resolve config.", err: err}
	}
	merged := hostForwardEnv(rc.CircleCIToken)
	if merged == nil {
		merged = make(map[string]string, len(envVars))
	}
	for k, v := range envVars {
		merged[k] = v
	}
	execFn := func(ctx context.Context, script string) (string, string, int, error) {
		result, err := sidecar.ExecOverSSH(ctx, session, "sh -c "+sidecar.ShellEscape(script), nil, merged)
		if err != nil {
			return "", "", 0, err
		}
		return result.Stdout, result.Stderr, result.ExitCode, nil
	}
	return validate.NewRemoteExecutor(execFn, dest, streams), nil
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

// runSplit handles per-command remote routing when no specific command name is
// given: remote-tagged commands go to the sidecar, the rest run locally.
// When freshlyCreated is true, SSH failures are hard errors rather than
// silent local fallbacks (a newly provisioned sidecar that can't be reached
// indicates a real problem, not temporary unavailability).
func runSplit(ctx context.Context, workDir string, opts *validateOpts, cfg *config.ProjectConfig, freshlyCreated bool, envVars map[string]string, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	remoteCfg, localCfg := splitByRemote(cfg)
	if len(remoteCfg.Commands) > 0 {
		statusFn(iostream.LevelInfo, fmt.Sprintf("running on sidecar %s: %s", opts.sidecarID, commandNames(remoteCfg.Commands)))
	}
	if len(localCfg.Commands) > 0 {
		statusFn(iostream.LevelInfo, fmt.Sprintf("running locally: %s", commandNames(localCfg.Commands)))
	}
	var runErr error
	if len(remoteCfg.Commands) > 0 {
		remoteExec, err := openSSHSession(ctx, opts.sidecarID, opts.identityFile, opts.workdir, envVars, streams)
		if err != nil {
			if freshlyCreated {
				return newUserError(fmt.Sprintf("Could not reach newly created sidecar %s.", opts.sidecarID)).
					withCode("sidecar.unreachable").
					withSuggestion("The sidecar may still be starting. Try again in a moment.").
					withExitCode(ExitAPIError).
					wrap(err)
			}
			streams.ErrPrintf("warning: could not reach sidecar (%v); running %s locally instead\n", err, commandNames(remoteCfg.Commands))
			localCfg.Commands = append(remoteCfg.Commands, localCfg.Commands...)
		} else if wsErr := remoteExec.WorkspaceExists(ctx); wsErr != nil {
			if freshlyCreated {
				return newUserError(fmt.Sprintf("Workspace not found on newly created sidecar %s.", opts.sidecarID)).
					withCode("sidecar.workspace_missing").
					withSuggestion("Run 'chunk sidecar env build' to prepare the workspace.").
					withExitCode(ExitNotFound).
					wrap(wsErr)
			}
			streams.ErrPrintf("warning: %v (%q); run 'chunk sidecar env build' to set up the workspace; running %s locally instead\n", wsErr, remoteExec.Dest(), commandNames(remoteCfg.Commands))
			localCfg.Commands = append(remoteCfg.Commands, localCfg.Commands...)
		} else {
			runner := validate.NewRunner(remoteCfg, remoteExec, statusFn, streams)
			runErr = runner.RunAll(ctx)
		}
	}
	if len(localCfg.Commands) > 0 {
		localExec := validate.NewLocalExecutor(workDir, streams)
		runner := validate.NewRunner(localCfg, localExec, statusFn, streams)
		if err := runner.RunAll(ctx); err != nil {
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
