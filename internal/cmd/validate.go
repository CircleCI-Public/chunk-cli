package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
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
		case iostream.LevelError:
			streams.ErrPrintf("  %s\n", ui.ErrError(msg))
		}
	}
}

func runValidateList(workDir string, jsonOut bool, streams iostream.Streams, statusFn iostream.StatusFunc) error {
	cfg, err := config.LoadProjectConfig(workDir)
	if err != nil {
		cfg = &config.ProjectConfig{}
	}
	if jsonOut {
		cmds := cfg.Validate
		if cmds == nil {
			cmds = []config.ValidateCommand{}
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
			err := runValidateCmdE(cmd, args, &opts)
			if mapped := outdatedSidecarAPI(err); mapped != nil {
				return mapped
			}
			return err
		},
	}

	cmd.Flags().BoolVar(&opts.remote, "remote", false, "Run on active sidecar, or create one if none is set")
	cmd.Flags().StringVar(&opts.sidecarID, "sidecar-id", "", "Sidecar ID for remote execution")
	cmd.Flags().StringVar(&opts.orgID, "org-id", "", "Organization ID (used when creating a new sidecar)")
	cmd.Flags().StringVar(&opts.identityFile, "identity-file", "", "SSH identity file (uses ssh-agent or ~/.ssh/chunk_ai when omitted)")
	cmd.Flags().StringVar(&opts.workdir, "workdir", "", "Working directory on sidecar (reads from sidecar.json, defaults to /home/user/<repo>)")
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

func validateNeedsSidecar(explicitRemote bool, cfg *config.ProjectConfig) bool {
	if explicitRemote || cfg.HasRemoteCommands() {
		return true
	}
	return cfg.HasSidecarImage()
}

func loadSidecarEnvVars(ctx context.Context, client *circleci.Client, opts *validateOpts, workDir string, statusFn iostream.StatusFunc, streams iostream.Streams) (map[string]string, error) {
	if opts.sidecarID == "" {
		return nil, nil
	}
	envVars, err := resolveEnvVars(ctx, workDir, opts.envFile, opts.envVarsFlag)
	if err != nil {
		return nil, err
	}
	if err := syncToSidecar(ctx, client, opts.sidecarID, opts.identityFile, opts.workdir, statusFn, streams); err != nil {
		return nil, err
	}
	return envVars, nil
}

func maybeEnsureCircleCIClient(ctx context.Context, cmd *cobra.Command, rc config.ResolvedConfig, needsSidecar bool, streams iostream.Streams) (*circleci.Client, error) {
	if !needsSidecar {
		return nil, nil
	}
	return ensureCircleCIClient(ctx, cmd, rc, streams, tui.PromptHidden)
}

func runValidateCmdE(cmd *cobra.Command, args []string, opts *validateOpts) error {
	streams := iostream.FromCmd(cmd)

	start := time.Now()

	workDir := opts.projectDir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return err
		}
	}

	ctx := cmd.Context()
	if id := sidecar.LoadSessionID(workDir); id != "" {
		ctx = session.WithID(ctx, id)
	}

	statusFn := newStatusFunc(streams)
	insecureStorage := insecureStorageFlag(cmd)

	var name string
	if len(args) == 1 {
		name = args[0]
	}

	if opts.list {
		return runValidateList(workDir, opts.jsonOut, streams, statusFn)
	}
	if opts.jsonOut {
		return fmt.Errorf("--json requires --list")
	}

	cfg, err := config.LoadProjectConfig(workDir)
	if (err != nil || !cfg.HasValidateCommands()) && opts.inlineCmd == "" {
		return &userError{
			msg:        "No validate commands configured.",
			suggestion: suggestionValidateNotConfigured,
			errMsg:     "no validate commands configured",
			hideDetail: true,
		}
	}

	if err := validateEnvFlag(opts.envVarsFlag); err != nil {
		return err
	}

	if opts.dryRun {
		return runValidateDryRun(name, opts.inlineCmd, cfg, statusFn)
	}

	rc, _ := config.ResolveCircleCI(insecureStorage)

	explicitRemote := opts.remote || opts.sidecarID != ""
	needsSidecar := validateNeedsSidecar(explicitRemote, cfg)

	allRemote := explicitRemote
	if cfg.HasSidecarImage() {
		allRemote = true
	}

	image := resolveImage(name, cfg)

	circleCIClient, err := maybeEnsureCircleCIClient(ctx, cmd, rc, needsSidecar, streams)
	if err != nil {
		return err
	}

	statusFn = newStatusFunc(streams)
	statusFn(iostream.LevelStep, "chunk validate")

	// Reap before reading state, so a file naming a sidecar that no longer exists
	// is gone before it can be promoted and reused. Loading first would hand
	// setupRemote the very dead ID the reap just cleaned up.
	if needsSidecar {
		reapAbandonedSidecars(ctx, circleCIClient, workDir, statusFn, streams)
	}
	activeSidecar, _ := sidecar.LoadActive(ctx)

	freshlyCreated, err := setupRemote(ctx, circleCIClient, opts, image, cfg, activeSidecar, statusFn, workDir, streams)
	if err != nil {
		return err
	}

	// Wire event log after setupRemote fills opts.sidecarID but before
	// loadSidecarEnvVars so sync/env-resolve events are captured.
	// Kept unwrapped so a replacement sidecar can be rewrapped against its own ID.
	baseStatusFn := statusFn
	statusFn = wrapEventLogStatusFn(statusFn, opts.sidecarID, activeSidecar, workDir)

	envVars, err := loadSidecarEnvVars(ctx, circleCIClient, opts, workDir, statusFn, streams)
	if errors.Is(err, errSidecarUnusable) {
		statusFn(iostream.LevelWarn, "sidecar was unusable, provisioning a replacement")
		opts.sidecarID = ""
		if _, createErr := resolveOrCreateSidecarID(ctx, circleCIClient, &opts.sidecarID, opts.orgID, image, workDir, streams); createErr != nil {
			return createErr
		}
		freshlyCreated = true
		statusFn = wrapEventLogStatusFn(baseStatusFn, opts.sidecarID, nil, workDir)
		envVars, err = loadSidecarEnvVars(ctx, circleCIClient, opts, workDir, statusFn, streams)
	}
	if err != nil {
		if errors.Is(err, errSidecarUnusable) {
			// Twice in one run is not a stale sidecar, so stop rather than churn.
			return newUserError("Could not get a usable sidecar.").
				withCode("sidecar.unusable").
				withSuggestion("Create one explicitly with: chunk sidecar create").
				wrap(err)
		}
		return err
	}

	execErr := runValidate(ctx, circleCIClient, rc, workDir, name, opts.inlineCmd, opts.save, opts.sidecarID, freshlyCreated, opts.workdir, allRemote, envVars, cfg, statusFn, streams)
	return finishValidate(execErr, start, opts.sidecarID, statusFn, streams)
}

