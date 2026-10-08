package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// finishedRun is a run that ended with work committed on its branch: an edit,
// a new file and a deleted one, measured against its baseline.
func finishedRun(t *testing.T) (root string, rec Record) {
	t.Helper()
	ctx := context.Background()
	root = newProject(t)
	wt, err := CreateWorktree(ctx, root, filepath.Join(t.TempDir(), "wt"), "run-1")
	assert.NilError(t, err)
	writeFile(t, wt.Path, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, wt.Path, "flag.go", "package main\n")
	assert.NilError(t, os.Remove(filepath.Join(wt.Path, "mine.go")))
	_, err = wt.Commit(ctx, CommitMessage("add a flag", "run-1", Outcome{Result: ResultExhausted, Rounds: 3}))
	assert.NilError(t, err)
	return root, Record{
		RunID: "run-1", Prompt: "add a flag",
		Worktree: wt.Path, Branch: wt.Branch, Baseline: wt.Baseline, Head: wt.Head,
		Result: ResultExhausted, Rounds: 3,
	}
}

// Edits the developer made in the worktree after the run are part of the work
// a continued run picks up, so they are committed before anything else.
func TestOpenWorktreeCommitsEditsMadeSinceTheRun(t *testing.T) {
	ctx := context.Background()
	root, rec := finishedRun(t)
	writeFile(t, rec.Worktree, "by-hand.go", "package main\n")

	wt, err := openWorktree(ctx, root, rec, "run-2")
	assert.NilError(t, err)

	assert.Equal(t, wt.Baseline, rec.Baseline, "the work is still measured from the original baseline")
	assert.Equal(t, gitOutput(t, wt.Path, "status", "--porcelain"), "")
	assert.Equal(t, gitOutput(t, root, "log", "-1", "--format=%s", rec.Branch),
		"Edits made in the worktree before chunk factory run run-2 continued it")
	assert.Equal(t, gitOutput(t, root, "show", rec.Branch+":by-hand.go"), "package main")
}

// A worktree the developer removed is added back from the run's branch, which
// still has the work.
func TestOpenWorktreeAddsARemovedWorktreeBack(t *testing.T) {
	ctx := context.Background()
	root, rec := finishedRun(t)
	assert.NilError(t, os.RemoveAll(rec.Worktree))

	wt, err := openWorktree(ctx, root, rec, "run-2")
	assert.NilError(t, err)
	assert.Equal(t, readFile(t, wt.Path, "flag.go"), "package main\n")
}

func TestOpenWorktreeRefusesAMissingBranch(t *testing.T) {
	ctx := context.Background()
	root, rec := finishedRun(t)
	rec.Branch = "chunk/factory/gone"

	_, err := openWorktree(ctx, root, rec, "run-2")
	assert.ErrorContains(t, err, "the run's branch chunk/factory/gone is gone")
}

func TestOpenWorktreeRefusesABranchThatLeftItsBaseline(t *testing.T) {
	ctx := context.Background()
	root, rec := finishedRun(t)
	rec.Baseline = gitOutput(t, root, "rev-parse", rec.Branch)
	gitOutput(t, rec.Worktree, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--amend", "-m", "rewritten")

	_, err := openWorktree(ctx, root, rec, "run-2")
	assert.ErrorContains(t, err, "no longer starts from the run's baseline")
}

// showBaseline puts the baseline's files in the worktree, for sidecars to be
// synced from, without moving the branch; restoreWork puts the work back.
func TestShowBaselineThenRestoreWork(t *testing.T) {
	ctx := context.Background()
	root, rec := finishedRun(t)
	wt, err := openWorktree(ctx, root, rec, "run-2")
	assert.NilError(t, err)
	tip := gitOutput(t, wt.Path, "rev-parse", "HEAD")

	assert.NilError(t, wt.showBaseline(ctx))
	assert.Equal(t, readFile(t, wt.Path, "main.go"), "package main\n\n// in progress\n")
	assert.Equal(t, readFile(t, wt.Path, "mine.go"), "package main\n")
	_, err = os.Stat(filepath.Join(wt.Path, "flag.go"))
	assert.Assert(t, os.IsNotExist(err), "a file the work added is still there")
	assert.Equal(t, gitOutput(t, wt.Path, "rev-parse", "HEAD"), tip, "the branch moved")

	assert.NilError(t, wt.restoreWork(ctx))
	assert.Equal(t, readFile(t, wt.Path, "main.go"), "package main\n\nfunc main() {}\n")
	assert.Equal(t, readFile(t, wt.Path, "flag.go"), "package main\n")
	_, err = os.Stat(filepath.Join(wt.Path, "mine.go"))
	assert.Assert(t, os.IsNotExist(err), "a file the work deleted came back")
	assert.Equal(t, gitOutput(t, wt.Path, "status", "--porcelain"), "")
}

func TestContinuationWithoutGuidanceChecksFirst(t *testing.T) {
	c := &Continuation{From: Record{RunID: "run-1", Prompt: "add a flag\n\nwith tests"}}

	assert.Assert(t, c.checkFirst())
	assert.Equal(t, c.Rounds(3), 4, "the round that checks first does not use up an attempt")
	assert.Equal(t, c.request(), "add a flag\n\nwith tests")
	assert.Assert(t, strings.Contains(c.prompt(), "It was asked:\n\nadd a flag\n\nwith tests"))
	assert.Assert(t, strings.Contains(c.prompt(), "`git diff HEAD` shows it"))
	assert.Assert(t, !strings.Contains(c.prompt(), "The developer adds"))
	assert.Equal(t, c.commitMessage("run-2", Outcome{Result: ResultPassed, Rounds: 2}),
		"add a flag\n\nWritten by chunk factory run run-2, continuing run run-1 (passed after 2 round(s)).\n")
}

func TestContinuationWithGuidanceImplementsFirst(t *testing.T) {
	c := &Continuation{From: Record{RunID: "run-1", Prompt: "add a flag"}, Guidance: "you may update the test"}

	assert.Assert(t, !c.checkFirst())
	assert.Equal(t, c.Rounds(3), 3)
	assert.Equal(t, c.request(), "add a flag\n\nFollow-up from the developer:\n\nyou may update the test")
	assert.Assert(t, strings.HasSuffix(c.prompt(), "The developer adds:\n\nyou may update the test"))
	assert.Equal(t, c.commitMessage("run-2", Outcome{Result: ResultExhausted, Rounds: 3}),
		"add a flag\n\nyou may update the test\n\nWritten by chunk factory run run-2, continuing run run-1 (exhausted after 3 round(s)).\n")
}

func TestRecordRoundTrip(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	root := newProject(t)
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	want := Record{RunID: "run-2", Prompt: "add a flag", Worktree: "/wt", Branch: "chunk/factory/run-1",
		Baseline: "abc", Head: "def", ContinuesRunID: "run-1", Result: ResultPassed, Rounds: 2}

	assert.NilError(t, saveRecord(dataDir, want))
	got, err := LoadRecord(root, "run-2")
	assert.NilError(t, err)
	assert.DeepEqual(t, got, want)

	_, err = LoadRecord(root, "nope")
	assert.ErrorIs(t, err, ErrNoRecord)
}

func TestParseRunID(t *testing.T) {
	assert.Equal(t, ParseRunID("20261006-171526"), "20261006-171526")
	assert.Equal(t, ParseRunID(" chunk/factory/20261006-171526\n"), "20261006-171526")
}
