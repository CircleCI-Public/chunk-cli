package watchd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// fakeTree drives the store's view of the working tree so a test can move the
// tree mid-run without a git repo.
type fakeTree struct {
	mu   sync.Mutex
	wt   gitutil.Worktree
	err  error
	seen int
}

func (f *fakeTree) fingerprint(string) (gitutil.Worktree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen++
	return f.wt, f.err
}

func (f *fakeTree) set(wt gitutil.Worktree, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wt, f.err = wt, err
}

func (f *fakeTree) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen
}

// newFakeStore returns a store whose tree state the caller controls.
func newFakeStore(t *testing.T, wt gitutil.Worktree) (*taskStore, *fakeTree) {
	t.Helper()
	tree := &fakeTree{wt: wt}
	s := newTaskStore(context.Background())
	s.fingerprint = tree.fingerprint
	// Identity, so these tests key by exactly the path they pass and never touch
	// the filesystem looking for a repo root. Real resolution is covered by the
	// tests that build a real repo.
	s.repoRoot = func(dir string) (string, error) { return dir, nil }
	t.Cleanup(s.stopAll)
	return s, tree
}

// assertDiscarded checks that a run the tree moved past was reported as
// discarded rather than as a verdict: the fact that it happened survives, the
// answer it produced does not.
func assertDiscarded(t *testing.T, got []TaskState) {
	t.Helper()
	assert.Equal(t, len(got), 1, "a discarded run was not reported at all, which reads as no run having happened")
	assert.Assert(t, got[0].Stale, "the run was reported as a verdict rather than as discarded")
	assert.Assert(t, !got[0].Passed(), "a discarded run must never read as passed")
	assert.Equal(t, got[0].ExitCode, 0, "a discarded run must not carry an exit code")
	assert.Equal(t, got[0].Output, "", "a discarded run must not carry output")
}

// collect is peek followed by acknowledge in one step: the finished tasks for
// root that nobody has been told about yet, including ones discarded as stale
// with their verdicts stripped, marked delivered on the way out.
//
// It lives here rather than on the store because no production caller can use
// it. Acknowledging is what makes a result unrepeatable, so it must not happen
// until whatever consumes the result has succeeded — in handleCollect,
// encoding and writing the response sit between the two halves and either can
// fail. A test has nothing in between, which is the only place collapsing them
// is safe, and keeping it out of the store means nobody adding a second
// endpoint can reach for it without noticing the ordering it skips.
func (s *taskStore) collect(root string) []TaskState {
	tasks := s.peek(root)
	s.acknowledge(tasks)
	return tasks
}

func tree(head, digest string) gitutil.Worktree {
	return gitutil.Worktree{Head: head, Digest: digest}
}

// waitFor polls cond until it holds, so a test never sleeps on a goroutine.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition never held: %s", msg)
}

// The ordinary case: nothing was edited while the run was in flight, so the
// answer still describes the tree and is handed to whoever collects it.
func TestCollectReturnsAResultForAnUnchangedTree(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", false, func(context.Context) (int, string) {
		return 0, "1/1 passed"
	})
	assert.NilError(t, err)

	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	got := s.collect("/repo")
	assert.Equal(t, len(got), 1)
	assert.Equal(t, got[0].ExitCode, 0)
	assert.Equal(t, got[0].Output, "1/1 passed")
	assert.Equal(t, got[0].Stale, false)
	assert.Assert(t, got[0].Passed(), "a zero exit on an unchanged tree must read as passed")
}

// The reason this store exists. An edit lands while the run is going, so the
// result describes code that is no longer on disk and must not be reported as
// if it described the tree the agent is now looking at.
func TestAnEditDuringTheRunDiscardsTheResult(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	release := make(chan struct{})
	_, err := s.start("/repo", false, func(context.Context) (int, string) {
		<-release
		return 0, "1/1 passed"
	})
	assert.NilError(t, err)

	// The edit: same commit, different content digest.
	fake.set(tree("abc", "d2"), nil)
	close(release)

	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	assertDiscarded(t, s.collect("/repo"))
	// Reported once. The discard is news the first time and noise after it.
	assert.Equal(t, len(s.collect("/repo")), 0)
}

// A commit during the run moves HEAD without touching the digest, and is just
// as much a different tree as an edit is.
func TestACommitDuringTheRunDiscardsTheResult(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	release := make(chan struct{})
	_, err := s.start("/repo", false, func(context.Context) (int, string) {
		<-release
		return 0, "ok"
	})
	assert.NilError(t, err)

	fake.set(tree("def", "d1"), nil)
	close(release)

	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")
	assertDiscarded(t, s.collect("/repo"))
}

