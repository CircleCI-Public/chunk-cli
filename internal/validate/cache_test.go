package validate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// --- CacheKey ---

func TestCacheKey_SameName_SameParts_Equal(t *testing.T) {
	a := CacheKey("cmd", "part1", "part2")
	b := CacheKey("cmd", "part1", "part2")
	assert.Equal(t, a, b)
}

func TestCacheKey_DifferentName_NotEqual(t *testing.T) {
	a := CacheKey("cmdA", "part1")
	b := CacheKey("cmdB", "part1")
	assert.Assert(t, a != b)
}

func TestCacheKey_BoundaryCollision_NotEqual(t *testing.T) {
	// Without length-prefixing ["ab","c"] and ["a","bc"] would hash identically.
	a := CacheKey("cmd", "ab", "c")
	b := CacheKey("cmd", "a", "bc")
	assert.Assert(t, a != b, "boundary collision: different part splits must produce different keys")
}

func TestCacheKey_EmptyName(t *testing.T) {
	// "" is the commandName for "run all"; must still produce a stable key.
	a := CacheKey("", "hash1", "sha", "status")
	b := CacheKey("", "hash1", "sha", "status")
	assert.Equal(t, a, b)
}

// --- BuildCacheKey ---

var testCommands = []config.Command{{Name: "test", Run: "go test ./..."}}

// buildKey asserts the key could be built and returns it.
func buildKey(t *testing.T, dir, commandName string, cmds []config.Command) string {
	t.Helper()
	key, ok := BuildCacheKey(dir, commandName, cmds)
	assert.Assert(t, ok, "expected a usable cache key")
	return key
}

func TestBuildCacheKey_SameInputs_Equal(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	a := buildKey(t, dir, "", testCommands)
	b := buildKey(t, dir, "", testCommands)
	assert.Equal(t, a, b)
}

func TestBuildCacheKey_DifferentCommandName_NotEqual(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	all := buildKey(t, dir, "", testCommands)
	named := buildKey(t, dir, "test", testCommands)
	assert.Assert(t, all != named)
}

func TestBuildCacheKey_DifferentCommands_NotEqual(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	a := buildKey(t, dir, "", testCommands)
	b := buildKey(t, dir, "", []config.Command{{Name: "test", Run: "go test -race ./..."}})
	assert.Assert(t, a != b)
}

func TestBuildCacheKey_UncommittedChange_NotEqual(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	before := buildKey(t, dir, "", testCommands)

	writeFile(t, dir, "new.go", "package p")

	after := buildKey(t, dir, "", testCommands)
	assert.Assert(t, before != after, "working tree change must invalidate key")
}

func TestBuildCacheKey_NewCommit_NotEqual(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	before := buildKey(t, dir, "", testCommands)

	writeFile(t, dir, "x.go", "package p")
	runCmd(t, dir, "git", "add", "x.go")
	runCmd(t, dir, "git", "commit", "-m", "add x")

	after := buildKey(t, dir, "", testCommands)
	assert.Assert(t, before != after, "new commit must invalidate key")
}

// TestBuildCacheKey_EditAlreadyDirtyFile_NotEqual covers the agent edit loop:
// "git status --porcelain" reports " M tracked.go" identically before and
// after a further edit, so a status-only key would report a false cache hit and
// skip validation of the new content.
func TestBuildCacheKey_EditAlreadyDirtyFile_NotEqual(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tracked.go", "package p\n")
	initGitRepo(t, dir)

	writeFile(t, dir, "tracked.go", "package p // first edit\n")
	first := buildKey(t, dir, "", testCommands)

	writeFile(t, dir, "tracked.go", "package p // second edit\n")
	second := buildKey(t, dir, "", testCommands)

	assert.Assert(t, first != second, "editing an already-modified file must invalidate key")
}

// TestBuildCacheKey_EditUntrackedFile_NotEqual is the untracked counterpart:
// "?? new.go" is also stable across edits to that file.
func TestBuildCacheKey_EditUntrackedFile_NotEqual(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	writeFile(t, dir, "new.go", "package p\n")
	first := buildKey(t, dir, "", testCommands)

	writeFile(t, dir, "new.go", "package p // edited\n")
	second := buildKey(t, dir, "", testCommands)

	assert.Assert(t, first != second, "editing an untracked file must invalidate key")
}

// TestBuildCacheKey_EditFileInUntrackedDir_NotEqual guards the -uall flag:
// without it git collapses an untracked directory to a single "dir/" entry and
// the contents of files inside it are never hashed.
func TestBuildCacheKey_EditFileInUntrackedDir_NotEqual(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "pkg"), 0o755))

	writeFile(t, dir, "pkg/new.go", "package pkg\n")
	first := buildKey(t, dir, "", testCommands)

	writeFile(t, dir, "pkg/new.go", "package pkg // edited\n")
	second := buildKey(t, dir, "", testCommands)

	assert.Assert(t, first != second, "editing a file in an untracked dir must invalidate key")
}

func TestBuildCacheKey_StagedChange_NotEqual(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tracked.go", "package p\n")
	initGitRepo(t, dir)

	before := buildKey(t, dir, "", testCommands)

	writeFile(t, dir, "tracked.go", "package p // edited\n")
	runCmd(t, dir, "git", "add", "tracked.go")

	after := buildKey(t, dir, "", testCommands)
	assert.Assert(t, before != after, "staged change must invalidate key")
}

func TestBuildCacheKey_SubDir_MatchesRepoRoot(t *testing.T) {
	// Porcelain paths are repo-root-relative, so a run from a subdirectory must
	// still hash the same working tree rather than failing to open any path.
	dir := t.TempDir()
	initGitRepo(t, dir)
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	writeFile(t, dir, "dirty.go", "package p\n")

	root := buildKey(t, dir, "", testCommands)
	sub := buildKey(t, filepath.Join(dir, "sub"), "", testCommands)
	assert.Equal(t, root, sub)
}

func TestBuildCacheKey_NotARepo_NotOK(t *testing.T) {
	// No git state means the key would depend only on the config, so it must be
	// refused rather than returned as a stable key.
	_, ok := BuildCacheKey(t.TempDir(), "", testCommands)
	assert.Assert(t, !ok, "non-repo dir must not produce a cache key")
}

func TestBuildCacheKey_RepoWithoutCommits_NotOK(t *testing.T) {
	dir := t.TempDir()
	runCmd(t, dir, "git", "init")

	_, ok := BuildCacheKey(dir, "", testCommands)
	assert.Assert(t, !ok, "repo with no HEAD must not produce a cache key")
}

func TestBuildCacheKey_DeletedFile_NotEqual(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "tracked.go", "package p\n")
	initGitRepo(t, dir)

	before := buildKey(t, dir, "", testCommands)

	assert.NilError(t, os.Remove(filepath.Join(dir, "tracked.go")))

	// A deletion is recorded by the status entry; the missing file must not be
	// treated as an error that disables caching.
	after := buildKey(t, dir, "", testCommands)
	assert.Assert(t, before != after, "deletion must invalidate key")
}

func TestBuildCacheKey_RenamedFile_NotEqual(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "old.go", "package p\n")
	initGitRepo(t, dir)

	before := buildKey(t, dir, "", testCommands)

	runCmd(t, dir, "git", "mv", "old.go", "new.go")

	after := buildKey(t, dir, "", testCommands)
	assert.Assert(t, before != after, "rename must invalidate key")
}

// writeFile writes content to dir/rel, failing the test on error.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644))
}

// runCmd executes a command in dir with git env set, failing the test on error.
func runCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, "%s %v: %s", name, args, out)
}
