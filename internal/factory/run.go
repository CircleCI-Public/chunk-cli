package factory

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/circleci"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/review"
	"github.com/smeltdev/smelt/internal/sidecar"
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
	// work first counts that round as one of them.
	Attempts int
	// Reviewers is how many reviewer sidecars to run; see ReviewerCount.
	Reviewers int
	Prompts   []review.Prompt
	Commands  []config.Command
	// Plan has the implementer write a plan, and the plan reviewed, before it
	// writes code. A continued run never plans: its plan is in the record.
	Plan bool
	// GatesFirst runs Commands before the reviews and skips the reviews of a
	// round whose commands failed.
	GatesFirst bool

	// Author is the configured author of the branch's commits (factory.author);
	// when it is empty, ResolveAuthor falls back to the developer's identity.
	Author config.CommitAuthor

	Client *circleci.Client
	OrgID  string
	Image  string
	// Backend provisions and drives the run's sidecars. When nil, the run uses
	// the CircleCI backend built from Client and OrgID. Set it to run the loop
	// on a different provider, such as local Docker.
	Backend sidecar.Backend
	// Install installs the project's dependencies on each sidecar before the
	// run starts; empty skips it.
	Install string
	// KeepSidecars leaves the sidecars running when the run ends.
	KeepSidecars bool

	// Harness is the coding agent for the implementer and the reviewers; Claude
	// Code when nil. Credential is its credential.
	Harness                 harness.Harness
	Credential              review.Credential
	BaseURL                 string
	Model                   string
	ImplementerInstructions string
	ImplementTimeout        time.Duration
	ReviewTimeout           time.Duration

	// Exec runs commands on the sidecars; review.ClientExec when nil.
	Exec review.Execer

	// Warnings are problems with the run's setup that did not stop it; they go
	// in the log.
	Warnings []string

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
	// Notes are the account of the committed change, nil when there was none
	// to describe. They are the fallback when they could not be written.
	Notes *Notes
	// WorktreeRemoved reports that the run ended before the implementer started
	// and its worktree, holding nothing, was removed.
	WorktreeRemoved bool
	Outcome         Outcome
	// KeptSidecars are the sidecars left running with KeepSidecars.
	KeptSidecars []string
	// Log is the path of the run's log, if it kept one.
	Log string
}

// backend is the backend that drives the run's pool and relay: the caller's
// choice (such as Docker), else the CircleCI backend built from the client.
func (o *RunOptions) backend() sidecar.Backend {
	if o.Backend != nil {
		return o.Backend
	}
	return sidecar.NewCircleCIBackend(o.Client, o.OrgID)
}

// prepareBackend lets a backend that needs it check its host once, before the
// pool creates any sidecar.
func prepareBackend(ctx context.Context, backend sidecar.Backend, image string, status iostream.StatusFunc) error {
	p, ok := backend.(sidecar.Preparer)
	if !ok {
		return nil
	}
	return p.Ready(ctx, image, status)
}

