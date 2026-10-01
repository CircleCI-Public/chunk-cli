package watchd

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// loopRig is a daemon wired to a stand-in sandbox that behaves like the real
// thing where it matters: "syncing" copies the user's files, dirt included, into
// a real git checkout, the scripts the daemon runs there are run for real, and
// the only things faked are the two Claude calls — what the reviewers say and
// what the fixing agent edits. The user's repository is real git in a temp dir.
type loopRig struct {
	t       *testing.T
	d       *daemon
	root    string
	sandbox string
	backend *fakeBackend

	// review is what the reviewers say, given the sandbox they are looking at.
	review func(sandbox string) string
	// fix is what the fixing agent edits in its sandbox.
	fix func(sandbox string)

	mu      sync.Mutex
	fixRuns int
}

func newLoopRig(t *testing.T, review func(sandbox string) string, fix func(sandbox string)) *loopRig {
	t.Helper()
	r := &loopRig{t: t, review: review, fix: fix, sandbox: filepath.Join(t.TempDir(), "sandbox")}
	r.backend = &fakeBackend{repoPath: r.sandbox}
	r.d, r.root = newSessionDaemon(t, r.backend)

	// The user's project: a committed app.go with a bug in it, plus the dirt of a
	// developer mid-task, none of which a fix may touch.
	put(t, r.root, "app.go", "BUG\n")
	put(t, r.root, "other.txt", "o0\n")
	put(t, r.root, "staged.txt", "s0\n")
	git(t, r.root, "add", "app.go", "other.txt", "staged.txt")
	git(t, r.root, "commit", "-q", "-m", "files")
	put(t, r.root, "other.txt", "o1\n")  // unstaged edit
	put(t, r.root, "staged.txt", "s1\n") // staged edit
	git(t, r.root, "add", "staged.txt")
	put(t, r.root, "notes.txt", "mine\n") // untracked

	r.backend.onPool = func(ReviewPoolSpec) {
		assert.NilError(t, os.RemoveAll(r.sandbox))
		assert.NilError(t, exec.Command("cp", "-a", r.root, r.sandbox).Run())
	}
	r.backend.respond = func(script string) (string, int) {
		switch {
		case strings.Contains(script, "git diff --binary"), strings.Contains(script, "git write-tree"):
			out, err := exec.Command("sh", "-c", script).Output()
			if err != nil {
				return string(out), 1
			}
			return string(out), 0
		case strings.Contains(script, "Glob,Edit,Write"): // the fixing agent's tool list, not a path
			r.mu.Lock()
			r.fixRuns++
			r.mu.Unlock()
			r.fix(r.sandbox)
			return "fixed", 0
		}
		return r.review(r.sandbox), 0
	}
	return r
}

func (r *loopRig) start(req SessionRequest) SessionDetail {
	r.t.Helper()
	req.ProjectRoot = r.root
	sess, err := r.d.startSession(req)
	assert.NilError(r.t, err)
	return waitForSession(r.t, r.d, sess.ID)
}

func (r *loopRig) fixCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fixRuns
}

// reportsBug is a reviewer that finds the bug while it is there.
func reportsBug(t *testing.T) func(string) string {
	return func(sandbox string) string {
		data, err := os.ReadFile(filepath.Join(sandbox, "app.go"))
		assert.NilError(t, err)
		if !strings.Contains(string(data), "BUG") {
			return findingsOutput(t)
		}
		return findingsOutput(t, review.Finding{File: "app.go", Line: 1, Severity: "high", Body: "BUG must go"})
	}
}

func fixesBug(t *testing.T) func(string) {
	return func(sandbox string) { put(t, sandbox, "app.go", "FIXED\n") }
}

// userState captures everything a fix must leave alone.
func userState(t *testing.T, r *loopRig) (index, head string) {
	t.Helper()
	return git(t, r.root, "ls-files", "--stage"), git(t, r.root, "rev-parse", "HEAD")
}