// Failures are collected like passes. Whether a failure should block is the
// caller's decision; the store's job is to have the answer ready.
func TestAFailureIsCollectedToo(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", false, func(context.Context) (int, string) {
		return 1, "0/1 passed"
	})
	assert.NilError(t, err)

	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	got := s.collect("/repo")
	assert.Equal(t, len(got), 1)
	assert.Equal(t, got[0].ExitCode, 1)
	assert.Assert(t, !got[0].Passed(), "a non-zero exit must not read as passed")
}

// Staleness is only detectable against a recorded fingerprint, so a tree that
// cannot be fingerprinted must not be run asynchronously at all. The caller
// falls back to a synchronous run, where the answer reaches the asker while it
// is still true.
func TestStartRefusesATreeItCannotFingerprint(t *testing.T) {
	s, fake := newFakeStore(t, gitutil.Worktree{})
	fake.set(gitutil.Worktree{}, errors.New("not a git repository"))

	var ran bool
	_, err := s.start("/repo", false, func(context.Context) (int, string) {
		ran = true
		return 0, ""
	})
	assert.Assert(t, err != nil, "a tree with no fingerprint must not start an async run")
	assert.Equal(t, ran, false, "the run started despite having no baseline to compare against")
	assert.Equal(t, len(s.inFlight("/repo")), 0)
}

// The tree was fingerprintable at the start and is not at the end — a submodule
// went dirty, or the changed set outgrew the digest budget. There is no evidence
// the result still holds, so it is treated as stale rather than reported.
func TestATreeThatCannotBeFingerprintedAtTheEndIsStale(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	release := make(chan struct{})
	_, err := s.start("/repo", false, func(context.Context) (int, string) {
		<-release
		return 0, "ok"
	})
	assert.NilError(t, err)

	fake.set(gitutil.Worktree{}, errors.New("dirty submodule"))
	close(release)

	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")
	assertDiscarded(t, s.collect("/repo"))
}

// A result reaches a caller once. Repeating it on the following turn would tell
// an agent its current work had been validated by a run that predates it.
func TestCollectReportsAResultOnlyOnce(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	assert.Equal(t, len(s.collect("/repo")), 1)
	assert.Equal(t, len(s.collect("/repo")), 0, "the same result was reported twice")
}

// Collecting mid-run must not consume a task that has not concluded, or its
// answer would be lost the moment a turn happened to start while it ran.
func TestCollectLeavesRunningTasksAlone(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	release := make(chan struct{})
	_, err := s.start("/repo", false, func(context.Context) (int, string) {
		<-release
		return 0, "ok"
	})
	assert.NilError(t, err)

	assert.Equal(t, len(s.collect("/repo")), 0, "a running task was collected")
	assert.Equal(t, len(s.inFlight("/repo")), 1, "a running task was forgotten by collect")

	close(release)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")
	assert.Equal(t, len(s.collect("/repo")), 1, "the result was lost by the earlier collect")
}

// Two runs in flight at once are tracked apart. The daemon serialises validate
// today, but the store must not be the thing that assumes it.
func TestTwoRunsInFlightAreTrackedSeparately(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	release := make(chan struct{})
	first, err := s.start("/repo", false, func(context.Context) (int, string) {
		<-release
		return 0, "first"
	})
	assert.NilError(t, err)
	second, err := s.start("/repo", false, func(context.Context) (int, string) {
		<-release
		return 1, "second"
	})
	assert.NilError(t, err)
	assert.Assert(t, first != second, "two runs shared one task ID")
	assert.Equal(t, len(s.inFlight("/repo")), 2)

	close(release)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "runs never finished")
	assert.Equal(t, len(s.collect("/repo")), 2)
}

// Tasks are filed per project, so one repo's results never surface in another.
func TestTasksAreKeptPerProject(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo-a", false, func(context.Context) (int, string) { return 0, "a" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo-a")) == 0 }, "run never finished")

	assert.Equal(t, len(s.collect("/repo-b")), 0, "another project's result leaked")
	got := s.collect("/repo-a")
	assert.Equal(t, len(got), 1)
	assert.Equal(t, got[0].Output, "a")
}

// Eviction keeps the store bounded, but a run that has not reported yet is not
// a candidate: dropping it would lose work about to produce an answer. So a
// project already holding its cap of finished results evicts one of those to
// make room, and the live run is what survives.
func TestEvictionNeverDropsARunningTask(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	for i := 0; i < MaxTasksPerProject; i++ {
		_, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "ok" })
		assert.NilError(t, err)
		waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")
	}

	// One more, still going: the store is at its cap, so taking it means
	// evicting something, and the only safe victims are the finished ones.
	release := make(chan struct{})
	live, err := s.start("/repo", false, func(context.Context) (int, string) {
		<-release
		return 0, "ok"
	})
	assert.NilError(t, err)

	assert.Equal(t, len(s.byProject["/repo"]), MaxTasksPerProject, "the cap was not enforced")
	_, kept := s.tasks[live]
	assert.Assert(t, kept, "the running task was evicted to stay under the cap")

	close(release)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")
}

