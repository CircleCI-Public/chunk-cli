package gitutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// fingerprint asserts the tree could be fingerprinted and returns it.
func fingerprint(t *testing.T, dir string) Worktree {
	t.Helper()
	wt, err := Fingerprint(dir)
	assert.NilError(t, err)
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
	wt, err := Fingerprint(t.TempDir())
	assert.Assert(t, err != nil, "a non-repo dir must not produce a fingerprint")
	assert.Equal(t, wt, Worktree{})
	assert.Equal(t, wt.Clean, false)
}

func TestFingerprintRepoWithoutCommitsIsUnusable(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init")

	wt, err := Fingerprint(dir)
	assert.Assert(t, err != nil, "a repo with no HEAD must not produce a fingerprint")
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
// at all, so an oversized tree fails like any other unusable state. The error
// has to name the budget, or a repo that silently never caches is undiagnosable.
func TestFingerprintOversizedWorktreeIsUnusable(t *testing.T) {
	dir := setupRepo(t)

	original := maxDigestBytes
	maxDigestBytes = 16
	t.Cleanup(func() { maxDigestBytes = original })

	writeFile(t, dir, "small.go", "package p")
	_, err := Fingerprint(dir)
	assert.NilError(t, err, "a tree within budget must still produce a fingerprint")

	writeFile(t, dir, "big.go", strings.Repeat("x", 64))
	_, err = Fingerprint(dir)
	assert.Assert(t, errors.Is(err, ErrDigestBudget), "want ErrDigestBudget, got %v", err)
	assert.Assert(t, strings.Contains(err.Error(), "big.go"),
		"the error must name the offending path, got %v", err)
}

// TestFingerprintNonRegularChangedPathIsUnusable covers the condition a repo with
// a dirty submodule hits: git reports the path as changed, but its state lives in
// another repository, so hashing it here would produce a digest that does not
// track it. A directory standing in for the submodule reaches the same branch.
func TestFingerprintNonRegularChangedPathIsUnusable(t *testing.T) {
	dir := setupRepo(t)
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	// An empty untracked directory is invisible to git status, so give the
	// submodule stand-in a nested file. With -uall git reports the file, not the
	// directory, so also stage the directory name as a path git must open.
	writeFile(t, dir, "sub/.gitkeep", "")
	gitRun(t, dir, "add", "sub/.gitkeep")
	gitRun(t, dir, "commit", "-m", "add sub")
	assert.NilError(t, os.Remove(filepath.Join(dir, "sub", ".gitkeep")))
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "sub", ".gitkeep"), 0o755))

	_, err := Fingerprint(dir)
	assert.Assert(t, errors.Is(err, ErrNotRegularFile), "want ErrNotRegularFile, got %v", err)
}

// TestFingerprintUnreadableChangedPathIsUnusable is the other half: a changed
// file whose contents cannot be read leaves the digest blind to it, so the whole
// fingerprint fails rather than silently omitting the path.
func TestFingerprintUnreadableChangedPathIsUnusable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	dir := setupRepo(t)
	writeFile(t, dir, "secret.go", "package p\n")
	assert.NilError(t, os.Chmod(filepath.Join(dir, "secret.go"), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "secret.go"), 0o644) })

	_, err := Fingerprint(dir)
	assert.Assert(t, err != nil, "an unreadable changed file must not produce a fingerprint")
	assert.Assert(t, strings.Contains(err.Error(), "secret.go"),
		"the error must name the offending path, got %v", err)
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