// wrapEventLogStatusFn wraps statusFn with event log recording when a sidecar
// is active. Returns statusFn unchanged when no sidecar is involved, so callers
// with empty sidecar IDs never write events with a blank sidecar_id.
func wrapEventLogStatusFn(statusFn iostream.StatusFunc, sidecarID string, activeSidecar *sidecar.ActiveSidecar, workDir string) iostream.StatusFunc {
	if sidecarID == "" {
		return statusFn
	}
	dataDir, err := sidecar.StateDir()
	if err != nil {
		return statusFn
	}
	scName := ""
	if activeSidecar != nil && activeSidecar.SidecarID == sidecarID {
		scName = activeSidecar.Name
	}
	return eventlog.WrapFromDir(dataDir, statusFn, eventlog.OpValidate, sidecarID, scName, sidecar.CurrentBranch(workDir))
}

// finishValidate reports the validate outcome.
func finishValidate(execErr error, start time.Time, sidecarID string, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	if execErr != nil {
		statusFn(iostream.LevelError, fmt.Sprintf("done in %s (failed)", ui.FormatDuration(time.Since(start))))
	} else {
		statusFn(iostream.LevelStep, fmt.Sprintf("done in %s", ui.FormatDuration(time.Since(start))))
	}
	if sidecarID != "" {
		if execErr != nil {
			statusFn(iostream.LevelError, "validate failed")
		} else {
			statusFn(iostream.LevelDone, "validate passed")
		}
	}
	if execErr == nil && sidecarID == "" {
		_, _ = fmt.Fprintf(streams.Out, "%s\n", ui.Success(fmt.Sprintf("chunk validate passed (%s)", ui.FormatDuration(time.Since(start)))))
	}
	return execErr
}

