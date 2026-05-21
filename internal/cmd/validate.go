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

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
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

// initHook applies hook-specific context, stream, and early-exit logic.
// Returns updated ctx and streams, a skip flag (true = return nil immediately),
// and a non-nil error when the hook should exit with a non-zero code.
func initHook(ctx context.Context, hook *hookContext, workDir string, streams iostream.Streams) (context.Context, iostream.Streams, bool, error) {
	if hook == nil {
		return ctx, streams, false, nil
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
		return ctx, streams, false, validate.NewHookExitError(1)
	}
	if !validate.HasGitChanges(workDir) {
		return ctx, streams, true, nil
	}
	return ctx, streams, false, nil
}

func maybeEnsureCircleCIClient(ctx context.Context, cmd *cobra.Command, rc config.ResolvedConfig, allRemote bool, cfg *config.ProjectConfig, streams iostream.Streams) (*circleci.Client, error) {
	if !allRemote && !cfg.HasRemoteCommands() {
		return nil, nil
	}
	return ensureCircleCIClient(ctx, cmd, rc, streams, tui.PromptHidden)
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

	var skip bool
	var hookErr error
	ctx, streams, skip, hookErr = initHook(ctx, hook, workDir, streams)
	if hookErr != nil {
		return hookErr
	}
	if skip {
		return nil
	}
	statusFn := newStatusFunc(streams)
	insecureStorage := insecureStorageFlag(cmd)

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
	rc, _ := config.Resolve("", "", insecureStorage)
	if hook != nil && cfg.HasRemoteCommands() && rc.CircleCIToken == "" {
		streams.ErrPrintln("CircleCI auth is not configured.")
		streams.ErrPrintln("Suggestion: " + suggestionCircleCIAuth)
		streams.ErrPrintln("Don't have an account? Sign up at https://app.circleci.com/signup")
		return errSilentExit
	}

	// allRemote is true when the caller explicitly targets the sidecar
	// (--remote or --sidecar-id), meaning every command runs there.
	// Per-command routing only applies when the sidecar is resolved implicitly.
	allRemote := opts.remote || opts.sidecarID != ""

	image := resolveImage(name, cfg)

	circleCIClient, err := maybeEnsureCircleCIClient(cmd.Context(), cmd, rc, allRemote, cfg, streams)
	if err != nil {
		return err
	}

	freshlyCreated, err := setupRemote(ctx, circleCIClient, opts, image, cfg, hook, statusFn, workDir, streams)
	if err != nil {
		return err
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
		if err := syncToSidecar(ctx, circleCIClient, opts.sidecarID, opts.identityFile, opts.workdir, statusFn); err != nil {
			return err
		}
	}

	execErr := runValidate(ctx, circleCIClient, rc, workDir, name, opts.inlineCmd, opts.save, opts.sidecarID, freshlyCreated, opts.identityFile, opts.workdir, allRemote, envVars, cfg, statusFn, streams)

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
	return mapValidateError(validate.RunDryRun(cfg, name, statusFn))
}

