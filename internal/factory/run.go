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
	// Prompt is what the implementer is asked to do.
	Prompt string
	// Attempts is the most rounds to check.
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

	Credential       review.Credential
	BaseURL          string
	Model            string
	ImplementTimeout time.Duration
	ReviewTimeout    time.Duration

	// Exec runs commands on the sidecars; review.ClientExec when nil.
	Exec review.Execer

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
	Outcome   Outcome
	// KeptSidecars are the sidecars left running with KeepSidecars.
	KeptSidecars []string
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
	status := opts.Status
	rep.RunID = time.Now().UTC().Format("20060102-150405")

	dataDir, err := config.ProjectDataDir(opts.Root)
	if err != nil {
		return rep, fmt.Errorf("find chunk's data directory for the project: %w", err)
	}
	wt, err := CreateWorktree(ctx, opts.Root, filepath.Join(dataDir, "factory", rep.RunID), rep.RunID)
	if err != nil {
		return rep, fmt.Errorf("create the run's worktree: %w", err)
	}
	rep.Worktree = wt
	if opts.OnStart != nil {
		opts.OnStart(rep.RunID, wt)
	}
	// Until the implementer starts, the worktree holds nothing the developer
	// does not already have. The defers set rep, so it is a named result.
	defer func() {
		if !rep.Started {
			removeWorktree(ctx, opts.Root, wt, status)
		}
	}()

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
			Model: opts.Model, Timeout: opts.ImplementTimeout,
			OnActivity: opts.OnActivity,
		},
		Acquire:   pool.Acquire,
		Release:   pool.Release,
		Reviewers: Members(impl, pool.IDs()),
		Relay:     NewRelay(opts.Client, wt.Path, status),
		Prompts:   opts.Prompts,
		Review: review.Options{
			Credential: opts.Credential, BaseURL: opts.BaseURL, Model: opts.Model, Timeout: opts.ReviewTimeout,
			StructuredFindings: true,
			ProgressFn:         opts.OnReviewProgress,
		},
		Commands: opts.Commands,
		OnCheck:  opts.OnCheck,
	}
	if err := steps.Prepare(ctx); err != nil {
		return rep, fmt.Errorf("set up the implementer's workspace: %w", err)
	}

	rep.Started = true
	loop := Loop{Attempts: opts.Attempts, OnEvent: opts.OnEvent}
	rep.Outcome, err = loop.Run(ctx, steps, opts.Prompt)
	rep.Committed = commitWork(ctx, steps, wt, CommitMessage(opts.Prompt, rep.RunID, rep.Outcome), status)
	return rep, err
}

// removeWorktree removes the worktree of a run that ended before the
// implementer started.
func removeWorktree(ctx context.Context, root string, wt Worktree, status iostream.StatusFunc) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := wt.Remove(ctx, root); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not remove the run's worktree %s: %v", wt.Path, err))
	}
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