func validateEnvFlag(envVarsFlag []string) error {
	if _, err := sidecar.ParseEnvPairs(envVarsFlag); err != nil {
		return &userError{msg: fmt.Sprintf("invalid --env value: %s", err), err: err}
	}
	return nil
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
	return mapValidateError(validate.RunDryRun(cfg.Validate, name, statusFn))
}

// runValidate dispatches to the appropriate Run* function based on the
// provided options.
func runValidate(ctx context.Context, client *circleci.Client, rc config.ResolvedConfig, workDir, name, inlineCmd string, save bool, sidecarID string, freshlyCreated bool, workdir string, allRemote bool, envVars map[string]string, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	if inlineCmd != "" {
		cmdName := name
		if cmdName == "" {
			cmdName = "custom"
		}
		if save {
			if err := config.SaveValidateCommand(workDir, cmdName, inlineCmd); err != nil {
				return &userError{msg: "Could not save command to .chunk/config.json.", err: err}
			}
			streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", cmdName)))
		}
		if sidecarID != "" && allRemote {
			execFn, dest, err := newExecFn(ctx, client, sidecarID, workdir, envVars, rc, streams)
			if err != nil {
				return err
			}
			return validate.RunRemoteInline(ctx, execFn, cmdName, inlineCmd, dest, statusFn, streams)
		}
		return validate.RunInline(ctx, workDir, cmdName, inlineCmd, statusFn, streams)
	}

	if sidecarID != "" && allRemote {
		execFn, dest, err := newExecFn(ctx, client, sidecarID, workdir, envVars, rc, streams)
		if err != nil {
			return err
		}
		return validate.RunRemote(ctx, execFn, cfg.Validate, name, dest, workDir, statusFn, streams)
	}

	if sidecarID != "" {
		if name != "" {
			if cmd := cfg.FindValidateCommand(name); cmd != nil && cmd.Remote {
				statusFn(iostream.LevelInfo, fmt.Sprintf("running %s on sidecar %s", name, sidecarID))
				execFn, dest, err := newExecFn(ctx, client, sidecarID, workdir, envVars, rc, streams)
				if err != nil {
					return err
				}
				return validate.RunRemote(ctx, execFn, cfg.Validate, name, dest, workDir, statusFn, streams)
			}
			statusFn(iostream.LevelInfo, fmt.Sprintf("running %s locally (not marked remote)", name))
		} else {
			return runSplitCommands(ctx, client, sidecarID, freshlyCreated, workdir, workDir, envVars, rc, cfg, statusFn, streams)
		}
	}

	if name != "" {
		if cfg.FindValidateCommand(name) == nil {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return &userError{
					msg:        fmt.Sprintf("Command %q is not configured.", name),
					suggestion: "Add it to .chunk/config.json.",
					errMsg:     fmt.Sprintf("command %q is not configured", name),
				}
			}
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
			if err := config.SaveValidateCommand(workDir, name, input); err != nil {
				return &userError{msg: "Could not save command to .chunk/config.json.", err: err}
			}
			streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", name)))
			var err error
			cfg, err = config.LoadProjectConfig(workDir)
			if err != nil {
				return err
			}
		}
		return mapValidateError(validate.RunNamed(ctx, workDir, name, cfg.Validate, statusFn, streams))
	}

	return mapValidateError(validate.RunAll(ctx, workDir, cfg.Validate, statusFn, streams))
}

