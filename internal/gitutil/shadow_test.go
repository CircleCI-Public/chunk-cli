package gitutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func materialize(t *testing.T, dir, tree string) string {
	t.Helper()
	path, cleanup, err := MaterializeTree(dir, tree)
	assert.NilError(t, err)
	t.Cleanup(cleanup)
	return path
}

// Everything the snapshot held is there, committed or not: uncommitted work is
// the whole reason for validating a snapshot rather than a commit.
func TestMaterializeTreeCarriesUncommittedWork(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "tracked.txt", "committed\n")
	writeFile(t, dir, "untracked.go", "package main\n")
	writeFile(t, dir, "tracked.txt", "edited\n")

	path := materialize(t, dir, snapshot(t, dir))

	tracked, err := os.ReadFile(filepath.Join(path, "tracked.txt"))
	assert.NilError(t, err)
	assert.Equal(t, string(tracked), "edited\n")
	_, err = os.Stat(filepath.Join(path, "untracked.go"))
	assert.NilError(t, err, "untracked work did not reach the shadow")
}

// The point of the whole thing: the live tree can move as much as it likes and
// the shadow does not.
func TestTheShadowDoesNotMoveWithTheLiveTree(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "main.go", "before\n")
	path := materialize(t, dir, snapshot(t, dir))

	writeFile(t, dir, "main.go", "after\n")
	writeFile(t, dir, "another.go", "new since\n")

	shadowed, err := os.ReadFile(filepath.Join(path, "main.go"))
	assert.NilError(t, err)
	assert.Equal(t, string(shadowed), "before\n")
	_, err = os.Stat(filepath.Join(path, "another.go"))
	assert.Assert(t, err != nil, "a file created after the snapshot appeared in it")
}

// What it cannot carry, said out loud: gitignored content is absent, which is
// why running checks in a shadow is opt-in.
func TestMaterializeTreeLeavesIgnoredContentBehind(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, ".gitignore", "node_modules/\n")
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755))
	writeFile(t, dir, "node_modules/dep.js", "module.exports = 1\n")

	path := materialize(t, dir, snapshot(t, dir))

	_, err := os.Stat(filepath.Join(path, "node_modules"))
	assert.Assert(t, err != nil, "ignored content reached the shadow")
}

// It is a real worktree, so tools that ask git about it get answers.
func TestTheShadowIsAGitWorktree(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "main.go", "package main\n")
	path := materialize(t, dir, snapshot(t, dir))

	root, err := gitOut(path, "rev-parse", "--show-toplevel")
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(root) != "", true)
	// And it is clean, since it was checked out from exactly this state.
	status, err := gitOut(path, "status", "--porcelain")
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(status), "")
}

// Cleanup takes the directory and the metadata git recorded for it, so a repo
// does not accumulate worktrees one background run at a time.
func TestCleanupRemovesTheShadowAndItsMetadata(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "main.go", "package main\n")
	tree := snapshot(t, dir)

	path, cleanup, err := MaterializeTree(dir, tree)
	assert.NilError(t, err)
	before := gitRun(t, dir, "worktree", "list")
	assert.Assert(t, strings.Contains(before, path), "the worktree was not registered: %s", before)

	cleanup()

	_, statErr := os.Stat(path)
	assert.Assert(t, statErr != nil, "the shadow directory survived cleanup")
	after := gitRun(t, dir, "worktree", "list")
	assert.Assert(t, !strings.Contains(after, path), "metadata survived cleanup: %s", after)
}

// Two runs at once must not pick the same directory.
func TestTwoShadowsOfTheSameTreeCoexist(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "main.go", "package main\n")
	tree := snapshot(t, dir)

	first := materialize(t, dir, tree)
	second := materialize(t, dir, tree)
	assert.Assert(t, first != second, "both runs got the same shadow directory")
}

func TestMaterializeTreeRefusesAnEmptySnapshot(t *testing.T) {
	_, _, err := MaterializeTree(setupRepo(t), "")
	assert.Assert(t, err != nil)
}
