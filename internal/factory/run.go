package factory

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// Cleanup deadlines. Cleanup runs on the way out, after a failure or an
// interrupt too, so it gets time of its own rather than the run's context.
const (
	cleanupTimeout   = 30 * time.Second
	poolCloseTimeout = 5 * time.Minute
	pullTimeout      = 2 * time.Minute
)

// RunOptions is one factory run. Everything that needs a person, such as
// picking an org or finding a credential, is settled before it.
type RunOptions struct {
	// Root is the developer's repository root. The run's worktree is made from
	// it, and the pool's state is kept in it, where the dashboard finds it.
	Root string
	// Prompt is what the implementer is asked to do. A continued run makes its
	// own from Continue, and ignores this.
	Prompt string
	// Continue, when set, picks up the work of an earlier run instead of
	// starting from the developer's files.
	Continue *Continuation
	// Attempts is the most rounds to check. A continued run that checks the
	// work first adds a round for that; see Continuation.Rounds.
	Attempts int
	// Reviewers is how many reviewer sidecars to run; see ReviewerCount.
	Reviewers int
	Prompts   []review.Prompt
	Commands  []config.Command

	Client *circleci.Client
	OrgID  string
	Image  string
	// KeepSidecars leaves the sidecars running when the run ends.
	KeepSidecars bool

	Credential              review.Credential
	BaseURL                 string
	Model                   string
	ImplementerInstructions string
	ImplementTimeout        time.Duration
	ReviewTimeout           time.Duration

	// Exec runs commands on the sidecars; review.ClientExec when nil.
	Exec review.Execer

	// Log is where to keep a plain-text log of the run, with its full context
	// whatever a display of it filters out: a path, LogDefault for
	// DefaultLogPath, or "" for none.
	Log string
	// Verbose adds the review prompts, the output of commands that passed,
	// and a check each round that every reviewer has the implementer's change
	// to the log. It implies a log.
	Verbose bool

	// Status reports progress, warnings included. It must be set.
	Status iostream.StatusFunc
	// The rest report what the run is doing, and may be nil. OnStart is
	// called once the run's worktree exists.
	OnStart          func(runID string, wt Worktree)
	OnEvent          func(Event)
	OnActivity       func(Activity)
	OnReviewProgress func(review.ProgressEvent)
	OnCheck          func(Check)
}

// Report is what a run left behind.
type Report struct {
	RunID string
	// Worktree is the run's worktree. It is kept once the implementer has
	// started, and removed, with its branch, if the run failed before that.
	Worktree Worktree
	// Started reports whether the implementer started.
	Started bool
	// Committed reports whether the work was committed on Worktree.Branch.
	Committed bool
	// Stat is the committed work's diff stat against Worktree.Baseline, "" for
	// no changes. StatErr says why it could not be worked out.
	Stat    string
	StatErr error
	// WorktreeRemoved reports that the run ended before the implementer started
	// and its worktree, holding nothing, was removed.
	WorktreeRemoved bool
	Outcome         Outcome
	// KeptSidecars are the sidecars left running with KeepSidecars.
	KeptSidecars []string
	// Log is the path of the run's log, if it kept one.
	Log string
}

// ReviewerCount is how many reviewer sidecars a run with prompts needs when
// asked for requested: one per prompt unless fewer are asked for, and none
// without prompts.
func ReviewerCount(requested int, prompts []review.Prompt) int {
	if len(prompts) == 0 {
		return 0
	}
	if requested <= 0 || requested > len(prompts) {
		return len(prompts)
	}
	return requested
}