// Finished tasks are evicted oldest-first once a project is over the cap, so a
// project nobody collects from cannot grow without bound.
func TestFinishedTasksAreEvictedOverTheCap(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	for i := 0; i < MaxTasksPerProject+5; i++ {
		_, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "ok" })
		assert.NilError(t, err)
		waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")
	}

	assert.Equal(t, len(s.collect("/repo")), MaxTasksPerProject)
}

// Handing a result back and it arriving are not the same event. Deleting on read
// means a validation failure vanishes with nothing left to show it existed, so
// the task stays and is stamped instead.
func TestADeliveredResultIsKeptAfterBeingReported(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	id, err := s.start("/repo", false, func(context.Context) (int, string) { return 1, "1 test failed" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	got := s.collect("/repo")
	assert.Equal(t, len(got), 1)

	s.mu.Lock()
	entry, ok := s.tasks[id]
	s.mu.Unlock()
	assert.Assert(t, ok, "the task was deleted on read, so nothing records that the run happened")
	assert.Assert(t, !entry.state.DeliveredAt.IsZero(), "a reported result was not stamped delivered")
	assert.Equal(t, entry.state.ExitCode, 1, "the retained copy must still carry the result")
	assert.Equal(t, entry.state.Output, "1 test failed")
}

// The point of splitting the read from the acknowledgement: a result handed over
// but never acknowledged is still owed to somebody. This is the shape of a
// response the caller never received — the hook killed on its timeout, the
// socket write failing, the client gone before the daemon answers — and it must
// come back rather than being counted as told.
func TestAPeekedButUnacknowledgedResultIsReportedAgain(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", false, func(context.Context) (int, string) { return 1, "1 test failed" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	first := s.peek("/repo")
	assert.Equal(t, len(first), 1)
	// No acknowledge: the response never reached anybody.

	second := s.peek("/repo")
	assert.Equal(t, len(second), 1, "an unacknowledged failure was dropped instead of re-reported")
	assert.Equal(t, second[0].ID, first[0].ID)
	assert.Equal(t, second[0].ExitCode, 1)

	// Acknowledged, and only then does it stop coming back.
	s.acknowledge(second)
	assert.Equal(t, len(s.peek("/repo")), 0, "an acknowledged result was reported twice")
}

// Peeking must not report a task twice within one pass, nor lose the ones it
// does not acknowledge from the project index.
func TestAcknowledgeOnlyStampsWhatItIsGiven(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	for range 3 {
		_, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "ok" })
		assert.NilError(t, err)
	}
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "runs never finished")

	peeked := s.peek("/repo")
	assert.Equal(t, len(peeked), 3)

	// Acknowledge one of the three, as a partial write would.
	s.acknowledge(peeked[:1])

	remaining := s.peek("/repo")
	assert.Equal(t, len(remaining), 2, "acknowledging one result silenced the others")
	for _, t2 := range remaining {
		assert.Assert(t, t2.ID != peeked[0].ID, "an acknowledged result came back")
	}
}

// Stamping is what stops a repeat, so it has to hold across the tree moving
// afterwards: a delivered result must not come back as new because an edit
// landed and the staleness check now has something to say about it.
func TestADeliveredResultIsNotReportedAgainAfterAnEdit(t *testing.T) {
	s, tr := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	assert.Equal(t, len(s.collect("/repo")), 1)
	tr.set(tree("abc", "d2"), nil)
	assert.Equal(t, len(s.collect("/repo")), 0, "a delivered result was reported a second time")
}

// The answer is discarded; the fact that a run was discarded is not. The
// commonest reason the tree moved is the run itself — coverage output written, a
// golden regenerated — and dropping that in silence is indistinguishable from no
// run having happened, so a project whose commands are not perfectly gitignored
// would get nothing every turn with no way to tell why.
func TestAStaleResultIsReportedAsDiscardedRatherThanDropped(t *testing.T) {
	s, tr := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", false, func(context.Context) (int, string) { return 1, "0/1 passed" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	tr.set(tree("abc", "d2"), nil)
	assertDiscarded(t, s.collect("/repo"))

	// Reported once, then quiet: a discard repeated every turn is noise.
	assert.Equal(t, len(s.collect("/repo")), 0, "the same discard was reported twice")
}