// setupRemote resolves (or creates) the sidecar ID based on the validate flags
// and config, then returns whether a new sidecar was provisioned.
func setupRemote(ctx context.Context, client *circleci.Client, opts *validateOpts, image string, cfg *config.ProjectConfig, activeSidecar *sidecar.ActiveSidecar, statusFn iostream.StatusFunc, workDir string, streams iostream.Streams) (bool, error) {
	if validateNeedsSidecar(opts.remote || opts.sidecarID != "", cfg) {
		if opts.remote {
			created, err := resolveOrCreateSidecarID(ctx, client, &opts.sidecarID, opts.orgID, image, workDir, streams)
			if err != nil {
				return false, err
			}
			statusFn(iostream.LevelInfo, fmt.Sprintf("running all commands on sidecar %s", opts.sidecarID))
			return created, nil
		}
		return resolveSidecar(ctx, client, &opts.sidecarID, opts.orgID, image, workDir, activeSidecar, streams), nil
	}
	return false, nil
}

func syncToSidecar(ctx context.Context, client *circleci.Client, sidecarID, identityFile, workdir string, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	authSock := os.Getenv(config.EnvSSHAuthSock)
	cwd, err := os.Getwd()
	if err != nil {
		return &userError{msg: "Could not sync to sidecar.", err: err}
	}
	if err := sidecar.BundleSync(ctx, client, sidecarID, identityFile, authSock, workdir, cwd, statusFn); err != nil {
		return sidecarSyncError(ctx, client, sidecarID, err, streams)
	}
	return nil
}

// sidecarSyncError reports a failed sync, first dropping local state when the
// failure means this sidecar can never be used again.
//
// Reap already removes deleted sidecars before anything syncs to them, so this
// covers what a listing cannot show: a sidecar deleted since the list was
// fetched, and one the API rejects as out of date. Without the prune an
// out-of-date sidecar fails forever: it is still listed, and still recently
// used, so the age sweep never touches it.
func sidecarSyncError(ctx context.Context, client *circleci.Client, sidecarID string, err error, streams iostream.Streams) error {
	switch {
	case circleci.SidecarOutOfDate(err):
		// Still running and still billable, so delete it rather than orphan it.
		pruneSidecarState(ctx, client, sidecarID, true, streams)
	case circleci.SidecarGone(err):
		pruneSidecarState(ctx, client, sidecarID, false, streams)
	default:
		return &userError{msg: "Could not sync to sidecar.", err: err}
	}
	// Both wrapped, not replaced: the caller replaces the sidecar and retries, and
	// only if that fails does the original surface. GoneError in main's error path
	// still finds the 410 through the wrapping and phrases it.
	return fmt.Errorf("%w: %w", errSidecarUnusable, err)
}

// errSidecarUnusable marks a sync failure that a fresh sidecar would fix: the
// sidecar is gone, or permanently out of date, and its local state has already
// been dropped, so provisioning a replacement is safe.
var errSidecarUnusable = errors.New("sidecar unusable")

// pruneSidecarState drops local state for an unusable sidecar, warning on
// failure. A prune that fails must not mask the error that triggered it.
func pruneSidecarState(ctx context.Context, client *circleci.Client, sidecarID string, deleteRemote bool, streams iostream.Streams) {
	if err := sidecar.PruneID(ctx, client, sidecarID, deleteRemote); err != nil {
		streams.ErrPrintf("warning: could not clear state for unusable sidecar %s: %v\n", sidecarID, err)
	}
}

// reapAbandonedSidecars deletes sidecars this project has abandoned and drops
// their local state. Errors are reported and then ignored: a cleanup sweep must
// never fail a validate run.
func reapAbandonedSidecars(ctx context.Context, client *circleci.Client, workDir string, statusFn iostream.StatusFunc, streams iostream.Streams) {
	orgID, _ := config.ResolveOrgID(workDir)
	if orgID == "" {
		// The only remaining way to get an org is the interactive picker, and a
		// background sweep must never prompt.
		return
	}
	res, err := sidecar.Reap(ctx, client, orgID)
	if err != nil {
		streams.ErrPrintf("warning: could not reap abandoned sidecars: %v\n", err)
	}
	if summary := res.Summary(); summary != "" {
		statusFn(iostream.LevelInfo, summary)
	}
}