// runValidate dispatches to the appropriate Run* function based on the
// provided options. It is shared by both direct and hook invocations.
// allRemote is true when --remote is passed explicitly (all commands run on the
// sidecar); false means only commands with Remote:true are routed to the sidecar.
func runValidate(ctx context.Context, client *circleci.Client, rc config.ResolvedConfig, workDir, name, inlineCmd string, save bool, sidecarID string, freshlyCreated bool, identityFile, workdir string, allRemote bool, envVars map[string]string, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
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
			execFn, dest, err := openSSHSession(ctx, client, sidecarID, identityFile, workdir, envVars, rc)
			if err != nil {
				return err
			}
			return validate.RunRemoteInline(ctx, execFn, cmdName, inlineCmd, dest, statusFn, streams)
		}
		return validate.RunInline(ctx, workDir, cmdName, inlineCmd, statusFn, streams)
	}

	// All-remote execution (--remote flag): send everything to the sidecar.
	if sidecarID != "" && allRemote {
		execFn, dest, err := openSSHSession(ctx, client, sidecarID, identityFile, workdir, envVars, rc)
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
				execFn, dest, err := openSSHSession(ctx, client, sidecarID, identityFile, workdir, envVars, rc)
				if err != nil {
					return err
				}
				return validate.RunRemote(ctx, execFn, cfg, name, dest, statusFn, streams)
			}
			statusFn(iostream.LevelInfo, fmt.Sprintf("running %s locally (not marked remote)", name))
			// Named command is not marked remote; fall through to local execution.
		} else {
			return runSplitCommands(ctx, client, sidecarID, freshlyCreated, identityFile, workdir, workDir, envVars, rc, cfg, statusFn, streams)
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

// setupRemote resolves (or creates) the sidecar ID based on the validate flags
// and config, then returns whether a new sidecar was provisioned.
func setupRemote(ctx context.Context, client *circleci.Client, opts *validateOpts, image string, cfg *config.ProjectConfig, hook *hookContext, statusFn iostream.StatusFunc, workDir string, streams iostream.Streams) (bool, error) {
	if opts.remote || cfg.HasRemoteCommands() {
		if opts.remote {
			created, err := resolveOrCreateSidecarID(ctx, client, &opts.sidecarID, opts.orgID, image, workDir, streams)
			if err != nil {
				return false, err
			}
			statusFn(iostream.LevelInfo, fmt.Sprintf("running all commands on sidecar %s", opts.sidecarID))
			return created, nil
		}
		return resolveSidecar(ctx, client, &opts.sidecarID, opts.orgID, image, workDir, hook, streams), nil
	}
	return false, nil
}

func syncToSidecar(ctx context.Context, client *circleci.Client, sidecarID, identityFile, workdir string, statusFn iostream.StatusFunc) error {
	authSock := os.Getenv(config.EnvSSHAuthSock)
	err := sidecar.Sync(ctx, client, sidecarID, identityFile, authSock, workdir, statusFn)
	if err == nil {
		return nil
	}
	var baseErr *sidecar.RemoteBaseError
	if errors.As(err, &baseErr) {
		return &userError{
			msg:        "Could not sync to sidecar: your current branch has not been pushed.",
			suggestion: "Push your branch and try again.",
			err:        err,
		}
	}
	return &userError{msg: "Could not sync to sidecar.", err: err}
}

// openSSHSession establishes an SSH session to the sidecar and returns an
// exec function and the resolved remote working directory.
func openSSHSession(ctx context.Context, client *circleci.Client, sidecarID, identityFile, workdir string, envVars map[string]string, rc config.ResolvedConfig) (func(context.Context, string) (string, string, int, error), string, error) {
	authSock := os.Getenv(config.EnvSSHAuthSock)
	session, err := sidecar.OpenSession(ctx, client, sidecarID, identityFile, authSock)
	if err != nil {
		return nil, "", &userError{msg: "Could not open SSH session to sidecar.", err: err}
	}
	cwd, _ := os.Getwd()
	_, repo, _ := gitremote.DetectOrgAndRepo(cwd)
	dest := sidecar.ResolveWorkspace(ctx, workdir, repo)
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
func runSplitCommands(ctx context.Context, client *circleci.Client, sidecarID string, freshlyCreated bool, identityFile, workdir, workDir string, envVars map[string]string, rc config.ResolvedConfig, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	remoteCfg, localCfg := splitByRemote(cfg)
	if len(remoteCfg.Commands) > 0 {
		statusFn(iostream.LevelInfo, fmt.Sprintf("running on sidecar %s: %s", sidecarID, commandNames(remoteCfg.Commands)))
	}
	if len(localCfg.Commands) > 0 {
		statusFn(iostream.LevelInfo, fmt.Sprintf("running locally: %s", commandNames(localCfg.Commands)))
	}
	var runErr error
	if len(remoteCfg.Commands) > 0 {
		execFn, dest, err := openSSHSession(ctx, client, sidecarID, identityFile, workdir, envVars, rc)
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
		if err := mapValidateError(validate.RunAll(ctx, workDir, localCfg, statusFn, streams)); err != nil {
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
func resolveSidecar(ctx context.Context, client *circleci.Client, sidecarID *string, orgID, image, workDir string, hook *hookContext, streams iostream.Streams) bool {
	statusFn := newStatusFunc(streams)
	if active, err := sidecar.LoadActive(ctx); err == nil && active != nil {
		*sidecarID = active.SidecarID
		statusFn(iostream.LevelInfo, fmt.Sprintf("using sidecar %s for remote commands", *sidecarID))
		return false
	}
	if hook != nil || image != "" {
		// In Stop hook context, or when a sidecar image is configured: auto-create
		// from the stored snapshot so remote commands get the prepared environment.
		created, err := resolveOrCreateSidecarID(ctx, client, sidecarID, orgID, image, workDir, streams)
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
func resolveOrCreateSidecarID(ctx context.Context, client *circleci.Client, sidecarID *string, orgID, image, workDir string, streams iostream.Streams) (created bool, err error) {
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
	resolvedOrgID, err := resolveOrgID(orgID, configOrgID(workDir), orgPicker(ctx, client))
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
