package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

const (
	defaultIntentFile   = "INTENT.md"
	defaultWorkTimeout  = 30 * time.Minute
	factoryWorkerPool   = "factory-worker"
	factoryCheckPool    = "factory"
	defaultFactoryPool  = 5
	maxWorkerDiffBytes  = 16 * 1024 * 1024
	factoryAttemptLabel = "factory: attempt %d"
)

// factoryOpts holds the flags of a factory run.
type factoryOpts struct {
	maxAttempts   int
	failOn        string
	parallelism   int
	reviewsDir    string
	reviewsSet    bool // --reviews was passed, so a missing directory is an error
	orgID         string
	image         string
	model         string
	workTimeout   time.Duration
	reviewTimeout time.Duration
	keepSidecars  bool
}

func newFactoryCmd() *cobra.Command {
	var opts factoryOpts

	cmd := &cobra.Command{
		Use:   "factory [intent-file]",
		Short: "Implement an intent with Claude, looping on validate and review feedback",
		Long: `Implement an intent file (default INTENT.md) with Claude Code on a sidecar,
then check the result: 'chunk validate' commands and every review prompt in
.chunk/reviews run in parallel on a sidecar pool. Failed validation and any
review whose verdict fails (see --fail-on) go back to Claude, until an attempt
passes or --max-attempts is reached.

The work happens in a new git worktree under .chunk/worktrees, on its own
branch, with one commit per attempt. Your working tree is never touched.`,
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			intentFile := defaultIntentFile
			if len(args) == 1 {
				intentFile = args[0]
			}
			opts.reviewsSet = cmd.Flags().Changed("reviews")
			return runFactory(cmd, intentFile, opts)
		},
	}

	cmd.Flags().IntVar(&opts.maxAttempts, "max-attempts", factory.DefaultMaxAttempts, "maximum work rounds")
	cmd.Flags().StringVar(&opts.failOn, "fail-on", string(factory.VerdictBlocked), "least severe review verdict that fails an attempt: blocked or warn")
	cmd.Flags().IntVar(&opts.parallelism, "parallelism", defaultFactoryPool, "maximum sidecars for validation and review")
	cmd.Flags().StringVar(&opts.reviewsDir, "reviews", review.DefaultDir, "directory of review prompts")
	cmd.Flags().StringVar(&opts.orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&opts.image, "image", "", "Snapshot image ID (default: validation.sidecarImage from config)")
	cmd.Flags().StringVar(&opts.model, "model", "", "Claude model for the worker and reviews (default: Claude Code's default)")
	cmd.Flags().DurationVar(&opts.workTimeout, "timeout", defaultWorkTimeout, "max time for each work round")
	cmd.Flags().DurationVar(&opts.reviewTimeout, "review-timeout", review.DefaultTimeout, "max time for each review")
	cmd.Flags().BoolVar(&opts.keepSidecars, "keep-sidecars", false, "keep the run's sidecars instead of deleting them when it ends")
	return cmd
}

