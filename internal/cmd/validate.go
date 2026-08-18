package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/CircleCI-Public/chunk-cli/internal/filecache"
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
		case iostream.LevelError:
			streams.ErrPrintf("  %s\n", ui.ErrError(msg))
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

// runMarkRemote flips the remote flag on one command, or on all of them when
// name is empty, so plain `chunk validate` routes them to the sidecar. Until
// now only `chunk sidecar setup` set this, and only for install and gate
// commands, leaving a hand edit of .chunk/config.json as the only other way.
func runMarkRemote(workDir, name string, streams iostream.Streams) error {
	cfg, err := config.LoadProjectConfig(workDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return &userError{msg: msgCouldNotLoadConfig, suggestion: configFilePermHint, err: err}
	}
	if err != nil || !cfg.HasCommands() {
		return &userError{
			msg:        msgValidateNotConfigured,
			suggestion: suggestionValidateNotConfigured,
			errMsg:     "no validate commands configured",
			hideDetail: true,
		}
	}

	// Collected before the mark, which reports only what it changed.
	var skipped []string
	if name == "" {
		for _, c := range cfg.Commands {
			if c.Role == config.RoleAutofix && !c.Remote {
				skipped = append(skipped, c.Name)
			}
		}
	}

	changed, err := cfg.MarkCommandRemote(name)
	if err != nil {
		return &userError{
			msg:        fmt.Sprintf("No command named %q in .chunk/config.json.", name),
			suggestion: "List the configured commands with: chunk validate --list",
			err:        err,
		}
	}
	if len(changed) == 0 {
		streams.ErrPrintf("%s\n", ui.Dim("Nothing to change."))
		reportSkippedAutofix(skipped, streams)
		return nil
	}
	if err := config.SaveProjectConfig(workDir, cfg); err != nil {
		return &userError{msg: "Could not save project configuration.", suggestion: configFilePermHint, err: err}
	}

	streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Marked remote: %s", strings.Join(changed, ", "))))
	streams.ErrPrintf("  %-28s %s\n", ui.Cyan("chunk validate"), ui.Dim("now runs these on the sidecar"))
	reportSkippedAutofix(skipped, streams)
	return nil
}

// reportSkippedAutofix says which autofix commands the sweep left alone, so the
// omission reads as deliberate rather than as a command that got missed.
func reportSkippedAutofix(skipped []string, streams iostream.Streams) {
	if len(skipped) == 0 {
		return
	}
	streams.ErrPrintf("  %s\n", ui.Dim(fmt.Sprintf(
		"left local (autofix rewrites files): %s", strings.Join(skipped, ", "))))
	streams.ErrPrintf("  %s\n", ui.Dim("mark one by name to override"))
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
	markRemote   bool
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
			// Mapped here rather than at the exec call: the remote runners wrap
			// errors with %w, and the top-level error reporter type-asserts on the
			// error itself rather than unwrapping, so a userError buried under a
			// wrapper would lose its message and suggestion.
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
	cmd.Flags().BoolVar(&opts.markRemote, "mark-remote", false, "Mark [name] (or every command) as remote in .chunk/config.json and exit")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Show commands without executing")
	cmd.Flags().BoolVar(&opts.list, "list", false, "List all configured commands")
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "Output as JSON (only applies with --list)")
	cmd.Flags().StringVar(&opts.inlineCmd, "cmd", "", "Run an inline command instead of config")
	cmd.Flags().BoolVar(&opts.save, "save", false, "Save --cmd to .chunk/config.json")
	cmd.Flags().StringVar(&opts.projectDir, "project", "", "Override project directory")
	cmd.Flags().StringArrayVarP(&opts.envVarsFlag, "env", "e", nil, "KEY=VALUE pairs to set in remote sidecar session (repeatable)")
	cmd.Flags().StringVar(&opts.envFile, "env-file", defaultEnvFile, "Env file to load (default: .env.local; pass a path to override)")

	cmd.AddCommand(newValidateVariantsCmd())

	return cmd
}

