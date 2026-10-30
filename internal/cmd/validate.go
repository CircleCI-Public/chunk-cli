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
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/notify"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
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
			blocked:    true,
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
	sidecarID   string
	workdir     string
	orgID       string
	dryRun      bool
	list        bool
	save        bool
	remote      bool
	local       bool
	parallel    int
	markRemote  bool
	jsonOut     bool
	inlineCmd   string
	projectDir  string
	envVarsFlag []string
	envFile     string
	async       bool   // run via the daemon without waiting for the result
	noDaemon    bool   // bypasses daemon delegation; set by the daemon when calling in-process
	attributeTo string // project the results belong to, when commands run in a snapshot copy
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
	cmd.Flags().IntVar(&opts.parallel, "parallel", 0, fmt.Sprintf("max sidecars to use for remote commands (0: auto, up to %d)", defaultValidateParallelism))
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
	cmd.Flags().BoolVar(&opts.async, "async", false, "Run in the background via the chunk daemon and report on a later run")
	cmd.Flags().BoolVar(&opts.noDaemon, "no-daemon", false, "")
	_ = cmd.Flags().MarkHidden("no-daemon")
	cmd.Flags().StringVar(&opts.attributeTo, "attribute-to", "", "")
	_ = cmd.Flags().MarkHidden("attribute-to")

	cmd.AddCommand(newValidateVariantsCmd())
	cmd.AddCommand(newValidateResultsCmd())

	return cmd
}

func maybeEnsureCircleCIClient(ctx context.Context, cmd *cobra.Command, rc config.ResolvedConfig, needsSidecar bool, streams iostream.Streams) (*circleci.Client, error) {
	if !needsSidecar {
		return nil, nil
	}
	return ensureCircleCIClient(ctx, cmd, rc, streams, ui.PromptHidden)
}

// attributionRoot is the project a run's results belong to, which is not always
// the directory the commands run in.
//
// They differ in one case: the daemon running background checks in a
// checked-out snapshot. The copy is where the commands execute, and the real
// repository is where the developer is watching for their results — so the
// event log, the project breadcrumb the daemon discovers projects by, and the
// branch a run is filed under all come from here rather than from the copy,
// which is on a detached HEAD in a temp directory nobody is looking at.
func (o *validateOpts) attributionRoot(workDir string) string {
	if o.attributeTo != "" {
		return o.attributeTo
	}
	return workDir
}

func resolveWorkDir(opts *validateOpts) (string, error) {
	if opts.projectDir != "" {
		return opts.projectDir, nil
	}
	return os.Getwd()
}

