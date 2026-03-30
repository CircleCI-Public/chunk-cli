package gitrepo

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// GitEnv returns a clean environment for git commands isolated to dir.
func GitEnv(dir string) []string {
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

// AddFile stages a file in the git repo at workDir.
func AddFile(t *testing.T, workDir, filename string) {
	t.Helper()
	cmd := exec.Command("git", "add", filename)
	cmd.Dir = workDir
	cmd.Env = GitEnv(workDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git add %s failed: %v\n%s", filename, err, out)
	}
}

// SetupGitRepo creates a temp directory with a git repo and a remote pointing
// to github.com/{org}/{repo}. Returns the directory path.
func SetupGitRepo(t *testing.T, org, repo string) string {
	t.Helper()
	dir := t.TempDir()

	gitEnv := GitEnv(dir)

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
