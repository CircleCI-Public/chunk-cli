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

// An edit after the run ended but before anyone read the result is the common
// shape, and the one a completion-time check alone misses: the run finishes in
// seconds, the developer keeps typing, and the result is read a minute later.
// It was true when recorded and is not true when read.
func TestAnEditAfterTheRunFinishedDiscardsTheResult(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	// The run has already concluded against an unchanged tree. The edit lands
	// only now, between finishing and being read.
	fake.set(tree("abc", "d2"), nil)

	assert.Equal(t, len(s.collect("/repo")), 0, "a result about replaced code was reported")
}

// Likewise a commit landing after the run ended.
func TestACommitAfterTheRunFinishedDiscardsTheResult(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	fake.set(tree("def", "d1"), nil)
	assert.Equal(t, len(s.collect("/repo")), 0)
}

// A tree that cannot be fingerprinted at read time cannot be shown to match the
// tree the run validated, so nothing is reported for it.
func TestATreeThatCannotBeFingerprintedAtCollectReportsNothing(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	_, err := s.start("/repo", func(context.Context) (int, string) { return 0, "ok" })
	assert.NilError(t, err)
	waitFor(t, func() bool { return len(s.inFlight("/repo")) == 0 }, "run never finished")

	fake.set(gitutil.Worktree{}, errors.New("dirty submodule"))
	assert.Equal(t, len(s.collect("/repo")), 0, "an unverifiable result was reported as current")
}

// The tree is read once per collect rather than once per task, so a project
// holding several finished runs still costs one git call to report them.
func TestCollectReadsTheTreeOncePerCall(t *testing.T) {
	s, fake := newFakeStore(t, tree("abc", "d1"))

	for i := 0; i < 3; i++ {
		_, err := s.start("/repo", func(context.Context) (int, string) { return 0, "ok" })
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

// serve runs one request against the daemon's real mux, so the routes are
// covered rather than only the handler functions.
func serve(d *daemon, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	newServer(d).Handler.ServeHTTP(rec, req)
	return rec
}

func asyncReq(t *testing.T, root string) *http.Request {
	t.Helper()
	body, err := json.Marshal(AsyncValidateRequest{ValidateRequest: ValidateRequest{ProjectRoot: root}})
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
	d.runner = func(context.Context, []string, []string, io.Writer, io.Writer) int {
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
	dir, err := os.MkdirTemp("", "notarepo")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)
	var ran bool
	d.runner = func(context.Context, []string, []string, io.Writer, io.Writer) int {
		ran = true
		return 0
	}

	rec := serve(d, asyncReq(t, dir))
	assert.Equal(t, rec.Code, http.StatusConflict)
	assert.Equal(t, ran, false, "a run started against a tree with no baseline")
}

// A result that cannot be attributed to a project could never be collected, so
// the request is rejected rather than accepted and lost.
func TestAsyncValidateEndpointRequiresAProjectRoot(t *testing.T) {
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)
	d.runner = func(context.Context, []string, []string, io.Writer, io.Writer) int { return 0 }

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

func TestCollectEndpointRequiresARoot(t *testing.T) {
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)

	rec := serve(d, httptest.NewRequest(http.MethodGet, "/validate/collect", nil))
	assert.Equal(t, rec.Code, http.StatusBadRequest)
}