// shouldUseDaemon reports whether this validate run should be delegated to the
// chunk daemon. --no-daemon skips this to avoid re-delegation when the daemon
// calls us in-process. Delegation is skipped for daemons from a different build
// since they may not support the /validate endpoint.
func shouldUseDaemon(noDaemon bool) bool {
	return !noDaemon && chunkd.IsDaemonCompatible()
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

	ctx := cmd.Context()
	statusFn := newStatusFunc(streams)
	insecureStorage := insecureStorageFlag(cmd)

	var name string
	if len(args) == 1 {
		name = args[0]
	}

	cfg, done, err := validateEarlyExits(opts, name, workDir, streams, statusFn)
	if done || err != nil {
		return err
	}
	cfg, err = ensureRequestedValidateCommand(workDir, name, opts.inlineCmd, cfg, streams)
	if err != nil {
		return err
	}

	rc, _ := config.ResolveCircleCI(insecureStorage)

	executionPlan := planValidationExecution(cfg, opts, name)
	needsSidecar := executionPlan.PoolSize > 0

	image := resolveImage(name, cfg)
	if done, err := delegateToDaemon(opts, workDir, rc.CircleCIToken, cfg.OrgID, image, streams); done {
		return err
	}

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
	activeSidecar, _ := sidecar.LoadActive(ctx)

	validatePool, err := prepareValidationTarget(ctx, circleCIClient, opts, image, rc.CircleCITokenSource, executionPlan.PoolSize, activeSidecar, statusFn, workDir, streams)
	if err != nil {
		return err
	}

	// The wrap goes here, after target preparation fills opts.sidecarID but
	// before env loading, so that sync and env-resolve status events are
	// captured. Wired for every run, sidecar or not: an empty sidecar_id is what
	// files a run under a project's local row, not a reason to record nothing.
	//
	// Use the git root rather than workDir so the event log lands in the same
	// data directory as sidecar state, which keys on the git top-level. A
	// project whose .chunk sits below the git root would otherwise split its
	// state across two data directories. The plan gets the same root, so
	// registered commands are filed under the project the daemon tracks.
	attributionDir := opts.attributionRoot(workDir)
	if gitRoot := gitutil.TopLevelCtx(ctx, attributionDir); gitRoot != "" {
		attributionDir = gitRoot
	}
	pooled := executionPlan.PoolSize > 1
	statusFn, recorder := wrapEventLogStatusFn(statusFn, runRecorderSidecarID(pooled, opts.sidecarID), activeSidecar, attributionDir)
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
		result, execErr = runValidationPlan(ctx, validatePool, executionPlan, rc, workDir, attributionDir, envVars, newPoolRecorders(recorder, pooled), statusFn, streams)
	}
	// Saved after the plan runs, not right after the pool is built: a pool
	// grown beyond one sidecar finishes cloning in the background while
	// RunDistributed's workers wait to acquire them, so only once the run has
	// actually used them are they all guaranteed to be reflected in pool.IDs().
	if validatePool != nil {
		if err := saveActivePoolState(ctx, validatePool, opts.orgID); err != nil {
			statusFn(iostream.LevelWarn, fmt.Sprintf("could not save active sidecar: %v", err))
		}
	}
	return finishValidate(execErr, start, result, recorder, statusFn, notifyFunc(rc.Notifications))
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
	return validate.PlanCommands(commands, placement, validateParallelism(opts.parallel, opts.sidecarID))
}

// defaultValidateParallelism caps how many sidecars a validate run uses at
// once when --parallel is not set, so a config with many remote commands
// does not silently fan out to one sidecar per command.
const defaultValidateParallelism = 3

