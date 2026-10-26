package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// newPlanProject returns a repository whose origin is a bare repository on
// this machine, with main pushed to it, and then a local commit and an
// uncommitted edit on top that origin does not have.
func newPlanProject(t *testing.T) (root, originMain string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root = gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitOutput(t, root, "init", "--bare", origin)
	gitOutput(t, root, "remote", "set-url", "origin", origin)
	writeFile(t, root, "main.go", "package main\n")
	gitrepo.AddFile(t, root, "main.go")
	gitOutput(t, root, "commit", "-m", "base")
	gitOutput(t, root, "push", "origin", "HEAD:main")
	originMain = gitOutput(t, root, "rev-parse", "HEAD")

	writeFile(t, root, "local.go", "package main\n")
	gitrepo.AddFile(t, root, "local.go")
	gitOutput(t, root, "commit", "-m", "not pushed")
	writeFile(t, root, "main.go", "package main\n\n// in progress\n")
	return root, originMain
}

func TestOpenPlanWorktreeStartsDetachedAtOriginMain(t *testing.T) {
	ctx := context.Background()
	root, originMain := newPlanProject(t)
	before := gitOutput(t, root, "status", "--porcelain")

	path, err := OpenPlanWorktree(ctx, root, "plan-1")
	assert.NilError(t, err)

	assert.Check(t, cmp.Equal(filepath.Base(path), "plan-1"))
	head := gitOutput(t, path, "rev-parse", "HEAD")
	assert.Check(t, cmp.Equal(head, originMain))
	// Detached, so it holds no branch.
	branch := gitOutput(t, path, "rev-parse", "--abbrev-ref", "HEAD")
	assert.Check(t, cmp.Equal(branch, "HEAD"))
	// The developer's unpushed and uncommitted work stays theirs, so it
	// cannot end up in the plan's pull request.
	mainGo := readFile(t, path, "main.go")
	assert.Check(t, cmp.Equal(mainGo, "package main\n"))
	_, err = os.Stat(filepath.Join(path, "local.go"))
	assert.Check(t, errors.Is(err, os.ErrNotExist))
	after := gitOutput(t, root, "status", "--porcelain")
	assert.Check(t, cmp.Equal(after, before))
}

func TestOpenPlanWorktreeReopensTheSameWorktree(t *testing.T) {
	ctx := context.Background()
	root, _ := newPlanProject(t)
	first, err := OpenPlanWorktree(ctx, root, "plan-1")
	assert.NilError(t, err)
	// Put on a branch since it was opened.
	gitOutput(t, first, "switch", "-c", "plan-1-branch")

	again, err := OpenPlanWorktree(ctx, root, "plan-1")
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(again, first))
	branch := gitOutput(t, again, "rev-parse", "--abbrev-ref", "HEAD")
	assert.Check(t, cmp.Equal(branch, "plan-1-branch"))
}

func TestOpenPlanWorktreeAddsBackADeletedWorktree(t *testing.T) {
	ctx := context.Background()
	root, originMain := newPlanProject(t)
	path, err := OpenPlanWorktree(ctx, root, "plan-1")
	assert.NilError(t, err)
	err = os.RemoveAll(path)
	assert.NilError(t, err)

	again, err := OpenPlanWorktree(ctx, root, "plan-1")
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(again, path))
	head := gitOutput(t, again, "rev-parse", "HEAD")
	assert.Check(t, cmp.Equal(head, originMain))
}

func TestOpenPlanWorktreeFetchesOriginFirst(t *testing.T) {
	ctx := context.Background()
	root, _ := newPlanProject(t)
	// Someone else merges to main after the developer last fetched.
	other := filepath.Join(t.TempDir(), "other")
	origin := gitOutput(t, root, "remote", "get-url", "origin")
	gitOutput(t, root, "clone", "--quiet", "--branch", "main", origin, other)
	writeFile(t, other, "theirs.go", "package main\n")
	gitrepo.AddFile(t, other, "theirs.go")
	gitOutput(t, other, "commit", "-m", "theirs")
	gitOutput(t, other, "push", "--quiet", "origin", "HEAD:main")

	path, err := OpenPlanWorktree(ctx, root, "plan-1")
	assert.NilError(t, err)
	head := gitOutput(t, path, "rev-parse", "HEAD")
	theirs := gitOutput(t, other, "rev-parse", "HEAD")
	assert.Check(t, cmp.Equal(head, theirs))
}

func TestOpenPlanWorktreeAsksOriginForItsDefaultBranch(t *testing.T) {
	ctx := context.Background()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	// Given its origin with git remote add, so there is no origin/HEAD, and
	// origin's default branch is not main.
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitOutput(t, root, "init", "--bare", "--initial-branch=trunk", origin)
	gitOutput(t, root, "remote", "set-url", "origin", origin)
	writeFile(t, root, "main.go", "package main\n")
	gitrepo.AddFile(t, root, "main.go")
	gitOutput(t, root, "commit", "-m", "base")
	gitOutput(t, root, "push", "origin", "HEAD:trunk")

	path, err := OpenPlanWorktree(ctx, root, "plan-1")
	assert.NilError(t, err)
	head := gitOutput(t, path, "rev-parse", "HEAD")
	trunk := gitOutput(t, root, "rev-parse", "HEAD")
	assert.Check(t, cmp.Equal(head, trunk))
}

