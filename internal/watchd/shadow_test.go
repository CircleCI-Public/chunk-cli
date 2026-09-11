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

// Two runs of the same commands queued behind each other can only ever produce
// one useful answer, and it is the later one. The earlier is validating a tree
// that has already moved — that is what made the second hook fire.
func TestReleasingARunSupersedesTheOneInFlight(t *testing.T) {
	d, root, _ := riskDaemon(t, 0)
	writeSource(t, root, 10)

	release := make(chan struct{})
	started := make(chan struct{}, 2)
	d.runner = func(ctx context.Context, _ []string, _ []string, _ io.Writer, _ io.Writer) int {
		started <- struct{}{}
		select {
		case <-release:
			return 0
		case <-ctx.Done():
			return 1
		}
	}

	first := releaseRun(t, d, root)
	<-started

	writeSource(t, root, 20)
	second := releaseRun(t, d, root)
	assert.Assert(t, second.TaskID != first.TaskID)

	// The first run's context is cancelled, so it stops holding the validate
	// lock and the second one gets to work.
	<-started
	close(release)
	waitFor(t, func() bool { return len(d.tasks.inFlight(root)) == 0 }, "run never finished")

	got := d.tasks.collect(root)
	assert.Equal(t, len(got), 1, "both runs reported: %d", len(got))
	assert.Equal(t, got[0].ID, second.TaskID, "the superseded run reported instead of the live one")
}

// A superseded run concluded nothing, so it must not leave a failure behind:
// that would owe the project a blocking run it never earned.
func TestASupersededRunOwesNothing(t *testing.T) {
	d, root, _ := riskDaemon(t, 0)
	writeSource(t, root, 10)

	release := make(chan struct{})
	started := make(chan struct{}, 2)
	d.runner = func(ctx context.Context, _ []string, _ []string, _ io.Writer, _ io.Writer) int {
		started <- struct{}{}
		select {
		case <-release:
			return 0
		case <-ctx.Done():
			return 1
		}
	}

	releaseRun(t, d, root)
	<-started
	writeSource(t, root, 20)
	releaseRun(t, d, root)
	<-started
	close(release)
	waitFor(t, func() bool { return len(d.tasks.inFlight(root)) == 0 }, "run never finished")

	assert.Equal(t, d.risk.owesBlockingRun(root), false,
		"a cancelled run was recorded as a failure")
}

// A snapshot-backed run is left alone: its verdict stays true about the state
// it was handed, so it is the one piece of work here that will survive being
// overtaken.
func TestASnapshotRunIsNotSuperseded(t *testing.T) {
	d, root := worktreeProject(t)
	writeSource(t, root, 10)

	release := make(chan struct{})
	started := make(chan struct{}, 2)
	d.runner = func(ctx context.Context, _ []string, _ []string, _ io.Writer, _ io.Writer) int {
		started <- struct{}{}
		select {
		case <-release:
			return 0
		case <-ctx.Done():
			return 1
		}
	}

	first := releaseRun(t, d, root)
	<-started

	writeSource(t, root, 20)
	second := releaseRun(t, d, root)
	close(release)
	waitFor(t, func() bool { return len(d.tasks.inFlight(root)) == 0 }, "runs never finished")

	got := d.tasks.collect(root)
	assert.Equal(t, len(got), 2, "a snapshot-backed run was thrown away: %d results", len(got))
	ids := []string{got[0].ID, got[1].ID}
	assert.Assert(t, slices.Contains(ids, first.TaskID), "the first snapshot result was lost")
	assert.Assert(t, slices.Contains(ids, second.TaskID), "the second result was lost")
}