// initHook applies hook-specific context, stream, and early-exit logic.
// tree is the working-tree fingerprint for this run, and treeErr the reason it
// could not be taken. A zero tree reports as not clean, so validation still runs
// in ambiguous cases.
// Returns updated ctx and streams, a skip flag (true = return nil immediately),
// and a non-nil error when the hook should exit with a non-zero code.
func initHook(ctx context.Context, hook *hookContext, workDir string, tree gitutil.Worktree, treeErr error, streams iostream.Streams) (context.Context, iostream.Streams, bool, error) {
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
	// Say so when the tree could not be fingerprinted. Everything below degrades
	// silently on a zero tree — the clean-tree skip and the result cache both just
	// run the commands — so a repo that never once prints "skipped" (one with a
	// dirty submodule, say) is otherwise indistinguishable from one where the
	// cache is working and nothing is ever unchanged.
	if treeErr != nil {
		streams.ErrPrintf("  %s\n", ui.ErrDim(fmt.Sprintf("chunk validate: working tree state unavailable (%v); running everything, caching nothing", treeErr)))
	}
	if tree.Clean {
		return ctx, streams, true, nil
	}
	return ctx, streams, false, nil
}

// checkRemoteConfigured returns an error when an interactive run would fall
// back to local execution because no commands are marked remote. Hooks and
// explicit override flags (--remote, --sidecar-id, --cmd) bypass this gate.
func checkRemoteConfigured(hook *hookContext, opts *validateOpts, cfg *config.ProjectConfig) error {
	if hook != nil || opts.inlineCmd != "" || opts.remote || opts.sidecarID != "" {
		return nil
	}
	if cfg == nil || !cfg.HasCommands() || cfg.HasRemoteCommands() || cfg.HasSidecarImage() {
		return nil
	}
	return &userError{
		msg:        msgRemoteNotConfigured,
		suggestion: suggestionRemoteNotConfigured,
		errMsg:     "remote not configured",
		hideDetail: true,
	}
}

