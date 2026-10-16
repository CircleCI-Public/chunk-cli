package factory

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// warmTest is a started run whose worktree holds a draft, and a fake CircleCI
// whose sidecars accept every command.
type warmTest struct {
	root   string
	rec    Record
	client *circleci.Client
	cci    *fakes.FakeCircleCI
	// repo is the sidecars' copy of the repository.
	repo string
}

func newWarmTest(t *testing.T) warmTest {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	for _, bin := range []string{"rsync", "ssh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	// The fake sidecars run every command on this machine, so rsync and ssh
	// run for real and a sidecar's copy of the repository is a local path.
	sshSrv := fakes.NewSSHServerAcceptingAnyKey(t)
	sshSrv.RunLocally()
	repo := filepath.Join(t.TempDir(), "sidecar-repo")
	warmRepoPath = repo
	t.Cleanup(func() { warmRepoPath = "" })
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	api := httptest.NewServer(cci)
	t.Cleanup(api.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: api.URL})
	assert.NilError(t, err)

	root := newProject(t)
	rec, err := Start(context.Background(), root, Handoff{Prompt: "add a flag"})
	assert.NilError(t, err)
	writeFile(t, rec.Worktree, "flag.go", "package main\n")
	return warmTest{root: root, rec: rec, client: client, cci: cci, repo: repo}
}

func countRequests(cci *fakes.FakeCircleCI, method, path string) int {
	n := 0
	for _, r := range cci.Recorder.AllRequests() {
		if r.Method == method && r.URL.Path == path {
			n++
		}
	}
	return n
}

const sidecarsPath = "/api/v3/sidecar/instances"

// waitForSidecars waits until n sidecars have been made, and returns n.
func waitForSidecars(t *testing.T, cci *fakes.FakeCircleCI, n int) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for countRequests(cci, "POST", sidecarsPath) < n {
		assert.Assert(t, time.Now().Before(deadline), "only %d of %d sidecars were made", countRequests(cci, "POST", sidecarsPath), n)
		time.Sleep(20 * time.Millisecond)
	}
	return n
}

// The sidecars are made from the run's baseline, the developer's files as the
// run started from them, not from the draft still being made in its worktree.
func TestWarmPoolIsMadeFromTheBaseline(t *testing.T) {
	wt := newWarmTest(t)
	ctx := context.Background()

	w, err := NewWarmPool(ctx, wt.client, wt.root, wt.rec, 2, "org-1", "img", func(iostream.Level, string) {})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = w.Discard(context.Background()) })

	assert.Equal(t, readFile(t, wt.repo, "mine.go"), "package main\n", "the developer's files are on the sidecar")
	_, err = os.Stat(filepath.Join(wt.repo, "flag.go"))
	assert.Assert(t, os.IsNotExist(err), "the draft is not")
	assert.Assert(t, countRequests(wt.cci, "POST", sidecarsPath) >= 1, "sidecars were made")
	assert.Assert(t, w.Fits(2, "org-1", "img"))
	assert.Assert(t, !w.Fits(3, "org-1", "img"), "a run with other reviewers makes its own")
}

// A run continuing the started run takes its warm sidecars rather than making
// any, leaves its worktree's work where it is, and owns them from then on.
func TestAContinuedRunTakesItsWarmSidecars(t *testing.T) {
	wt := newWarmTest(t)
	ctx := context.Background()
	w, err := NewWarmPool(ctx, wt.client, wt.root, wt.rec, 1, "org-1", "img", func(iostream.Level, string) {})
	assert.NilError(t, err)
	// The pool clones its other members in the background once it has the
	// first; count once it has them all.
	made := waitForSidecars(t, wt.cci, PoolSize(1)+1)
	wtree, err := openWorktree(ctx, wt.root, wt.rec, "run-2")
	assert.NilError(t, err)
	dataDir, err := runsDir(wt.root)
	assert.NilError(t, err)

	opts := RunOptions{Root: wt.root, Client: wt.client, OrgID: "org-1", Image: "img", Reviewers: 1, Warm: w}
	pool, err := runPool(ctx, opts, &Continuation{From: wt.rec}, wtree, dataDir, "run-2", func(iostream.Level, string) {})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = pool.Destroy(context.Background()) })

	assert.Assert(t, w.Claimed())
	assert.Equal(t, countRequests(wt.cci, "POST", sidecarsPath), made, "no sidecars were made for the run")
	assert.Equal(t, readFile(t, wtree.Path, "flag.go"), "package main\n", "the work stays in the worktree")
	_, err = os.Stat(w.dir)
	assert.Assert(t, os.IsNotExist(err), "the baseline's checkout goes once the run has the sidecars")
}

// Sidecars made ahead of another run are not used.
func TestARunIgnoresWarmSidecarsMadeForAnother(t *testing.T) {
	wt := newWarmTest(t)
	ctx := context.Background()
	w, err := NewWarmPool(ctx, wt.client, wt.root, wt.rec, 1, "org-1", "img", func(iostream.Level, string) {})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = w.Discard(context.Background()) })
	wtree, err := openWorktree(ctx, wt.root, wt.rec, "run-2")
	assert.NilError(t, err)
	dataDir, err := runsDir(wt.root)
	assert.NilError(t, err)

	other := wt.rec
	other.RunID = "another-run"
	opts := RunOptions{Root: wt.root, Client: wt.client, OrgID: "org-1", Image: "img", Reviewers: 1, Warm: w}
	// The run makes sidecars of its own, which these fake ones cannot hold a
	// repository for; only that it left the warm ones alone matters here.
	if pool, err := runPool(ctx, opts, &Continuation{From: other}, wtree, dataDir, "run-2", func(iostream.Level, string) {}); err == nil {
		t.Cleanup(func() { _ = pool.Destroy(context.Background()) })
	}

	assert.Assert(t, !w.Claimed())
}

// Discarding sidecars no run took deletes them and the baseline's checkout.
func TestDiscardingAWarmPoolDeletesItsSidecars(t *testing.T) {
	wt := newWarmTest(t)
	ctx := context.Background()
	w, err := NewWarmPool(ctx, wt.client, wt.root, wt.rec, 1, "org-1", "img", func(iostream.Level, string) {})
	assert.NilError(t, err)

	assert.NilError(t, w.Discard(ctx))

	deletes := 0
	for _, r := range wt.cci.Recorder.AllRequests() {
		if r.Method == "DELETE" {
			deletes++
		}
	}
	assert.Assert(t, deletes >= 1, "the sidecars were deleted")
	_, err = os.Stat(w.dir)
	assert.Assert(t, os.IsNotExist(err))
	assert.Assert(t, !strings.Contains(gitOutput(t, wt.root, "worktree", "list"), "-baseline"), "the checkout is no longer a worktree")
}
