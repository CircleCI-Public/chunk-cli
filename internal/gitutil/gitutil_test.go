package gitutil

import (
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

func TestRepoRoot(t *testing.T) {
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
	_, err = RepoRoot(noRepo)
	assert.Assert(t, err != nil, "expected error for non-repo dir")
}

func TestCurrentBranch(t *testing.T) {
	dir := setupRepo(t)

	// Run CurrentBranch from the temp repo
	origDir, _ := os.Getwd()
	defer func() { _ = os.Chdir(origDir) }()
	_ = os.Chdir(dir)

	branch, err := CurrentBranch()
	assert.NilError(t, err)
	assert.Equal(t, branch, "main")
}

func TestSplitNonEmpty(t *testing.T) {
	assert.DeepEqual(t, splitNonEmpty(""), []string(nil))
	assert.DeepEqual(t, splitNonEmpty("a\nb\n"), []string{"a", "b"})
	assert.DeepEqual(t, splitNonEmpty("single"), []string{"single"})
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

func TestMergeBase(t *testing.T) {
	// Set up a "remote" bare repo and a local clone so origin/HEAD exists.
	bare := t.TempDir()
	gitRun(t, bare, "init", "--bare")

	local := t.TempDir()
	gitRun(t, local, "clone", bare, ".")
	gitRun(t, local, "checkout", "-b", "main")
	_ = os.WriteFile(filepath.Join(local, "file.txt"), []byte("hello\n"), 0o644)
	gitRun(t, local, "add", "file.txt")
	gitRun(t, local, "commit", "-m", "init")
	gitRun(t, local, "push", "-u", "origin", "main")
	// Set origin/HEAD so MergeBase fallback works
	gitRun(t, local, "remote", "set-head", "origin", "main")

	// Create a feature branch with a commit
	gitRun(t, local, "checkout", "-b", "feature")
	gitRun(t, local, "push", "-u", "origin", "feature")
	_ = os.WriteFile(filepath.Join(local, "feature.txt"), []byte("new\n"), 0o644)
	gitRun(t, local, "add", "feature.txt")
	gitRun(t, local, "commit", "-m", "feature work")

	origDir, _ := os.Getwd()
	defer func() { _ = os.Chdir(origDir) }()
	_ = os.Chdir(local)

	sha, err := MergeBase()
	assert.NilError(t, err)
	assert.Assert(t, len(sha) >= 7, "expected a commit SHA, got %q", sha)
}

func TestGeneratePatch(t *testing.T) {
	dir := setupRepo(t)

	origDir, _ := os.Getwd()
	defer func() { _ = os.Chdir(origDir) }()
	_ = os.Chdir(dir)

	// Get the base commit
	base := gitRun(t, dir, "rev-parse", "HEAD")

	// Add a tracked change
	_ = os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked content\n"), 0o644)
	gitRun(t, dir, "add", "tracked.txt")
	gitRun(t, dir, "commit", "-m", "add tracked")

	// Add an untracked file (should appear in the patch)
	_ = os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("untracked content\n"), 0o644)

	patch, err := GeneratePatch(base)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(patch, "tracked.txt"), "patch should contain tracked file")
	assert.Assert(t, strings.Contains(patch, "untracked.txt"), "patch should contain untracked file")

	// Verify untracked file was unstaged (reset) after patch generation
	statusOut := gitRun(t, dir, "status", "--porcelain")
	assert.Assert(t, strings.Contains(statusOut, "?? untracked.txt"), "untracked file should be restored to untracked state")
}

func TestGeneratePatchNoChanges(t *testing.T) {
	dir := setupRepo(t)

	origDir, _ := os.Getwd()
	defer func() { _ = os.Chdir(origDir) }()
	_ = os.Chdir(dir)

	base := gitRun(t, dir, "rev-parse", "HEAD")

	patch, err := GeneratePatch(base)
	assert.NilError(t, err)
	assert.Equal(t, patch, "", "patch should be empty when no changes")
}