func runFactory(cmd *cobra.Command, intentFile string, opts factoryOpts) error {
	ctx := cmd.Context()
	streams := iostream.FromCmd(cmd)
	statusFn := newStatusFunc(streams)

	failOn := factory.Verdict(opts.failOn)
	if failOn != factory.VerdictBlocked && failOn != factory.VerdictWarn {
		return newUserError(fmt.Sprintf("Unknown --fail-on value %q.", opts.failOn)).
			withCode("command.invalid_args").
			withSuggestion("Pass --fail-on blocked or --fail-on warn.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repoRoot := gitutil.TopLevelCtx(ctx, cwd)
	if repoRoot == "" {
		return newUserError("chunk factory must run inside a git repository.").
			withSuggestion(suggestionGitRepo).
			withExitCode(ExitBadArgs).
			withoutDetail()
	}

	in, err := loadFactoryInputs(repoRoot, intentFile, opts)
	if err != nil {
		return err
	}
	cfg, prompts, plan, reviewsDir := in.cfg, in.prompts, in.plan, in.reviewsDir

	rc, _ := config.Resolve("", "", insecureStorageFlag(cmd))
	cred, credSource, credErr := reviewCredential(rc)
	if credErr != nil {
		if err := setupClaudeCredential(ctx, cmd, streams, rc, false, credErr); err != nil {
			return err
		}
		rc, _ = config.Resolve("", "", insecureStorageFlag(cmd))
		if cred, credSource, credErr = reviewCredential(rc); credErr != nil {
			return credErr
		}
	}
	client, err := ensureCircleCIClient(ctx, cmd, rc, streams, ui.PromptHidden)
	if err != nil {
		return err
	}
	orgID := opts.orgID
	if orgID == "" {
		orgID = cfg.OrgID
	}
	orgID, err = resolveOrgID(orgID, repoRoot, orgPicker(ctx, client, rc.CircleCITokenSource, streams))
	if err != nil {
		return err
	}
	image := opts.image
	if image == "" {
		image = resolveImage("", cfg)
	}
	envVars, err := resolveEnvVars(ctx, repoRoot, "", nil)
	if err != nil {
		return err
	}

	wt, err := factory.CreateWorktree(ctx, repoRoot, time.Now())
	if err != nil {
		return &userError{msg: "Could not create a worktree for the run.", err: err}
	}
	statusFn(iostream.LevelStep, fmt.Sprintf("Working in %s on branch %s", wt.Dir, wt.Branch))
	if len(prompts) == 0 {
		statusFn(iostream.LevelWarn, fmt.Sprintf("no review prompts in %s; only validation will check the work", reviewsDir))
	}

	claudeOpts := review.Options{
		Credential: cred,
		BaseURL:    rc.AnthropicBaseURL,
		Model:      opts.model,
		Timeout:    opts.reviewTimeout,
		JSONSchema: factory.ReviewSchema,
		StatusFn:   statusFn,
	}
	r := &factoryRun{
		client:   client,
		wt:       wt,
		opts:     opts,
		orgID:    orgID,
		image:    image,
		repoPath: sidecar.DefaultWorkspace(filepath.Base(repoRoot)),
		claude:   claudeOpts,
		intent:   in.intent,
		prompts:  prompts,
		plan:     plan,
		autofix:  in.autofix,
		rc:       rc,
		envVars:  envVars,
		status:   statusFn,
		streams:  streams,
	}
	defer r.cleanup(ctx)

	attempts, runErr := factory.Run(ctx, factory.Options{
		Intent:      in.intent,
		MaxAttempts: opts.maxAttempts,
		FailOn:      failOn,
		Work:        r.work,
		Check:       r.check,
		Status:      statusFn,
	})
	printFactorySummary(streams, wt, attempts, failOn)

	switch {
	case errors.Is(runErr, review.ErrClaudeMissing):
		return &userError{
			msg:        "Claude Code is not installed on the sidecars.",
			suggestion: "Install it in the sidecar image (see 'chunk sidecar env build') and pass --image, or set validation.sidecarImage.",
			hideDetail: true,
			err:        runErr,
		}
	case errors.Is(runErr, review.ErrCredentialRejected):
		return credentialRejected(cred, credSource, rc.AnthropicBaseURL, runErr)
	case errors.Is(runErr, context.Canceled):
		return &userError{msg: "The factory run was interrupted.", err: runErr, hideDetail: true}
	case errors.Is(runErr, errWorkerNoChanges):
		return &userError{
			msg:        "Claude finished without changing anything.",
			suggestion: "Make the intent more specific about what to build, then run again.",
			err:        runErr,
			hideDetail: true,
		}
	case errors.Is(runErr, factory.ErrNotConverged):
		return &userError{
			msg:        fmt.Sprintf("Checks still failing after %d attempt(s).", len(attempts)),
			suggestion: fmt.Sprintf("Inspect the work in %s, or rerun with more --max-attempts.", wt.Dir),
			errMsg:     "factory did not converge",
			hideDetail: true,
		}
	case runErr != nil:
		if authErr := notAuthorized("create sidecars", rc.CircleCITokenSource, runErr); authErr != nil {
			return authErr
		}
		return &userError{msg: "The factory run stopped.", err: runErr}
	}
	return nil
}

// factoryInputs is what a run reads from the developer's tree before any
// sidecar boots.
type factoryInputs struct {
	intent     string
	cfg        *config.ProjectConfig
	prompts    []review.Prompt
	reviewsDir string
	plan       validate.Plan
	autofix    []config.Command
}

// loadFactoryInputs reads the intent, project config and review prompts.
// Config and prompts come from the developer's tree, not the worktree, so
// uncommitted edits to them apply to the run.
func loadFactoryInputs(repoRoot, intentFile string, opts factoryOpts) (factoryInputs, error) {
	intent, err := os.ReadFile(intentFile)
	if err != nil {
		return factoryInputs{}, &userError{
			msg:        fmt.Sprintf("Could not read %s.", intentFile),
			suggestion: "Describe what to build in INTENT.md, or pass the path of an intent file.",
			err:        err,
		}
	}
	if strings.TrimSpace(string(intent)) == "" {
		return factoryInputs{}, newUserError(fmt.Sprintf("%s is empty.", intentFile)).withExitCode(ExitBadArgs).withoutDetail()
	}

	cfg, err := config.LoadProjectConfig(repoRoot)
	if err != nil {
		return factoryInputs{}, &userError{msg: msgValidateNotConfigured, suggestion: suggestionRunInit, err: err}
	}
	reviewsDir := opts.reviewsDir
	if !filepath.IsAbs(reviewsDir) {
		reviewsDir = filepath.Join(repoRoot, reviewsDir)
	}
	prompts, err := review.LoadPrompts(reviewsDir)
	// Only the default directory may be missing or empty: a path someone
	// typed that holds no prompts is a mistake, not a run without reviews.
	missing := errors.Is(err, review.ErrNoPrompts) || errors.Is(err, os.ErrNotExist)
	if err != nil && (!missing || opts.reviewsSet) {
		return factoryInputs{}, &userError{msg: fmt.Sprintf("Could not read review prompts from %s.", reviewsDir), err: err}
	}
	plan := validate.PlanCommands(cfg.Commands, validate.PlacementConfigured, opts.parallelism)
	// Autofix commands run on their own before the check pool syncs, so the
	// gates and reviewers see the formatted tree; see factoryRun.runAutofix.
	autofix, local := splitAutofix(plan.LocalCommands)
	plan.LocalCommands = local
	if len(prompts) == 0 && len(cfg.Commands) == 0 {
		return factoryInputs{}, &userError{
			msg:        "Nothing to check the work with.",
			suggestion: fmt.Sprintf("Configure validate commands with 'chunk init', or add review prompts to %s.", review.DefaultDir),
			hideDetail: true,
		}
	}

	return factoryInputs{
		intent:     string(intent),
		cfg:        cfg,
		prompts:    prompts,
		reviewsDir: reviewsDir,
		plan:       plan,
		autofix:    autofix,
	}, nil
}

// factoryRun holds what the work and check steps of one run share.
type factoryRun struct {
	client   *circleci.Client
	wt       factory.Worktree
	opts     factoryOpts
	orgID    string
	image    string
	repoPath string
	claude   review.Options
	intent   string
	prompts  []review.Prompt
	plan     validate.Plan
	autofix  []config.Command
	rc       config.ResolvedConfig
	envVars  map[string]string
	status   iostream.StatusFunc
	streams  iostream.Streams

	// The latest pool of each kind, destroyed when the run ends. Each round
	// opens its pools anew with the same names, which reuses the sidecars
	// and syncs them with the worktree. A pool is recorded as soon as it
	// exists, so one that fails to sync is still cleaned up.
	workerPool *sidecar.Pool
	checkPool  *sidecar.Pool
}

// openPool opens a named pool synced with the worktree, records it in slot,
// and waits until every member is ready.
func (r *factoryRun) openPool(ctx context.Context, name string, size int, slot **sidecar.Pool) (*sidecar.Pool, error) {
	pool, err := sidecar.NewPool(ctx, r.client, sidecar.PoolOptions{
		Size:     size,
		Name:     name,
		OrgID:    r.orgID,
		Image:    r.image,
		WorkDir:  r.wt.Dir,
		RepoPath: r.repoPath,
	}, r.status)
	if err != nil {
		return nil, fmt.Errorf("prepare %s pool: %w", name, err)
	}
	*slot = pool
	if err := review.WaitReady(ctx, pool.WaitSynced); err != nil {
		pool.Close(ctx)
		return nil, err
	}
	return pool, nil
}

func (r *factoryRun) closePool(ctx context.Context, pool *sidecar.Pool) {
	closeCtx, cancel := context.WithTimeout(ctx, poolCloseTimeout)
	defer cancel()
	pool.Close(closeCtx)
}

// work runs the worker agent on its sidecar and commits what it changed to the
// worktree.
func (r *factoryRun) work(ctx context.Context, attempt int, prompt string) error {
	base, err := factory.HeadCommit(ctx, r.wt.Dir)
	if err != nil {
		return err
	}

	pool, err := r.openPool(ctx, factoryWorkerPool, 1, &r.workerPool)
	if err != nil {
		return err
	}
	defer r.closePool(ctx, pool)
	entry, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire worker sidecar: %w", err)
	}
	defer pool.Release(entry)

	r.status(iostream.LevelInfo, fmt.Sprintf("claude is working on %s...", entry.ID))
	workCtx, cancel := context.WithTimeout(ctx, r.opts.workTimeout)
	defer cancel()
	// The output streams to the terminal; its tail is kept to recognise a
	// rejected credential.
	var stdout, stderr tailBuffer
	code, err := review.ClientExec(workCtx, entry, factory.WorkScript(entry.RepoPath, prompt, r.opts.model), review.ClaudeEnv(r.claude),
		func(stream string, data []byte) {
			_, _ = r.streams.Err.Write(data)
			if stream == circleci.StreamStderr {
				stderr.Write(data)
			} else {
				stdout.Write(data)
			}
		})
	switch {
	case workCtx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("worker timed out after %s", r.opts.workTimeout)
	case err != nil:
		return fmt.Errorf("run worker: %w", err)
	case code == factory.ExitClaudeMissing:
		return review.ErrClaudeMissing
	case code != 0 && review.CredentialRejected(stdout.String(), stderr.String()):
		return review.ErrCredentialRejected
	case code != 0:
		return fmt.Errorf("worker claude exited %d", code)
	}

	var patch bytes.Buffer
	var diffErr tailBuffer
	overflow := false
	code, err = review.ClientExec(ctx, entry, factory.DiffScript(entry.RepoPath, base), nil, func(stream string, data []byte) {
		switch {
		case stream == circleci.StreamStderr:
			diffErr.Write(data)
		case patch.Len()+len(data) > maxWorkerDiffBytes:
			overflow = true
		default:
			patch.Write(data)
		}
	})
	if err != nil {
		return fmt.Errorf("read worker changes: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("read worker changes: git exited %d: %s", code, strings.TrimSpace(diffErr.String()))
	}
	// A cut-off patch could still apply cleanly with files missing.
	if overflow {
		return fmt.Errorf("worker changes exceed %d MiB; check for build output missing from .gitignore", maxWorkerDiffBytes>>20)
	}
	if err := factory.ApplyPatch(ctx, r.wt.Dir, patch.String()); err != nil {
		return fmt.Errorf("apply worker changes: %w", err)
	}
	err = factory.CommitAll(ctx, r.wt.Dir, fmt.Sprintf(factoryAttemptLabel, attempt))
	if errors.Is(err, factory.ErrNothingToCommit) {
		// Checking the untouched starting tree could pass a run that built
		// nothing. A later attempt changing nothing is only a warning: the
		// earlier work is still there to check.
		if base == r.wt.Base {
			return errWorkerNoChanges
		}
		r.status(iostream.LevelWarn, "claude made no changes this attempt")
		return nil
	}
	if err != nil {
		return err
	}
	r.status(iostream.LevelDone, fmt.Sprintf("committed attempt %d to %s", attempt, r.wt.Branch))
	return nil
}

// check runs local autofix commands and commits their output, then validates
// and reviews the result concurrently on one pool.
func (r *factoryRun) check(ctx context.Context, _ int) (factory.Check, error) {
	autofixPassed, autofixOut, err := r.runAutofix(ctx)
	if err != nil {
		return factory.Check{}, err
	}

	size := min(max(r.opts.parallelism, 1), len(r.plan.RemoteCommands)+len(r.prompts))
	var pool *sidecar.Pool
	if size > 0 {
		if pool, err = r.openPool(ctx, factoryCheckPool, size, &r.checkPool); err != nil {
			return factory.Check{}, err
		}
		defer r.closePool(ctx, pool)
	}

	var (
		wg        sync.WaitGroup
		check     factory.Check
		validErr  error
		results   []review.Result
		reviewErr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		check.ValidatePassed, check.ValidateOutput, validErr = r.validate(ctx, pool)
	}()
	go func() {
		defer wg.Done()
		results, reviewErr = r.review(ctx, pool)
	}()
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return factory.Check{}, err
	}
	check.ValidatePassed = check.ValidatePassed && autofixPassed
	check.ValidateOutput = autofixOut + check.ValidateOutput
	if validErr != nil {
		return factory.Check{}, validErr
	}
	if reviewErr != nil {
		return factory.Check{}, reviewErr
	}

	var broken []error
	for _, res := range results {
		rr := factory.ReviewResult{Name: res.Prompt, Err: res.Error}
		if rr.Err == "" {
			var err error
			if rr.Verdict, rr.Feedback, err = factory.ParseReview(res.Output); err != nil {
				rr.Err = err.Error()
			}
		}
		if rr.Err != "" {
			broken = append(broken, fmt.Errorf("review %s: %s", rr.Name, rr.Err))
		}
		check.Reviews = append(check.Reviews, rr)
		r.status(iostream.LevelInfo, fmt.Sprintf("review %s: %s", rr.Name, reviewLabel(rr)))
		// The feedback is what the next attempt is asked to fix, so the log
		// shows why every retry happened, not only the last one.
		if rr.Verdict != factory.VerdictApproved && rr.Feedback != "" {
			r.streams.ErrPrintf("%s\n", indent(rr.Feedback, "    "))
		}
	}
	// A review that could not run is nothing the worker can fix, so it stops
	// the run rather than spending an attempt on it.
	if len(broken) > 0 {
		return factory.Check{}, fmt.Errorf("reviews could not run: %w", errors.Join(broken...))
	}
	return check, nil
}

// indent prefixes every line of s with prefix.
func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// runAutofix runs the local autofix commands in the worktree and commits what
// they change, before the check pool syncs, so the gates and reviewers check
// the tree the branch ends up with. A failing command is feedback, like a
// failing gate.
func (r *factoryRun) runAutofix(ctx context.Context) (bool, string, error) {
	if len(r.autofix) == 0 {
		return true, "", nil
	}
	out := &syncBuffer{}
	streams := iostream.Streams{Out: io.MultiWriter(r.streams.Err, out), Err: io.MultiWriter(r.streams.Err, out)}
	_, runErr := runLocalCommands(ctx, r.autofix, r.wt.Dir, r.envVars, newStatusFunc(streams), streams)
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	if err := factory.CommitAll(ctx, r.wt.Dir, "factory: autofix"); err != nil && !errors.Is(err, factory.ErrNothingToCommit) {
		return false, "", err
	}
	if runErr != nil {
		_, _ = fmt.Fprintf(out, "\n%v\n", runErr)
		return false, out.String(), nil
	}
	return true, out.String(), nil
}

// validate runs the configured commands against the worktree and returns
// whether they passed and their combined output. A failing command is
// feedback; a sidecar that could not run one, reported as a *userError, stops
// the run instead, since the worker cannot fix it.
func (r *factoryRun) validate(ctx context.Context, pool *sidecar.Pool) (bool, string, error) {
	if len(r.plan.RemoteCommands)+len(r.plan.LocalCommands) == 0 {
		return true, "", nil
	}
	out := &syncBuffer{}
	streams := iostream.Streams{Out: io.MultiWriter(r.streams.Err, out), Err: io.MultiWriter(r.streams.Err, out)}
	_, err := runValidationPlan(ctx, pool, r.plan, r.rc, r.wt.Dir, r.wt.Dir, r.envVars, nil, newStatusFunc(streams), streams)
	if ctx.Err() != nil {
		return false, out.String(), ctx.Err()
	}
	if infraErr, ok := errors.AsType[*userError](err); ok {
		return false, out.String(), infraErr
	}
	if err != nil {
		_, _ = fmt.Fprintf(out, "\n%v\n", err)
		return false, out.String(), nil
	}
	return true, out.String(), nil
}

// review runs every review prompt, wrapped with the intent and verdict
// instructions.
func (r *factoryRun) review(ctx context.Context, pool *sidecar.Pool) ([]review.Result, error) {
	if len(r.prompts) == 0 {
		return nil, nil
	}
	wrapped := make([]review.Prompt, len(r.prompts))
	for i, p := range r.prompts {
		wrapped[i] = review.Prompt{Name: p.Name, Body: factory.ReviewPrompt(p.Body, r.intent, r.wt.Base)}
	}
	return review.RunPass(ctx, pool.Acquire, pool.Release, review.ClientExec, wrapped, r.claude)
}

// cleanup deletes the run's sidecars unless --keep-sidecars was passed. Pool
// state lives in the worktree, so kept sidecars are reused only by a later run
// in the same worktree.
func (r *factoryRun) cleanup(ctx context.Context) {
	if r.opts.keepSidecars {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	for name, pool := range map[string]*sidecar.Pool{factoryWorkerPool: r.workerPool, factoryCheckPool: r.checkPool} {
		if pool == nil {
			continue
		}
		if err := pool.Destroy(cleanupCtx); err != nil {
			r.status(iostream.LevelWarn, fmt.Sprintf("could not delete %s sidecars: %v", name, err))
		}
	}
}

func reviewLabel(r factory.ReviewResult) string {
	if r.Err != "" {
		return "failed: " + r.Err
	}
	return string(r.Verdict)
}

// printFactorySummary writes where the work is and how each attempt went.
func printFactorySummary(streams iostream.Streams, wt factory.Worktree, attempts []factory.Attempt, failOn factory.Verdict) {
	streams.Printf("Worktree: %s\nBranch:   %s\n\n", wt.Dir, wt.Branch)
	for _, a := range attempts {
		result := "failed"
		if a.Passed {
			result = "passed"
		}
		validation := "passed"
		if !a.Check.ValidatePassed {
			validation = "failed"
		}
		streams.Printf("Attempt %d: %s (validate %s)\n", a.N, result, validation)
		for _, rv := range a.Check.Reviews {
			streams.Printf("  %-20s %s\n", rv.Name, reviewLabel(rv))
		}
	}
	if len(attempts) > 0 && !attempts[len(attempts)-1].Passed {
		if fb := factory.Feedback(attempts[len(attempts)-1].Check, failOn); fb != "" {
			streams.Printf("\nOutstanding feedback:\n\n%s\n", fb)
		}
	}
}

// errWorkerNoChanges is returned when the first attempt leaves the worktree
// as it started.
var errWorkerNoChanges = errors.New("worker made no changes")

// tailBufferSize is how much of a stream a tailBuffer keeps.
const tailBufferSize = 8 * 1024

// tailBuffer keeps the last tailBufferSize bytes written to it.
type tailBuffer struct {
	buf []byte
}

func (b *tailBuffer) Write(p []byte) {
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - tailBufferSize; over > 0 {
		b.buf = b.buf[over:]
	}
}

func (b *tailBuffer) String() string { return string(b.buf) }

// syncBuffer is a bytes.Buffer safe for the concurrent writes of parallel
// validate commands.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
