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
	"github.com/CircleCI-Public/chunk-cli/internal/envctx"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/filecache"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/notify"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

const (
	defaultInlineCommandName = "custom"
	errNoValidateCommands    = "no validate commands configured"
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

// hookResponse is the JSON hook response written to stdout.
//
// Deliberately absent: hookSpecificOutput.additionalContext. On Stop that field
// is injected into the model's context and the conversation *continues* so the
// model can act on it, so announcing a pass with it re-signals the agent and the
// turn never ends. systemMessage is display-only and leaves the turn alone.
// Blocking stays with exit code 2 plus stderr, which stopHookMaxAttempts caps.
type hookResponse struct {
	SystemMessage string `json:"systemMessage,omitempty"`
}

// writeStopHookMessage writes a display-only Stop hook response, in the Claude
// Code format also understood by Codex and Cursor. Hook stdout must contain JSON
// only; progress and command output continue to use stderr.
func writeStopHookMessage(w io.Writer, message string) error {
	if err := json.NewEncoder(w).Encode(hookResponse{SystemMessage: message}); err != nil {
		return fmt.Errorf("write Stop hook response: %w", err)
	}
	return nil
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

// runMarkRemote records explicit remote placement on one command, or on all of
// them when name is empty. Remote is already the default; the annotation is
// retained for compatibility and for making intent visible in configuration.
func runMarkRemote(workDir, name string, streams iostream.Streams) error {
	cfg, err := config.LoadProjectConfig(workDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return &userError{msg: msgCouldNotLoadConfig, suggestion: configFilePermHint, err: err}
	}
	if err != nil || !cfg.HasCommands() {
		return &userError{
			msg:        msgValidateNotConfigured,
			suggestion: suggestionValidateNotConfigured,
			errMsg:     errNoValidateCommands,
			hideDetail: true,
		}
	}

	// Collect before marking because MarkCommandRemote reports only changes.
	var skipped []string
	if name == "" {
		for _, c := range cfg.Commands {
			if c.Role == config.RoleAutofix && c.RunsLocally() {
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

	streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Recorded remote placement: %s", strings.Join(changed, ", "))))
	streams.ErrPrintf("  %-28s %s\n", ui.Cyan("chunk validate"), ui.Dim("runs these on the sidecar"))
	reportSkippedAutofix(skipped, streams)
	return nil
}

// reportSkippedAutofix makes clear that a bulk operation intentionally leaves
// commands that modify the local working tree alone.
func reportSkippedAutofix(skipped []string, streams iostream.Streams) {
	if len(skipped) == 0 {
		return
	}
	streams.ErrPrintf("  %s\n", ui.Dim(fmt.Sprintf(
		"left local (autofix rewrites files): %s", strings.Join(skipped, ", "))))
	streams.ErrPrintf("  %s\n", ui.Dim("mark one by name to override"))
}

type validateOpts struct {
	sidecarID      string
	workdir        string
	orgID          string
	dryRun         bool
	list           bool
	save           bool
	remote         bool
	local          bool
	markRemote     bool
	jsonOut        bool
	inlineCmd      string
	projectDir     string
	envVarsFlag    []string
	envFile        string
	async          bool   // run via the daemon without waiting for the result
	noDaemon       bool   // bypasses daemon delegation; set by the daemon when calling in-process
	hookSessionID  string // hook session ID forwarded from client to daemon subprocess
	stopHookActive bool   // stop_hook_active forwarded from client to daemon subprocess
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

	cmd.Flags().BoolVar(&opts.remote, "remote", false, "Run all selected commands on a sidecar, overriding local placement")
	cmd.Flags().BoolVar(&opts.local, "local", false, "Run commands locally instead of on sidecar")
	cmd.MarkFlagsMutuallyExclusive("remote", "local")
	cmd.Flags().StringVar(&opts.sidecarID, "sidecar-id", "", "Sidecar ID for remote execution")
	cmd.Flags().StringVar(&opts.orgID, "org-id", "", "Organization ID (used when creating a new sidecar)")
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
	cmd.Flags().BoolVar(&opts.async, "async", false, "Run in the background via the watch daemon and report on a later run")
	cmd.Flags().BoolVar(&opts.noDaemon, "no-daemon", false, "")
	_ = cmd.Flags().MarkHidden("no-daemon")
	cmd.Flags().StringVar(&opts.hookSessionID, "hook-session-id", "", "")
	_ = cmd.Flags().MarkHidden("hook-session-id")
	cmd.Flags().BoolVar(&opts.stopHookActive, "stop-hook-active", false, "")
	_ = cmd.Flags().MarkHidden("stop-hook-active")

	cmd.AddCommand(newValidateVariantsCmd())
	cmd.AddCommand(newValidateResultsCmd())

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
	// Print a header so concurrent stop-hook runs are distinguishable.
	sessionLabel := hook.sessionID
	if len(sessionLabel) > 8 {
		sessionLabel = sessionLabel[:8]
	}
	if branch, err := gitutil.CurrentBranchIn(workDir); err == nil && branch != "" {
		streams.ErrPrintln(ui.ErrBold(fmt.Sprintf("── validate · %s [%s]", branch, sessionLabel)))
	} else {
		streams.ErrPrintln(ui.ErrBold(fmt.Sprintf("── validate [%s]", sessionLabel)))
	}
	if validate.HooksDisabled(workDir, envctx.Getenv(ctx, config.EnvChunkHooksDisabled) != "") {
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

// hookMissingAuth reports whether a hook run should stop because remote work
// needs a CircleCI token. Interactive runs prompt through client setup instead.
func hookMissingAuth(hook *hookContext, needsSidecar bool, token string, streams iostream.Streams) bool {
	if hook == nil || !needsSidecar || token != "" {
		return false
	}
	streams.ErrPrintln("CircleCI auth is not configured.")
	streams.ErrPrintln("Suggestion: " + suggestionCircleCIAuth)
	streams.ErrPrintln("Don't have an account? Sign up at https://app.circleci.com/signup")
	return true
}

func maybeEnsureCircleCIClient(ctx context.Context, cmd *cobra.Command, rc config.ResolvedConfig, needsSidecar bool, streams iostream.Streams) (*circleci.Client, error) {
	if !needsSidecar {
		return nil, nil
	}
	return ensureCircleCIClient(ctx, cmd, rc, streams, ui.PromptHidden)
}

func resolveWorkDir(opts *validateOpts) (string, error) {
	if opts.projectDir != "" {
		return opts.projectDir, nil
	}
	return os.Getwd()
}

// shouldUseDaemon reports whether this validate run should be delegated to the
// watch daemon. Hook runs always run inline (stdin consumed, per-session attempt
// tracking). --no-daemon skips this to avoid re-delegation when the daemon calls
// us in-process. Delegation is skipped for daemons from a different build since
// they may not support the /validate endpoint.
func shouldUseDaemon(hook *hookContext, noDaemon bool) bool {
	return hook == nil && !noDaemon && watchd.IsDaemonCompatible()
}

func maybeReturnCachedHookResult(
	cmd *cobra.Command,
	hook *hookContext,
	resultCache *filecache.FileCache[validate.CachedResult],
	cacheKey string,
	start time.Time,
	cfg *config.ProjectConfig,
	statusFn iostream.StatusFunc,
	streams iostream.Streams,
) (bool, error) {
	if resultCache == nil {
		return false, nil
	}
	if _, ok := resultCache.Get(cacheKey); !ok {
		return false, nil
	}
	streams.ErrPrintln("chunk validate: skipped (no changes since last successful run)")
	n := len(cfg.Commands)
	return true, finishValidate(cmd, hook, nil, start, cfg, validate.Result{Passed: n, Total: n}, nil, statusFn, streams, nil)
}

func runValidateCmdE(cmd *cobra.Command, args []string, opts *validateOpts) error {
	streams := iostream.FromCmd(cmd)

	// --no-daemon is a persistent root flag; read it here so the rest of the
	// function can use opts.noDaemon uniformly regardless of where the flag
	// was defined. When the flag isn't in the set (e.g. the daemon calling
	// in-process sets opts.noDaemon directly), leave the existing value alone.
	if v, err := cmd.Flags().GetBool("no-daemon"); err == nil {
		opts.noDaemon = v
	}

	// Record before git-status check so total captures setup overhead too.
	start := time.Now()

	workDir, err := resolveWorkDir(opts)
	if err != nil {
		return err
	}

	hook := detectHook(cmd.InOrStdin())
	// When running as a daemon subprocess, hook context arrives via flags.
	if opts.hookSessionID != "" && hook == nil {
		hook = &hookContext{sessionID: opts.hookSessionID, stopHookActive: opts.stopHookActive}
	}
	ctx := cmd.Context()

	// Delegate hook runs to the daemon before initHook so the subprocess prints
	// the session header (not the client, which would cause it to appear twice).
	if done, err := tryHookDelegate(cmd, hook, workDir, opts.noDaemon, streams); done {
		return err
	}

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
		return nil // nothing ran; stdout stays empty so the turn can end
	}
	statusFn := newStatusFunc(streams)
	insecureStorage := insecureStorageFlag(cmd)

	var name string
	if len(args) == 1 {
		name = args[0]
	}

	cfg, done, err := validateEarlyExits(hook, opts, name, workDir, streams, statusFn)
	if done || err != nil {
		return err
	}
	cfg, err = ensureRequestedValidateCommand(workDir, name, opts.inlineCmd, cfg, streams)
	if err != nil {
		return err
	}

	// Hook: fail early when CircleCI auth is missing and remote commands need it.
	// In non-hook context ensureCircleCIClient prompts interactively; hooks have
	// no TTY so we surface a clear message here instead of a confusing fallback.
	rc, _ := config.ResolveCircleCI(insecureStorage)

	executionPlan := planValidationExecution(cfg, opts, name)
	needsSidecar := executionPlan.PoolSize > 0
	if err := checkHookAuth(hook, needsSidecar, rc.CircleCIToken, streams); err != nil {
		return err
	}

	activeSidecar, _ := sidecar.LoadActive(ctx)
	resultCache, cacheKey := hookResultCache(hook, opts.inlineCmd, workDir, tree, name, cfg, execTarget(opts, cfg, activeSidecar))
	if cached, err := maybeReturnCachedHookResult(cmd, hook, resultCache, cacheKey, start, cfg, statusFn, streams); cached || err != nil {
		return err
	}

	if done, err := delegateToDaemon(opts, hook, workDir, rc.CircleCIToken, streams); done {
		return err
	}
	image := resolveImage(name, cfg)

	circleCIClient, err := maybeEnsureCircleCIClient(cmd.Context(), cmd, rc, needsSidecar, streams)
	if err != nil {
		return err
	}

	statusFn(iostream.LevelStep, "chunk validate")

	// Reap before reading state, so a file naming a sidecar that no longer exists
	// is gone before it can be promoted and reused. Loading first would hand
	// target preparation the very dead ID the reap just cleaned up.
	if needsSidecar {
		reapAbandonedSidecars(ctx, circleCIClient, workDir, statusFn, streams)
	}
	activeSidecar, _ = sidecar.LoadActive(ctx)

	validatePool, err := prepareValidationTarget(ctx, circleCIClient, opts, image, rc.CircleCITokenSource, executionPlan.PoolSize, activeSidecar, statusFn, workDir, streams)
	if err != nil {
		return err
	}

	// The wrap goes here, after target preparation fills opts.sidecarID but
	// before env loading, so that sync and env-resolve status events are
	// captured. Wired for every run, sidecar or not: an empty sidecar_id is what
	// files a run under a project's local row, not a reason to record nothing.
	statusFn, recorder := wrapEventLogStatusFn(statusFn, opts.sidecarID, activeSidecar, workDir, hook)
	setupComplete := false
	var setupErr error
	defer func() {
		if !setupComplete && setupErr != nil && recorder != nil {
			_ = failBeforeRun(recorder, start, setupErr)
		}
	}()

	envVars, err := resolveEnvVars(ctx, workDir, opts.envFile, opts.envVarsFlag)
	if err != nil {
		setupErr = err
		return err
	}
	setupComplete = true

	var result validate.Result
	var execErr error
	if err := saveInlineValidateCommand(workDir, name, opts.inlineCmd, opts.save, streams); err != nil {
		execErr = err
	} else {
		result, execErr = runValidationPlan(ctx, validatePool, executionPlan, rc, workDir, envVars, recorderCommandIDSetter(recorder), statusFn, streams)
	}
	if execErr == nil && resultCache != nil {
		if err := resultCache.Put(cacheKey, validate.CachedResult{CachedAt: time.Now()}); err != nil {
			streams.ErrPrintf("  %s\n", ui.ErrDim(fmt.Sprintf("chunk validate: cache write failed: %v", err)))
		}
	}
	return finishValidate(cmd, hook, execErr, start, cfg, result, recorder, statusFn, streams, notifyFunc(rc.Notifications))
}

func planValidationExecution(cfg *config.ProjectConfig, opts *validateOpts, name string) validate.Plan {
	var commands []config.Command
	if cfg != nil {
		commands = cfg.Commands
	}
	if opts.inlineCmd != "" {
		commandName := name
		if commandName == "" {
			commandName = defaultInlineCommandName
		}
		commands = []config.Command{{Name: commandName, Run: opts.inlineCmd}}
	} else if name != "" {
		if command := cfg.FindCommand(name); command != nil {
			commands = []config.Command{*command}
		}
	}

	placement := validate.PlacementConfigured
	switch {
	case opts.remote:
		placement = validate.PlacementRemote
	case opts.local:
		placement = validate.PlacementLocal
	}
	return validate.PlanCommands(commands, placement, 1)
}

func recorderCommandIDSetter(recorder *eventlog.Recorder) func(string) {
	if recorder == nil {
		return nil
	}
	return recorder.SetCommandID
}

func prepareValidationTarget(
	ctx context.Context,
	client *circleci.Client,
	opts *validateOpts,
	image, tokenSource string,
	poolSize int,
	activeSidecar *sidecar.ActiveSidecar,
	statusFn iostream.StatusFunc,
	workDir string,
	streams iostream.Streams,
) (*sidecar.Pool, error) {
	if poolSize == 0 {
		opts.sidecarID = ""
		return nil, nil
	}

	var existingIDs []string
	var freshIDs []string
	if opts.sidecarID != "" {
		existingIDs = []string{opts.sidecarID}
		if activeSidecar != nil {
			belongsToActivePool := false
			for _, id := range activeSidecar.SidecarIDs {
				if id == opts.sidecarID {
					belongsToActivePool = true
					break
				}
			}
			if !belongsToActivePool {
				activeSidecar = nil
			}
		}
	} else {
		created, resolvedImage, err := resolveOrCreateSidecarID(ctx, client, &opts.sidecarID, opts.orgID, image, workDir, tokenSource, streams)
		if err != nil {
			return nil, err
		}
		image = resolvedImage
		activeSidecar, _ = sidecar.LoadActive(ctx)
		if activeSidecar != nil {
			existingIDs = append(existingIDs, activeSidecar.SidecarIDs...)
		}
		if created {
			freshIDs = []string{opts.sidecarID}
		}
	}

	pool, err := setupValidatePool(ctx, client, opts, image, tokenSource, poolSize, activeSidecar, existingIDs, freshIDs, statusFn, workDir)
	if err != nil {
		return nil, err
	}
	entry, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire validation sidecar: %w", err)
	}
	opts.sidecarID = entry.ID
	pool.Release(entry)
	statusFn(iostream.LevelInfo, fmt.Sprintf("using sidecar %s for remote commands", entry.ID))
	return pool, nil
}

func checkHookAuth(hook *hookContext, needsSidecar bool, token string, streams iostream.Streams) error {
	if hookMissingAuth(hook, needsSidecar, token, streams) {
		return errSilentExit
	}
	return nil
}

func prepareValidateConfig(workDir string, hook *hookContext, opts *validateOpts, name string, statusFn iostream.StatusFunc) (*config.ProjectConfig, bool, error) {
	cfg, err := config.LoadProjectConfig(workDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, &userError{msg: msgCouldNotLoadConfig, suggestion: configFilePermHint, err: err}
	}
	if (err != nil || !cfg.HasCommands()) && opts.inlineCmd == "" {
		if hook != nil {
			return nil, true, nil // no config in hook context: skip silently
		}
		return nil, false, &userError{
			msg:        msgValidateNotConfigured,
			suggestion: suggestionValidateNotConfigured,
			errMsg:     errNoValidateCommands,
			hideDetail: true,
		}
	}
	if cfg == nil {
		cfg = &config.ProjectConfig{}
	}
	if err := validateEnvFlag(opts.envVarsFlag); err != nil {
		return nil, false, err
	}
	if opts.dryRun {
		return nil, true, runValidateDryRun(name, opts.inlineCmd, cfg, statusFn)
	}
	return cfg, false, nil
}

func ensureValidateCommand(workDir, name string, cfg *config.ProjectConfig, streams iostream.Streams) (*config.ProjectConfig, error) {
	if cfg.FindCommand(name) != nil {
		return cfg, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, &userError{
			msg:        fmt.Sprintf("Command %q is not configured.", name),
			suggestion: "Add it to .chunk/config.json.",
			errMsg:     fmt.Sprintf("command %q is not configured", name),
		}
	}

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
	updated, err := config.LoadProjectConfig(workDir)
	if err != nil {
		return nil, fmt.Errorf("reload project config: %w", err)
	}
	return updated, nil
}

func ensureRequestedValidateCommand(workDir, name, inlineCmd string, cfg *config.ProjectConfig, streams iostream.Streams) (*config.ProjectConfig, error) {
	if inlineCmd != "" || name == "" {
		return cfg, nil
	}
	return ensureValidateCommand(workDir, name, cfg, streams)
}

// wrapEventLogStatusFn wraps statusFn so a run's progress is recorded in its
// project's event log, and returns the recorder alongside it.
//
// Wired for every run, local included. A run with no sidecar is still a run
// whose results a developer will look for, and the daemon reads these logs off
// disk rather than being sent anything — so skipping the recorder here is how a
// --local run, or a repo with no remote commands, ends up reporting to nobody.
// An empty sidecar ID is what the dashboard files under a project's "local"
// row, not a reason to write nothing.
//
// A project with no readable data directory is the one case that gets statusFn
// back unchanged: there is nowhere to write, so the run reports without
// recording.
func wrapEventLogStatusFn(statusFn iostream.StatusFunc, sidecarID string, activeSidecar *sidecar.ActiveSidecar, workDir string, hook *hookContext) (iostream.StatusFunc, *eventlog.Recorder) {
	// Keyed on workDir rather than sidecar.StateDir, which walks up from the
	// process's own working directory and so answers for the wrong project under
	// --project.
	//
	// workDir is only as good as what reached it, and two cases are known to
	// leave it pointing elsewhere. A project whose .chunk lives below the git
	// root keys its log here but its sidecar state under the root, splitting one
	// project's state in two. And a daemon-delegated run without an explicit
	// --project resolves workDir to the daemon's own working directory, because
	// the request carries Args and Env but nothing about where the caller stood.
	// Both file a run under a project that did not run it.
	dataDir, err := config.ProjectDataDir(workDir)
	if err != nil {
		// A missing data dir leaves the recorder reporting without recording.
		return statusFn, nil
	}
	scName := ""
	if activeSidecar != nil && activeSidecar.ID() == sidecarID {
		scName = activeSidecar.Name
	}
	op := eventlog.OpValidate
	if hook != nil && hook.stopHookActive {
		op = eventlog.OpHook
	}
	// Register the project alongside the log about to be written to it. A run's
	// results are never sent to the watch daemon — it reads this same log off
	// disk — and only saving sidecar state used to register a project, so a run
	// with no sidecar logged its results where no daemon would ever look for
	// them. Registered remote commands were reached the same way: the daemon
	// buffers their output under a project root it lists only once it has
	// discovered that project.
	// Best-effort: a run still records without the breadcrumb, and still reports
	// without the log.
	_ = sidecar.RegisterProjectRoot(dataDir, workDir)
	recorder := eventlog.Record(dataDir, statusFn, op, sidecarID, scName, sidecar.CurrentBranch(workDir))
	return recorder.Status, recorder
}

// failBeforeRun closes the event-log entry when setup fails before commands run.
func failBeforeRun(rec *eventlog.Recorder, start time.Time, err error) error {
	rec.Final(iostream.LevelError, fmt.Sprintf("setup failed  %s: %s", ui.FormatDuration(time.Since(start)), err), 0, 0)
	return err
}

// notifyFunc returns notify.Send when enabled is true, or nil to skip notifications.
func notifyFunc(enabled bool) func(title, body string) {
	if enabled {
		return notify.Send
	}
	return nil
}

// finishValidate reports the validate outcome and handles hook exit codes.
// notifyFn, when non-nil, is called with the notification title and body;
// pass notify.Send for real desktop notifications, or a capturing closure in tests.
func finishValidate(cmd *cobra.Command, hook *hookContext, execErr error, start time.Time, cfg *config.ProjectConfig, result validate.Result, recorder *eventlog.Recorder, statusFn iostream.StatusFunc, streams iostream.Streams, notifyFn func(title, body string)) error {
	maxAttempts := validate.DefaultMaxAttempts
	if hook != nil {
		if ma := cfg.StopHookMaxAttempts; ma > 0 {
			maxAttempts = ma
		}
	}

	elapsed := ui.FormatDuration(time.Since(start))
	summary := fmt.Sprintf("%d/%d passed", result.Passed, result.Total)
	level := iostream.LevelDone
	message := fmt.Sprintf("%s  %s", summary, elapsed)
	switch {
	case execErr != nil && hook != nil:
		attempt := validate.ReadAttempts(hook.sessionID) + 1
		level = iostream.LevelError
		message = fmt.Sprintf("%s  %s (attempt %d/%d)", summary, elapsed, attempt, maxAttempts)
	case execErr != nil:
		level = iostream.LevelError
	}
	if recorder != nil {
		recorder.Final(level, message, result.Passed, result.Total)
	} else {
		statusFn(level, message)
	}
	if notifyFn != nil {
		if execErr != nil {
			notifyFn("chunk validate failed", fmt.Sprintf("%d/%d checks passed · %s", result.Passed, result.Total, elapsed))
		} else {
			notifyFn("chunk validate passed", fmt.Sprintf("%d/%d checks passed · %s", result.Passed, result.Total, elapsed))
		}
	}
	if hook == nil {
		return execErr
	}
	hookErr := validate.WrapHookResult(hook.sessionID, execErr, maxAttempts, streams.Err)
	if hookErr == nil && execErr == nil {
		return writeStopHookMessage(cmd.OutOrStdout(), fmt.Sprintf("chunk validate passed (%s)", elapsed))
	}
	return hookErr
}

func validateEnvFlag(envVarsFlag []string) error {
	if _, err := sidecar.ParseEnvPairs(envVarsFlag); err != nil {
		return &userError{msg: fmt.Sprintf("invalid --env value: %s", err), err: err}
	}
	return nil
}

// runValidateViaDaemon delegates a validate run to the watch daemon and writes
// its captured output to streams. When hook is non-nil its context is forwarded
// to the subprocess via hidden flags so it runs as a hook invocation.
//
// workDir travels with the request because the daemon cannot work it out: it
// serves every repo on the machine from whatever directory it was launched in,
// so a run that resolved the project from the daemon's cwd would validate that
// repo instead of this one.
func runValidateViaDaemon(workDir string, args []string, circleCIToken string, hook *hookContext, streams iostream.Streams) error {
	reqArgs := args
	if hook != nil {
		reqArgs = append(append([]string(nil), args...), "--hook-session-id", hook.sessionID)
		if hook.stopHookActive {
			reqArgs = append(reqArgs, "--stop-hook-active")
		}
	}
	resp, err := watchd.RunValidate(workDir, reqArgs, circleCIToken)
	if err != nil {
		return fmt.Errorf("daemon validate: %w", err)
	}
	_, _ = streams.Out.Write([]byte(resp.Stdout))
	_, _ = streams.Err.Write([]byte(resp.Stderr))
	if resp.ExitCode != 0 {
		return &silentExitError{code: resp.ExitCode}
	}
	return nil
}

// tryHookDelegate delegates a hook-invoked validate run to the daemon before
// initHook runs, so the subprocess prints the session header (not the client).
// Returns (true, err) when the call was delegated, (false, nil) to run inline.
func tryHookDelegate(cmd *cobra.Command, hook *hookContext, workDir string, noDaemon bool, streams iostream.Streams) (bool, error) {
	if hook == nil || noDaemon || !watchd.IsDaemonRunning() {
		return false, nil
	}
	// If hooks are disabled in this environment, don't delegate — the daemon
	// subprocess won't inherit this process's env var, so initHook must handle it.
	if os.Getenv(config.EnvChunkHooksDisabled) != "" {
		return false, nil
	}
	insecureStorage := insecureStorageFlag(cmd)
	rc, err := config.ResolveCircleCI(insecureStorage)
	if err != nil {
		return false, nil // fall back to inline; inline path handles auth
	}
	err = runValidateViaDaemon(workDir, os.Args[1:], rc.CircleCIToken, hook, streams)
	if errors.Is(err, watchd.ErrDaemonUnavailable) {
		return false, nil // daemon disappeared between check and POST; run inline
	}
	return true, err
}

// delegateToDaemon hands the run to the watch daemon when it should be, and
// reports whether it did. A caller told false runs the commands inline.
//
// Async and synchronous delegation are decided together because they are the
// same decision: both give the run to the daemon, and the only difference is
// whether this process waits for the answer. Either can decline — an
// unfingerprintable tree, an old daemon, no daemon at all — and every decline
// means the same thing, which is to run the commands here instead.
func delegateToDaemon(opts *validateOpts, hook *hookContext, workDir, circleCIToken string, streams iostream.Streams) (bool, error) {
	if opts.async && !opts.noDaemon {
		// A refusal falls through to inline: slower than the caller asked for,
		// but it always tells them the truth about the tree in front of them.
		return tryAsyncDelegate(workDir, circleCIToken, streams)
	}
	if shouldUseDaemon(hook, opts.noDaemon) {
		err := runValidateViaDaemon(workDir, os.Args[1:], circleCIToken, nil, streams)
		// ErrDaemonUnavailable covers two cases: the daemon disappeared between
		// the IsDaemonCompatible check and the POST (connection refused), and the
		// daemon lacks the /validate endpoint because it is from an older build
		// (404). Both fall through to inline execution.
		if !errors.Is(err, watchd.ErrDaemonUnavailable) {
			return true, err
		}
	}
	return false, nil
}

// tryAsyncDelegate hands the run to the daemon without waiting for it. The
// bool reports whether the run was accepted, so a caller that gets false runs
// the commands inline instead.
//
// A refusal is not a failure: the daemon declines a tree it cannot fingerprint,
// because it could not then tell whether the result still described that tree by
// the time it finished. Running inline is the honest fallback — the answer is
// slower to arrive but reaches the caller while it is still true.
func tryAsyncDelegate(workDir, circleCIToken string, streams iostream.Streams) (bool, error) {
	if !watchd.IsDaemonCompatible() {
		streams.ErrPrintln(ui.ErrDim("chunk validate: no watch daemon, running inline"))
		return false, nil
	}
	taskID, err := watchd.StartAsyncValidate(workDir, os.Args[1:], circleCIToken)
	switch {
	case errors.Is(err, watchd.ErrAsyncRefused):
		// The daemon's own words: it refuses for more than one reason, and a
		// developer told the wrong one goes looking in the wrong place.
		reason := strings.TrimPrefix(err.Error(), watchd.ErrAsyncRefused.Error()+": ")
		streams.ErrPrintln(ui.ErrDim("chunk validate: " + reason + ", running inline"))
		return false, nil
	case errors.Is(err, watchd.ErrDaemonUnavailable):
		streams.ErrPrintln(ui.ErrDim("chunk validate: watch daemon unavailable, running inline"))
		return false, nil
	case err != nil:
		return true, err
	}
	streams.ErrPrintf("  %s\n", ui.ErrDim("validating in the background: "+shortTaskID(taskID)))
	return true, nil
}

// shortTaskID trims a task ID for display. The full UUID is only needed to
// correlate with the daemon; a developer reading a line of output is not.
func shortTaskID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// newValidateResultsCmd reports what background validation runs concluded.
//
// A subcommand rather than a flag on validate because it validates nothing: it
// reads results the daemon is already holding and prints them. A flag would
// read as a modifier of a run that never happens.
//
// Nobody can have a validate command named "results" any more, the same trade
// `validate variants` already makes.
func newValidateResultsCmd() *cobra.Command {
	var projectDir string

	cmd := &cobra.Command{
		Use:          "results",
		Short:        "Print results of finished background runs",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			workDir, err := resolveWorkDir(&validateOpts{projectDir: projectDir})
			if err != nil {
				return err
			}
			return runResults(workDir, iostream.FromCmd(cmd))
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")

	return cmd
}

// runResults prints the results of finished background runs for workDir and
// clears them, so a result is reported once rather than on every later run.
//
// Output goes to stdout and nothing else does. This is what a turn-start hook
// calls, and on those hooks Claude Code adds stdout to the agent's context — so
// stdout is the channel by which a background result reaches the agent that
// caused it. Progress and warnings stay on stderr.
func runResults(workDir string, streams iostream.Streams) error {
	tasks, err := watchd.CollectValidateResults(workDir)
	if err != nil {
		// Nothing to collect is the common case, not a failure: no daemon means
		// no background runs. Reporting it as an error on a hook path would put
		// noise in front of the agent on every turn.
		if errors.Is(err, watchd.ErrDaemonUnavailable) {
			return nil
		}
		return err
	}
	printResults(streams, tasks)
	return nil
}

// printResults writes what background runs concluded. Split from runResults so
// the wording can be tested directly: what an agent is told is the whole point
// of the feature, and driving it through a socket to find out is a poor trade.
func printResults(streams iostream.Streams, tasks []watchd.TaskState) {
	for _, t := range tasks {
		// A discarded run reports no verdict, only that it was discarded. The
		// daemon strips the exit code and output of a stale task precisely so this
		// cannot be read as a pass or a failure — the run described code that has
		// since changed, and the honest thing to say is that there is no answer.
		//
		// Said out loud rather than swallowed because the usual cause is the run
		// itself: a command that writes coverage output or regenerates a golden
		// moves a path git is watching and invalidates its own result. Silence
		// there is indistinguishable from no run happening, which leaves a whole
		// class of projects getting nothing with nothing to explain it.
		if t.Stale {
			streams.Printf("chunk validate discarded a background run (%s): the working tree changed while it ran, so its result no longer describes your code\n", shortTaskID(t.ID))
			streams.Printf("  if this repeats, the run is probably changing the tree itself; gitignore what it writes\n")
			continue
		}
		if t.Passed() {
			streams.Printf("chunk validate passed in the background (%s)\n", shortTaskID(t.ID))
			continue
		}
		streams.Printf("chunk validate FAILED in the background (%s), exit status %d\n", shortTaskID(t.ID), t.ExitCode)
		if t.Output != "" {
			streams.Printf("%s\n", strings.TrimRight(t.Output, "\n"))
		}
	}
}

// validateEarlyExits handles --list, --mark-remote, missing config, --dry-run.
// Returns (cfg, done=true, err) to stop, or (cfg, false, nil) to continue.
func validateEarlyExits(hook *hookContext, opts *validateOpts, name, workDir string, streams iostream.Streams, statusFn iostream.StatusFunc) (*config.ProjectConfig, bool, error) {
	if opts.list {
		return nil, true, runValidateList(workDir, opts.jsonOut, streams, statusFn)
	}
	if opts.jsonOut {
		return nil, true, fmt.Errorf("--json requires --list")
	}
	if opts.markRemote {
		return nil, true, runMarkRemote(workDir, name, streams)
	}
	return prepareValidateConfig(workDir, hook, opts, name, statusFn)
}

func runValidateDryRun(name, inlineCmd string, cfg *config.ProjectConfig, statusFn iostream.StatusFunc) error {
	if inlineCmd != "" {
		cmdName := name
		if cmdName == "" {
			cmdName = defaultInlineCommandName
		}
		statusFn(iostream.LevelInfo, fmt.Sprintf("%s: %s", cmdName, inlineCmd))
		return nil
	}
	_, err := mapValidateError(validate.Result{}, validate.RunDryRun(cfg, name, statusFn))
	return err
}

func saveInlineValidateCommand(workDir, name, inlineCmd string, save bool, streams iostream.Streams) error {
	if inlineCmd == "" || !save {
		return nil
	}
	if name == "" {
		name = defaultInlineCommandName
	}
	if err := config.SaveCommand(workDir, name, inlineCmd); err != nil {
		return &userError{msg: "Could not save command to .chunk/config.json.", err: err}
	}
	streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Saved %s to .chunk/config.json", name)))
	return nil
}

func runValidationPlan(
	ctx context.Context,
	pool *sidecar.Pool,
	plan validate.Plan,
	rc config.ResolvedConfig,
	localWorkDir string,
	envVars map[string]string,
	setCommandID func(string),
	statusFn iostream.StatusFunc,
	streams iostream.Streams,
) (validate.Result, error) {
	var remote validate.DistributedRunResult
	if len(plan.RemoteCommands) > 0 {
		if pool == nil {
			return validate.Result{}, errors.New("remote validation requires a sidecar pool")
		}
		remote = validate.RunDistributed(ctx, plan.RemoteCommands, validate.DistributedRunOptions[*sidecar.PoolEntry]{
			Parallelism: plan.PoolSize,
			Acquire:     pool.Acquire,
			Release:     pool.Release,
			WorkerName: func(entry *sidecar.PoolEntry) string {
				return "sidecar " + entry.ID
			},
			Run: func(ctx context.Context, entry *sidecar.PoolEntry, command config.Command, status iostream.StatusFunc, commandStreams iostream.Streams) validate.DistributedJobResult {
				return runPooledValidateCommand(ctx, entry, command, rc.CircleCIToken, localWorkDir, envVars, setCommandID, status, commandStreams)
			},
			Status:  statusFn,
			Streams: streams,
		})
		renderDistributedOutput(remote.Output, streams)
	}

	result := validate.Result{
		Passed: remote.Passed,
		Total:  len(plan.RemoteCommands) + len(plan.LocalCommands),
	}
	if len(plan.LocalCommands) == 0 {
		return result, remote.Err
	}

	statusFn(iostream.LevelInfo, fmt.Sprintf("running locally: %s", commandNames(plan.LocalCommands)))
	localCfg := &config.ProjectConfig{Commands: plan.LocalCommands}
	localResult, err := mapValidateError(validate.RunAll(ctx, localWorkDir, localCfg, envVars, statusFn, streams))
	result.Passed += localResult.Passed
	return result, errors.Join(remote.Err, err)
}

func runPooledValidateCommand(
	ctx context.Context,
	entry *sidecar.PoolEntry,
	command config.Command,
	token, localWorkDir string,
	envVars map[string]string,
	setCommandID func(string),
	statusFn iostream.StatusFunc,
	streams iostream.Streams,
) validate.DistributedJobResult {
	if setCommandID != nil {
		// A submission failure has no command ID. Clear any ID left by a prior
		// command before starting so its terminal event cannot inherit one.
		setCommandID("")
	}
	target := sidecar.Target{
		Client:      entry.Client,
		SidecarID:   entry.ID,
		Workdir:     entry.RepoPath,
		OnSubmitted: onValidateCommandSubmitted(entry.ID, localWorkDir, command.Name, setCommandID),
	}
	execFn, dest, err := target.ReadyExecRunner(ctx, localWorkDir, remoteExecEnv(token, envVars), streams)
	if err != nil {
		var workspaceErr *sidecar.WorkspaceNotFoundError
		if errors.As(err, &workspaceErr) {
			return validate.DistributedJobResult{Err: missingWorkspace(entry.ID, entry.RepoPath, command.Name, err)}
		}
		return validate.DistributedJobResult{Err: unreachableSidecar(entry.ID, command.Name, err)}
	}
	commandCfg := &config.ProjectConfig{Commands: []config.Command{command}}
	result, err := validate.RunRemoteStreamedResult(ctx, execFn, commandCfg, "", dest, localWorkDir, statusFn, streams)
	return validate.DistributedJobResult{Passed: result.Passed, Err: err}
}

func onValidateCommandSubmitted(sidecarID, projectRoot, commandName string, setCommandID func(string)) func(string) {
	return func(commandID string) {
		if setCommandID != nil {
			setCommandID(commandID)
		}
		watchd.RegisterCommand(watchd.CommandReg{
			CommandID:   commandID,
			SidecarID:   sidecarID,
			ProjectRoot: projectRoot,
			Op:          string(eventlog.OpValidate),
			Name:        clampLabel(commandName),
			SubmittedAt: time.Now(),
		})
	}
}

func renderDistributedOutput(output []validate.DistributedRunOutput, streams iostream.Streams) {
	for _, command := range output {
		streams.ErrPrintln(ui.ErrBold(command.Command.Name + ":"))
		if command.Stdout != "" {
			_, _ = fmt.Fprint(streams.Out, command.Stdout)
		}
		if command.Stderr != "" {
			_, _ = fmt.Fprint(streams.Err, command.Stderr)
		}
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

// remoteExecEnv collects host environment variables that should be forwarded
// into commands running on the sidecar. The resolved CircleCI token (which may
// come from env, the on-disk config, or any future keychain backend) is
// forwarded as CIRCLE_TOKEN so remote validate commands can authenticate to
// CircleCI APIs (e.g. smarter-testing endpoints), mirroring the local behavior
// where the token is picked up from the resolved config.
func remoteExecEnv(token string, envVars map[string]string) map[string]string {
	merged := map[string]string{}
	if token != "" {
		merged[config.EnvCircleToken] = token
	}
	for k, v := range envVars {
		merged[k] = v
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
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

// resolveOrCreateSidecarID fills sidecarID from the active sidecar, or creates
// a new sidecar when none is configured. Returns true when a new sidecar was
// provisioned (as opposed to loaded from the active state file). The returned
// image is the configured or auto-selected image used for new pool members.
func resolveOrCreateSidecarID(ctx context.Context, client *circleci.Client, sidecarID *string, orgID, image, workDir, tokenSource string, streams iostream.Streams) (created bool, resolvedImage string, err error) {
	if *sidecarID != "" {
		return false, image, nil
	}
	active, loadErr := sidecar.LoadActive(ctx)
	if loadErr != nil {
		return false, image, &userError{msg: msgCouldNotLoadSidecar, suggestion: configFilePermHint, err: loadErr}
	}
	if active != nil {
		*sidecarID = active.ID()
		return false, image, nil
	}
	// A status line, not stderr prose: having no sidecar yet is the normal state
	// of a first run, and printing it raw made it the headline of the hook's
	// "Stop hook error:" banner even when everything then went fine.
	statusFn := newStatusFunc(streams)
	statusFn(iostream.LevelInfo, "no active sidecar; creating one")
	resolvedOrgID, err := resolveOrgID(orgID, workDir, orgPicker(ctx, client, tokenSource))
	if err != nil {
		return false, image, err
	}
	// No image configured means no snapshot was ever recorded for this repo.
	// Rather than boot the bare default image, look for one of the org's
	// snapshots that fits this repo; autoSelectSnapshotImage returns "" (the
	// old behaviour) when none does.
	if image == "" {
		image = autoSelectSnapshotImage(ctx, client, resolvedOrgID, workDir, newStatusFunc(streams), streams)
	}
	sandboxName := sidecarAutoName(ctx, workDir)
	sc, err := sidecar.Create(ctx, client, resolvedOrgID, sandboxName, image)
	if err != nil {
		if authErr := cannotCreateSidecar(resolvedOrgID, orgSource(orgID, workDir), err); authErr != nil {
			return false, image, authErr
		}
		return false, image, &userError{
			msg:        "Could not create a sidecar.",
			suggestion: "Check your network connection or run 'chunk sidecar create' manually.",
			err:        err,
		}
	}
	if saveErr := sidecar.SaveActive(ctx, sidecar.ActiveSidecar{SidecarIDs: []string{sc.ID}, Name: sc.Name, OrgID: resolvedOrgID}); saveErr != nil {
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
	return true, image, nil
}

// branchSanitizer is kept for the no-session fallback path.
var branchSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

// sidecarAutoName builds a sidecar name from workDir, the Claude session ID,
// and the current git branch.
//
// When a session ID is present the branch is encoded as an 8-hex-char suffix
// (sha256(sessionID+":"+branch)[:4]) so the raw branch name is never exposed,
// and the session ID is trimmed to its first 8 characters so a name stays
// readable in `chunk sidecar list` — a session ID is a 36-character UUID, and
// unlike the state file name this one only has to be recognisable, not unique
// (two sessions sharing a prefix get two sidecars with one name and different
// IDs):
//   - Both present → "<base>-<sessionID8>-<hash8>"
//   - Session only → "<base>-<sessionID8>"
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
		short := shortSessionID(sessionID)
		if branch != "" {
			sum := sha256.Sum256([]byte(sessionID + ":" + branch))
			hash8 := fmt.Sprintf("%x", sum[:4])
			return base + "-" + short + "-" + hash8
		}
		return base + "-" + short
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
		id = active.ID()
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

func mapValidateError(r validate.Result, err error) (validate.Result, error) {
	if errors.Is(err, validate.ErrNotConfigured) {
		return r, &userError{
			msg:        msgValidateNotConfigured,
			suggestion: suggestionValidateNotConfigured,
			hideDetail: true,
			err:        err,
		}
	}
	return r, err
}

// setupValidatePool resolves a validate pool at the capacity selected by the
// execution plan, reusing cached sidecars when possible and creating or
// replacing members through the pool lifecycle when needed.
func setupValidatePool(ctx context.Context, client *circleci.Client, opts *validateOpts, image, tokenSource string, n int, active *sidecar.ActiveSidecar, existingIDs, freshIDs []string, statusFn iostream.StatusFunc, workDir string) (*sidecar.Pool, error) {
	statusFn(iostream.LevelStep, fmt.Sprintf("Preparing sidecar pool with capacity %d...", n))
	resolvedOrgID := opts.orgID
	if resolvedOrgID == "" && active != nil {
		resolvedOrgID = active.OrgID
	}
	if resolvedOrgID == "" {
		resolvedOrgID, _ = config.ResolveOrgID(workDir)
	}
	pool, err := sidecar.NewPool(ctx, client, sidecar.PoolOptions{
		Size:        n,
		Name:        "validate",
		OrgID:       resolvedOrgID,
		Image:       image,
		WorkDir:     workDir,
		RepoPath:    validationRepoPath(opts.workdir, active),
		ExistingIDs: existingIDs,
		FreshIDs:    freshIDs,
	}, statusFn)
	if err != nil {
		if sshErr := sshSessionError(err); sshErr != nil {
			return nil, sshErr
		}
		if authErr := notAuthorized("create sidecars", tokenSource, err); authErr != nil {
			return nil, authErr
		}
		return nil, &userError{
			msg:        "Could not prepare the sidecar pool for validation.",
			suggestion: "Check your network connection or run 'chunk sidecar create' manually.",
			err:        err,
		}
	}
	return pool, nil
}

func validationRepoPath(configured string, active *sidecar.ActiveSidecar) string {
	if configured != "" || active == nil {
		return configured
	}
	return active.Workspace
}