func TestOpenPlanWorktreeFallsBackToTheTipLastFetched(t *testing.T) {
	ctx := context.Background()
	root, originMain := newPlanProject(t)
	// Offline, say: the fetch fails, but origin/main is still on disk.
	gitOutput(t, root, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone"))

	path, err := OpenPlanWorktree(ctx, root, "plan-1")
	assert.NilError(t, err)
	head := gitOutput(t, path, "rev-parse", "HEAD")
	assert.Check(t, cmp.Equal(head, originMain))
}

func TestOpenPlanWorktreeCopiesAnUncommittedChunkConfig(t *testing.T) {
	ctx := context.Background()
	root, _ := newPlanProject(t)
	writeFile(t, root, ".chunk/config.json", `{"validation":{}}`)

	path, err := OpenPlanWorktree(ctx, root, "plan-1")
	assert.NilError(t, err)
	config := readFile(t, path, ".chunk/config.json")
	assert.Check(t, cmp.Equal(config, `{"validation":{}}`))
}

func TestOpenPlanWorktreeCopiesAChunkConfigAddedSinceIntoAReopenedOne(t *testing.T) {
	ctx := context.Background()
	root, _ := newPlanProject(t)
	path, err := OpenPlanWorktree(ctx, root, "plan-1")
	assert.NilError(t, err)
	writeFile(t, root, ".chunk/config.json", `{"validation":{}}`)

	_, err = OpenPlanWorktree(ctx, root, "plan-1")
	assert.NilError(t, err)
	config := readFile(t, path, ".chunk/config.json")
	assert.Check(t, cmp.Equal(config, `{"validation":{}}`))
}

func TestOpenPlanWorktreeKeepsACommittedChunkConfig(t *testing.T) {
	ctx := context.Background()
	root, _ := newPlanProject(t)
	writeFile(t, root, ".chunk/config.json", `{"committed":true}`)
	gitrepo.AddFile(t, root, ".chunk/config.json")
	gitOutput(t, root, "commit", "-m", "config")
	gitOutput(t, root, "push", "origin", "HEAD:main")
	writeFile(t, root, ".chunk/config.json", `{"local":true}`)

	path, err := OpenPlanWorktree(ctx, root, "done")
	assert.NilError(t, err)
	config := readFile(t, path, ".chunk/config.json")
	assert.Check(t, cmp.Equal(config, `{"committed":true}`))
	status := gitOutput(t, path, "status", "--porcelain")
	assert.Check(t, cmp.Equal(status, ""))

	err = PrunePlanWorktrees(ctx, root, func(context.Context, string) (bool, error) { return true, nil })
	assert.NilError(t, err)
	_, err = os.Stat(path)
	assert.Check(t, errors.Is(err, os.ErrNotExist), "the ended plan's worktree is still there")
}

func TestPlanWorktreePathRefusesAnIDThatIsNotOneDirectory(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	for _, id := range []string{"", ".", "..", "../x", "a/b"} {
		_, err := PlanWorktreePath(root, id)
		assert.Check(t, err != nil, "plan ID %q", id)
	}
}

func TestPrunePlanWorktreesRemovesThoseOfEndedPlans(t *testing.T) {
	ctx := context.Background()
	root, _ := newPlanProject(t)
	writeFile(t, root, ".chunk/config.json", `{}`)
	done, err := OpenPlanWorktree(ctx, root, "done")
	assert.NilError(t, err)
	open, err := OpenPlanWorktree(ctx, root, "open")
	assert.NilError(t, err)
	unknown, err := OpenPlanWorktree(ctx, root, "unknown")
	assert.NilError(t, err)

	err = PrunePlanWorktrees(ctx, root, func(_ context.Context, id string) (bool, error) {
		if id == "unknown" {
			return false, errors.New("503 from factory")
		}
		return id == "done", nil
	})
	assert.Check(t, cmp.ErrorContains(err, "503 from factory"))

	// The copied config does not keep an ended plan's worktree.
	_, err = os.Stat(done)
	assert.Check(t, errors.Is(err, os.ErrNotExist), "the ended plan's worktree is still there")
	_, err = os.Stat(open)
	assert.Check(t, err)
	_, err = os.Stat(unknown)
	assert.Check(t, err, "a plan whose state could not be read keeps its worktree")
}

func TestPrunePlanWorktreesKeepsAWorktreeWithChanges(t *testing.T) {
	ctx := context.Background()
	root, _ := newPlanProject(t)
	path, err := OpenPlanWorktree(ctx, root, "done")
	assert.NilError(t, err)
	writeFile(t, path, "main.go", "package main\n\n// unsaved\n")

	err = PrunePlanWorktrees(ctx, root, func(context.Context, string) (bool, error) { return true, nil })
	assert.Check(t, cmp.ErrorContains(err, "remove the worktree of plan done"))
	mainGo := readFile(t, path, "main.go")
	assert.Check(t, cmp.Equal(mainGo, "package main\n\n// unsaved\n"))
}

func TestPrunePlanWorktreesWithNonePlanned(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	err := PrunePlanWorktrees(context.Background(), root, func(context.Context, string) (bool, error) {
		t.Fatal("no plan to ask about")
		return false, nil
	})
	assert.NilError(t, err)
}
