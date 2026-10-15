package factory

import (
	"context"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// A started run is a worktree for the developer's own work, and its record is
// what a continued run reads to refine that work.
func TestStartRecordsAWorktreeForWorkByHand(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	ctx := context.Background()
	root := newProject(t)
	before := gitOutput(t, root, "status", "--porcelain")

	rec, err := Start(ctx, root, Handoff{Prompt: "Add a --verbose flag"})
	assert.NilError(t, err)

	assert.Assert(t, rec.NeedsWork)
	assert.Equal(t, rec.Brief, handoffBrief("Add a --verbose flag"), "the record says how the work is described")
	assert.Equal(t, rec.Branch, "chunk/factory/add-a-verbose-flag/"+rec.RunID)
	assert.Equal(t, gitOutput(t, rec.Worktree, "rev-parse", "HEAD"), rec.Baseline)
	assert.Equal(t, readFile(t, rec.Worktree, "mine.go"), "package main\n", "the developer's files are the starting point")
	assert.Equal(t, gitOutput(t, root, "status", "--porcelain"), before)
	got, err := LoadRecord(root, rec.RunID)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, rec)
}

// What the developer does in a started run's worktree is the change a
// continued run refines: it is measured from the baseline, not folded into it.
func TestContinuingAStartedRunPicksUpTheWorkDoneByHand(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	ctx := context.Background()
	root := newProject(t)
	rec, err := Start(ctx, root, Handoff{Prompt: "add a flag"})
	assert.NilError(t, err)
	writeFile(t, rec.Worktree, "flag.go", "package main\n")

	c := &Continuation{From: rec}
	wt, stat, err := c.handOff(ctx, root, "run-2")
	assert.NilError(t, err)

	assert.Equal(t, wt.Baseline, rec.Baseline)
	assert.Equal(t, stat, "1 file changed, 1 insertion(+)")
}

// The factory refines a diff, so a started run continued before anything was
// done in its worktree has nothing to hand over.
func TestContinuingAStartedRunWithNoWorkIsRefused(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	ctx := context.Background()
	root := newProject(t)
	rec, err := Start(ctx, root, Handoff{Prompt: "add a flag"})
	assert.NilError(t, err)

	c := &Continuation{From: rec}
	_, _, err = c.handOff(ctx, root, "run-2")

	assert.ErrorIs(t, err, ErrNothingToRefine)
}

func TestContinuationOfAStartedRunFinishesTheDevelopersWork(t *testing.T) {
	c := &Continuation{From: Record{RunID: "run-1", Prompt: "add a flag", Brief: handoffBrief("add a flag")}}

	assert.Assert(t, c.checkFirst())
	assert.Equal(t, c.request(), "add a flag")
	assert.Assert(t, strings.HasPrefix(c.prompt(), "You are finishing a change the developer started. They want:\n\nadd a flag"))
	assert.Assert(t, strings.Contains(c.prompt(), "`git diff HEAD` shows it"))
}

// A record that does not say how its work is described, like a build's, gets
// the default: an earlier session's unfinished work.
func TestContinuationWithNoBriefContinuesAnEarlierSession(t *testing.T) {
	c := &Continuation{From: Record{RunID: "run-1", Prompt: "add a flag"}}

	assert.Assert(t, strings.HasPrefix(c.prompt(), "You are continuing a change that an earlier session started and did not finish. It was asked:\n\nadd a flag"))
}

// A diff of work done elsewhere is laid on the developer's files as the work
// so far, so a continued run refines it like work done in the worktree.
func TestStartFromADiffHandsItToTheFactory(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	ctx := context.Background()
	root := newProject(t)
	diff := "diff --git a/flag.go b/flag.go\nnew file mode 100644\n--- /dev/null\n+++ b/flag.go\n@@ -0,0 +1 @@\n+package main\n"

	rec, err := Start(ctx, root, Handoff{Prompt: "add a flag", Diff: []byte(diff)})
	assert.NilError(t, err)
	assert.Equal(t, readFile(t, rec.Worktree, "flag.go"), "package main\n")

	c := &Continuation{From: rec}
	_, stat, err := c.handOff(ctx, root, "run-2")
	assert.NilError(t, err)
	assert.Equal(t, stat, "1 file changed, 1 insertion(+)")
}

// A diff that does not apply leaves nothing behind: no worktree, branch or
// record for a run that has no work.
func TestStartFromADiffThatDoesNotApplyCleansUp(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	ctx := context.Background()
	root := newProject(t)
	diff := "diff --git a/missing.go b/missing.go\n--- a/missing.go\n+++ b/missing.go\n@@ -1 +1 @@\n-old\n+new\n"

	_, err := Start(ctx, root, Handoff{Prompt: "add a flag", Diff: []byte(diff)})

	assert.ErrorContains(t, err, "apply the diff")
	assert.Equal(t, gitOutput(t, root, "branch", "--list", "chunk/factory/*"), "")
}

// The worktree starts from the developer's uncommitted changes, so a diff of
// those same changes is already in it and is refused rather than misapplied.
// Nothing is left behind.
func TestStartFromADiffOfTheCheckoutsOwnChangesIsRefused(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	ctx := context.Background()
	root := newProject(t)
	diff := gitOutput(t, root, "diff") + "\n"

	_, err := Start(ctx, root, Handoff{Prompt: "add a flag", Diff: []byte(diff)})

	assert.ErrorIs(t, err, ErrDiffAlreadyApplied)
	assert.Equal(t, gitOutput(t, root, "branch", "--list", "chunk/factory/*"), "")
}

// A run continued in the second the run it continues was named in still gets
// its own ID, so the earlier run's record is not overwritten.
func TestRunIDAfterNeverReusesTheEarlierRunsID(t *testing.T) {
	prev := NewRunID()

	assert.Assert(t, runIDAfter(prev) != prev)
}
