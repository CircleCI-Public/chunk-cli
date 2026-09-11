package filestate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/changeset"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	assert.NilError(t, os.WriteFile(path, []byte(content), 0o644))
}

func build(t *testing.T, dir string) Index {
	t.Helper()
	idx, err := Build(dir)
	assert.NilError(t, err)
	return idx
}

func TestBuildIndexesEveryFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main\n")
	write(t, dir, "docs/guide.md", "one\ntwo\n")

	idx := build(t, dir)
	assert.Equal(t, len(idx), 2)
	assert.Equal(t, idx["main.go"].Lines, 1)
	assert.Equal(t, idx["docs/guide.md"].Lines, 2)
	assert.Assert(t, idx["main.go"].Hash != "", "no content hash was recorded")
}

// No earlier state means the whole tree is the change: there is nothing it
// could be smaller than.
func TestChangesAgainstNothingIsTheWholeTree(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "one\ntwo\n")
	write(t, dir, "b.txt", "three\n")

	ch := build(t, dir).Changes(nil)
	assert.DeepEqual(t, ch.Paths, []string{"a.txt", "b.txt"})
	assert.Equal(t, ch.Lines, 3)
	assert.Equal(t, ch.Baseline, changeset.BaselineWholeTree)
	assert.Equal(t, ch.Incremental(), false)
}

func TestChangesReportsAddedRemovedAndModified(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "kept.txt", "one\n")
	write(t, dir, "gone.txt", "one\ntwo\n")
	before := build(t, dir)

	assert.NilError(t, os.Remove(filepath.Join(dir, "gone.txt")))
	write(t, dir, "new.txt", "three\n")

	ch := build(t, dir).Changes(before)
	assert.DeepEqual(t, ch.Paths, []string{"gone.txt", "new.txt"})
	// The deleted file's two lines and the new file's one.
	assert.Equal(t, ch.Lines, 3)
	assert.Equal(t, ch.Incremental(), true)
}

// An unchanged tree is unchanged, however many files it has.
func TestChangesIsEmptyForAnUntouchedTree(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "one\n")
	before := build(t, dir)

	assert.Equal(t, build(t, dir).Changes(before).Empty(), true)
}

// Without the earlier contents there is no telling how much of a modified file
// moved, so it counts the most it could have. Over-measuring makes somebody
// wait; under-measuring waves a change through.
func TestAModifiedFileCountsItsWholeLength(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", strings.Repeat("// line\n", 100))
	before := build(t, dir)

	write(t, dir, "main.go", strings.Repeat("// line\n", 99)+"// edited\n")

	ch := build(t, dir).Changes(before)
	assert.DeepEqual(t, ch.Paths, []string{"main.go"})
	assert.Equal(t, ch.Lines, 100, "a one-line edit must count the file, not the line")
}

func TestBuildSkipsTheGitDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".git/objects/ab/cdef", "blob")
	write(t, dir, "main.go", "package main\n")

	idx := build(t, dir)
	assert.Equal(t, len(idx), 1)
	assert.Assert(t, idx["main.go"].Hash != "")
}

func TestBuildHonoursTheSimplePartsOfGitignore(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".gitignore", "# comment\nnode_modules/\n*.log\nsecrets.env\n")
	write(t, dir, "node_modules/pkg/index.js", "junk\n")
	write(t, dir, "debug.log", "noise\n")
	write(t, dir, "secrets.env", "TOKEN=x\n")
	write(t, dir, "main.go", "package main\n")

	idx := build(t, dir)
	assert.Equal(t, len(idx), 2, "got %v", idx)
	assert.Assert(t, idx["main.go"].Hash != "")
	assert.Assert(t, idx[".gitignore"].Hash != "")
}

// A pattern this does not fully understand is walked rather than guessed at.
// Counting a file that should have been ignored costs time; missing one that
// changed is the failure this package must not have.
func TestBuildWalksPatternsItDoesNotUnderstand(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".gitignore", "!keep.txt\nsrc/**/gen.go\ndeep/nested/path/thing\n")
	write(t, dir, "keep.txt", "one\n")
	write(t, dir, "src/a/gen.go", "package a\n")

	idx := build(t, dir)
	assert.Assert(t, idx["keep.txt"].Hash != "", "a negation was misread as an ignore")
	assert.Assert(t, idx["src/a/gen.go"].Hash != "", "a ** pattern was misread as an ignore")
}

// Newlines in a binary file are not lines.
func TestBinaryContentCountsNoLines(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "blob.bin", "\x00\x01\n\n\n")

	assert.Equal(t, build(t, dir)["blob.bin"].Lines, 0)
}

// Past the budget it fails rather than returning a partial index, since a
// partial index is a change that reads smaller than it is.
func TestBuildRefusesATreeOverTheBudget(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		write(t, dir, name, "one\n")
	}

	original := maxFiles
	maxFiles = 2
	defer func() { maxFiles = original }()

	_, err := Build(dir)
	assert.Assert(t, errors.Is(err, ErrWalkBudget), "got %v", err)
}

func TestBuildNotADirectoryIsAnError(t *testing.T) {
	_, err := Build(filepath.Join(t.TempDir(), "nope"))
	assert.Assert(t, err != nil)
}

// Two equal digests are the same content, which is all the staleness check
// needs of them.
func TestDigestIsStableAndContentAddressed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "one\n")
	write(t, dir, "b/c.txt", "two\n")

	first := build(t, dir).Digest()
	assert.Equal(t, first, build(t, dir).Digest())

	write(t, dir, "a.txt", "changed\n")
	assert.Assert(t, build(t, dir).Digest() != first, "an edit did not change the digest")
}

// A file moved between two paths with the same contents is still a change: the
// digest folds in the paths, not only what is in them.
func TestDigestCoversPathsNotJustContents(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "one\n")
	before := build(t, dir).Digest()

	assert.NilError(t, os.Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")))
	assert.Assert(t, build(t, dir).Digest() != before, "a rename did not change the digest")
}
