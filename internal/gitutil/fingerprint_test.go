package gitutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// fingerprint asserts the tree could be fingerprinted and returns it.
func fingerprint(t *testing.T, dir string) Worktree {
	t.Helper()
	wt, ok := Fingerprint(dir)
	assert.Assert(t, ok, "expected a usable fingerprint for %s", dir)
	return wt
}

// writeFile writes content to dir/rel, failing the test on error.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644))
}

func TestFingerprintSameTreeIsStable(t *testing.T) {
	dir := setupRepo(t)
	assert.Equal(t, fingerprint(t, dir), fingerprint(t, dir))
}

func TestFingerprintCleanRepoIsClean(t *testing.T) {
	wt := fingerprint(t, setupRepo(t))
	assert.Equal(t, wt.Clean, true)
}

func TestFingerprintUntrackedFileIsNotClean(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "dirty.txt", "change")

	wt := fingerprint(t, dir)
	assert.Equal(t, wt.Clean, false)
}

// TestFingerprintNotARepoIsUnusable pins the contract callers depend on to fail
// open: a directory with no git state yields the zero Worktree, which reports
// the tree as not clean rather than claiming there is nothing to do.
func TestFingerprintNotARepoIsUnusable(t *testing.T) {
	wt, ok := Fingerprint(t.TempDir())
	assert.Assert(t, !ok, "a non-repo dir must not produce a fingerprint")
	assert.Equal(t, wt, Worktree{})
	assert.Equal(t, wt.Clean, false)
}

func TestFingerprintRepoWithoutCommitsIsUnusable(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init")

	wt, ok := Fingerprint(dir)
	assert.Assert(t, !ok, "a repo with no HEAD must not produce a fingerprint")
	assert.Equal(t, wt, Worktree{})
}

func TestFingerprintUncommittedChangeChangesDigest(t *testing.T) {
	dir := setupRepo(t)
	before := fingerprint(t, dir)

	writeFile(t, dir, "new.go", "package p")

	assert.Assert(t, before.Digest != fingerprint(t, dir).Digest,
		"working tree change must change the digest")
}

func TestFingerprintNewCommitChangesHead(t *testing.T) {
	dir := setupRepo(t)
	before := fingerprint(t, dir)

	writeFile(t, dir, "x.go", "package p")
	gitRun(t, dir, "add", "x.go")
	gitRun(t, dir, "commit", "-m", "add x")

	after := fingerprint(t, dir)
	assert.Assert(t, before.Head != after.Head, "new commit must change HEAD")
	assert.Equal(t, after.Clean, true)
}

// TestFingerprintEditAlreadyDirtyFileChangesDigest covers the agent edit loop:
// "git status --porcelain" reports " M tracked.go" identically before and after
// a further edit, so a status-only digest would call the tree unchanged and let
// a caller skip work on the new content.
func TestFingerprintEditAlreadyDirtyFileChangesDigest(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "tracked.go", "package p\n")
	gitRun(t, dir, "add", "tracked.go")
	gitRun(t, dir, "commit", "-m", "add tracked")

	writeFile(t, dir, "tracked.go", "package p // first edit\n")
	first := fingerprint(t, dir)

	writeFile(t, dir, "tracked.go", "package p // second edit\n")
	second := fingerprint(t, dir)

	assert.Assert(t, first.Digest != second.Digest,
		"editing an already-modified file must change the digest")
}

// TestFingerprintEditUntrackedFileChangesDigest is the untracked counterpart:
// "?? new.go" is also stable across edits to that file.
func TestFingerprintEditUntrackedFileChangesDigest(t *testing.T) {
	dir := setupRepo(t)

	writeFile(t, dir, "new.go", "package p\n")
	first := fingerprint(t, dir)

	writeFile(t, dir, "new.go", "package p // edited\n")
	second := fingerprint(t, dir)

	assert.Assert(t, first.Digest != second.Digest,
		"editing an untracked file must change the digest")
}

// TestFingerprintEditFileInUntrackedDirChangesDigest guards the -uall flag:
// without it git collapses an untracked directory to a single "dir/" entry and
// the contents of files inside it are never hashed.
func TestFingerprintEditFileInUntrackedDirChangesDigest(t *testing.T) {
	dir := setupRepo(t)
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "pkg"), 0o755))

	writeFile(t, dir, "pkg/new.go", "package pkg\n")
	first := fingerprint(t, dir)

	writeFile(t, dir, "pkg/new.go", "package pkg // edited\n")
	second := fingerprint(t, dir)

	assert.Assert(t, first.Digest != second.Digest,
		"editing a file in an untracked dir must change the digest")
}

func TestFingerprintStagedChangeChangesDigest(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "tracked.go", "package p\n")
	gitRun(t, dir, "add", "tracked.go")
	gitRun(t, dir, "commit", "-m", "add tracked")

	before := fingerprint(t, dir)

	writeFile(t, dir, "tracked.go", "package p // edited\n")
	gitRun(t, dir, "add", "tracked.go")

	assert.Assert(t, before.Digest != fingerprint(t, dir).Digest,
		"staged change must change the digest")
}

func TestFingerprintFromSubDirMatchesRepoRoot(t *testing.T) {
	// Porcelain paths are repo-root-relative, so a run from a subdirectory must
	// still hash the same working tree rather than failing to open any path.
	dir := setupRepo(t)
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	writeFile(t, dir, "dirty.go", "package p\n")

	assert.Equal(t, fingerprint(t, dir), fingerprint(t, filepath.Join(dir, "sub")))
}

// TestFingerprintOversizedWorktreeIsUnusable guards the digest budget: hashing
// an unbounded amount of content on every call is worse than not fingerprinting
// at all, so an oversized tree fails like any other unusable state.
func TestFingerprintOversizedWorktreeIsUnusable(t *testing.T) {
	dir := setupRepo(t)

	original := maxDigestBytes
	maxDigestBytes = 16
	t.Cleanup(func() { maxDigestBytes = original })

	writeFile(t, dir, "small.go", "package p")
	_, ok := Fingerprint(dir)
	assert.Assert(t, ok, "a tree within budget must still produce a fingerprint")

	writeFile(t, dir, "big.go", strings.Repeat("x", 64))
	_, ok = Fingerprint(dir)
	assert.Assert(t, !ok, "a tree over the digest budget must not produce a fingerprint")
}

func TestFingerprintDeletedFileChangesDigest(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "tracked.go", "package p\n")
	gitRun(t, dir, "add", "tracked.go")
	gitRun(t, dir, "commit", "-m", "add tracked")

	before := fingerprint(t, dir)

	assert.NilError(t, os.Remove(filepath.Join(dir, "tracked.go")))

	// A deletion is recorded by the status entry; the missing file must not be
	// treated as an error that makes the whole tree unusable.
	assert.Assert(t, before.Digest != fingerprint(t, dir).Digest,
		"deletion must change the digest")
}

func TestFingerprintRenamedFileChangesDigest(t *testing.T) {
	dir := setupRepo(t)
	writeFile(t, dir, "old.go", "package p\n")
	gitRun(t, dir, "add", "old.go")
	gitRun(t, dir, "commit", "-m", "add old")

	before := fingerprint(t, dir)

	gitRun(t, dir, "mv", "old.go", "new.go")

	assert.Assert(t, before.Digest != fingerprint(t, dir).Digest,
		"rename must change the digest")
}
