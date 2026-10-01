package watchd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// factoryPoolName names a session's sandbox pool. Its state is kept in the
// session's directory, so every session has a pool of its own.
const factoryPoolName = "factory"

// runFactory runs a session in a worktree of its own: it makes the worktree,
// runs the loop there, and on the way out, however the loop ended, commits the
// work to the session's branch, removes the worktree and deletes the pool.
func (d *daemon) runFactory(ctx context.Context, entry *sessionEntry, plan *sessionPlan) error {
	id := entry.snapshot().ID
	dir, err := sessionDir(id)
	if err != nil {
		return err
	}
	path, branch, err := createWorktree(ctx, plan.root, id, dir)
	if err != nil {
		return err
	}
	plan.workDir, plan.stateRoot = path, dir
	entry.setWork(path, branch)

	err = d.runLoop(ctx, entry, plan)
	if finishErr := d.finishSession(ctx, entry, plan, branch); finishErr != nil && err == nil {
		err = finishErr
	}
	return err
}

// finishSession commits the work, removes the worktree and deletes the
// session's sidecars. It gets its own deadline: the session's context may
// already be cancelled, and the work must be kept regardless.
func (d *daemon) finishSession(ctx context.Context, entry *sessionEntry, plan *sessionPlan, branch string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeFinishTimeout)
	defer cancel()

	deletePool := d.rcfg.DeletePool
	if deletePool == nil {
		deletePool = d.defaultDeletePool
	}
	if err := deletePool(ctx, plan.poolSpec()); err != nil {
		log.Printf("watchd: session %s: delete sidecars: %v", entry.snapshot().ID, err)
	}

	snap := entry.snapshot()
	snap.Outcome = entry.pendingOutcome()
	commit, kept, err := finishWorktree(ctx, plan.root, plan.workDir, branch, commitMessage(snap))
	if err != nil {
		// The worktree is left where it is, with the work in it.
		return fmt.Errorf("keep the work: %w (it is still in %s)", err, plan.workDir)
	}
	entry.finishWork(commit, kept)
	return nil
}

// runLoop has the implementer write the task, then runs up to plan.maxRounds
// rounds of {checks in parallel, the implementer fixes what is worth changing,
// the fixes land in the worktree}, and stops early once a round's checks all
// pass.
//
// "Worth changing" is a review finding of severity high or medium, or a
// validation command that failed.
func (d *daemon) runLoop(ctx context.Context, entry *sessionEntry, plan *sessionPlan) error {
	changed, err := d.runImplement(ctx, entry, plan)
	if err != nil {
		return err
	}
	if !changed {
		entry.finishLoop("the implementer made no changes", OutcomeNoChange)
		return nil
	}
	entry.startStage(StageImplement, StageReviewLoop)

	for range plan.maxRounds {
		stop, err := d.runRound(ctx, entry, plan)
		if err != nil {
			return err
		}
		if stop != nil {
			entry.finishLoop(stop.reason, stop.outcome)
			return nil
		}
	}
	entry.finishLoop(fmt.Sprintf("stopped after %d round(s)", plan.maxRounds), OutcomeExhausted)
	return nil
}

// runImplement runs the implementer's first turn, which writes the task, and
// applies what it wrote to the worktree. It reports whether anything changed.
func (d *daemon) runImplement(ctx context.Context, entry *sessionEntry, plan *sessionPlan) (bool, error) {
	pool, err := d.openRoundPool(ctx, plan)
	if err != nil {
		return false, err
	}
	defer pool.close(ctx)
	worker, err := d.acquireWorker(ctx, entry, pool)
	if err != nil {
		return false, err
	}
	defer pool.Release(worker)

	entry.startImplement()
	patch, err := d.implementerTurn(ctx, entry, implementTurn, plan, worker, plan.req.Task, "implement")
	if err != nil {
		entry.failFix(implementTurn, err)
		return false, fmt.Errorf("implement: %w", err)
	}
	if strings.TrimSpace(patch) == "" {
		entry.endFix(implementTurn, FixEmpty, nil)
		return false, nil
	}
	files, err := d.applyTurn(ctx, entry, 0, plan, patch)
	if err != nil {
		entry.failFix(implementTurn, err)
		return false, err
	}
	entry.endFix(implementTurn, FixApplied, files)
	return true, nil
}

// loopStop is why the loop is over.
type loopStop struct {
	reason  string
	outcome Outcome
}

