package watchd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
	t.Cleanup(s.stopAll)
	return s, tree
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

	_, err := s.start("/repo", func(context.Context) (int, string) {
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
	_, err := s.start("/repo", func(context.Context) (int, string) {
		<-release
		return 0, "1/1 passed"
	})
	assert.NilError(t, err)

	// The edit: same commit, different content digest.
	fake.set(tree("abc", "d2"), nil)
	close(release)

	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	assert.Equal(t, len(s.collect("/repo")), 0, "a result about replaced code was reported")
	// Dropped, not merely withheld: a task nobody will be told about must not
	// accumulate.
	assert.Equal(t, len(s.collect("/repo")), 0)
}

// A commit during the run moves HEAD without touching the digest, and is just
// as much a different tree as an edit is.
func TestACommitDuringTheRunDiscardsTheResult(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	release := make(chan struct{})
	_, err := s.start("/repo", func(context.Context) (int, string) {
		<-release
		return 0, "ok"
	})
	assert.NilError(t, err)

	fake.set(tree("def", "d1"), nil)
	close(release)

	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")
	assert.Equal(t, len(s.collect("/repo")), 0)
}

// Failures are collected like passes. Whether a failure should block is the
// caller's decision; the store's job is to have the answer ready.
func TestAFailureIsCollectedToo(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", func(context.Context) (int, string) {
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
	_, err := s.start("/repo", func(context.Context) (int, string) {
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
	_, err := s.start("/repo", func(context.Context) (int, string) {
		<-release
		return 0, "ok"
	})
	assert.NilError(t, err)

	fake.set(gitutil.Worktree{}, errors.New("dirty submodule"))
	close(release)

	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")
	assert.Equal(t, len(s.collect("/repo")), 0, "an unverifiable result was reported as current")
}

// A result reaches a caller once. Repeating it on the following turn would tell
// an agent its current work had been validated by a run that predates it.
func TestCollectReportsAResultOnlyOnce(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", func(context.Context) (int, string) { return 0, "ok" })
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
	_, err := s.start("/repo", func(context.Context) (int, string) {
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
	first, err := s.start("/repo", func(context.Context) (int, string) {
		<-release
		return 0, "first"
	})
	assert.NilError(t, err)
	second, err := s.start("/repo", func(context.Context) (int, string) {
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

	_, err := s.start("/repo-a", func(context.Context) (int, string) { return 0, "a" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo-a")) == 0 }, "run never finished")

	assert.Equal(t, len(s.collect("/repo-b")), 0, "another project's result leaked")
	got := s.collect("/repo-a")
	assert.Equal(t, len(got), 1)
	assert.Equal(t, got[0].Output, "a")
}

// Eviction keeps the store bounded, but a run that has not reported yet is not
// a candidate: dropping it would lose work about to produce an answer.
func TestEvictionNeverDropsARunningTask(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	release := make(chan struct{})
	for i := 0; i < MaxTasksPerProject+5; i++ {
		_, err := s.start("/repo", func(context.Context) (int, string) {
			<-release
			return 0, "ok"
		})
		assert.NilError(t, err)
	}
	// Over the cap on purpose: every task is still running.
	assert.Equal(t, len(s.inFlight("/repo")), MaxTasksPerProject+5)

	close(release)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "runs never finished")
}

// Finished tasks are evicted oldest-first once a project is over the cap, so a
// project nobody collects from cannot grow without bound.
func TestFinishedTasksAreEvictedOverTheCap(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	for i := 0; i < MaxTasksPerProject+5; i++ {
		_, err := s.start("/repo", func(context.Context) (int, string) { return 0, "ok" })
		assert.NilError(t, err)
		waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")
	}

	assert.Equal(t, len(s.collect("/repo")), MaxTasksPerProject)
}

// Shutdown cancels in-flight runs rather than leaving them to outlive the daemon.
func TestStopAllCancelsRunsInFlight(t *testing.T) {
	s, _ := newFakeStore(t, tree("abc", "d1"))

	cancelled := make(chan struct{})
	_, err := s.start("/repo", func(ctx context.Context) (int, string) {
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

// The fingerprint is read twice per run — once to record the baseline, once to
// check it — and not on the hot path of a collect.
func TestFingerprintIsReadOncePerEndOfRun(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	before := fake.calls()
	assert.Equal(t, before, 2, "expected one fingerprint at start and one at finish")
	s.collect("/repo")
	assert.Equal(t, fake.calls(), before, "collect re-read the tree")
}
