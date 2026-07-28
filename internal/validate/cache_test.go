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

func TestBuildCacheKey_SameInputs_Equal(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	cmds := []config.Command{{Name: "test", Run: "go test ./..."}}
	a := BuildCacheKey(dir, "", cmds)
	b := BuildCacheKey(dir, "", cmds)
	assert.Equal(t, a, b)
}

func TestBuildCacheKey_DifferentCommandName_NotEqual(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	cmds := []config.Command{{Name: "test", Run: "go test ./..."}}
	all := BuildCacheKey(dir, "", cmds)
	named := BuildCacheKey(dir, "test", cmds)
	assert.Assert(t, all != named)
}

func TestBuildCacheKey_DifferentCommands_NotEqual(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	a := BuildCacheKey(dir, "", []config.Command{{Name: "test", Run: "go test ./..."}})
	b := BuildCacheKey(dir, "", []config.Command{{Name: "test", Run: "go test -race ./..."}})
	assert.Assert(t, a != b)
}

func TestBuildCacheKey_UncommittedChange_NotEqual(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	cmds := []config.Command{{Name: "test", Run: "go test ./..."}}

	before := BuildCacheKey(dir, "", cmds)

	assert.NilError(t, os.WriteFile(filepath.Join(dir, "new.go"), []byte("package p"), 0o644))

	after := BuildCacheKey(dir, "", cmds)
	assert.Assert(t, before != after, "working tree change must invalidate key")
}

func TestBuildCacheKey_NewCommit_NotEqual(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	cmds := []config.Command{{Name: "test", Run: "go test ./..."}}

	before := BuildCacheKey(dir, "", cmds)

	assert.NilError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte("package p"), 0o644))
	runCmd(t, dir, "git", "add", "x.go")
	runCmd(t, dir, "git", "commit", "-m", "add x")

	after := BuildCacheKey(dir, "", cmds)
	assert.Assert(t, before != after, "new commit must invalidate key")
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