// Delivered results are the first thing reclaimed at the cap. They are kept only
// against a lost response, while an unread one is still owed to somebody, so
// evicting oldest-first regardless would drop a report to preserve a retry.
//
// The unread task is deliberately the older of the two, which is what separates
// "delivered first" from plain oldest-first: it is held running across the
// collect that delivers the newer one, so it is still unread when it finishes.
func TestEvictionTakesDeliveredResultsBeforeUnreadOnes(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	// Waits on one task rather than on the project going quiet: a run is held
	// open on purpose here, so inFlight is never empty until the end.
	done := func(id string) func() bool {
		return func() bool {
			for _, ts := range s.inFlight("/repo") {
				if ts.ID == id {
					return false
				}
			}
			return true
		}
	}
	runAndFinish := func() string {
		id, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "ok" })
		assert.NilError(t, err)
		waitFor(t, done(id), "run never finished")
		return id
	}

	release := make(chan struct{})
	unread, err := s.start("/repo", false, func(context.Context) (int, string) {
		<-release
		return 0, "ok"
	})
	assert.NilError(t, err)

	delivered := runAndFinish()
	got := s.collect("/repo")
	assert.Equal(t, len(got), 1, "only the finished run was collectable")
	assert.Equal(t, got[0].ID, delivered)

	close(release)
	waitFor(t, done(unread), "the held run never finished")

	// Push the project one past the cap, so exactly one task has to go.
	for i := 0; i < MaxTasksPerProject-1; i++ {
		runAndFinish()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Equal(t, len(s.byProject["/repo"]), MaxTasksPerProject, "the cap was not enforced")
	_, deliveredKept := s.tasks[delivered]
	assert.Assert(t, !deliveredKept, "a delivered result was kept while an unread one was at risk")
	_, unreadKept := s.tasks[unread]
	assert.Assert(t, unreadKept, "an unread result was evicted to hold on to a delivered one")
}

// Shutdown cancels in-flight runs rather than leaving them to outlive the daemon.
func TestStopAllCancelsRunsInFlight(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	cancelled := make(chan struct{})
	_, err := s.start("/repo", false, func(ctx context.Context) (int, string) {
		<-ctx.Done()
		close(cancelled)
		return 1, "cancelled"
	})
	assert.NilError(t, err)

	s.stopAll()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("stopAll did not cancel the run in flight")
	}
}

// An edit after the run ended but before anyone read the result is the common
// shape, and the one a completion-time check alone misses: the run finishes in
// seconds, the developer keeps typing, and the result is read a minute later.
// It was true when recorded and is not true when read.
func TestAnEditAfterTheRunFinishedDiscardsTheResult(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	// The run has already concluded against an unchanged tree. The edit lands
	// only now, between finishing and being read.
	fake.set(tree("abc", "d2"), nil)

	assertDiscarded(t, s.collect("/repo"))
}

// Likewise a commit landing after the run ended.
func TestACommitAfterTheRunFinishedDiscardsTheResult(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	fake.set(tree("def", "d1"), nil)
	assertDiscarded(t, s.collect("/repo"))
}

// A tree that cannot be fingerprinted at read time cannot be shown to match the
// tree the run validated, so its verdict is withheld — reported as a discard,
// which says there is no answer rather than implying there is one.
func TestATreeThatCannotBeFingerprintedAtCollectWithholdsTheVerdict(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	fake.set(gitutil.Worktree{}, errors.New("dirty submodule"))
	assertDiscarded(t, s.collect("/repo"))
}

// The guard that keeps a discard from reading as success. Stripping the verdict
// leaves ExitCode at zero, so Passed must consult Stale or every discarded run
// reads as a clean pass to anything that asks — the dashboard, a later caller,
// the risk assessment upstream.
func TestAStaleTaskNeverReadsAsPassed(t *testing.T) {
	discarded := TaskState{Stale: true, ExitCode: 0}
	assert.Assert(t, !discarded.Passed(), "a discarded run read as passed")

	clean := TaskState{ExitCode: 0}
	assert.Assert(t, clean.Passed(), "a genuine pass stopped reading as passed")
}

// The tree is read once per collect rather than once per task, so a project
// holding several finished runs still costs one git call to report them.
func TestCollectReadsTheTreeOncePerCall(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	for i := 0; i < 3; i++ {
		_, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "ok" })
		assert.NilError(t, err)
		waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")
	}

	before := fake.calls()
	assert.Equal(t, len(s.collect("/repo")), 3)
	assert.Equal(t, fake.calls(), before+1, "collect read the tree more than once")
}

// gitRepo returns a temp git repo with one commit, for handler tests that need a
// tree the daemon can actually fingerprint.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "asyncrepo")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, "git %v: %s", args, out)
	}
	return dir
}

// The two ends of an async run do not agree on where they are. A developer can
// start one from a subdirectory — a package inside a monorepo, with its own
// .chunk/config.json — but the hook that reads results runs at the project root.
// Keyed by the directory each one happened to be in, the run is filed under
// /repo/sub and the lookup asks for /repo, so the result is never delivered and
// nothing is ever reported about it.
//
// Uses a real repo and the real gitutil helpers: the whole question is what git
// considers one project, which a fake would only restate.
func TestATaskStartedInASubdirectoryIsCollectedAtTheRepoRoot(t *testing.T) {
	repo := gitRepo(t)
	sub := filepath.Join(repo, "packages", "api")
	assert.NilError(t, os.MkdirAll(sub, 0o755))

	s := newTaskStore(context.Background())
	t.Cleanup(s.stopAll)

	_, err := s.start(sub, false, func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight(repo)) == 0 }, "run never finished")

	got := s.collect(repo)
	assert.Equal(t, len(got), 1, "a run started in a subdirectory was not collectable at the repo root")
	assert.Equal(t, got[0].ExitCode, 0)
}

