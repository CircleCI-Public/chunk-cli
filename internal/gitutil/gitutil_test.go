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