// runRound runs one round. It returns a loopStop when the loop is over.
func (d *daemon) runRound(ctx context.Context, entry *sessionEntry, plan *sessionPlan) (*loopStop, error) {
	ridx := entry.beginRound(plan.prompts, plan.commands)
	pool, err := d.openRoundPool(ctx, plan)
	if err != nil {
		return nil, err
	}
	defer pool.close(ctx)

	// The implementer keeps its sidecar from round to round. Validation runs
	// there while the reviews run on the others, and the fixes are made there.
	worker, err := d.acquireWorker(ctx, entry, pool)
	if err != nil {
		return nil, err
	}
	defer pool.Release(worker)

	var (
		wg         sync.WaitGroup
		validation []ReviewResult
	)
	if len(plan.commands) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			validation = d.runValidation(ctx, entry, ridx, plan, worker)
		}()
	}
	results, err := d.runReviews(ctx, entry, ridx, plan, pool)
	wg.Wait()
	entry.finishReviews(ridx, results, validation)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// A check that failed to run found nothing, which is not the same as
	// finding nothing wrong. If none of them ran, there is no round to call
	// clean.
	failed, first := checkErrors(results, validation)
	total := len(results) + len(validation)
	if total > 0 && failed == total {
		err := fmt.Errorf("every check failed to run: %s", first)
		entry.endRound(ridx, RoundFailed, err.Error())
		return nil, err
	}

	worth := entry.worthFindings(ridx)
	if len(worth) == 0 {
		if failed > 0 {
			// Nothing to fix, but the checks that could not run have not shown
			// the work is clean, so the next round checks the same work again.
			entry.endRound(ridx, RoundDone, fmt.Sprintf("nothing to fix, but %d of %d checks could not run; checking again", failed, total))
			return nil, nil
		}
		entry.endRound(ridx, RoundDone, "every check passed")
		return &loopStop{reason: "every check passed", outcome: OutcomePassed}, nil
	}

	entry.startFix(ridx, worth)
	label := fmt.Sprintf("round %d fix", entry.roundNumber(ridx))
	patch, err := d.implementerTurn(ctx, entry, ridx, plan, worker, feedbackPrompt(plan.req.Task, worth), label)
	if err != nil {
		entry.failFix(ridx, err)
		return nil, fmt.Errorf("fix: %w", err)
	}
	if strings.TrimSpace(patch) == "" {
		entry.endFix(ridx, FixEmpty, nil)
		entry.endRound(ridx, RoundDone, "the implementer made no changes")
		return &loopStop{reason: "the implementer made no changes for what the checks found", outcome: OutcomeStuck}, nil
	}

	entry.setRoundState(ridx, RoundApplying)
	files, err := d.applyTurn(ctx, entry, entry.roundNumber(ridx), plan, patch)
	if err != nil {
		entry.failFix(ridx, err)
		return nil, err
	}
	entry.endFix(ridx, FixApplied, files)
	entry.endRound(ridx, RoundDone, "")
	return nil, nil
}

// checkErrors counts a round's checks that could not run, and gives the first
// one's error.
func checkErrors(results []review.Result, validation []ReviewResult) (n int, first string) {
	errs := make([]string, 0, len(results)+len(validation))
	for _, r := range results {
		errs = append(errs, r.Error)
	}
	for _, r := range validation {
		errs = append(errs, r.Error)
	}
	for _, e := range errs {
		if e == "" {
			continue
		}
		if n == 0 {
			first = e
		}
		n++
	}
	return n, first
}

// acquireWorker checks out the implementer's sidecar. Every turn runs on the
// same one, so it can resume the Claude session of the turn before. If that
// sidecar is gone from the pool, any member takes its place and the next turn
// starts a new Claude session there.
func (d *daemon) acquireWorker(ctx context.Context, entry *sessionEntry, pool roundPool) (*sidecar.PoolEntry, error) {
	if id := entry.workerID(); id != "" && pool.AcquireID != nil {
		pe, err := pool.AcquireID(ctx, id)
		if err == nil {
			return pe, nil
		}
		if !errors.Is(err, sidecar.ErrNotPoolMember) {
			return nil, fmt.Errorf("acquire the implementer's sandbox: %w", err)
		}
	}
	pe, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire the implementer's sandbox: %w", err)
	}
	entry.setWorker(pe.ID)
	return pe, nil
}

// implementerTurn runs one implementer turn on the worker and returns its
// patch. A turn after the first continues the first turn's Claude session, so
// feedback arrives with the context of the work so far.
func (d *daemon) implementerTurn(ctx context.Context, entry *sessionEntry, ridx int, plan *sessionPlan, worker *sidecar.PoolEntry, prompt, label string) (string, error) {
	a := review.ImplementAgent(label, prompt)
	a.Model = plan.req.Model
	if plan.req.ImplementTimeoutSeconds > 0 {
		a.Timeout = time.Duration(plan.req.ImplementTimeoutSeconds) * time.Second
	}
	a.SessionID, a.Resume = entry.claudeSessionFor(worker.ID)

	patch, res, err := d.editOnSandbox(ctx, entry, ridx, plan.root, worker, a, label)
	// The Claude session exists once claude has started it, even if the turn
	// then failed, so the next turn resumes it rather than starts it again.
	if res.SessionID != "" {
		entry.setClaudeSession(res.SessionID)
	}
	if err != nil {
		return "", err
	}
	if err := checkPatchPaths(patch); err != nil {
		return "", err
	}
	return patch, nil
}

