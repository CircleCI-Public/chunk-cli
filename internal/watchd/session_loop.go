package watchd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// loopState is what the review loop remembers between rounds.
type loopState struct {
	// expected is the user's working tree as the session last saw it: at the
	// start, then as it left it after each round's fixes. If the files are not
	// this when the session is about to look at or change them, they moved
	// underneath it.
	expected string
	// touched are the files the session's fixes have changed so far.
	touched []string
}

// outcome of one round, for the loop to act on.
type outcome int

const (
	roundAdvanced outcome = iota // fixes applied; go round again if rounds remain
	roundStopped                 // nothing left worth changing; the loop is finished
	roundRestart                 // files changed underneath; paused and resumed, so run it again
)

// runLoop runs up to MaxRounds rounds of {reviews in parallel, a sandbox agent
// fixes what is worth changing, the fixes land in the user's working tree}, and
// stops early once a round finds nothing worth changing.
//
// "Worth changing" is severity high or medium. Before it looks at the files and
// before it writes to them, the loop checks they are still as it left them; if
// not it pauses (see pauseSession) rather than overwrite anything.
func (d *daemon) runLoop(ctx context.Context, entry *sessionEntry, prompts []review.Prompt, req SessionRequest) error {
	root := entry.snapshot().ProjectRoot
	maxRounds := MaxRounds
	if req.MaxRounds > 0 && req.MaxRounds < MaxRounds {
		maxRounds = req.MaxRounds
	}

	tree, err := treeNow(root)
	if err != nil {
		return err
	}
	st := &loopState{expected: tree}

	for done := 0; done < maxRounds; {
		now, err := treeNow(root)
		if err != nil {
			return err
		}
		if now != st.expected {
			// Changed between rounds: wait for the user to say what to do.
			if st.expected, err = d.pauseSession(ctx, entry, root, st.expected, now); err != nil {
				return err
			}
			continue
		}

		out, reason, err := d.runRound(ctx, entry, root, prompts, req, st)
		if err != nil {
			return err
		}
		switch out {
		case roundStopped:
			entry.finishLoop(reason)
			return nil
		case roundAdvanced:
			done++
		case roundRestart:
			// The round was abandoned and does not count.
		}
	}
	entry.finishLoop(fmt.Sprintf("stopped after %d round(s)", maxRounds))
	return nil
}

// runRound runs one round. The returned reason is why the loop is over, when the
// outcome is roundStopped.
func (d *daemon) runRound(ctx context.Context, entry *sessionEntry, root string, prompts []review.Prompt, req SessionRequest, st *loopState) (outcome, string, error) {
	ridx := entry.beginRound(prompts)
	pool, err := d.openRoundPool(ctx, root, len(prompts), req)
	if err != nil {
		return 0, "", err
	}
	defer pool.close(ctx)

	results, err := d.runReviews(ctx, entry, ridx, root, prompts, req, pool)
	entry.finishReviews(ridx, results)
	if err != nil {
		return 0, "", err
	}
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}

	worth := entry.worthFindings(ridx)
	if len(worth) == 0 {
		entry.endRound(ridx, RoundDone, "no findings worth changing")
		return roundStopped, "no findings worth changing", nil
	}

	entry.startFix(ridx, worth)
	patch, err := d.fixOnSandbox(ctx, entry, ridx, root, pool, worth, req)
	if err != nil {
		entry.failFix(ridx, err)
		return 0, "", fmt.Errorf("fix: %w", err)
	}
	if strings.TrimSpace(patch) == "" {
		entry.endFix(ridx, FixEmpty, nil)
		entry.endRound(ridx, RoundDone, "the agent made no changes")
		return roundStopped, "the agent made no changes", nil
	}
	if err := checkPatchPaths(patch); err != nil {
		entry.failFix(ridx, err)
		return 0, "", err
	}

	// The reviewers and the fixer saw the files as they were when the round
	// began. If the user has edited since, the patch is against files that no
	// longer exist; applying it could overwrite their work.
	now, err := treeNow(root)
	if err != nil {
		return 0, "", err
	}
	if now != st.expected {
		entry.supersede(ridx, "files changed while the round ran; it will run again")
		if st.expected, err = d.pauseSession(ctx, entry, root, st.expected, now); err != nil {
			return 0, "", err
		}
		return roundRestart, "", nil
	}

	entry.setRoundState(ridx, RoundApplying)
	if err := d.applyFixes(ctx, entry, ridx, root, patch, st); err != nil {
		entry.failFix(ridx, err)
		return 0, "", err
	}
	entry.endRound(ridx, RoundDone, "")
	return roundAdvanced, "", nil
}

// applyFixes saves the restore point if this is the first change, then writes
// the patch into the user's working tree.
func (d *daemon) applyFixes(ctx context.Context, entry *sessionEntry, ridx int, root, patch string, st *loopState) error {
	snap := entry.snapshot()
	patchPath, err := savePatch(snap.ID, snap.Rounds[ridx].Number, patch)
	if err != nil {
		return err
	}
	if snap.Restore == nil {
		// Before the first byte is written: the tree is exactly st.expected.
		rp, err := saveRestorePoint(ctx, root, snap.ID, st.expected)
		if err != nil {
			return fmt.Errorf("could not save a restore point, so nothing was changed: %w", err)
		}
		entry.setRestore(rp)
	}

	files, err := applyPatchToTree(ctx, root, patchPath)
	if err != nil {
		return err
	}
	for _, f := range files {
		st.touched = appendUnique(st.touched, f.Path)
	}
	after, err := treeNow(root)
	if err != nil {
		return err
	}
	st.expected = after
	entry.recordFix(ridx, files, st.touched, after)
	return nil
}

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