// And the same key from the other direction: asking from a subdirectory must
// find a run started at the root, or a developer who moves between the two is
// told different things depending on where they are standing.
func TestATaskStartedAtTheRepoRootIsCollectableFromASubdirectory(t *testing.T) {
	repo := gitRepo(t)
	sub := filepath.Join(repo, "packages", "api")
	assert.NilError(t, os.MkdirAll(sub, 0o755))

	s := newTaskStore(context.Background())
	t.Cleanup(s.stopAll)

	_, err := s.start(repo, false, func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight(sub)) == 0 }, "run never finished")

	assert.Equal(t, len(s.collect(sub)), 1, "a run started at the repo root was invisible from a subdirectory")
}

// In-flight has to agree with collect on what a project is, since it is what the
// dashboard reads and what a caller checks before queueing another run.
func TestInFlightFindsASubdirectoryRunAtTheRepoRoot(t *testing.T) {
	repo := gitRepo(t)
	sub := filepath.Join(repo, "packages", "api")
	assert.NilError(t, os.MkdirAll(sub, 0o755))

	s := newTaskStore(context.Background())
	t.Cleanup(s.stopAll)

	release := make(chan struct{})
	_, err := s.start(sub, false, func(context.Context) (int, string) {
		<-release
		return 0, "ok"
	})
	assert.NilError(t, err)

	assert.Equal(t, len(s.inFlight(repo)), 1, "a subdirectory run was not in flight at the repo root")
	close(release)
	waitFor(t, func() bool { return len(s.inFlight(repo)) == 0 }, "run never finished")
}

// Two repos must not collapse into one another just because keys are now
// resolved: the normalisation walks up to a repo root, it does not widen.
func TestSeparateReposStillKeyApart(t *testing.T) {
	a, b := gitRepo(t), gitRepo(t)

	s := newTaskStore(context.Background())
	t.Cleanup(s.stopAll)

	_, err := s.start(a, false, func(context.Context) (int, string) { return 0, "a" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight(a)) == 0 }, "run never finished")

	assert.Equal(t, len(s.collect(b)), 0, "one repo's result surfaced in another")
	assert.Equal(t, len(s.collect(a)), 1)
}

// A directory outside any repo keys to itself rather than resolving to
// something surprising: there is no repo root to walk up to, so the key stays
// the directory as given. The run is accepted — hashing the tree gives the
// identity git has none to offer, see fingerprintTree — and the result comes
// back under that same directory.
func TestANonRepoKeyFallsBackToTheDirectory(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))

	s := newTaskStore(context.Background())
	t.Cleanup(s.stopAll)

	_, err := s.start(dir, false, func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight(dir)) == 0 }, "run never finished")
	assert.Equal(t, len(s.collect(dir)), 1, "the result was not filed under the directory itself")
}

// serve runs one request against the daemon's real mux, so the routes are
// covered rather than only the handler functions.
func serve(d *daemon, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	newServer(d).Handler.ServeHTTP(rec, req)
	return rec
}

func asyncReq(t *testing.T, root string) *http.Request {
	t.Helper()
	body, err := json.Marshal(ValidateRequest{ProjectRoot: root})
	assert.NilError(t, err)
	return httptest.NewRequest(http.MethodPost, "/validate/async", bytes.NewReader(body))
}

// The whole round trip over the daemon's API: a run is accepted, the caller is
// released before it finishes, and the result is there to be collected after.
func TestAsyncValidateEndpointAcceptsAndCollects(t *testing.T) {
	root := gitRepo(t)
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)

	release := make(chan struct{})
	d.runner = func(context.Context, string, []string, []string, io.Writer, io.Writer) int {
		<-release
		return 0
	}

	rec := serve(d, asyncReq(t, root))
	assert.Equal(t, rec.Code, http.StatusAccepted)
	var started AsyncValidateResponse
	assert.NilError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	assert.Assert(t, started.TaskID != "", "no task ID was returned")

	// Released while the run is still going: that is the point of the endpoint.
	assert.Equal(t, len(d.tasks.inFlight(root)), 1)

	close(release)
	waitFor(t, func() bool { return len(d.tasks.inFlight(root)) == 0 }, "run never finished")

	rec = serve(d, httptest.NewRequest(http.MethodGet, "/validate/collect?root="+url.QueryEscape(root), nil))
	assert.Equal(t, rec.Code, http.StatusOK)
	var collected CollectResponse
	assert.NilError(t, json.Unmarshal(rec.Body.Bytes(), &collected))
	assert.Equal(t, len(collected.Tasks), 1)
	assert.Equal(t, collected.Tasks[0].ID, started.TaskID)
	assert.Assert(t, collected.Tasks[0].Passed())
}