// hookMissingAuth reports whether a hook run should be aborted because the
// CircleCI token is missing. It also prints the actionable message. Returns
// false (do not abort) for non-hook runs — those prompt interactively instead.
func hookMissingAuth(hook *hookContext, needsSidecar bool, token string, streams iostream.Streams) bool {
	if hook == nil || !needsSidecar || token != "" {
		return false
	}
	streams.ErrPrintln("CircleCI auth is not configured.")
	streams.ErrPrintln("Suggestion: " + suggestionCircleCIAuth)
	streams.ErrPrintln("Don't have an account? Sign up at https://app.circleci.com/signup")
	return true
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

	// Record before git-status check so total captures setup overhead too.
	start := time.Now()

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

	// The working-tree fingerprint answers both hook-only questions about the
	// tree: whether there is anything to validate at all, and whether this exact
	// tree already validated successfully. Computing it once keeps the two
	// consistent and costs a single pass over the changed files. On failure it is
	// the zero Worktree, which reads as "not clean" and is refused as a cache
	// key, so both questions fall back to running the commands.
	var tree gitutil.Worktree
	var treeErr error
	if hook != nil {
		tree, treeErr = gitutil.Fingerprint(workDir)
	}

	var skip bool
	var hookErr error
	ctx, streams, skip, hookErr = initHook(ctx, hook, workDir, tree, treeErr, streams)
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

	// --mark-remote edits the config and stops; it never runs anything, so it
	// takes its own read-modify-write path ahead of the run setup below.
	if opts.markRemote {
		return runMarkRemote(workDir, name, streams)
	}

	cfg, err := config.LoadProjectConfig(workDir)
	if (err != nil || !cfg.HasCommands()) && opts.inlineCmd == "" {
		if hook != nil {
			return nil // no config in hook context: skip silently
		}
		return &userError{
			msg:        msgValidateNotConfigured,
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

	if err := checkRemoteConfigured(hook, opts, cfg); err != nil {
		return err
	}

	// Hook: fail early when CircleCI auth is missing and remote commands need it.
	// In non-hook context ensureCircleCIClient prompts interactively; hooks have
	// no TTY so we surface a clear message here instead of a confusing fallback.
	rc, _ := config.ResolveCircleCI(insecureStorage)

	explicitRemote := opts.remote || opts.sidecarID != ""
	needsSidecar := validateNeedsSidecar(explicitRemote, cfg)
	if hookMissingAuth(hook, needsSidecar, rc.CircleCIToken, streams) {
		return errSilentExit
	}

	activeSidecar, _ := sidecar.LoadActive(ctx)
	resultCache, cacheKey := hookResultCache(hook, opts.inlineCmd, workDir, tree, name, cfg, execTarget(opts, cfg, activeSidecar))
	if resultCache != nil {
		if _, ok := resultCache.Get(cacheKey); ok {
			streams.ErrPrintln("chunk validate: skipped (no changes since last successful run)")
			// A hit is a success, so it finishes the same way a real successful run
			// does rather than repeating that bookkeeping here: clearing the failure
			// counter, and whatever else the success branch grows later.
			return finishValidate(cmd, hook, nil, start, opts.sidecarID, cfg, statusFn, streams)
		}
	}

	// allRemote is true when the caller explicitly targets the sidecar
	// (--remote or --sidecar-id), meaning every command runs there.
	// Per-command routing only applies when the sidecar is resolved implicitly.
	allRemote := explicitRemote
	if cfg.HasSidecarImage() {
		allRemote = true
	}

	image := resolveImage(name, cfg)

	circleCIClient, err := maybeEnsureCircleCIClient(cmd.Context(), cmd, rc, needsSidecar, streams)
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
	activeSidecar, _ = sidecar.LoadActive(ctx)

	freshlyCreated, err := setupRemote(ctx, circleCIClient, opts, image, cfg, activeSidecar, statusFn, workDir, streams)
	if err != nil {
		return err
	}

	// Wire event log only when a sidecar is involved. The wrap goes here — after
	// setupRemote fills opts.sidecarID but before loadSidecarEnvVars — so that
	// sync and env-resolve status events are captured. Skipping when there is no
	// sidecar avoids writing events with an empty sidecar_id that the TUI would
	// filter out and never display.
	// Kept unwrapped so a replacement sidecar can be rewrapped against its own ID
	// rather than logging its events under the sidecar it replaced.
	baseStatusFn := statusFn
	statusFn = wrapEventLogStatusFn(statusFn, opts.sidecarID, activeSidecar, workDir, hook)

	// Only load env vars and resolve secrets when a sidecar is actually
	// being used — avoids parsing .env.local or hitting secrets APIs on
	// purely local runs.
	envVars, statusFn, freshlyCreated, err := loadEnvVarsWithRetry(ctx, circleCIClient, opts, image, freshlyCreated, baseStatusFn, statusFn, workDir, hook, streams)
	if err != nil {
		return err
	}

	execErr := runValidate(ctx, circleCIClient, rc, workDir, name, opts.inlineCmd, opts.save, opts.sidecarID, freshlyCreated, opts.workdir, allRemote, envVars, cfg, statusFn, streams)
	if execErr == nil && resultCache != nil {
		if err := resultCache.Put(cacheKey, validate.CachedResult{CachedAt: time.Now()}); err != nil {
			streams.ErrPrintf("  %s\n", ui.ErrDim(fmt.Sprintf("chunk validate: cache write failed: %v", err)))
		}
	}
	return finishValidate(cmd, hook, execErr, start, opts.sidecarID, cfg, statusFn, streams)
}

// loadEnvVarsWithRetry loads sidecar env vars, and if the sidecar is unusable
// provisions a replacement and retries once.
func loadEnvVarsWithRetry(
	ctx context.Context,
	circleCIClient *circleci.Client,
	opts *validateOpts,
	image string,
	freshlyCreated bool,
	baseStatusFn, statusFn iostream.StatusFunc,
	workDir string,
	hook *hookContext,
	streams iostream.Streams,
) (map[string]string, iostream.StatusFunc, bool, error) {
	envVars, err := loadSidecarEnvVars(ctx, circleCIClient, opts, workDir, statusFn, streams)
	if errors.Is(err, errSidecarUnusable) {
		// The sidecar could not be used and its state has been dropped, so replace
		// it here rather than failing and asking for the same command again. The
		// reap cannot prevent this on its own: a sidecar can go away between the
		// listing and the sync, and one the API rejects as out of date is listed
		// like any other.
		statusFn(iostream.LevelWarn, "sidecar was unusable, provisioning a replacement")
		opts.sidecarID = ""
		if _, createErr := resolveOrCreateSidecarID(ctx, circleCIClient, &opts.sidecarID, opts.orgID, image, workDir, streams); createErr != nil {
			return nil, statusFn, freshlyCreated, createErr
		}
		// A replacement has none of the setup the old one had, so exec failures on
		// it are real failures rather than grounds for falling back to local.
		freshlyCreated = true
		statusFn = wrapEventLogStatusFn(baseStatusFn, opts.sidecarID, nil, workDir, hook)
		envVars, err = loadSidecarEnvVars(ctx, circleCIClient, opts, workDir, statusFn, streams)
	}
	if err != nil {
		if errors.Is(err, errSidecarUnusable) {
			// Twice in one run is not a stale sidecar, so stop rather than churn.
			return nil, statusFn, freshlyCreated, newUserError("Could not get a usable sidecar.").
				withCode("sidecar.unusable").
				withSuggestion("Create one explicitly with: chunk sidecar create").
				wrap(err)
		}
		return nil, statusFn, freshlyCreated, err
	}
	return envVars, statusFn, freshlyCreated, nil
}

// wrapEventLogStatusFn wraps statusFn with event log recording when a sidecar
// is active. Returns statusFn unchanged when no sidecar is involved, so callers
// with empty sidecar IDs never write events with a blank sidecar_id.
func wrapEventLogStatusFn(statusFn iostream.StatusFunc, sidecarID string, activeSidecar *sidecar.ActiveSidecar, workDir string, hook *hookContext) iostream.StatusFunc {
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
	op := eventlog.OpValidate
	if hook != nil && hook.stopHookActive {
		op = eventlog.OpHook
	}
	return eventlog.WrapFromDir(dataDir, statusFn, op, sidecarID, scName, sidecar.CurrentBranch(workDir))
}

// finishValidate reports the validate outcome and handles hook exit codes.
func finishValidate(cmd *cobra.Command, hook *hookContext, execErr error, start time.Time, sidecarID string, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	maxAttempts := validate.DefaultMaxAttempts
	if hook != nil {
		if ma := cfg.StopHookMaxAttempts; ma > 0 {
			maxAttempts = ma
		}
	}
	if execErr != nil {
		if hook != nil {
			attempt := validate.ReadAttempts(hook.sessionID) + 1
			statusFn(iostream.LevelError, fmt.Sprintf("done in %s (failed, attempt %d/%d)", ui.FormatDuration(time.Since(start)), attempt, maxAttempts))
		} else {
			statusFn(iostream.LevelError, fmt.Sprintf("done in %s (failed)", ui.FormatDuration(time.Since(start))))
		}
	} else if hook == nil {
		statusFn(iostream.LevelStep, fmt.Sprintf("done in %s", ui.FormatDuration(time.Since(start))))
	}
	if sidecarID != "" {
		if execErr != nil {
			statusFn(iostream.LevelError, "validate failed")
		} else {
			statusFn(iostream.LevelDone, "validate passed")
		}
	}
	if hook == nil {
		return execErr
	}
	hookErr := validate.WrapHookResult(hook.sessionID, execErr, maxAttempts, streams.Err)
	if hookErr == nil && execErr == nil {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", ui.Success(fmt.Sprintf("chunk validate passed (%s)", ui.FormatDuration(time.Since(start)))))
		return nil
	}
	return hookErr
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
	return mapValidateError(validate.RunDryRun(cfg, name, statusFn))
}

// runValidate dispatches to the appropriate Run* function based on the
// provided options. It is shared by both direct and hook invocations.
// allRemote is true when --remote is passed explicitly (all commands run on the
// sidecar); false means only commands with Remote:true are routed to the sidecar.
func runValidate(ctx context.Context, client *circleci.Client, rc config.ResolvedConfig, workDir, name, inlineCmd string, save bool, sidecarID string, freshlyCreated bool, workdir string, allRemote bool, envVars map[string]string, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
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
			execFn, dest, err := newExecFn(ctx, client, sidecarID, workdir, envVars, rc, streams)
			if err != nil {
				return err
			}
			return validate.RunRemoteInline(ctx, execFn, cmdName, inlineCmd, dest, statusFn, streams)
		}
		return validate.RunInline(ctx, workDir, cmdName, inlineCmd, statusFn, streams)
	}

	// All-remote execution (--remote flag): send everything to the sidecar.
	if sidecarID != "" && allRemote {
		execFn, dest, err := newExecFn(ctx, client, sidecarID, workdir, envVars, rc, streams)
		if err != nil {
			return err
		}
		return validate.RunRemote(ctx, execFn, cfg, name, dest, workDir, statusFn, streams)
	}

	// Per-command remote routing: commands with Remote:true go to the sidecar,
	// the rest run locally.
	if sidecarID != "" {
		if name != "" {
			if cmd := cfg.FindCommand(name); cmd != nil && cmd.Remote {
				statusFn(iostream.LevelInfo, fmt.Sprintf("running %s on sidecar %s", name, sidecarID))
				execFn, dest, err := newExecFn(ctx, client, sidecarID, workdir, envVars, rc, streams)
				if err != nil {
					return err
				}
				return validate.RunRemote(ctx, execFn, cfg, name, dest, workDir, statusFn, streams)
			}
			statusFn(iostream.LevelInfo, fmt.Sprintf("running %s locally (not marked remote)", name))
			// Named command is not marked remote; fall through to local execution.
		} else {
			return runSplitCommands(ctx, client, sidecarID, freshlyCreated, workdir, workDir, envVars, rc, cfg, statusFn, streams)
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
//
// Output is written to streams as it arrives rather than buffered, so a long
// command shows progress instead of going silent for minutes. The returned
// stdout and stderr are therefore always empty — callers print output only when
// it was not already streamed, so there is nothing left for them to do.
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
	// Raw bytes straight through, so carriage-return redraws and ANSI colour
	// render as the remote command intended.
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
// When freshlyCreated is true, exec failures are hard errors rather than
// silent local fallbacks (a newly provisioned sidecar that can't be reached
// indicates a real problem, not temporary unavailability).
func runSplitCommands(ctx context.Context, client *circleci.Client, sidecarID string, freshlyCreated bool, workdir, workDir string, envVars map[string]string, rc config.ResolvedConfig, cfg *config.ProjectConfig, statusFn iostream.StatusFunc, streams iostream.Streams) error {
	remoteCfg, localCfg := splitByRemote(cfg)
	if len(remoteCfg.Commands) > 0 {
		statusFn(iostream.LevelInfo, fmt.Sprintf("running on sidecar %s: %s", sidecarID, commandNames(remoteCfg.Commands)))
	}
	if len(localCfg.Commands) > 0 {
		statusFn(iostream.LevelInfo, fmt.Sprintf("running locally: %s", commandNames(localCfg.Commands)))
	}
	var runErr error
	if len(remoteCfg.Commands) > 0 {
		execFn, dest, err := newExecFn(ctx, client, sidecarID, workdir, envVars, rc, streams)
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
			runErr = validate.RunRemote(ctx, execFn, remoteCfg, "", dest, workDir, statusFn, streams)
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

// resolveImage returns the sidecar image to use for sidecar creation.
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
// (i.e. when --remote is not set but some commands have Remote:true or a
// sidecarImage is configured). It uses the active sidecar when available,
// and auto-creates one otherwise.
// Returns true when a brand-new sidecar was provisioned in this call.
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
// a new sidecar when none is configured. Returns true when a new sidecar was
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
	// Fall back to any existing sidecar for this project before creating a new one.
	// This prevents accumulation of one sidecar per Claude Code session.
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
	// Persist the org ID so future sidecar creation skips the picker.
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

// branchSanitizer is kept for the no-session fallback path.
var branchSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

// sidecarAutoName builds a sidecar name from workDir, the Claude session ID,
// and the current git branch.
//
// When a session ID is present the branch is encoded as an 8-hex-char suffix
// (sha256(sessionID+":"+branch)[:4]) so the raw branch name is never exposed:
//   - Both present → "<base>-<sessionID>-<hash8>"
//   - Session only → "<base>-<sessionID>"
//
// Without a session ID the branch is sanitised and included directly (legacy
// fallback):
//   - Branch only → "<base>-<branch>-validate"
//   - Neither     → "<base>-validate"
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

	// No session ID: fall back to sanitised branch name for human readability.
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

// validateCacheDir returns the directory used to store validate result cache
// entries for the given project root.
func validateCacheDir(workDir string) (string, error) {
	projectDir, err := config.ProjectDataDir(workDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(projectDir, "validate-cache"), nil
}

// hookResultCache returns a cache and cache key for hook-mode runs, or
// (nil, "") when caching does not apply: non-hook runs, inline commands, or a
// working tree whose state could not be fingerprinted.
//
// The cache keeps itself to a bounded size as it is written to; entries live for
// filecache.DefaultMaxAge.
func hookResultCache(hook *hookContext, inlineCmd, workDir string, tree gitutil.Worktree, commandName string, cfg *config.ProjectConfig, target string) (*filecache.FileCache[validate.CachedResult], string) {
	if hook == nil || inlineCmd != "" {
		return nil, ""
	}
	cacheDir, err := validateCacheDir(workDir)
	if err != nil {
		return nil, ""
	}
	key, ok := validate.BuildCacheKey(validate.CacheKeyInputs{
		Worktree:    tree,
		CommandName: commandName,
		Config:      cfg,
		Target:      target,
	})
	if !ok {
		return nil, ""
	}
	return &filecache.FileCache[validate.CachedResult]{Dir: cacheDir}, key
}

// execTarget describes where this run's commands will execute, for inclusion in
// the cache key. Which sidecar is used depends on the configured snapshot image
// and on the active sidecar — mutable state that lives outside the repo, so
// neither shows up in the working-tree digest. Without it, switching the active
// sidecar between runs leaves the key unchanged and the second sidecar is never
// validated against.
//
// Returns "" for a purely local run. The sidecar ID is empty when one will be
// created during this run; the image still distinguishes those from local runs.
func execTarget(opts *validateOpts, cfg *config.ProjectConfig, active *sidecar.ActiveSidecar) string {
	id := opts.sidecarID
	if id == "" && active != nil {
		id = active.SidecarID
	}
	var image string
	if cfg.HasSidecarImage() {
		image = cfg.Validation.SidecarImage
	}
	if id == "" && image == "" {
		return ""
	}
	return id + "\x00" + image
}

func mapValidateError(err error) error {
	if errors.Is(err, validate.ErrNotConfigured) {
		return &userError{
			msg:        msgValidateNotConfigured,
			suggestion: suggestionValidateNotConfigured,
			hideDetail: true,
			err:        err,
		}
	}
	return err
}
