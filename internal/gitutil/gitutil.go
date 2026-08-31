package gitutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoRoot returns the root directory of the current git repository
// by walking up from the given directory looking for .git/.
func RepoRoot(from string) (string, error) {
	dir := from
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in a git repository")
		}
		dir = parent
	}
}

// CurrentBranchIn returns the current git branch name for the repo rooted at dir.
// Returns an error if in detached HEAD state or not in a git repo.
func CurrentBranchIn(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("get current branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		return "", fmt.Errorf("detached HEAD state")
	}
	return branch, nil
}

// HeadRef returns the SHA of the current HEAD commit in the repo at cwd.
func HeadRef(cwd string) (string, error) {
	return HeadRefCtx(context.Background(), cwd)
}

// HeadRefCtx returns the SHA of the current HEAD commit in the repo at cwd,
// honouring ctx for cancellation/timeout.
func HeadRefCtx(ctx context.Context, cwd string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	sha := trimNewline(out)
	if sha == "" {
		return "", fmt.Errorf("resolve HEAD: empty output")
	}
	return sha, nil
}

// TopLevelCtx returns the git repository root for dir, or "" if not in a git
// repo, honouring ctx for cancellation/timeout.
func TopLevelCtx(ctx context.Context, dir string) string {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return trimNewline(out)
}

func trimNewline(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