// A tree with no fingerprint is refused with 409 rather than 500: nothing is
// broken, this tree just cannot be validated asynchronously, and the caller is
// meant to read that as "run it inline" instead of as a daemon failure.
func TestAsyncValidateEndpointRefusesAnUnfingerprintableTree(t *testing.T) {
	// A path with nothing at it. Git cannot identify it and neither can hashing
	// it, so there is no baseline to detect staleness against — which is the
	// only condition that refuses a run now that a tree git has no answer for
	// can be identified by its contents instead. See fingerprintTree.
	dir := filepath.Join(t.TempDir(), "gone")

	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)
	var ran bool
	d.runner = func(context.Context, string, []string, []string, io.Writer, io.Writer) int {
		ran = true
		return 0
	}

	rec := serve(d, asyncReq(t, dir))
	assert.Equal(t, rec.Code, http.StatusConflict)
	assert.Equal(t, ran, false, "a run started against a tree with no baseline")
}

// A directory that is not a repository used to be refused, because git had no
// identity to give it and staleness would have been undetectable. Hashing it
// gives the same guarantee, so it is tracked like any other tree.
func TestAsyncValidateEndpointAcceptsATreeThatIsNotARepository(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))

	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)
	d.runner = func(context.Context, string, []string, []string, io.Writer, io.Writer) int { return 0 }

	rec := serve(d, asyncReq(t, dir))
	assert.Equal(t, rec.Code, http.StatusAccepted)
	waitFor(t, func() bool { return len(d.tasks.inFlight(dir)) == 0 }, "run never finished")
	assert.Equal(t, len(d.tasks.collect(dir)), 1, "the result was not kept")
}

// A result that cannot be attributed to a project could never be collected, so
// the request is rejected rather than accepted and lost.
func TestAsyncValidateEndpointRequiresAProjectRoot(t *testing.T) {
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)
	d.runner = func(context.Context, string, []string, []string, io.Writer, io.Writer) int { return 0 }

	rec := serve(d, asyncReq(t, ""))
	assert.Equal(t, rec.Code, http.StatusBadRequest)
}

// Without a runner there is nothing to run, and accepting the request would
// promise a result that never arrives.
func TestAsyncValidateEndpointNeedsARunner(t *testing.T) {
	root := gitRepo(t)
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)

	rec := serve(d, asyncReq(t, root))
	assert.Equal(t, rec.Code, http.StatusServiceUnavailable)
}

// failingWriter accepts the header and then fails the body write, which is what
// a client that has hung up looks like to a handler.
type failingWriter struct {
	header http.Header
	code   int
}

func (f *failingWriter) Header() http.Header {
	if f.header == nil {
		f.header = make(http.Header)
	}
	return f.header
}

func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("connection reset") }
func (f *failingWriter) WriteHeader(code int)      { f.code = code }

// The ordering is the fix. Acknowledging inside the encode call marks a result
// delivered before a byte has left the process, so a client that is already gone
// takes the result with it — and the client turns that transport failure into
// ErrDaemonUnavailable, which the results hook reports as silence. A failed
// validation would vanish into what reads as an ordinary quiet turn.
func TestCollectEndpointDoesNotAcknowledgeWhenTheWriteFails(t *testing.T) {
	root := gitRepo(t)
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)
	d.runner = func(context.Context, string, []string, []string, io.Writer, io.Writer) int { return 1 }

	rec := serve(d, asyncReq(t, root))
	assert.Equal(t, rec.Code, http.StatusAccepted)
	waitFor(t, func() bool { return len(d.tasks.inFlight(root)) == 0 }, "run never finished")

	req := httptest.NewRequest(http.MethodGet, "/validate/collect?root="+url.QueryEscape(root), nil)
	newServer(d).Handler.ServeHTTP(&failingWriter{}, req)

	// Still owed to somebody, because nobody received it.
	after := d.tasks.peek(root)
	assert.Equal(t, len(after), 1, "a result was marked delivered even though the write failed")
	assert.Equal(t, after[0].ExitCode, 1)
}

