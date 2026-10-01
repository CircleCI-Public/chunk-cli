package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

const (
	defaultIntentFile  = "INTENT.md"
	defaultWorkTimeout = 30 * time.Minute
	factoryWorkerPool  = "factory-worker"
	factoryReviewPool  = "factory-review"
	maxWorkerDiffBytes = 16 * 1024 * 1024
)

// errWorkerNoChanges is returned when the first attempt leaves the worktree
// as it started, which reviewers could otherwise approve.
var errWorkerNoChanges = errors.New("worker made no changes")

// factoryOpts holds the flags of a factory run.
type factoryOpts struct {
	maxAttempts   int
	failOn        string
	parallelism   int
	reviewsDir    string
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
		Short: "Implement an intent with Claude, looping on review feedback",
		Long: `Implement an intent file (default INTENT.md) with Claude Code on a sidecar,
then run every review prompt in .chunk/reviews against the result on a sidecar
pool. Reviews whose verdict fails (see --fail-on) go back to Claude, until an
attempt passes or --max-attempts is reached.

The work happens in a new git worktree under .chunk/worktrees, on its own
branch, with one commit per attempt. Your working tree is never touched.`,
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			intentFile := defaultIntentFile
			if len(args) == 1 {
				intentFile = args[0]
			}
			return runFactory(cmd, intentFile, cmd.Flags().Changed("reviews"), opts)
		},
	}

	cmd.Flags().IntVar(&opts.maxAttempts, "max-attempts", factory.DefaultMaxAttempts, "maximum work rounds")
	cmd.Flags().StringVar(&opts.failOn, "fail-on", string(factory.VerdictBlocked), "least severe review verdict that fails an attempt: blocked or warn")
	cmd.Flags().IntVar(&opts.parallelism, "parallelism", 5, "maximum review sidecars (0: one per prompt)")
	cmd.Flags().StringVar(&opts.reviewsDir, "reviews", review.DefaultDir, "directory of review prompts")
	cmd.Flags().StringVar(&opts.orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&opts.image, "image", "", "Snapshot image ID (default: validation.sidecarImage from config)")
	cmd.Flags().StringVar(&opts.model, "model", "", "Claude model for the worker and reviews (default: Claude Code's default)")
	cmd.Flags().DurationVar(&opts.workTimeout, "timeout", defaultWorkTimeout, "max time for each work round")
	cmd.Flags().DurationVar(&opts.reviewTimeout, "review-timeout", review.DefaultTimeout, "max time for each review")
	cmd.Flags().BoolVar(&opts.keepSidecars, "keep-sidecars", false, "keep the run's sidecars instead of deleting them when it ends")
	return cmd
}