// A round in which no review produced a result has not found the code clean.
func TestLoopFailsWhenEveryReviewFails(t *testing.T) {
	r := newLoopRig(t, nil, nil)
	r.review = func(string) string { return "this is not a claude result" }

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.State, SessionFailed)
	assert.Assert(t, strings.Contains(detail.Error, "every review failed"), detail.Error)
	assert.Equal(t, len(detail.Rounds), 1)
	assert.Equal(t, detail.Rounds[0].State, RoundFailed)
	assert.Equal(t, r.fixCount(), 0, "nothing was reviewed, so nothing is fixed")
}

func TestLoopFixesTheUsersFilesThenStopsWhenNothingIsLeft(t *testing.T) {
	r := newLoopRig(t, nil, nil)
	r.review, r.fix = reportsBug(t), fixesBug(t)
	original, err := treeNow(r.root)
	assert.NilError(t, err)
	indexBefore, headBefore := userState(t, r)

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.State, SessionDone, detail.Error)
	assert.Equal(t, len(detail.Rounds), 2, "one round to fix the bug, one to see nothing is left")
	r1, r2 := detail.Rounds[0], detail.Rounds[1]
	assert.Equal(t, r1.State, RoundDone)
	assert.Equal(t, r1.Worth, 1)
	assert.Equal(t, r1.Fix.State, FixApplied)
	assert.DeepEqual(t, r1.Fix.Files, []FileChange{{Path: "app.go", Insertions: 1, Deletions: 1}})
	assert.Equal(t, r2.Worth, 0)
	assert.Assert(t, r2.Fix == nil)
	assert.Equal(t, r2.Note, "no findings worth changing")
	assert.Equal(t, detail.Stages[0].State, StageDone)
	assert.Equal(t, detail.Stages[0].Note, "no findings worth changing")

	// The fix is in the user's files; everything of theirs is exactly as it was.
	assert.Equal(t, read(t, r.root, "app.go"), "FIXED\n")
	assert.Equal(t, read(t, r.root, "other.txt"), "o1\n")
	assert.Equal(t, read(t, r.root, "notes.txt"), "mine\n")
	index, head := userState(t, r)
	assert.Equal(t, index, indexBefore, "the index is not the session's to touch")
	assert.Equal(t, head, headBefore)

	// One command undoes the lot.
	assert.Assert(t, detail.Restore != nil)
	assert.DeepEqual(t, detail.Restore.Paths, []string{"app.go"})
	res, err := r.d.restoreFiles(t.Context(), detail.ID, false)
	assert.NilError(t, err)
	assert.DeepEqual(t, res.Paths, []string{"app.go"})
	after, err := treeNow(r.root)
	assert.NilError(t, err)
	assert.Equal(t, after, original, "the working tree is exactly as it was before the session")

	_, err = r.d.restoreFiles(t.Context(), detail.ID, false)
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae) && ae.status == http.StatusConflict, "a second restore is refused: %v", err)
}

func TestLoopRunsAtMostThreeRounds(t *testing.T) {
	// Always something to fix: the reviewer reports while n.txt exists, and the
	// agent only ever bumps a counter.
	for _, tc := range []struct{ requested, want int }{{0, 3}, {2, 2}, {9, 3}} {
		counter := func(sandbox string) int {
			data, err := os.ReadFile(filepath.Join(sandbox, "n.txt"))
			if err != nil {
				return 0
			}
			n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			return n
		}
		r := newLoopRig(t, nil, nil)
		put(t, r.root, "n.txt", "0\n")
		git(t, r.root, "add", "n.txt")
		git(t, r.root, "commit", "-q", "-m", "counter")
		r.review = func(string) string {
			return findingsOutput(t, review.Finding{File: "n.txt", Line: 1, Severity: "medium", Body: "still not done"})
		}
		r.fix = func(sandbox string) { put(t, sandbox, "n.txt", strconv.Itoa(counter(sandbox)+1)+"\n") }

		detail := r.start(SessionRequest{MaxRounds: tc.requested})

		assert.Equal(t, detail.State, SessionDone, detail.Error)
		assert.Equal(t, len(detail.Rounds), tc.want, "requested %d", tc.requested)
		assert.Equal(t, r.fixCount(), tc.want)
		assert.Equal(t, read(t, r.root, "n.txt"), strconv.Itoa(tc.want)+"\n")
		assert.Equal(t, detail.Stages[0].Note, "stopped after "+strconv.Itoa(tc.want)+" round(s)")
		assert.DeepEqual(t, detail.Restore.Paths, []string{"n.txt"})
		r.d.sessions.stopAll()
	}
}