// And the ordinary case still acknowledges, or every result would be reported on
// every turn forever.
func TestCollectEndpointAcknowledgesAfterASuccessfulWrite(t *testing.T) {
	root := gitRepo(t)
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)
	d.runner = func(context.Context, string, []string, []string, io.Writer, io.Writer) int { return 0 }

	rec := serve(d, asyncReq(t, root))
	assert.Equal(t, rec.Code, http.StatusAccepted)
	waitFor(t, func() bool { return len(d.tasks.inFlight(root)) == 0 }, "run never finished")

	rec = serve(d, httptest.NewRequest(http.MethodGet, "/validate/collect?root="+url.QueryEscape(root), nil))
	assert.Equal(t, rec.Code, http.StatusOK)
	var collected CollectResponse
	assert.NilError(t, json.Unmarshal(rec.Body.Bytes(), &collected))
	assert.Equal(t, len(collected.Tasks), 1)

	assert.Equal(t, len(d.tasks.peek(root)), 0, "a delivered result was left to be reported again")
}

func TestCollectEndpointRequiresARoot(t *testing.T) {
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)

	rec := serve(d, httptest.NewRequest(http.MethodGet, "/validate/collect", nil))
	assert.Equal(t, rec.Code, http.StatusBadRequest)
}

// Collecting is a read to its caller but a write to the store: it stamps every
// result it returns as delivered, and a delivered result is never reported
// again. The stdlib mux enforces no method of its own, so without this guard
// any request that reaches the path — a stray DELETE, a probe, a form POST —
// drains the results the next prompt was going to be told about.
func TestCollectEndpointRejectsNonGET(t *testing.T) {
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		rec := serve(d, httptest.NewRequest(method, "/validate/collect?root=/repo", nil))
		assert.Equal(t, rec.Code, http.StatusMethodNotAllowed, "%s reached the collect handler", method)
	}
}

// The project root has to reach the runner, not just the task store. One daemon
// serves every repo on the machine from whatever directory it was launched in,
// so a runner left to resolve the project itself validates that directory — and
// the answer is filed under the project that asked, whose fingerprint has not
// moved, so it is delivered as a pass for code that was never run.
func TestAsyncValidateEndpointTellsTheRunnerWhichProject(t *testing.T) {
	root := gitRepo(t)
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)

	got := make(chan string, 1)
	d.runner = func(_ context.Context, projectRoot string, _ []string, _ []string, _ io.Writer, _ io.Writer) int {
		got <- projectRoot
		return 0
	}

	rec := serve(d, asyncReq(t, root))
	assert.Equal(t, rec.Code, http.StatusAccepted)

	select {
	case ran := <-got:
		assert.Equal(t, ran, root, "the runner was not told which project to validate")
	case <-time.After(3 * time.Second):
		t.Fatal("the runner was never called")
	}
}

// Same defect on the synchronous path, which predates async and shares the
// runner. Async is what makes it authoritative — a result nobody is waiting for
// is read back later as fact — but a caller waiting on the wrong repo's exit
// code is being lied to just the same.
func TestValidateEndpointTellsTheRunnerWhichProject(t *testing.T) {
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)

	var ran string
	d.runner = func(_ context.Context, projectRoot string, _ []string, _ []string, _ io.Writer, _ io.Writer) int {
		ran = projectRoot
		return 0
	}

	body, err := json.Marshal(ValidateRequest{Args: []string{"validate"}, ProjectRoot: "/repo/b"})
	assert.NilError(t, err)
	rec := serve(d, httptest.NewRequest(http.MethodPost, "/validate", bytes.NewReader(body)))

	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, ran, "/repo/b", "the runner was not told which project to validate")
}

// Codex detection travels as a request field so that a daemon too old to know
// the flag ignores it instead of rejecting the run. A daemon that does know it
// has to hand it on, or the run never learns it is under Codex.
func TestValidateEndpointPassesHookCodexToTheRunner(t *testing.T) {
	for name, tc := range map[string]struct {
		codex bool
	}{
		"codex":  {codex: true},
		"claude": {codex: false},
	} {
		t.Run(name, func(t *testing.T) {
			d := newTestDaemon()
			t.Cleanup(d.tasks.stopAll)

			var ran []string
			d.runner = func(_ context.Context, _ string, args []string, _ []string, _ io.Writer, _ io.Writer) int {
				ran = args
				return 0
			}

			body, err := json.Marshal(ValidateRequest{Args: []string{"validate"}, ProjectRoot: "/repo", HookCodex: tc.codex})
			assert.NilError(t, err)
			rec := serve(d, httptest.NewRequest(http.MethodPost, "/validate", bytes.NewReader(body)))

			assert.Equal(t, rec.Code, http.StatusOK)
			assert.Equal(t, slices.Contains(ran, "--hook-codex"), tc.codex, "args: %q", ran)
		})
	}
}

// Output under the cap is the whole of what the run printed, unmarked. A
// marker on a short result would be a lie about what was dropped.
func TestTailOutputKeepsShortOutputWhole(t *testing.T) {
	out := strings.Repeat("line\n", 10)
	assert.Equal(t, tailOutput(out), out)
}