// createPool readies the backend, then creates the run's sidecars from the
// worktree.
func createPool(ctx context.Context, opts RunOptions, backend sidecar.Backend, runID string, wt Worktree, status iostream.StatusFunc) (*sidecar.Pool, error) {
	if err := prepareBackend(ctx, backend, opts.Image, status); err != nil {
		return nil, err
	}
	return sidecar.NewPool(ctx, opts.Client, sidecar.PoolOptions{
		Size:    PoolSize(opts.Reviewers),
		Name:    PoolName(runID),
		OrgID:   opts.OrgID,
		Image:   opts.Image,
		Backend: backend,
		// The sidecars start from the worktree, and its state stays in the
		// project, where the dashboard finds it.
		WorkDir:  wt.Path,
		StateDir: opts.Root,
	}, status)
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
		return rep, fmt.Errorf("find smelt's data directory for the project: %w", err)
	}
	author := ResolveAuthor(ctx, opts.Root, opts.Author).CommitAuthor
	var wt Worktree
	if cont == nil {
		wt, err = CreateWorktree(ctx, WorktreeOptions{
			Root: opts.Root, Path: filepath.Join(dataDir, "factory", rep.RunID),
			RunID: rep.RunID, Prompt: opts.Prompt, Author: author,
		})
		if err != nil {
			return rep, fmt.Errorf("create the run's worktree: %w", err)
		}
	} else {
		status(iostream.LevelInfo, fmt.Sprintf("Continuing run %s on %s", cont.From.RunID, cont.From.Branch))
		wt, err = openWorktree(ctx, opts.Root, cont.From, rep.RunID, author)
		if err != nil {
			return rep, fmt.Errorf("open the worktree of run %s: %w", cont.From.RunID, err)
		}
	}
	rep.Worktree = wt
	if opts.OnStart != nil {
		opts.OnStart(rep.RunID, wt)
	}
	rec := newRecord(rep.RunID, request, wt)
	rec.Harness, rec.Model = opts.harnessName(), opts.Model
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
			keepEndedRecord(dataDir, rec, rep, status)
			cont.resolved(dataDir, status)
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

	status(iostream.LevelInfo, opts.summary())
	status(iostream.LevelStep, fmt.Sprintf("Preparing an implementer sidecar and %d reviewer sidecar(s)...", opts.Reviewers))
	// One backend drives both the pool and the relay.
	backend := opts.backend()
	pool, err := createPool(ctx, opts, backend, rep.RunID, wt, status)
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
			Harness: opts.Harness, Exec: exec, Entry: impl, Credential: opts.Credential, BaseURL: opts.BaseURL,
			Model: opts.Model, Timeout: opts.ImplementTimeout, Instructions: opts.ImplementerInstructions,
			OnActivity: opts.OnActivity, Gates: gateCommands(opts.Commands),
		},
		Acquire:   pool.Acquire,
		Release:   pool.Release,
		Reviewers: Members(impl, pool.IDs()),
		Relay:     NewRelay(backend, wt.Path, status),
		Request:   request,
		Prompts:   opts.Prompts,
		Review: review.Options{
			Harness: opts.Harness, Credential: opts.Credential, BaseURL: opts.BaseURL, Model: opts.Model, Timeout: opts.ReviewTimeout,
			StructuredFindings: true,
			ProgressFn:         opts.OnReviewProgress,
		},
		Commands:   opts.Commands,
		GatesFirst: opts.GatesFirst,
		Status:     status,
		OnCheck:    opts.OnCheck,
		Install:    opts.Install,
		OnInstallOutput: func(id, line string) {
			status(iostream.LevelInfo, "install "+id+": "+line)
		},
	}
	if opts.Verbose {
		steps.OnReviewerTree = func(t ReviewerTree) { lg.reviewerTree(status, t) }
	}
	if err := steps.Prepare(ctx); err != nil {
		return rep, fmt.Errorf("set up the implementer's workspace: %w", err)
	}
	// The implementer's last turn goes into the notes.
	lastSummary := ""
	loop := Loop{Attempts: opts.Attempts, OnEvent: keepSummary(&lastSummary, opts.OnEvent)}
	loop.Planner = planFor(opts.Plan, cont, steps)
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
	concludeRun(ctx, steps, wt, &rep, NotesInput{Request: request, Summary: lastSummary, Log: lg.block}, status)
	return rep, err
}

// planFor is the planner a run uses, nil for none. A continued run never plans:
// the plan already exists in the record of the run it continues.
func planFor(enabled bool, cont *Continuation, p Planner) Planner {
	if !enabled || cont != nil {
		return nil
	}
	return p
}

// keepSummary is an event handler that keeps the summary of the implementer's
// latest turn in last, and passes every event on to next when it is set.
func keepSummary(last *string, next func(Event)) func(Event) {
	return func(e Event) {
		if e.Kind == EventImplemented {
			*last = e.Turn.Summary
		}
		if next != nil {
			next(e)
		}
	}
}