// newExecFn builds a function that runs shell scripts on a remote sidecar via
// the async HTTP exec API, along with the resolved remote working directory.
func newExecFn(
	ctx context.Context, client *circleci.Client, sidecarID, workdir string,
	envVars map[string]string, rc config.ResolvedConfig, streams iostream.Streams,
) (func(context.Context, string) (string, string, int, error), string, error) {
	cwd, _ := os.Getwd()
	_, repo, _ := gitremote.DetectOrgAndRepo(cwd)
	dest, err := sidecar.ResolveWorkspace(ctx, workdir, repo)
	if err != nil {
		return nil, "", &userError{msg: "Could not determine workspace path.", err: err}
	}
	merged := hostForwardEnv(rc.CircleCIToken)
	if merged == nil {
		merged = make(map[string]string, len(envVars))
	}
	for k, v := range envVars {
		merged[k] = v
	}
	onOutput := func(stream string, data []byte) {
		w := streams.Out
		if stream == circleci.StreamStderr {
			w = streams.Err
		}
		_, _ = w.Write(data)
	}
	execFn := func(ctx context.Context, script string) (string, string, int, error) {
		result, err := client.Exec(ctx, sidecarID, "sh", []string{"-c", script}, merged, onOutput)
		if err != nil {
			return "", "", 0, err
		}
		return "", "", result.ExitCode, nil
	}
	return execFn, dest, nil
}

// hostForwardEnv collects host environment variables that should be forwarded
// into commands running on the sidecar.
func hostForwardEnv(token string) map[string]string {
	if token == "" {
		return nil
	}
	return map[string]string{config.EnvCircleToken: token}
}