// applyTurn writes a turn's patch into the worktree and returns the files it
// changed. The patch is kept in the session's directory as well.
func (d *daemon) applyTurn(ctx context.Context, entry *sessionEntry, number int, plan *sessionPlan, patch string) ([]FileChange, error) {
	patchPath, err := savePatch(entry.snapshot().ID, number, patch)
	if err != nil {
		return nil, err
	}
	return applyPatchToTree(ctx, plan.workDir, patchPath)
}

// ---- session record updates ------------------------------------------------

func (e *sessionEntry) roundNumber(ridx int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.s.Rounds[ridx].Number
}

// worthFindings returns the distinct findings of a round worth changing.
func (e *sessionEntry) worthFindings(ridx int) []review.Finding {
	e.mu.Lock()
	defer e.mu.Unlock()
	var all []review.Finding
	for _, res := range e.details[ridx].Results {
		all = append(all, res.Findings...)
	}
	return worthChanging(review.DedupeFindings(all))
}

func (e *sessionEntry) setRoundState(ridx int, state RoundState) {
	e.mu.Lock()
	e.s.Rounds[ridx].State = state
	e.mu.Unlock()
}

func (e *sessionEntry) setWork(path, branch string) {
	e.mu.Lock()
	e.s.WorkDir, e.s.WorkBranch = path, branch
	e.mu.Unlock()
}

// finishWork records where the work ended up once the worktree is gone.
func (e *sessionEntry) finishWork(commit string, kept bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.s.WorkDir, e.s.WorkCommit = "", commit
	if !kept {
		e.s.WorkBranch = ""
	}
}

func (e *sessionEntry) workerID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.worker
}

// setWorker makes id the implementer's sidecar. A new one has no Claude session
// to resume.
func (e *sessionEntry) setWorker(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.worker != id {
		e.worker, e.claudeSession = id, ""
	}
}

// claudeSessionFor is the Claude session a turn on sidecarID uses, and whether
// it continues one already started there.
func (e *sessionEntry) claudeSessionFor(sidecarID string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.worker == sidecarID && e.claudeSession != "" {
		return e.claudeSession, true
	}
	return uuid.NewString(), false
}

func (e *sessionEntry) setClaudeSession(id string) {
	e.mu.Lock()
	e.claudeSession = id
	e.mu.Unlock()
}

// startStage ends one stage and starts the next.
func (e *sessionEntry) startStage(done, next StageID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if st := e.stageLocked(done); st != nil {
		st.State = StageDone
	}
	if st := e.stageLocked(next); st != nil {
		st.State = StageRunning
	}
}

func (e *sessionEntry) startImplement() {
	e.mu.Lock()
	e.s.Implement = &RoundFix{State: FixRunning}
	e.mu.Unlock()
}

func (e *sessionEntry) startFix(ridx int, findings []review.Finding) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fix := &RoundFix{State: FixRunning}
	for _, f := range findings {
		fix.FindingIDs = append(fix.FindingIDs, f.ID)
	}
	e.s.Rounds[ridx].Fix = fix
	e.s.Rounds[ridx].State = RoundFixing
}

// updateFix changes the fix record ridx names under the lock: a round's, or for
// implementTurn the first turn's. A record not yet started is left alone.
func (e *sessionEntry) updateFix(ridx int, fn func(*RoundFix)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fix := e.s.Implement
	if ridx >= 0 {
		fix = e.s.Rounds[ridx].Fix
	}
	if fix != nil {
		fn(fix)
	}
}

// updateCheck changes a round's validation command record under the lock.
func (e *sessionEntry) updateCheck(ridx int, name string, fn func(*ReviewPrompt)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i := e.checkIndexLocked(ridx, CheckValidate, name); i >= 0 {
		fn(&e.s.Rounds[ridx].Reviews[i])
	}
}

func (e *sessionEntry) endFix(ridx int, state FixState, files []FileChange) {
	e.updateFix(ridx, func(fix *RoundFix) {
		fix.State, fix.Files = state, files
		fix.Insertions, fix.Deletions = 0, 0
		for _, f := range files {
			fix.Insertions += f.Insertions
			fix.Deletions += f.Deletions
		}
	})
}

func (e *sessionEntry) failFix(ridx int, err error) {
	e.updateFix(ridx, func(fix *RoundFix) {
		fix.State, fix.Error, fix.Activity = FixFailed, err.Error(), ""
	})
}

// finishLoop records why the loop ended, to be shown on the stage, and how.
func (e *sessionEntry) finishLoop(reason string, o Outcome) {
	e.mu.Lock()
	e.loopNote, e.outcome = reason, o
	e.mu.Unlock()
}

// pendingOutcome is how the loop ended, before the session settles.
func (e *sessionEntry) pendingOutcome() Outcome {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.outcome
}
