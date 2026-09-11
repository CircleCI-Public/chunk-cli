package watchd

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// worktreeProject returns a repo configured to run its background checks in a
// snapshot, and a daemon to serve it.
func worktreeProject(t *testing.T) (*daemon, string) {
	t.Helper()
	root := gitRepo(t)
	assert.NilError(t, os.MkdirAll(filepath.Join(root, ".chunk"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(root, ".chunk", "config.json"),
		[]byte(`{"commands":[{"name":"test","run":"true"}],"asyncValidateWorktree":true}`), 0o644))

	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)
	return d, root
}

func releaseRun(t *testing.T, d *daemon, root string) ValidateResponse {
	t.Helper()
	resp := decodeValidate(t, serve(d, validateReq(t, ValidateRequest{
		Args: []string{"validate"}, ProjectRoot: root, AllowAsync: true,
	})))
	assert.Assert(t, resp.TaskID != "", "the run was not released: %s", resp.Reason)
	return resp
}

// The commands run in a copy of the tree, and the results still belong to the
// real project — the copy is a detached HEAD in a temp directory that nobody is
// watching for their validate output.
func TestWorktreeModeRunsInASnapshotAndAttributesItBack(t *testing.T) {
	d, root := worktreeProject(t)
	writeSource(t, root, 10)

	args := make(chan []string, 1)
	d.runner = func(_ context.Context, a []string, _ []string, _ io.Writer, _ io.Writer) int {
		args <- a
		return 0
	}

	releaseRun(t, d, root)
	got := <-args

	i := slices.Index(got, "--project")
	assert.Assert(t, i >= 0 && i+1 < len(got), "no --project in %v", got)
	shadow := got[i+1]
	assert.Assert(t, shadow != root, "the run happened in the live tree")

	j := slices.Index(got, "--attribute-to")
	assert.Assert(t, j >= 0 && j+1 < len(got), "no --attribute-to in %v", got)
	assert.Equal(t, got[j+1], root)
}

// The payoff: an edit while the run is in flight no longer throws the answer
// away. The run validated a copy that cannot move, so its verdict is still
// exactly true about the state it ran against — and it is reported with that
// said rather than discarded.
func TestASnapshotResultSurvivesAnEditDuringTheRun(t *testing.T) {
	d, root := worktreeProject(t)
	writeSource(t, root, 10)

	release := make(chan struct{})
	shadowSeen := make(chan string, 1)
	d.runner = func(_ context.Context, a []string, _ []string, _ io.Writer, _ io.Writer) int {
		if i := slices.Index(a, "--project"); i >= 0 {
			shadowSeen <- a[i+1]
		}
		<-release
		return 0
	}

	releaseRun(t, d, root)
	shadow := <-shadowSeen

	// The live tree moves while the run is going. The snapshot does not.
	writeSource(t, root, 900)
	inShadow, err := os.ReadFile(filepath.Join(shadow, "main.go"))
	assert.NilError(t, err)
	assert.Equal(t, strings.Count(string(inShadow), "\n"), 10, "the snapshot followed the live tree")

	close(release)
	waitFor(t, func() bool { return len(d.tasks.inFlight(root)) == 0 }, "run never finished")

	got := d.tasks.collect(root)
	assert.Equal(t, len(got), 1, "a snapshot-backed result was thrown away")
	assert.Equal(t, got[0].Passed(), true)
	assert.Equal(t, got[0].Snapshot, true)
	assert.Equal(t, got[0].Stale, true, "the result did not say the tree had moved on")
}

// A result about the live tree is still discarded when the tree moves: it
// describes code that is no longer there, and nothing can qualify that into
// being useful.
func TestALiveTreeResultIsStillDiscardedAfterAnEdit(t *testing.T) {
	d, root, _ := riskDaemon(t, 0)
	writeSource(t, root, 10)

	release := make(chan struct{})
	started := make(chan struct{})
	d.runner = func(context.Context, []string, []string, io.Writer, io.Writer) int {
		close(started)
		<-release
		return 0
	}

	releaseRun(t, d, root)
	<-started
	writeSource(t, root, 900)
	close(release)
	waitFor(t, func() bool { return len(d.tasks.inFlight(root)) == 0 }, "run never finished")

	assert.Equal(t, len(d.tasks.collect(root)), 0, "a result about replaced code was reported")
}

// One background run, one temp worktree, removed afterwards — a repo must not
// accumulate them a turn at a time.
func TestTheShadowIsRemovedWhenTheRunEnds(t *testing.T) {
	d, root := worktreeProject(t)
	writeSource(t, root, 10)

	shadowSeen := make(chan string, 1)
	d.runner = func(_ context.Context, a []string, _ []string, _ io.Writer, _ io.Writer) int {
		if i := slices.Index(a, "--project"); i >= 0 {
			shadowSeen <- a[i+1]
		}
		return 0
	}

	releaseRun(t, d, root)
	shadow := <-shadowSeen
	waitFor(t, func() bool { return len(d.tasks.inFlight(root)) == 0 }, "run never finished")
	waitFor(t, func() bool {
		_, err := os.Stat(shadow)
		return err != nil
	}, "the shadow directory was not removed")

	cmd := exec.Command("git", "worktree", "list")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(out), shadow), "worktree metadata was left behind: %s", out)
}

// Off by default. A snapshot holds nothing git was told to ignore, so for many
// projects every run in one would report an environment failure as a code
// failure.
func TestTheLiveTreeIsUsedUnlessTheProjectAsksOtherwise(t *testing.T) {
	d, root, _ := riskDaemon(t, 0)
	writeSource(t, root, 10)

	args := make(chan []string, 1)
	d.runner = func(_ context.Context, a []string, _ []string, _ io.Writer, _ io.Writer) int {
		args <- a
		return 0
	}

	releaseRun(t, d, root)
	got := <-args
	assert.Equal(t, slices.Contains(got, "--attribute-to"), false, "args were %v", got)
}