func TestLoopLeavesLowSeverityFindingsAloneAndWritesNothing(t *testing.T) {
	r := newLoopRig(t, nil, nil)
	r.review = func(string) string {
		return findingsOutput(t,
			review.Finding{File: "app.go", Line: 1, Severity: "low", Body: "naming"},
			review.Finding{File: "app.go", Line: 1, Severity: "info", Body: "fyi"})
	}
	r.fix = func(string) { t.Error("the fixing agent must not run for findings that are not worth changing") }
	before, err := treeNow(r.root)
	assert.NilError(t, err)

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.State, SessionDone)
	assert.Equal(t, len(detail.Rounds), 1)
	assert.Equal(t, detail.Rounds[0].Findings, 2)
	assert.Equal(t, detail.Rounds[0].Worth, 0)
	assert.Equal(t, r.fixCount(), 0)
	assert.Assert(t, detail.Restore == nil, "no change, so no restore point")
	after, err := treeNow(r.root)
	assert.NilError(t, err)
	assert.Equal(t, after, before)
}

// The user keeps typing while the round runs. The session must notice, stop, and
// say so — not apply a patch made for files that no longer exist.
func TestFilesChangedMidRoundPauseTheSessionInsteadOfOverwriting(t *testing.T) {
	r := newLoopRig(t, nil, nil)
	r.review = reportsBug(t)
	typed := false
	r.fix = func(sandbox string) {
		fixesBug(t)(sandbox)
		if !typed { // the user edits their own copy while the agent works on the sandbox
			typed = true
			put(t, r.root, "mine.txt", "typed during the round\n")
		}
	}
	original := read(t, r.root, "app.go")

	sess, err := r.d.startSession(SessionRequest{ProjectRoot: r.root})
	assert.NilError(t, err)
	paused := waitForSession(t, r.d, sess.ID)

	assert.Equal(t, paused.State, SessionPaused)
	assert.Assert(t, strings.Contains(paused.PauseReason, "mine.txt"), paused.PauseReason)
	assert.DeepEqual(t, paused.PausedPaths, []string{"mine.txt"})
	assert.Equal(t, paused.Stages[0].State, StagePaused)
	assert.Equal(t, paused.Rounds[0].State, RoundSuperseded)
	// Nothing was written: no fix landed and no restore point was needed.
	assert.Equal(t, read(t, r.root, "app.go"), original)
	assert.Equal(t, read(t, r.root, "mine.txt"), "typed during the round\n")
	assert.Assert(t, paused.Restore == nil)

	// While paused it can neither be restored (nothing to restore) nor resumed twice.
	_, err = r.d.restoreFiles(t.Context(), sess.ID, false)
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae) && ae.status == http.StatusConflict, "got %v", err)

	// Resume: the files as they are now become the baseline and the round runs again.
	assert.NilError(t, r.d.resumeSession(sess.ID))
	done := waitForSessionEnd(t, r.d, sess.ID)

	assert.Equal(t, done.State, SessionDone, done.Error)
	var states []string
	var numbers []int
	for _, rd := range done.Rounds {
		states = append(states, string(rd.State))
		numbers = append(numbers, rd.Number)
	}
	assert.DeepEqual(t, states, []string{"superseded", "done", "done"})
	assert.DeepEqual(t, numbers, []int{1, 1, 2})
	assert.Equal(t, read(t, r.root, "app.go"), "FIXED\n")
	assert.Equal(t, read(t, r.root, "mine.txt"), "typed during the round\n", "what the user typed survives")
	assert.Equal(t, done.PauseReason, "")
	assert.Equal(t, done.Stages[0].State, StageDone)
}

