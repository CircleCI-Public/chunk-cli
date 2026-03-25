package gitutil

import (
	"fmt"
	"os"
	"os/exec"
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
