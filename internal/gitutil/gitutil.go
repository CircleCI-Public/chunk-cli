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

// DefaultBranchIn returns the default branch of the repo rooted at dir — the
// branch a PR merges into. It reads the remote HEAD symref git records at clone
// time, trying origin before upstream so a fork checkout whose canonical remote
// is upstream still resolves. An error is a routine answer, not a failure: a
// repo created with `git init` and pushed has no remote HEAD at all, and
// callers are expected to fall back rather than treat this as fatal.
func DefaultBranchIn(dir string) (string, error) {
	for _, remote := range []string{"origin", "upstream"} {
		out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD").Output()
		if err != nil {
			continue
		}
		// The symref reads back qualified — "origin/main" — and only the branch
		// name is comparable to a config's branch filters.
		if branch := strings.TrimPrefix(strings.TrimSpace(string(out)), remote+"/"); branch != "" {
			return branch, nil
		}
	}
	return "", fmt.Errorf("no remote HEAD is set for %s", dir)
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