// pauseSession stops the session until the user decides, because the files
// changed underneath it. It returns the working tree to adopt as the new
// baseline once resumed. Nothing is overwritten while it waits.
func (d *daemon) pauseSession(ctx context.Context, entry *sessionEntry, root, expected, now string) (string, error) {
	paths, err := changedBetween(ctx, root, expected, now)
	if err != nil {
		return "", err
	}
	entry.pause(pauseReason(paths), paths)

	select {
	case <-entry.resume:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	// Adopt whatever the files are now, not what they were when paused: the user
	// may have kept editing while deciding.
	latest, err := treeNow(root)
	if err != nil {
		return "", err
	}
	entry.unpause()
	return latest, nil
}

// pauseReason describes what changed, for a person to read.
func pauseReason(paths []string) string {
	const show = 5
	shown := paths
	more := ""
	if len(paths) > show {
		shown = paths[:show]
		more = fmt.Sprintf(" and %d more", len(paths)-show)
	}
	return "files changed while the session was running: " + strings.Join(shown, ", ") + more
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

func (e *sessionEntry) endFix(ridx int, state FixState, files []FileChange) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fix := e.s.Rounds[ridx].Fix
	fix.State, fix.Files = state, files
	for _, f := range files {
		fix.Insertions += f.Insertions
		fix.Deletions += f.Deletions
	}
}

func (e *sessionEntry) failFix(ridx int, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if fix := e.s.Rounds[ridx].Fix; fix != nil {
		fix.State, fix.Error = FixFailed, err.Error()
	}
}

// supersede marks a round abandoned because the files moved under it.
func (e *sessionEntry) supersede(ridx int, note string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	r := &e.s.Rounds[ridx]
	r.State, r.Note, r.EndedAt = RoundSuperseded, note, &now
	if r.Fix != nil && r.Fix.State == FixRunning {
		r.Fix.State, r.Fix.Error = FixFailed, "not applied: files changed"
	}
}

// recordFix stores what a round's fixes did to the files, and where the files
// stood afterwards.
func (e *sessionEntry) recordFix(ridx int, files []FileChange, touched []string, after string) {
	e.endFix(ridx, FixApplied, files)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.leftTree = after
	if e.s.Restore != nil {
		e.s.Restore.Paths = append([]string(nil), touched...)
	}
}

func (e *sessionEntry) setRestore(rp RestorePoint) {
	e.mu.Lock()
	e.s.Restore = &rp
	e.mu.Unlock()
}

// pause puts the session in the paused state.
func (e *sessionEntry) pause(reason string, paths []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.s.State = SessionPaused
	e.s.PauseReason, e.s.PausedPaths = reason, paths
	if st := e.stageLocked(StageReviewLoop); st != nil {
		st.State, st.Note = StagePaused, reason
	}
}

func (e *sessionEntry) unpause() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.s.State = SessionRunning
	e.s.PauseReason, e.s.PausedPaths = "", nil
	if st := e.stageLocked(StageReviewLoop); st != nil {
		st.State, st.Note = StageRunning, ""
	}
}

// finishLoop records why the loop ended, to be shown on the stage.
func (e *sessionEntry) finishLoop(reason string) {
	e.mu.Lock()
	e.loopNote = reason
	e.mu.Unlock()
}

// resumeSession wakes a paused session so it adopts the files as they are now
// and runs the round again.
func (d *daemon) resumeSession(id string) error {
	entry := d.sessions.get(id)
	if entry == nil {
		return apiErr(http.StatusNotFound, "no such session")
	}
	if entry.snapshot().State != SessionPaused {
		return apiErr(http.StatusConflict, "the session is not paused")
	}
	select {
	case entry.resume <- struct{}{}:
	default: // a resume is already on its way
	}
	return nil
}

// restoreFiles undoes everything a session changed in the working tree, using
// the restore point it saved before the first change.
//
// It refuses while the session is still going, since the loop would be writing
// to the same files, and refuses to overwrite edits the user made after the
// session left the files unless asked to force it.
func (d *daemon) restoreFiles(ctx context.Context, id string, force bool) (RestoreResult, error) {
	entry := d.sessions.get(id)
	if entry == nil {
		return RestoreResult{}, apiErr(http.StatusNotFound, "no such session")
	}
	entry.mu.Lock()
	sess := cloneSession(entry.s)
	left := entry.leftTree
	entry.mu.Unlock()

	switch {
	case !sess.State.Finished():
		return RestoreResult{}, apiErr(http.StatusConflict, "the session is still %s; cancel it first", sess.State)
	case sess.Restore == nil:
		return RestoreResult{}, apiErr(http.StatusConflict, "this session did not change any files")
	case sess.Restore.Restored:
		return RestoreResult{}, apiErr(http.StatusConflict, "this session was already restored")
	}

	paths, err := restoreSession(ctx, sess.ProjectRoot, *sess.Restore, sess.Restore.Paths, left, force)
	var edited *EditedSinceError
	if errors.As(err, &edited) {
		return RestoreResult{}, apiErr(http.StatusConflict, "%s", edited.Error())
	}
	if err != nil {
		return RestoreResult{}, apiErr(http.StatusInternalServerError, "%v", err)
	}
	entry.mu.Lock()
	entry.s.Restore.Restored = true
	entry.mu.Unlock()
	return RestoreResult{Paths: paths}, nil
}
