package gitrepo

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// SetupGitRepo creates a temp directory with a git repo and a remote pointing
// to github.com/{org}/{repo}. Returns the directory path.
func SetupGitRepo(t *testing.T, org, repo string) string {
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
		{"git", "remote", "add", "origin", fmt.Sprintf("git@github.com:%s/%s.git", org, repo)},
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