// Run makes the run's worktree and sidecars, runs the loop, and commits the
// implementer's work on the run's branch. A failure before the loop starts
// says which step failed; one in the loop is returned as Loop.Run returns it,
// with the report so far. The work is committed either way once the implementer has
// started, so it is not lost with the sidecars.
func Run(ctx context.Context, opts RunOptions) (rep Report, err error) {
	rep.RunID = time.Now().UTC().Format("20060102-150405")
	cont := opts.Continue
	request := opts.Prompt
	if cont != nil {
		opts.Prompt, request = cont.prompt(), cont.request()
		opts.Attempts = cont.Rounds(opts.Attempts)
	}
	if opts.Verbose && opts.Log == "" {
		opts.Log = LogDefault
	}
	lg, err := openLog(opts.Log, rep.RunID, opts.Verbose)
	if err != nil {
		return rep, fmt.Errorf("open the run's log: %w", err)
	}
	if lg != nil {
		rep.Log = lg.path
	}
	// Deferred first, so it runs last: the other defers report as they clean
	// up, and that goes in the log too.
	defer func() { lg.close(rep, err) }()
	opts = lg.wrap(opts)
	status := opts.Status
	lg.start(rep.RunID, opts, request)

	dataDir, err := config.ProjectDataDir(opts.Root)
	if err != nil {
		return rep, fmt.Errorf("find chunk's data directory for the project: %w", err)
	}
	var wt Worktree
	if cont == nil {
		wt, err = CreateWorktree(ctx, opts.Root, filepath.Join(dataDir, "factory", rep.RunID), rep.RunID, opts.Prompt)
		if err != nil {
			return rep, fmt.Errorf("create the run's worktree: %w", err)
		}
	} else {
		status(iostream.LevelInfo, fmt.Sprintf("Continuing run %s on %s", cont.From.RunID, cont.From.Branch))
		wt, err = openWorktree(ctx, opts.Root, cont.From, rep.RunID)
		if err != nil {
			return rep, fmt.Errorf("open the worktree of run %s: %w", cont.From.RunID, err)
		}
	}
	rep.Worktree = wt
	if opts.OnStart != nil {
		opts.OnStart(rep.RunID, wt)
	}
	rec := Record{
		RunID: rep.RunID, Prompt: request,
		Worktree: wt.Path, Branch: wt.Branch, Baseline: wt.Baseline, Head: wt.Head,
	}
	if cont != nil {
		rec.Prompt, rec.ContinuesRunID = cont.From.Prompt, cont.From.RunID
	}
	// Until the implementer starts, a new worktree holds nothing the developer
	// does not already have. A continued run's worktree holds the earlier
	// run's work, so it stays. The defers set rep, so it is a named result.
	defer func() {
		if !rep.Started && cont == nil {
			rep.WorktreeRemoved = removeWorktree(ctx, opts.Root, wt, status)
		}
	}()
	defer func() {
		if rep.Started {
			rec.Result, rec.Rounds = rep.Outcome.Result, rep.Outcome.Rounds
			keepRecord(dataDir, rec, status)
		}
	}()
	// A continued run's sidecars are synced from the baseline's files, and
	// the work is laid on the implementer's afterwards, so the reviewers see
	// the whole change rather than only what this run adds.
	showingBaseline := false
	if cont != nil {
		if err := wt.showBaseline(ctx); err != nil {
			return rep, err
		}
		showingBaseline = true
		defer func() {
			if showingBaseline {
				restoreWorktree(ctx, wt, status)
			}
		}()
	}

	status(iostream.LevelStep, fmt.Sprintf("Preparing an implementer sidecar and %d reviewer sidecar(s)...", opts.Reviewers))
	pool, err := sidecar.NewPool(ctx, opts.Client, sidecar.PoolOptions{
		Size:  PoolSize(opts.Reviewers),
		Name:  PoolName(rep.RunID),
		OrgID: opts.OrgID,
		Image: opts.Image,
		// The sidecars start from the worktree, and its state stays in the
		// project, where the dashboard finds it.
		WorkDir:  wt.Path,
		StateDir: opts.Root,
	}, status)
	if err != nil {
		return rep, fmt.Errorf("create the run's sidecars: %w", err)
	}
	defer func() { rep.KeptSidecars = closePool(ctx, pool, opts.KeepSidecars, status) }()
	if err := pool.WaitSynced(ctx); err != nil {
		return rep, fmt.Errorf("get the run's sidecars ready: %w", err)
	}
	if showingBaseline {
		showingBaseline = false
		if err := wt.restoreWork(ctx); err != nil {
			return rep, err
		}
	}
	// The implementer holds its member for the whole run; reviews are handed
	// the rest.
	impl, err := pool.Acquire(ctx)
	if err != nil {
		return rep, fmt.Errorf("check out the implementer's sidecar: %w", err)
	}

	exec := opts.Exec
	if exec == nil {
		exec = review.ClientExec
	}
	steps := &Sidecars{
		Exec: exec,
		Implementer: &Implementer{
			Exec: exec, Entry: impl, Credential: opts.Credential, BaseURL: opts.BaseURL,
			Model: opts.Model, Timeout: opts.ImplementTimeout, Instructions: opts.ImplementerInstructions,
			OnActivity: opts.OnActivity,
		},
		Acquire:   pool.Acquire,
		Release:   pool.Release,
		Reviewers: Members(impl, pool.IDs()),
		Relay:     NewRelay(opts.Client, wt.Path, status),
		Request:   request,
		Prompts:   opts.Prompts,
		Review: review.Options{
			Credential: opts.Credential, BaseURL: opts.BaseURL, Model: opts.Model, Timeout: opts.ReviewTimeout,
			StructuredFindings: true,
			ProgressFn:         opts.OnReviewProgress,
		},
		Commands: opts.Commands,
		OnCheck:  opts.OnCheck,
	}
	if opts.Verbose {
		steps.OnReviewerTree = func(t ReviewerTree) { lg.reviewerTree(status, t) }
	}
	if err := steps.Prepare(ctx); err != nil {
		return rep, fmt.Errorf("set up the implementer's workspace: %w", err)
	}
	loop := Loop{Attempts: opts.Attempts, OnEvent: opts.OnEvent}
	if cont != nil {
		if err := steps.Relay.Push(ctx, []*sidecar.PoolEntry{impl}); err != nil {
			return rep, fmt.Errorf("bring the work of run %s to the implementer: %w", cont.From.RunID, err)
		}
		loop.CheckFirst = cont.checkFirst()
	}

	// The record is kept once there is work to continue, and again with how
	// the run ended, so a run whose daemon died can still be continued.
	keepRecord(dataDir, rec, status)
	rep.Started = true
	rep.Outcome, err = loop.Run(ctx, steps, opts.Prompt)
	message := CommitMessage(opts.Prompt, rep.RunID, rep.Outcome)
	if cont != nil {
		message = cont.commitMessage(rep.RunID, rep.Outcome)
	}
	rep.Committed = commitWork(ctx, steps, wt, message, status)
	if rep.Committed {
		rep.Stat, rep.StatErr = workStat(ctx, wt)
	}
	return rep, err
}

