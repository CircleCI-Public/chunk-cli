package gitutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// changes asserts the tree could be measured and returns the measurement.
func changes(t *testing.T, dir string) Changes {
	t.Helper()
	ch, err := WorkingChanges(dir)
	assert.NilError(t, err)
	return ch
}

// commitFile writes rel and commits it, so later edits to it are tracked
// changes rather than a new file.
func commitFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	writeFile(t, dir, rel, content)
	gitRun(t, dir, "add", rel)
	gitRun(t, dir, "commit", "-m", "add "+rel)
}

func TestWorkingChangesCleanTreeIsEmpty(t *testing.T) {
	ch := changes(t, setupRepo(t))
	assert.Equal(t, ch.Empty(), true)
	assert.Equal(t, ch.Lines, 0)
}

// An untracked file is the change git will not diff, so its lines are counted
// here or not at all.
func TestWorkingChangesCountsEveryLineOfAnUntrackedFile(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "new.go", "package main\n\nfunc main() {}\n")

	ch := changes(t, dir)
	assert.DeepEqual(t, ch.Paths, []string{"new.go"})
	assert.Equal(t, ch.Lines, 3)
}

// A final line with no trailing newline is still a line.
func TestWorkingChangesCountsAnUnterminatedLastLine(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "new.txt", "one\ntwo")

	assert.Equal(t, changes(t, dir).Lines, 2)
}

func TestWorkingChangesCountsAnEditAsInsertionAndDeletion(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, dir, "main.go", "package main\n\nfunc main() { println() }\n")

	ch := changes(t, dir)
	assert.DeepEqual(t, ch.Paths, []string{"main.go"})
	// One rewritten line is one insertion and one deletion.
	assert.Equal(t, ch.Lines, 2)
}

// Staged and unstaged edits are one change against HEAD, not two measurements.
func TestWorkingChangesCountsStagedAndUnstagedTogether(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "a.txt", "one\n")
	commitFile(t, dir, "b.txt", "one\n")
	writeFile(t, dir, "a.txt", "one\ntwo\n")
	gitRun(t, dir, "add", "a.txt")
	writeFile(t, dir, "b.txt", "one\ntwo\n")

	ch := changes(t, dir)
	assert.Equal(t, len(ch.Paths), 2)
	assert.Equal(t, ch.Lines, 2)
}

// Deleting is a change, and a large deletion is a large change.
func TestWorkingChangesCountsDeletedLines(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "gone.txt", "one\ntwo\nthree\n")
	assert.NilError(t, os.Remove(filepath.Join(dir, "gone.txt")))

	assert.Equal(t, changes(t, dir).Lines, 3)
}

// A rename names the file now on disk, since that is the one a caller can
// classify or read.
func TestWorkingChangesNamesTheDestinationOfARename(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "before.md", "docs\n")
	gitRun(t, dir, "mv", "before.md", "after.md")

	assert.DeepEqual(t, changes(t, dir).Paths, []string{"after.md"})
}

// Newlines in a binary file are not lines. The path is still reported: what
// changed is not in doubt, only how much of it.
func TestWorkingChangesCountsNoLinesInBinaryContent(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "blob.bin", "\x00\x01\n\n\n\x00")

	ch := changes(t, dir)
	assert.DeepEqual(t, ch.Paths, []string{"blob.bin"})
	assert.Equal(t, ch.Lines, 0)
}

// Past the read budget the measurement fails rather than reporting a partial
// count, because a partial count reads as a smaller change than the real one.
func TestWorkingChangesRefusesMoreUntrackedContentThanTheBudget(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "big.txt", strings.Repeat("line\n", 1000))

	original := maxCountBytes
	maxCountBytes = 10
	defer func() { maxCountBytes = original }()

	ch, err := WorkingChanges(dir)
	assert.Assert(t, errors.Is(err, ErrCountBudget), "got %v", err)
	assert.DeepEqual(t, ch, Changes{})
}

// A tree whose state cannot be established reports the zero value, which reads
// as a clean tree — so callers must treat the error, not the value, as the
// answer.
func TestWorkingChangesNotARepoIsUnusable(t *testing.T) {
	ch, err := WorkingChanges(t.TempDir())
	assert.Assert(t, err != nil, "a non-repo dir must not produce a measurement")
	assert.DeepEqual(t, ch, Changes{})
}

func TestWorkingChangesRepoWithoutCommitsIsUnusable(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init")
	writeFile(t, dir, "new.txt", "one\n")

	_, err := WorkingChanges(dir)
	assert.Assert(t, err != nil, "a repo with no HEAD must not produce a measurement")
}