// runSplitCommands handles per-command remote routing when no specific command
// name is given: remote-tagged commands go to the sidecar, the rest run locally.
func runSplitCommands(ctx context.Context, client *circleci.Client, sidecarID string, freshlyCreated bool, workdir, workDir string, envVars map[string]string, rc config.ResolvedConfig, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	remoteCmds, localCmds := splitByRemote(cfg.Validate)
	if len(remoteCmds) > 0 {
		statusFn(iostream.LevelInfo, fmt.Sprintf("running on sidecar %s: %s", sidecarID, validateCommandNames(remoteCmds)))
	}
	if len(localCmds) > 0 {
		statusFn(iostream.LevelInfo, fmt.Sprintf("running locally: %s", validateCommandNames(localCmds)))
	}
	var runErr error
	if len(remoteCmds) > 0 {
		execFn, dest, err := newExecFn(ctx, client, sidecarID, workdir, envVars, rc, streams)
		if err != nil {
			if freshlyCreated {
				return newUserError(fmt.Sprintf("Could not reach newly created sidecar %s.", sidecarID)).
					withCode("sidecar.unreachable").
					withSuggestion("The sidecar may still be starting. Try again in a moment.").
					withExitCode(ExitAPIError).
					wrap(err)
			}
			streams.ErrPrintf("warning: could not reach sidecar (%v); running %s locally instead\n", err, validateCommandNames(remoteCmds))
			localCmds = append(remoteCmds, localCmds...)
		} else if wsErr := validate.WorkspaceExists(ctx, execFn, dest); wsErr != nil {
			if freshlyCreated {
				return newUserError(fmt.Sprintf("Workspace not found on newly created sidecar %s.", sidecarID)).
					withCode("sidecar.workspace_missing").
					withSuggestion("Run 'chunk sidecar env build' to prepare the workspace.").
					withExitCode(ExitNotFound).
					wrap(wsErr)
			}
			streams.ErrPrintf("warning: %v (%q); run 'chunk sidecar env build' to set up the workspace; running %s locally instead\n", wsErr, dest, validateCommandNames(remoteCmds))
			localCmds = append(remoteCmds, localCmds...)
		} else {
			runErr = validate.RunRemote(ctx, execFn, remoteCmds, "", dest, workDir, statusFn, streams)
		}
	}
	if len(localCmds) > 0 {
		if err := mapValidateError(validate.RunAll(ctx, workDir, localCmds, statusFn, streams)); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	return runErr
}

func validateCommandNames(cmds []config.ValidateCommand) string {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

// splitByRemote partitions validate commands into remote and local slices.
func splitByRemote(cmds []config.ValidateCommand) (remote, local []config.ValidateCommand) {
	for _, cmd := range cmds {
		if cmd.Remote {
			remote = append(remote, cmd)
		} else {
			local = append(local, cmd)
		}
	}
	return remote, local
}

// resolveImage returns the sidecar image to use for sidecar creation.
func resolveImage(name string, cfg *config.ProjectConfig) string {
	if name != "" && cfg != nil {
		if cmd := cfg.FindValidateCommand(name); cmd != nil && cmd.SidecarImage != "" {
			return cmd.SidecarImage
		}
	}
	if cfg != nil && cfg.Validation != nil {
		return cfg.Validation.SidecarImage
	}
	return ""
}

// resolveSidecar fills sidecarID for per-command remote routing.
func resolveSidecar(ctx context.Context, client *circleci.Client, sidecarID *string, orgID, image, workDir string, active *sidecar.ActiveSidecar, streams iostream.Streams) bool {
	statusFn := newStatusFunc(streams)
	if active != nil {
		*sidecarID = active.SidecarID
		statusFn(iostream.LevelInfo, fmt.Sprintf("using sidecar %s for remote commands", *sidecarID))
		return false
	}
	created, err := resolveOrCreateSidecarID(ctx, client, sidecarID, orgID, image, workDir, streams)
	if err != nil {
		streams.ErrPrintf("warning: could not create sidecar (%v); running commands locally instead\n", err)
	}
	return created
}

// resolveOrCreateSidecarID fills sidecarID from the active sidecar, or creates
// a new sidecar when none is configured.
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
	if existing, err := sidecar.LoadAnyActive(ctx); err == nil && existing != nil {
		if saveErr := sidecar.SaveActive(ctx, *existing); saveErr != nil {
			streams.ErrPrintf("warning: could not promote active sidecar: %v\n", saveErr)
		}
		*sidecarID = existing.SidecarID
		return false, nil
	}
	streams.ErrPrintf("No active sidecar found, creating a new sidecar...\n")
	resolvedOrgID, err := resolveOrgID(orgID, workDir, orgPicker(ctx, client))
	if err != nil {
		return false, err
	}
	sandboxName := sidecarAutoName(ctx, workDir)
	sc, err := sidecar.Create(ctx, client, resolvedOrgID, sandboxName, image)
	if err != nil {
		if authErr := notAuthorized("create sidecars", err); authErr != nil {
			return false, authErr
		}
		return false, &userError{
			msg:        "Could not create a sidecar.",
			suggestion: "Check your network connection or run 'chunk sidecar create' manually.",
			err:        err,
		}
	}
	if saveErr := sidecar.SaveActive(ctx, sidecar.ActiveSidecar{SidecarID: sc.ID, Name: sc.Name, OrgID: resolvedOrgID}); saveErr != nil {
		streams.ErrPrintf("warning: could not save active sidecar: %v\n", saveErr)
	}
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
	streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Created sidecar %s (%s)", sc.Name, sc.ID)))
	*sidecarID = sc.ID
	return true, nil
}

var branchSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

func sidecarAutoName(ctx context.Context, workDir string) string {
	base := filepath.Base(workDir)
	sessionID := session.IDFromCtx(ctx)
	branch := sidecar.CurrentBranch(workDir)

	if sessionID != "" {
		if branch != "" {
			sum := sha256.Sum256([]byte(sessionID + ":" + branch))
			hash8 := fmt.Sprintf("%x", sum[:4])
			return base + "-" + sessionID + "-" + hash8
		}
		return base + "-" + sessionID
	}

	if branch != "" {
		branch = strings.ReplaceAll(branch, "/", "-")
		branch = strings.ToLower(branch)
		branch = branchSanitizer.ReplaceAllString(branch, "")
		if len(branch) > 30 {
			branch = branch[:30]
		}
		if branch != "" {
			return base + "-" + branch + "-validate"
		}
	}
	return base + "-validate"
}

const suggestionValidateNotConfigured = "Run 'chunk init' to detect and configure validation commands.\n" +
	"This also installs the /chunk-sidecar skill so your AI coding agent can help you set up remote validation on a sidecar."

func mapValidateError(err error) error {
	if errors.Is(err, validate.ErrNotConfigured) {
		return &userError{
			msg:        "No validate commands configured.",
			suggestion: suggestionValidateNotConfigured,
			hideDetail: true,
			err:        err,
		}
	}
	return err
}