func TestCancellingAPausedSessionLeavesTheFilesAlone(t *testing.T) {
	r := newLoopRig(t, nil, nil)
	r.review = reportsBug(t)
	r.fix = func(sandbox string) {
		fixesBug(t)(sandbox)
		put(t, r.root, "mine.txt", "typed\n")
	}
	sess, err := r.d.startSession(SessionRequest{ProjectRoot: r.root})
	assert.NilError(t, err)
	assert.Equal(t, waitForSession(t, r.d, sess.ID).State, SessionPaused)

	found, active := r.d.sessions.cancelSession(sess.ID)
	assert.Assert(t, found && active)
	<-r.d.sessions.get(sess.ID).done

	got := r.d.sessions.get(sess.ID).snapshot()
	assert.Equal(t, got.State, SessionCancelled)
	assert.Equal(t, read(t, r.root, "app.go"), "BUG\n")
	assert.Assert(t, got.Restore == nil)
	// Resuming something that is not paused is refused.
	var ae *apiError
	assert.Assert(t, errors.As(r.d.resumeSession(sess.ID), &ae) && ae.status == http.StatusConflict)
	assert.Assert(t, errors.As(r.d.resumeSession("nope"), &ae) && ae.status == http.StatusNotFound)
}

func TestAFixThatEditsCIConfigIsRefusedAndNothingIsWritten(t *testing.T) {
	r := newLoopRig(t, nil, nil)
	r.review = reportsBug(t)
	r.fix = func(sandbox string) {
		fixesBug(t)(sandbox)
		put(t, sandbox, ".github/workflows/ci.yml", "on: push\n")
	}
	before, err := treeNow(r.root)
	assert.NilError(t, err)

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.State, SessionFailed)
	assert.Assert(t, strings.Contains(detail.Error, "may not edit"), detail.Error)
	assert.Equal(t, detail.Rounds[0].Fix.State, FixFailed)
	assert.Assert(t, detail.Restore == nil)
	after, err := treeNow(r.root)
	assert.NilError(t, err)
	assert.Equal(t, after, before, "a refused fix must not partly land")
}

// With no restore point there is no safe way to write, so nothing is written.
func TestNothingIsWrittenWhenARestorePointCannotBeSaved(t *testing.T) {
	r := newLoopRig(t, nil, nil)
	r.review, r.fix = reportsBug(t), fixesBug(t)
	// A ref named refs/chunk/restore blocks refs/chunk/restore/<id> (a ref cannot
	// be both a file and a directory).
	git(t, r.root, "update-ref", "refs/chunk/restore", "HEAD")
	before, err := treeNow(r.root)
	assert.NilError(t, err)

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.State, SessionFailed)
	assert.Assert(t, strings.Contains(detail.Error, "nothing was changed"), detail.Error)
	after, err := treeNow(r.root)
	assert.NilError(t, err)
	assert.Equal(t, after, before)
	assert.Equal(t, read(t, r.root, "app.go"), "BUG\n")
}

func TestRestoreNeedsAFinishedSessionThatChangedSomething(t *testing.T) {
	r := newLoopRig(t, nil, nil)
	r.review = func(string) string { return "Looks good." }
	r.fix = func(string) {}
	detail := r.start(SessionRequest{})

	_, err := r.d.restoreFiles(t.Context(), detail.ID, false)
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae) && ae.status == http.StatusConflict, "got %v", err)
	assert.Assert(t, strings.Contains(ae.msg, "did not change any files"), ae.msg)

	_, err = r.d.restoreFiles(t.Context(), "nope", false)
	assert.Assert(t, errors.As(err, &ae) && ae.status == http.StatusNotFound)
}

// waitForSessionEnd waits for a session that has been resumed to finish.
func waitForSessionEnd(t *testing.T, d *daemon, id string) SessionDetail {
	t.Helper()
	entry := d.sessions.get(id)
	waitUntil(t, "session to end", func() bool { return entry.snapshot().State.Finished() })
	<-entry.done
	return entry.detail()
}
