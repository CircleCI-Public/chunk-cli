package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func setupRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	gitEnv := []string{
		fmt.Sprintf("HOME=%s", dir),
		fmt.Sprintf("PATH=%s", os.Getenv("PATH")),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	}

	commands := [][]string{
		{"git", "init"},
		{"git", "checkout", "-b", "main"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}

	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	return dir
}

// gitEnvFor returns a clean git environment rooted at dir.
func gitEnvFor(dir string) []string {
	return []string{
		fmt.Sprintf("HOME=%s", dir),
		fmt.Sprintf("PATH=%s", os.Getenv("PATH")),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	}
}

// gitRun runs a git command in dir with a clean environment.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnvFor(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRepoRoot(t *testing.T) {
	t.Parallel()

	dir := setupRepo(t)

	// From repo root itself
	root, err := RepoRoot(dir)
	assert.NilError(t, err)
	assert.Equal(t, root, dir)

	// From a subdirectory
	sub := filepath.Join(dir, "sub", "deep")
	err = os.MkdirAll(sub, 0o755)
	assert.NilError(t, err)
	root, err = RepoRoot(sub)
	assert.NilError(t, err)
	assert.Equal(t, root, dir)

	// From a non-repo directory
	noRepo := t.TempDir()
	root, err = RepoRoot(noRepo)
	if err == nil {
		t.Skipf("temporary directory is inside git repository %s", root)
	}
	assert.Assert(t, err != nil, "expected error for non-repo dir")
}

func TestCurrentBranchIn(t *testing.T) {
	dir := setupRepo(t)

	branch, err := CurrentBranchIn(dir)
	assert.NilError(t, err)
	assert.Equal(t, branch, "main")

	gitRun(t, dir, "checkout", "--detach", "HEAD")
	_, err = CurrentBranchIn(dir)
	assert.ErrorContains(t, err, "detached HEAD")
}

func TestCurrentBranchInCtxHonoursCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := CurrentBranchInCtx(ctx, setupRepo(t))
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
}

func TestDefaultBranchIn(t *testing.T) {
	t.Run("prefers origin", func(t *testing.T) {
		dir := setupRepo(t)
		setRemoteHead(t, dir, "upstream", "trunk")
		setRemoteHead(t, dir, "origin", "main")

		branch, err := DefaultBranchIn(dir)
		assert.NilError(t, err)
		assert.Equal(t, branch, "main")
	})

	t.Run("falls back to upstream", func(t *testing.T) {
		dir := setupRepo(t)
		setRemoteHead(t, dir, "upstream", "trunk")

		branch, err := DefaultBranchIn(dir)
		assert.NilError(t, err)
		assert.Equal(t, branch, "trunk")
	})

	t.Run("reports missing remote HEAD", func(t *testing.T) {
		dir := setupRepo(t)

		_, err := DefaultBranchIn(dir)
		assert.ErrorContains(t, err, "no remote HEAD")
	})
}

func TestHeadRef(t *testing.T) {
	dir := setupRepo(t)
	want := gitRun(t, dir, "rev-parse", "HEAD")

	got, err := HeadRef(dir)
	assert.NilError(t, err)
	assert.Equal(t, got, want)
}

func TestHeadRefCtxHonoursCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := HeadRefCtx(ctx, setupRepo(t))
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
}

func TestTopLevelCtx(t *testing.T) {
	dir := setupRepo(t)
	subdir := filepath.Join(dir, "one", "two")
	assert.NilError(t, os.MkdirAll(subdir, 0o755))

	assert.Equal(t, TopLevelCtx(context.Background(), subdir), dir)
	noRepo := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(noRepo))
	assert.Equal(t, TopLevelCtx(context.Background(), noRepo), "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Equal(t, TopLevelCtx(ctx, dir), "")
}

func setRemoteHead(t *testing.T, dir, remote, branch string) {
	t.Helper()
	gitRun(t, dir, "remote", "add", remote, "https://example.com/repo.git")
	gitRun(t, dir, "update-ref", "refs/remotes/"+remote+"/"+branch, "HEAD")
	gitRun(t, dir, "symbolic-ref", "refs/remotes/"+remote+"/HEAD", "refs/remotes/"+remote+"/"+branch)
}
