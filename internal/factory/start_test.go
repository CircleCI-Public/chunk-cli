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

	rec, err := Start(ctx, root, "Add a --verbose flag")
	assert.NilError(t, err)

	assert.Assert(t, rec.ByHand)
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
	rec, err := Start(ctx, root, "add a flag")
	assert.NilError(t, err)
	writeFile(t, rec.Worktree, "flag.go", "package main\n")

	wt, err := openWorktree(ctx, root, rec, "run-2")
	assert.NilError(t, err)

	assert.Equal(t, wt.Baseline, rec.Baseline)
	stat, err := wt.Stat(ctx)
	assert.NilError(t, err)
	assert.Equal(t, stat, "1 file changed, 1 insertion(+)")
}

func TestContinuationOfAStartedRunFinishesTheDevelopersWork(t *testing.T) {
	c := &Continuation{From: Record{RunID: "run-1", Prompt: "add a flag", ByHand: true}}

	assert.Assert(t, c.checkFirst())
	assert.Equal(t, c.request(), "add a flag")
	assert.Assert(t, strings.HasPrefix(c.prompt(), "You are finishing a change the developer started. They want:\n\nadd a flag"))
	assert.Assert(t, strings.Contains(c.prompt(), "`git diff HEAD` shows it"))
}