// concludeRun commits the implementer's work and describes it. The commit is
// made before the notes are written, so the implementer can read the change
// from it; its message is rewritten once they are. It sets what the run left
// behind in rep.
func concludeRun(ctx context.Context, steps *Sidecars, wt Worktree, rep *Report, in NotesInput, status iostream.StatusFunc) {
	rep.Committed = commitWork(ctx, steps, wt, Notes{Title: requestTitle(in.Request)}.CommitMessage(rep.RunID, rep.Outcome, ""), status)
	if !rep.Committed {
		return
	}
	rep.Stat, rep.StatErr = workStat(ctx, wt)
	if rep.Stat != "" {
		rep.Notes = describeWork(ctx, steps, wt, *rep, in, status)
	}
}

// describeWork writes the notes of the committed work and rewrites the
// commit's message with them. When the notes cannot be written the commit
// carries the fallback, with a warning saying why.
func describeWork(ctx context.Context, steps *Sidecars, wt Worktree, rep Report, in NotesInput, status iostream.StatusFunc) *Notes {
	in.Outcome, in.Baseline, in.Plan = rep.Outcome, wt.Baseline, rep.Outcome.Plan
	var err error
	if in.DiffStat, err = diffStat(context.WithoutCancel(ctx), wt); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not read the diff stat for the PR notes: %v", err))
	}
	status(iostream.LevelStep, "writing PR notes…")
	notes, err := steps.WriteNotes(ctx, in)
	notes.Skipped = rep.Outcome.Skipped()
	if err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("PR notes were not generated: %v", err))
	} else {
		status(iostream.LevelDone, "PR notes written")
	}
	amendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := wt.Amend(amendCtx, notes.CommitMessage(rep.RunID, rep.Outcome, rep.Stat)); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not put the notes in the commit message: %v", err))
	}
	return &notes
}

// diffStat is the committed work's per-file diff stat, after the run has ended.
func diffStat(ctx context.Context, wt Worktree) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	return wt.DiffStat(ctx)
}

// keepRecord saves the run's record, so a later run can continue it. A run
// that cannot save one still runs; it just cannot be continued.
func keepRecord(dataDir string, rec Record, status iostream.StatusFunc) {
	if err := saveRecord(dataDir, rec); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("this run cannot be continued later: %v", err))
	}
}

// keepEndedRecord saves rec with how the run ended and the notes it wrote.
func keepEndedRecord(dataDir string, rec Record, rep Report, status iostream.StatusFunc) {
	rec.Result, rec.Rounds, rec.Notes, rec.Plan = rep.Outcome.Result, rep.Outcome.Rounds, rep.Notes, rep.Outcome.Plan
	keepRecord(dataDir, rec, status)
}

// resolved forgets the conflicts a continued run was asked to resolve in the
// record it continued, now that the run has ended. It is a no-op on a nil
// Continuation.
func (c *Continuation) resolved(dataDir string, status iostream.StatusFunc) {
	if c == nil || len(c.From.Conflicts) == 0 {
		return
	}
	from := c.From
	from.Conflicts = nil
	keepRecord(dataDir, from, status)
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
// dashboard and 'smelt sidecar' still find its sidecars; no later run reuses
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

// harnessName is the agent the run uses, Claude Code when none is set.
func (o *RunOptions) harnessName() string {
	if o.Harness == nil {
		return config.HarnessClaude
	}
	return string(o.Harness.Name())
}

// summary describes the run's configuration; an explicit Backend means docker.
func (o *RunOptions) summary() string {
	f := summaryFields{Image: o.Image, Attempts: o.Attempts, Reviewers: o.Reviewers, Model: o.Model}
	if o.Backend != nil {
		f.Backend = config.BackendDocker
	}
	f.Harness = o.harnessName()
	return runSummary(f)
}

// summaryFields are what runSummary describes.
type summaryFields struct {
	Backend, Image, Harness, Model string
	Attempts, Reviewers            int
}

// runSummary is the one-line description of a run's configuration. Empty
// fields show a readable default, and nothing secret is included.
func runSummary(f summaryFields) string {
	return fmt.Sprintf("Factory run: backend=%s image=%s attempts=%d reviewers=%d harness=%s model=%s",
		orDefault(f.Backend, config.BackendCircleCI), orDefault(f.Image, "default"), f.Attempts, f.Reviewers,
		orDefault(f.Harness, config.HarnessClaude), orDefault(f.Model, "default"))
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
