package gitutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/changeset"
)

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	tree, err := SnapshotTree(dir)
	assert.NilError(t, err)
	assert.Assert(t, tree != "", "no tree SHA was returned")
	return tree
}

func between(t *testing.T, dir, base string) changeset.Changes {
	t.Helper()
	ch, err := ChangesBetween(dir, base)
	assert.NilError(t, err)
	return ch
}

func TestSnapshotTreeIsStableForTheSameTree(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	assert.Equal(t, snapshot(t, dir), snapshot(t, dir))
}

func TestSnapshotTreeChangesWithTheTree(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	before := snapshot(t, dir)
	writeFile(t, dir, "a.txt", "two\n")
	assert.Assert(t, snapshot(t, dir) != before, "an edit did not change the snapshot")
}

// The developer's staging area is not ours to move. A snapshot that staged
// their work would show up in the next commit they made by hand.
func TestSnapshotTreeLeavesTheRealIndexAlone(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "staged.txt", "one\n")
	writeFile(t, dir, "staged.txt", "two\n")
	gitRun(t, dir, "add", "staged.txt")
	writeFile(t, dir, "unstaged.txt", "three\n")

	before := gitRun(t, dir, "status", "--porcelain")
	snapshot(t, dir)
	assert.Equal(t, gitRun(t, dir, "status", "--porcelain"), before,
		"the snapshot moved the index")
}

// Staged and unstaged work are both part of the state being validated, so both
// are in the snapshot.
func TestSnapshotTreeIncludesStagedAndUntrackedWork(t *testing.T) {
	dir := setupRepo(t)
	base := snapshot(t, dir)

	writeFile(t, dir, "staged.txt", "one\n")
	gitRun(t, dir, "add", "staged.txt")
	writeFile(t, dir, "untracked.txt", "two\n")

	ch := between(t, dir, base)
	assert.Equal(t, len(ch.Paths), 2, "got %v", ch.Paths)
	assert.Equal(t, ch.Lines, 2)
}

func TestSnapshotTreeExcludesIgnoredFiles(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, ".gitignore", "build/\n")
	gitRun(t, dir, "add", ".gitignore")
	gitRun(t, dir, "commit", "-m", "ignore build")
	base := snapshot(t, dir)

	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "build"), 0o755))
	writeFile(t, dir, "build/artifact.bin", strings.Repeat("x\n", 5000))

	assert.Equal(t, between(t, dir, base).Empty(), true,
		"an ignored build artifact was measured as a change")
}

func TestChangesBetweenMeasuresTheEditSinceTheSnapshot(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "main.go", "package main\n")
	base := snapshot(t, dir)

	writeFile(t, dir, "main.go", "package main\n"+strings.Repeat("// line\n", 100))

	ch := between(t, dir, base)
	assert.DeepEqual(t, ch.Paths, []string{"main.go"})
	assert.Equal(t, ch.Lines, 100)
	assert.Equal(t, ch.Baseline, base)
}

func TestChangesBetweenIsEmptyForAnUntouchedTree(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	base := snapshot(t, dir)

	ch := between(t, dir, base)
	assert.Equal(t, ch.Empty(), true)
	assert.Equal(t, ch.Lines, 0)
}

// The whole reason for measuring against a snapshot rather than HEAD: a commit
// moves HEAD but not the content, so it neither hides a change nor resets the
// count. Measured against HEAD the same tree reports nothing at all.
func TestChangesBetweenIsUnaffectedByACommit(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "main.go", "package main\n")
	base := snapshot(t, dir)

	writeFile(t, dir, "main.go", "package main\n"+strings.Repeat("// line\n", 100))
	gitRun(t, dir, "add", "main.go")
	gitRun(t, dir, "commit", "-m", "the agent committed its work")

	assert.Equal(t, between(t, dir, base).Lines, 100,
		"a commit hid the change from the measurement")
	assert.Equal(t, changes(t, dir).Lines, 0,
		"HEAD is expected to see nothing here — that is the problem being solved")
}

// A snapshot is not a durable handle: git collects it in its own time. A caller
// holding a collected one is told so, and falls back to measuring against HEAD.
func TestChangesBetweenRefusesAnUnknownSnapshot(t *testing.T) {
	dir := setupRepo(t)
	_, err := ChangesBetween(dir, "0000000000000000000000000000000000000000")
	assert.Assert(t, err != nil, "an unknown tree must not produce a measurement")
}

func TestSnapshotTreeNotARepoIsUnusable(t *testing.T) {
	_, err := SnapshotTree(t.TempDir())
	assert.Assert(t, err != nil, "a non-repo dir must not produce a snapshot")
}