// keepRecord saves the run's record, so a later run can continue it. A run
// that cannot save one still runs; it just cannot be continued.
func keepRecord(dataDir string, rec Record, status iostream.StatusFunc) {
	if err := saveRecord(dataDir, rec); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("this run cannot be continued later: %v", err))
	}
}

// restoreWorktree puts the work's files back in a continued run's worktree
// when the run ends before its sidecars were synced from the baseline's.
func restoreWorktree(ctx context.Context, wt Worktree, status iostream.StatusFunc) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := wt.restoreWork(ctx); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("%v; run git read-tree -u --reset HEAD in %s to get them back", err, wt.Path))
	}
}

// removeWorktree removes the worktree of a run that ended before the
// implementer started, and reports whether it did.
func removeWorktree(ctx context.Context, root string, wt Worktree, status iostream.StatusFunc) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := wt.Remove(ctx, root); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not remove the run's worktree %s: %v", wt.Path, err))
		return false
	}
	return true
}

// workStat summarizes the committed work, after the run has ended.
func workStat(ctx context.Context, wt Worktree) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	return wt.Stat(ctx)
}

// commitWork brings the implementer's last work into the worktree and commits
// it on the run's branch. It gets one deadline for the pull and a fresh one
// for the commit, so a pull that hangs cannot use up the commit's time. What
// an earlier round pulled is committed even if the last pull fails.
func commitWork(ctx context.Context, steps *Sidecars, wt Worktree, message string, status iostream.StatusFunc) bool {
	base := context.WithoutCancel(ctx)
	pullCtx, cancelPull := context.WithTimeout(base, pullTimeout)
	err := steps.Pull(pullCtx)
	cancelPull()
	if err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not bring back the implementer's last changes: %v", err))
	}
	ctx, cancel := context.WithTimeout(base, cleanupTimeout)
	defer cancel()
	if _, err := wt.Commit(ctx, message); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not commit the work in %s: %v", wt.Path, err))
		return false
	}
	return true
}

// closePool deletes the run's sidecars, or with keep leaves them running and
// returns their IDs. A kept pool stays in its state file, which is how the
// dashboard and 'chunk sidecar' still find its sidecars; no later run reuses
// it, since the name is the run's own.
func closePool(ctx context.Context, pool *sidecar.Pool, keep bool, status iostream.StatusFunc) []string {
	if !keep {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if err := pool.Destroy(ctx); err != nil {
			status(iostream.LevelWarn, fmt.Sprintf("could not destroy pool: %v", err))
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), poolCloseTimeout)
	defer cancel()
	pool.Close(ctx)
	ids := pool.IDs()
	status(iostream.LevelInfo, "kept sidecars: "+strings.Join(ids, " "))
	return ids
}