func runFactory(cmd *cobra.Command, intentFile string, reviewsExplicit bool, opts factoryOpts) error {
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
	intent, err := readIntent(intentFile)
	if err != nil {
		return err
	}
	// Config and prompts come from the developer's tree, not the worktree,
	// so uncommitted edits to them apply to the run.
	reviewsDir := opts.reviewsDir
	if !filepath.IsAbs(reviewsDir) {
		reviewsDir = filepath.Join(repoRoot, reviewsDir)
	}
	prompts, err := loadReviewPrompts(reviewsDir, reviewsExplicit)
	if err != nil {
		return err
	}

	target, err := resolveClaudePool(ctx, cmd, streams, repoRoot, opts.orgID, opts.image, false)
	if err != nil {
		return err
	}
	rc := target.rc

	wt, err := factory.CreateWorktree(ctx, repoRoot, time.Now())
	if err != nil {
		return &userError{msg: "Could not create a worktree for the run.", err: err}
	}
	statusFn(iostream.LevelStep, fmt.Sprintf("Working in %s on branch %s", wt.Dir, wt.Branch))

	r := &factoryRun{
		target:   target,
		wt:       wt,
		opts:     opts,
		repoPath: sidecar.DefaultWorkspace(filepath.Base(repoRoot)),
		intent:   intent,
		prompts:  prompts,
		worker: review.Options{
			Credential: target.cred, BaseURL: rc.AnthropicBaseURL, Model: opts.model,
			Timeout: opts.workTimeout, AllowEdits: true,
		},
		reviewer: review.Options{
			Credential: target.cred, BaseURL: rc.AnthropicBaseURL, Model: opts.model,
			Timeout: opts.reviewTimeout, JSONSchema: factory.ReviewSchema, StatusFn: statusFn,
		},
		status:  statusFn,
		streams: streams,
	}
	defer r.cleanup(ctx)

	attempts, runErr := factory.Run(ctx, factory.Options{
		Intent:      intent,
		MaxAttempts: opts.maxAttempts,
		FailOn:      failOn,
		Work:        r.work,
		Review:      r.review,
		Status:      statusFn,
	})
	printFactorySummary(streams, wt, attempts)

	if err := claudeRunError(runErr, target.cred, target.credSource, rc.AnthropicBaseURL); err != nil {
		return err
	}
	switch {
	case errors.Is(runErr, errWorkerNoChanges):
		return &userError{
			msg:        "Claude finished without changing anything.",
			suggestion: "Make the intent more specific about what to build, then run again.",
			err:        runErr,
			hideDetail: true,
		}
	case errors.Is(runErr, factory.ErrNotConverged):
		return &userError{
			msg:        fmt.Sprintf("Reviews still failing after %d attempt(s).", len(attempts)),
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

func readIntent(path string) (string, error) {
	intent, err := os.ReadFile(path)
	if err != nil {
		return "", &userError{
			msg:        fmt.Sprintf("Could not read %s.", path),
			suggestion: "Describe what to build in INTENT.md, or pass the path of an intent file.",
			err:        err,
		}
	}
	if strings.TrimSpace(string(intent)) == "" {
		return "", newUserError(fmt.Sprintf("%s is empty.", path)).withExitCode(ExitBadArgs).withoutDetail()
	}
	return string(intent), nil
}

// factoryRun holds what the work and review steps of one run share.
type factoryRun struct {
	target   claudePool
	wt       factory.Worktree
	opts     factoryOpts
	repoPath string
	intent   string
	prompts  []review.Prompt
	worker   review.Options
	reviewer review.Options
	status   iostream.StatusFunc
	streams  iostream.Streams

	// The latest pool of each kind, destroyed when the run ends. Each round
	// opens its pool anew under the same name, which reuses the sidecars and
	// syncs them with the worktree. A pool is recorded as soon as it exists,
	// so one that fails to sync is still cleaned up.
	workerPool *sidecar.Pool
	reviewPool *sidecar.Pool
}

// openPool opens a named pool synced with the worktree, records it in slot,
// and waits until every member is ready.
func (r *factoryRun) openPool(ctx context.Context, name string, size int, slot **sidecar.Pool) (*sidecar.Pool, error) {
	pool, err := sidecar.NewPool(ctx, r.target.client, sidecar.PoolOptions{
		Size:     size,
		Name:     name,
		OrgID:    r.target.orgID,
		Image:    r.target.image,
		WorkDir:  r.wt.Dir,
		RepoPath: r.repoPath,
	}, r.status)
	if err != nil {
		return nil, fmt.Errorf("prepare %s pool: %w", name, err)
	}
	*slot = pool
	if err := review.WaitReady(ctx, pool.WaitSynced); err != nil {
		return nil, err
	}
	return pool, nil
}

func closePool(ctx context.Context, pool *sidecar.Pool) {
	closeCtx, cancel := context.WithTimeout(ctx, poolCloseTimeout)
	defer cancel()
	pool.Close(closeCtx)
}

// work runs the worker prompt on its sidecar, then pulls its changes back and
// commits them to the worktree.
func (r *factoryRun) work(ctx context.Context, attempt int, prompt string) error {
	base, err := factory.HeadCommit(ctx, r.wt.Dir)
	if err != nil {
		return err
	}
	pool, err := r.openPool(ctx, factoryWorkerPool, 1, &r.workerPool)
	if err != nil {
		return err
	}
	defer closePool(ctx, pool)

	r.status(iostream.LevelInfo, "claude is working...")
	results, err := review.RunPass(ctx, pool.Acquire, pool.Release, review.ClientExec,
		[]review.Prompt{{Name: "worker", Body: prompt}}, r.worker)
	if err != nil {
		return err
	}
	if results[0].Error != "" {
		return fmt.Errorf("worker: %s", results[0].Error)
	}
	if out := results[0].Output; out != "" {
		r.streams.ErrPrintf("%s\n", indent(out, "    "))
	}

	entry, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire worker sidecar: %w", err)
	}
	defer pool.Release(entry)
	patch, err := workerDiff(ctx, entry, base)
	if err != nil {
		return err
	}
	if err := factory.ApplyPatch(ctx, r.wt.Dir, patch); err != nil {
		return fmt.Errorf("apply worker changes: %w", err)
	}
	err = factory.CommitAll(ctx, r.wt.Dir, fmt.Sprintf("factory: attempt %d", attempt))
	if errors.Is(err, factory.ErrNothingToCommit) {
		// A later attempt changing nothing is only a warning: the earlier
		// work is still there to review.
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

// workerDiff reads everything the worker changed since base as a binary patch.
func workerDiff(ctx context.Context, entry *sidecar.PoolEntry, base string) (string, error) {
	var patch, stderr bytes.Buffer
	overflow := false
	code, err := review.ClientExec(ctx, entry, factory.DiffScript(entry.RepoPath, base), nil, func(stream string, data []byte) {
		switch {
		case stream == circleci.StreamStderr:
			stderr.Write(data)
		case patch.Len()+len(data) > maxWorkerDiffBytes:
			overflow = true
		default:
			patch.Write(data)
		}
	})
	switch {
	case err != nil:
		return "", fmt.Errorf("read worker changes: %w", err)
	case code != 0:
		return "", fmt.Errorf("read worker changes: git exited %d: %s", code, strings.TrimSpace(stderr.String()))
	case overflow:
		// A cut-off patch could still apply cleanly with files missing.
		return "", fmt.Errorf("worker changes exceed %d MiB; check for build output missing from .gitignore", maxWorkerDiffBytes>>20)
	}
	return patch.String(), nil
}

// review runs every review prompt, wrapped with the intent and verdict
// instructions, and reads each verdict. A review that could not run or gave no
// verdict is nothing the worker can fix, so it stops the run.
func (r *factoryRun) review(ctx context.Context, _ int) ([]factory.ReviewResult, error) {
	pool, err := r.openPool(ctx, factoryReviewPool, review.PoolSize(r.opts.parallelism, len(r.prompts)), &r.reviewPool)
	if err != nil {
		return nil, err
	}
	defer closePool(ctx, pool)

	wrapped := make([]review.Prompt, len(r.prompts))
	for i, p := range r.prompts {
		wrapped[i] = review.Prompt{Name: p.Name, Body: factory.ReviewPrompt(p.Body, r.intent, r.wt.Base)}
	}
	results, err := review.RunPass(ctx, pool.Acquire, pool.Release, review.ClientExec, wrapped, r.reviewer)
	if err != nil {
		return nil, err
	}

	var reviews []factory.ReviewResult
	var broken []error
	for _, res := range results {
		if res.Error != "" {
			broken = append(broken, fmt.Errorf("review %s: %s", res.Prompt, res.Error))
			continue
		}
		verdict, feedback, err := factory.ParseReview(res.Output)
		if err != nil {
			broken = append(broken, fmt.Errorf("review %s: %w", res.Prompt, err))
			continue
		}
		reviews = append(reviews, factory.ReviewResult{Name: res.Prompt, Verdict: verdict, Feedback: feedback})
		r.status(iostream.LevelInfo, fmt.Sprintf("review %s: %s", res.Prompt, verdict))
		// Logged as it arrives, so the log shows why every retry happened.
		if verdict != factory.VerdictApproved && feedback != "" {
			r.streams.ErrPrintf("%s\n", indent(feedback, "    "))
		}
	}
	if len(broken) > 0 {
		return nil, fmt.Errorf("reviews could not run: %w", errors.Join(broken...))
	}
	return reviews, nil
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
	for name, pool := range map[string]*sidecar.Pool{factoryWorkerPool: r.workerPool, factoryReviewPool: r.reviewPool} {
		if pool == nil {
			continue
		}
		if err := pool.Destroy(cleanupCtx); err != nil {
			r.status(iostream.LevelWarn, fmt.Sprintf("could not delete %s sidecars: %v", name, err))
		}
	}
}

// indent prefixes every line of s with prefix.
func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// printFactorySummary writes where the work is and each attempt's verdicts.
// Feedback was already logged as each review arrived.
func printFactorySummary(streams iostream.Streams, wt factory.Worktree, attempts []factory.Attempt) {
	streams.Printf("Worktree: %s\nBranch:   %s\n", wt.Dir, wt.Branch)
	for _, a := range attempts {
		result := "failed"
		if a.Passed {
			result = "passed"
		}
		streams.Printf("Attempt %d: %s\n", a.N, result)
		for _, rv := range a.Reviews {
			streams.Printf("  %-20s %s\n", rv.Name, rv.Verdict)
		}
	}
}