// The bound is on what the daemon retains, so it has to hold whatever the run
// printed: a verbose test suite is megabytes, and twenty of them per project
// sit in the heap until somebody asks for them.
func TestTailOutputCapsLongOutput(t *testing.T) {
	out := strings.Repeat("x", 4*maxTaskOutput) + "\nthe tally\n"

	got := tailOutput(out)

	assert.Assert(t, len(got) <= maxTaskOutput+64, "output was not capped: %d bytes", len(got))
	assert.Assert(t, strings.HasSuffix(got, "the tally\n"), "the tail was not what survived")
	assert.Assert(t, strings.Contains(got, "bytes of earlier output dropped"),
		"a truncated result must say so")
}

// A cut landing inside a multi-byte rune would leave the result invalid UTF-8,
// which survives a Go string but breaks whatever renders it.
func TestTailOutputStaysValidUTF8(t *testing.T) {
	// No line breaks, so the line-boundary step cannot do the trimming.
	out := strings.Repeat("é", maxTaskOutput)

	got := tailOutput(out)

	assert.Assert(t, utf8.ValidString(got), "the cut left a partial rune")
	assert.Assert(t, len(got) <= maxTaskOutput+64, "output was not capped: %d bytes", len(got))
}

// The store is what holds the memory, so the cap belongs on the way in rather
// than on whichever caller happens to produce the output.
func TestFinishCapsStoredOutput(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", false, func(context.Context) (int, string) {
		return 0, strings.Repeat("y", 4*maxTaskOutput)
	})
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	got := s.collect("/repo")
	assert.Equal(t, len(got), 1)
	assert.Assert(t, len(got[0].Output) <= maxTaskOutput+64,
		"the store retained %d bytes", len(got[0].Output))
}

// Output with no line breaks must still fill the window. Seeking forward to a
// line boundary unboundedly would hand back only whatever trails the last
// newline — a few bytes of a 64 KiB budget — for a progress bar or a single
// JSON blob.
func TestTailOutputKeepsTheWindowWhenThereAreNoLineBreaks(t *testing.T) {
	out := strings.Repeat("x", 4*maxTaskOutput) + "\nthe tally"

	got := tailOutput(out)

	assert.Assert(t, len(got) > maxTaskOutput/2, "only %d bytes survived a full window", len(got))
	assert.Assert(t, strings.HasSuffix(got, "the tally"), "the tail was not what survived")
}

// Nothing else bounds the store. evictLocked will not reclaim a running task,
// so once a project is at its cap of in-flight runs every further start would
// add an entry and a goroutine that no eviction can ever take back.
func TestStartRefusesOnceTheProjectIsAtItsInFlightCap(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	for i := 0; i < MaxTasksPerProject; i++ {
		_, err := s.start("/repo", false, func(context.Context) (int, string) {
			<-block
			return 0, ""
		})
		assert.NilError(t, err, "run %d was refused below the cap", i)
	}

	_, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "" })
	assert.ErrorContains(t, err, "already in flight")
	assert.Equal(t, len(s.inFlight("/repo")), MaxTasksPerProject, "a refused run was still filed")
}

// The cap is per project, as the eviction it compensates for is. One busy repo
// must not stop another from validating in the background.
func TestTheInFlightCapIsPerProject(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	for i := 0; i < MaxTasksPerProject; i++ {
		_, err := s.start("/repo", false, func(context.Context) (int, string) {
			<-block
			return 0, ""
		})
		assert.NilError(t, err)
	}

	_, err := s.start("/other", false, func(context.Context) (int, string) { return 0, "" })
	assert.NilError(t, err, "a different project was refused for a busy neighbour")
}

// Refusing has to be temporary. The cap counts runs still going, not runs ever
// started, or a project would validate asynchronously twenty times and then
// never again for the life of the daemon.
func TestTheInFlightCapFreesUpAsRunsFinish(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	for i := 0; i < MaxTasksPerProject*2; i++ {
		_, err := s.start("/repo", false, func(context.Context) (int, string) { return 0, "" })
		assert.NilError(t, err, "run %d was refused although the earlier ones had finished", i)
		waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")
	}
}

// Collecting is a hook-path call: it runs in front of a prompt and must stay
// quiet when there is no daemon to ask. Its callers recognise that case by
// ErrDaemonUnavailable, so a socket path that cannot even be resolved — no
// HOME, a container with no user dir — has to arrive under the same sentinel.
// Returned bare it becomes an error message before the prompt instead.
func TestCollectValidateResultsReportsAnUnresolvableSocketAsUnavailable(t *testing.T) {
	t.Setenv("CHUNK_WATCHD_DIR", "")
	t.Setenv("HOME", "")

	if _, err := SocketPath(); err == nil {
		t.Skip("this platform resolves a home dir without HOME")
	}

	_, err := CollectValidateResults(t.TempDir())
	assert.ErrorIs(t, err, ErrDaemonUnavailable)
}