// validateParallelism resolves the max sidecars a run may use. PlanCommands
// still clamps this to the number of remote commands actually selected.
//
// An explicit --sidecar-id always forces exactly one: it names a single
// target the caller chose, not a pool to grow around it.
func validateParallelism(parallel int, explicitSidecarID string) int {
	if explicitSidecarID != "" {
		return 1
	}
	if parallel > 0 {
		return parallel
	}
	return defaultValidateParallelism
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

// saveActivePoolState persists every sidecar id currently backing pool, so a
// pool grown beyond one sidecar is reused in full on the next invocation
// instead of only its first member. It reloads the active sidecar itself
// rather than trusting a snapshot taken before the pool was built, since
// prepareValidationTarget's local copy can diverge from it (a reload after
// creating the first sidecar, or dropping it when an explicit --sidecar-id
// does not belong to it) without that change reaching the caller.
func saveActivePoolState(ctx context.Context, pool *sidecar.Pool, fallbackOrgID string) error {
	next := sidecar.ActiveSidecar{SidecarIDs: pool.IDs(), OrgID: fallbackOrgID}
	if active, _ := sidecar.LoadActive(ctx); active != nil {
		next.Name = active.Name
		next.Workspace = active.Workspace
		if active.OrgID != "" {
			next.OrgID = active.OrgID
		}
	}
	return sidecar.SaveActive(ctx, next)
}

func prepareValidateConfig(workDir string, opts *validateOpts, name string, statusFn iostream.StatusFunc) (*config.ProjectConfig, bool, error) {
	cfg, err := config.LoadProjectConfig(workDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, &userError{msg: msgCouldNotLoadConfig, suggestion: configFilePermHint, err: err}
	}
	if (err != nil || !cfg.HasCommands()) && opts.inlineCmd == "" {
		return nil, false, &userError{
			msg:        msgValidateNotConfigured,
			blocked:    true,
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
func wrapEventLogStatusFn(statusFn iostream.StatusFunc, sidecarID string, activeSidecar *sidecar.ActiveSidecar, workDir string) (iostream.StatusFunc, *eventlog.Recorder) {
	// Callers pass the git top-level (not their cwd) so the event log lands in
	// the same data directory as sidecar state, which sidecar.StateDir also keys
	// on the git top-level.  See the call site in runValidateCmdE for where the
	// root is derived.
	dataDir, err := config.ProjectDataDir(workDir)
	if err != nil {
		// A missing data dir leaves the recorder reporting without recording.
		return statusFn, nil
	}
	scName := ""
	if activeSidecar != nil && activeSidecar.ID() == sidecarID {
		scName = activeSidecar.Name
	}
	// Register the project alongside the log about to be written to it. A run's
	// results are never sent to the chunk daemon — it reads this same log off
	// disk — and only saving sidecar state used to register a project, so a run
	// with no sidecar logged its results where no daemon would ever look for
	// them. Registered remote commands were reached the same way: the daemon
	// buffers their output under a project root it lists only once it has
	// discovered that project.
	// Best-effort: a run still records without the breadcrumb, and still reports
	// without the log.
	_ = sidecar.RegisterProjectRoot(dataDir, workDir)
	recorder := eventlog.Record(dataDir, statusFn, eventlog.OpValidate, sidecarID, scName, sidecar.CurrentBranch(workDir))
	return recorder.Status, recorder
}

// poolRecorders hands out one event-log recorder per pool sidecar. They share
// the run's log, so its mutex serialises the concurrent workers' appends, and
// each closes its own commands: the dashboard matches an event to a sidecar row
// on SidecarID alone, so a pooled run recorded under one representative ID puts
// every command on one row and leaves the rest of the pool blank.
//
// A command's ID is kept per sidecar too. The run's own recorder never sees a
// pooled command's closing event, so an ID set there would stamp nothing, or
// land on whichever local command finished next.
//
// A run on one sidecar is not split: its commands report through the run's own
// recorder, so they and the run-wide summary close as one run. Closing each
// command there too would show one run as one per command plus the summary.
type poolRecorders struct {
	run    *eventlog.Recorder
	pooled bool

	mu   sync.Mutex
	byID map[string]*eventlog.Recorder
}

// newPoolRecorders returns nil when there is no log to record in, which status
// and commandIDSetter tolerate.
func newPoolRecorders(run *eventlog.Recorder, pooled bool) *poolRecorders {
	if run == nil {
		return nil
	}
	return &poolRecorders{run: run, pooled: pooled, byID: map[string]*eventlog.Recorder{}}
}

// recorder returns the recorder for one sidecar, creating it on first use.
func (p *poolRecorders) recorder(sidecarID string) *eventlog.Recorder {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, ok := p.byID[sidecarID]
	if !ok {
		rec = p.run.For(sidecarID, "")
		p.byID[sidecarID] = rec
	}
	return rec
}

// status returns the reporter for one sidecar, or nil when nothing is recorded
// or the run is not pooled, leaving the run's own reporter in place.
func (p *poolRecorders) status(sidecarID string) iostream.StatusFunc {
	if p == nil || !p.pooled {
		return nil
	}
	return p.recorder(sidecarID).PerCommand()
}

// commandIDSetter returns what stamps a submitted command's ID onto the closing
// event of the sidecar that runs it, or nil when nothing is recorded.
func (p *poolRecorders) commandIDSetter(sidecarID string) func(string) {
	if p == nil {
		return nil
	}
	if !p.pooled {
		return p.run.SetCommandID
	}
	return p.recorder(sidecarID).SetCommandID
}

// runRecorderSidecarID is the sidecar a run's own events are filed under. A
// pooled run's, the run-wide summary among them, go under none: each pool
// member records the commands it ran, and the summary tallies the whole pool.
// Under the representative's ID that tally would close a run on its row and
// mark it failed for another member's failure.
func runRecorderSidecarID(pooled bool, sidecarID string) string {
	if pooled {
		return ""
	}
	return sidecarID
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

// finishValidate reports the validate outcome and returns execErr.
// notifyFn, when non-nil, is called with the notification title and body;
// pass notify.Send for real desktop notifications, or a capturing closure in tests.
func finishValidate(execErr error, start time.Time, result validate.Result, recorder *eventlog.Recorder, statusFn iostream.StatusFunc, notifyFn func(title, body string)) error {
	elapsed := ui.FormatDuration(time.Since(start))
	level := iostream.LevelDone
	if execErr != nil {
		level = iostream.LevelError
	}
	message := fmt.Sprintf("%d/%d passed  %s", result.Passed, result.Total, elapsed)
	if recorder != nil {
		recorder.Final(level, message, result.Passed, result.Total)
	} else {
		statusFn(level, message)
	}
	if notifyFn != nil {
		verdict := "passed"
		if execErr != nil {
			verdict = "failed"
		}
		notifyFn("chunk validate "+verdict, fmt.Sprintf("%d/%d checks passed · %s", result.Passed, result.Total, elapsed))
	}
	return execErr
}

func validateEnvFlag(envVarsFlag []string) error {
	if _, err := sidecar.ParseEnvPairs(envVarsFlag); err != nil {
		return &userError{msg: fmt.Sprintf("invalid --env value: %s", err), err: err}
	}
	return nil
}

// runValidateViaDaemon delegates a validate run to the chunk daemon and writes
// its captured output to streams.
//
// workDir travels with the request because the daemon cannot work it out: it
// serves every repo on the machine from whatever directory it was launched in,
// so a run that resolved the project from the daemon's cwd would validate that
// repo instead of this one.
//
// image is the sidecar image this process would have booted, sent so the
// daemon's sidecar matches a --no-daemon run's.
//
// The run is never offered to the background: the caller waits for the
// verdict, and only --async (a different endpoint) releases it.
func runValidateViaDaemon(workDir string, args []string, circleCIToken, orgID, image string, streams iostream.Streams) error {
	req := chunkd.ValidateRequest{
		Args:         args,
		ProjectRoot:  workDir,
		WorkDir:      workDir,
		OrgID:        orgID,
		SidecarImage: image,
	}
	// Forward local credentials only over the Unix socket (isolated to the local
	// filesystem). Over TCP the remote daemon runs with its own credentials and
	// can provision its own sidecar when an org ID is provided.
	if chunkd.TCPRemoteAddr() == "" {
		req.CircleCIToken = circleCIToken
		req.Env = os.Environ()
	} else {
		req.OrgID = orgID
	}
	resp, err := chunkd.RunValidate(req)
	if err != nil {
		return fmt.Errorf("daemon validate: %w", err)
	}
	return reportDelegatedValidate(resp, streams)
}

// reportDelegatedValidate writes what the daemon made of a delegated run and
// returns its outcome.
//
// Three shapes arrive here. A task ID means the run was taken into the
// background and nothing has happened yet, so there is no output and no verdict
// to pass on — the answer is collected later with `chunk validate results`. A
// reason with no task ID means the run was offered to the background and held
// here instead, and the reason says why. Neither means the caller never
// offered, and there was no decision to explain.
func reportDelegatedValidate(resp chunkd.ValidateResponse, streams iostream.Streams) error {
	if resp.TaskID != "" {
		streams.ErrPrintf("  %s\n", ui.ErrDim(fmt.Sprintf(
			"validating in the background: %s (%s)", shortTaskID(resp.TaskID), resp.Reason)))
		reportRisk(resp.Risk, streams)
		return nil
	}
	if resp.Reason != "" {
		streams.ErrPrintf("  %s\n", ui.ErrDim("validating now: "+resp.Reason))
	}
	reportRisk(resp.Risk, streams)
	_, _ = streams.Out.Write([]byte(resp.Stdout))
	_, _ = streams.Err.Write([]byte(resp.Stderr))
	if resp.ExitCode != 0 {
		return &silentExitError{code: resp.ExitCode}
	}
	return nil
}

// reportRisk writes the daemon's judgement of the change, when it is worth
// saying.
//
// A low-risk change gets no line. It is the common case, it is what everyone
// expects, and a score printed on every turn is how a number stops being read
// at all. Advice is printed whenever there is any, since advice exists only
// where there is something to act on.
func reportRisk(risk *chunkd.RiskSummary, streams iostream.Streams) {
	if risk == nil {
		return
	}
	if risk.Band != chunkd.BandLow {
		streams.ErrPrintf("  %s\n", ui.ErrDim(risk.String()))
	}
	if risk.Advice != "" {
		streams.ErrPrintf("  %s\n", ui.ErrDim(risk.Advice))
	}
}

// delegateToDaemon hands the run to the chunk daemon when it should be, and
// reports whether it did. A caller told false runs the commands inline.
//
// Async and synchronous delegation are decided together because they are the
// same decision: both give the run to the daemon, and the only difference is
// whether this process waits for the answer. Either can decline — an
// unfingerprintable tree, an old daemon, no daemon at all — and every decline
// means the same thing, which is to run the commands here instead.
func delegateToDaemon(opts *validateOpts, workDir, circleCIToken, orgID, image string, streams iostream.Streams) (bool, error) {
	if opts.async && !opts.noDaemon {
		// A refusal falls through to inline: slower than the caller asked for,
		// but it always tells them the truth about the tree in front of them.
		return tryAsyncDelegate(workDir, circleCIToken, streams)
	}
	if shouldUseDaemon(opts.noDaemon) {
		err := runValidateViaDaemon(workDir, os.Args[1:], circleCIToken, orgID, image, streams)
		// ErrDaemonUnavailable covers two cases: the daemon disappeared between
		// the IsDaemonCompatible check and the POST (connection refused), and the
		// daemon lacks the /validate endpoint because it is from an older build
		// (404). Both fall through to inline execution.
		if !errors.Is(err, chunkd.ErrDaemonUnavailable) {
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
	if !chunkd.IsDaemonCompatible() {
		streams.ErrPrintln(ui.ErrDim("chunk validate: no chunk daemon, running inline"))
		return false, nil
	}
	taskID, err := chunkd.StartAsyncValidate(workDir, os.Args[1:], circleCIToken)
	switch {
	case errors.Is(err, chunkd.ErrAsyncRefused):
		// The daemon's own words: it refuses for more than one reason, and a
		// developer told the wrong one goes looking in the wrong place.
		reason := strings.TrimPrefix(err.Error(), chunkd.ErrAsyncRefused.Error()+": ")
		streams.ErrPrintln(ui.ErrDim("chunk validate: " + reason + ", running inline"))
		return false, nil
	case errors.Is(err, chunkd.ErrDaemonUnavailable):
		streams.ErrPrintln(ui.ErrDim("chunk validate: chunk daemon unavailable, running inline"))
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
	tasks, err := chunkd.CollectValidateResults(workDir)
	if err != nil {
		// Nothing to collect is the common case, not a failure: no daemon means
		// no background runs. Reporting it as an error on a hook path would put
		// noise in front of the agent on every turn.
		if errors.Is(err, chunkd.ErrDaemonUnavailable) {
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
func printResults(streams iostream.Streams, tasks []chunkd.TaskState) {
	for _, t := range tasks {
		// A live-tree run the tree has moved past reports no verdict, only that it
		// was discarded. The daemon strips the exit code and output of such a task
		// precisely so this cannot be read as a pass or a failure — the run
		// described code that has since changed, and the honest thing to say is
		// that there is no answer.
		//
		// Said out loud rather than swallowed because the usual cause is the run
		// itself: a command that writes coverage output or regenerates a golden
		// moves a path git is watching and invalidates its own result. Silence
		// there is indistinguishable from no run happening, which leaves a whole
		// class of projects getting nothing with nothing to explain it.
		if t.Stale && !t.Snapshot {
			streams.Printf("chunk validate discarded a background run (%s): the working tree changed while it ran, so its result no longer describes your code\n", shortTaskID(t.ID))
			streams.Printf("  if this repeats, the run is probably changing the tree itself; gitignore what it writes\n")
			continue
		}
		// A snapshot-backed run keeps its verdict when the tree has moved past it,
		// because the answer is still exactly true about the state it ran against.
		// Saying which state that was is the difference between useful and
		// misleading: the agent needs to know this is not a verdict on what it has
		// since written.
		as := ""
		if t.Stale {
			as = " — for the code as it was when that turn ended; the tree has changed since"
		}
		if t.Passed() {
			streams.Printf("chunk validate passed in the background (%s)%s\n", shortTaskID(t.ID), as)
			continue
		}
		streams.Printf("chunk validate FAILED in the background (%s), exit status %d%s\n", shortTaskID(t.ID), t.ExitCode, as)
		if t.Output != "" {
			streams.Printf("%s\n", strings.TrimRight(t.Output, "\n"))
		}
	}
}

// validateEarlyExits handles --list, --mark-remote, missing config, --dry-run.
// Returns (cfg, done=true, err) to stop, or (cfg, false, nil) to continue.
func validateEarlyExits(opts *validateOpts, name, workDir string, streams iostream.Streams, statusFn iostream.StatusFunc) (*config.ProjectConfig, bool, error) {
	if opts.list {
		return nil, true, runValidateList(workDir, opts.jsonOut, streams, statusFn)
	}
	if opts.jsonOut {
		return nil, true, fmt.Errorf("--json requires --list")
	}
	if opts.markRemote {
		return nil, true, runMarkRemote(workDir, name, streams)
	}
	return prepareValidateConfig(workDir, opts, name, statusFn)
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
	// projectRoot is the repository a submitted command is registered under. It
	// is the git top-level, because the daemon buckets commands by canonicalised
	// project root and discovers projects from the git-rooted breadcrumb: a
	// registration under anything else is output the dashboard cannot reach.
	projectRoot string,
	envVars map[string]string,
	recorders *poolRecorders,
	statusFn iostream.StatusFunc,
	streams iostream.Streams,
) (validate.Result, error) {
	if len(plan.RemoteCommands) > 0 && pool == nil {
		return validate.Result{}, errors.New("remote validation requires a sidecar pool")
	}
	result := validate.Result{Total: len(plan.RemoteCommands) + len(plan.LocalCommands)}

	// Local autofix commands go first, so what the gates check is the tree as
	// the formatter leaves it. The remote batch syncs the tree when it starts:
	// run after it, a formatter's rewrites would reach no gate at all.
	autofix, local := splitAutofix(plan.LocalCommands)
	var autofixErr error
	if len(autofix) > 0 {
		var passed int
		passed, autofixErr = runLocalCommands(ctx, autofix, localWorkDir, envVars, statusFn, streams)
		result.Passed += passed
	}

	var remote validate.DistributedRunResult
	if len(plan.RemoteCommands) > 0 {
		remote = validate.RunDistributed(ctx, plan.RemoteCommands, validate.DistributedRunOptions[*sidecar.PoolEntry]{
			Parallelism: plan.PoolSize,
			Acquire:     pool.Acquire,
			Release:     pool.Release,
			WorkerName: func(entry *sidecar.PoolEntry) string {
				return "sidecar " + entry.ID
			},
			WorkerStatus: func(entry *sidecar.PoolEntry) iostream.StatusFunc {
				return recorders.status(entry.ID)
			},
			Run: func(ctx context.Context, entry *sidecar.PoolEntry, command config.Command, status iostream.StatusFunc, commandStreams iostream.Streams) validate.DistributedJobResult {
				return runPooledValidateCommand(ctx, entry, command, rc.CircleCIToken, localWorkDir, projectRoot, envVars, recorders.commandIDSetter(entry.ID), status, commandStreams)
			},
			Status:  statusFn,
			Streams: streams,
		})
		renderDistributedOutput(remote.Output, streams)
	}

	result.Passed += remote.Passed
	if len(local) == 0 {
		return result, errors.Join(autofixErr, remote.Err)
	}

	passed, err := runLocalCommands(ctx, local, localWorkDir, envVars, statusFn, streams)
	result.Passed += passed
	return result, errors.Join(autofixErr, remote.Err, err)
}

// splitAutofix separates autofix commands from the rest, keeping the configured
// order within each.
func splitAutofix(commands []config.Command) (autofix, rest []config.Command) {
	for _, c := range commands {
		if c.Role == config.RoleAutofix {
			autofix = append(autofix, c)
		} else {
			rest = append(rest, c)
		}
	}
	return autofix, rest
}

// runLocalCommands runs commands in order in the local working tree, returning
// how many passed.
func runLocalCommands(ctx context.Context, commands []config.Command, workDir string, envVars map[string]string, statusFn iostream.StatusFunc, streams iostream.Streams) (int, error) {
	statusFn(iostream.LevelInfo, fmt.Sprintf("running locally: %s", commandNames(commands)))
	localCfg := &config.ProjectConfig{Commands: commands}
	r, err := mapValidateError(validate.RunAll(ctx, workDir, localCfg, envVars, statusFn, streams))
	return r.Passed, err
}

func runPooledValidateCommand(
	ctx context.Context,
	entry *sidecar.PoolEntry,
	command config.Command,
	token, localWorkDir, projectRoot string,
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
		OnSubmitted: onValidateCommandSubmitted(entry.ID, projectRoot, command.Name, setCommandID),
	}
	execFn, dest, err := target.ReadyExecRunner(ctx, localWorkDir, remoteExecEnv(token, envVars), streams)
	if err != nil {
		var workspaceErr *sidecar.WorkspaceNotFoundError
		if errors.As(err, &workspaceErr) {
			err = missingWorkspace(entry.ID, entry.RepoPath, command.Name, err)
		} else {
			err = unreachableSidecar(entry.ID, command.Name, err)
		}
		// The command never reached RunRemoteStreamedResult, which is what reports
		// a failure. Without a line here nothing closes the run RunDistributed
		// opened on this sidecar, so a reader shows it running until it goes quiet.
		statusFn(iostream.LevelError, fmt.Sprintf("%s  failed on sidecar %s: %v", command.Name, entry.ID, err))
		return validate.DistributedJobResult{Err: err}
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
		chunkd.RegisterCommand(chunkd.CommandReg{
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
	// of a first run, not something to headline.
	statusFn := newStatusFunc(streams)
	statusFn(iostream.LevelInfo, "no active sidecar; creating one")
	resolvedOrgID, err := resolveOrgID(orgID, workDir, orgPicker(ctx, client, tokenSource, streams))
	if err != nil {
		return false, image, err
	}
	// No image configured means no snapshot was ever recorded for this repo.
	// Rather than boot the bare default image, look for one of the org's
	// snapshots that fits this repo; autoSelectSnapshotImage returns "" (the
	// old behaviour) when none does.
	// A passed-in image always comes from the project config; one chosen here
	// is chunk's pick and gets no source.
	var imageSource string
	if image != "" {
		imageSource = config.SourceProjectConfig
	} else {
		image = autoSelectSnapshotImage(ctx, client, resolvedOrgID, workDir, newStatusFunc(streams), streams)
	}
	sandboxName := sidecarAutoName(ctx, workDir)
	sc, err := sidecar.Create(ctx, client, resolvedOrgID, sandboxName, image, "")
	if err != nil {
		if authErr := cannotCreateSidecar(resolvedOrgID, orgSource(orgID, workDir), err); authErr != nil {
			return false, image, authErr
		}
		if rejectErr := sidecarCreateRejected("Could not create a sidecar.", resolvedOrgID, orgSource(orgID, workDir), image, imageSource, err); rejectErr != nil {
			return false, image, rejectErr
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

func mapValidateError(r validate.Result, err error) (validate.Result, error) {
	if errors.Is(err, validate.ErrNotConfigured) {
		return r, &userError{
			msg:        msgValidateNotConfigured,
			blocked:    true,
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
