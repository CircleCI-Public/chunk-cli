package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNoOriginHEAD is returned by MergeBase when the upstream tracking branch is
// set but origin/HEAD is not — a common state after git init + push without fetch.
var ErrNoOriginHEAD = errors.New("origin/HEAD is not set")

const gitHEAD = "HEAD"

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

// CurrentBranch returns the current git branch name.
// Returns an error if in detached HEAD state or not in a git repo.
func CurrentBranch() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", gitHEAD).Output()
	if err != nil {
		return "", fmt.Errorf("get current branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == gitHEAD {
		return "", fmt.Errorf("detached HEAD state")
	}
	return branch, nil
}

// IsBranchPushed returns true if the current branch exists on the remote
// (i.e. refs/remotes/origin/<branch> is present locally).
func IsBranchPushed() bool {
	branch, err := CurrentBranch()
	if err != nil {
		return false
	}
	ref := "refs/remotes/origin/" + branch
	return exec.Command("git", "rev-parse", "--verify", ref).Run() == nil
}

// MergeBase returns a commit SHA that the remote is guaranteed to have.
// Tries merge-base between upstream and origin/HEAD first, falls back to origin/HEAD.
func MergeBase() (string, error) {
	out, err := exec.Command("git", "merge-base", "@{upstream}", "origin/HEAD").Output()
	if err == nil {
		sha := strings.TrimSpace(string(out))
		if sha != "" {
			return sha, nil
		}
	}

	out, err = exec.Command("git", "rev-parse", "origin/HEAD").Output()
	if err != nil {
		if exec.Command("git", "rev-parse", "--verify", "@{upstream}").Run() == nil {
			return "", fmt.Errorf("resolve remote base: %w", ErrNoOriginHEAD)
		}
		return "", fmt.Errorf("resolve remote base: no upstream tracking branch or origin/HEAD found")
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("resolve remote base: origin/HEAD is empty")
	}
	return sha, nil
}

// GeneratePatch generates a binary diff from the given base commit,
// including untracked files. It temporarily stages untracked files
// with git add -N and resets them after generating the diff.
func GeneratePatch(base string) (string, error) {
	// Find untracked files
	lsOut, err := exec.Command("git", "ls-files", "--others", "--exclude-standard").Output()
	if err != nil {
		return "", fmt.Errorf("list untracked files: %w", err)
	}

	untracked := splitNonEmpty(strings.TrimSpace(string(lsOut)))

	// Temporarily stage untracked files so they appear in the diff
	if len(untracked) > 0 {
		args := append([]string{"add", "-N", "--"}, untracked...)
		if err := exec.Command("git", args...).Run(); err != nil {
			return "", fmt.Errorf("stage untracked files: %w", err)
		}
		defer func() {
			args := append([]string{"reset", gitHEAD, "--"}, untracked...)
			_ = exec.Command("git", args...).Run()
		}()
	}

	out, err := exec.Command("git", "diff", base, "--binary").Output()
	if err != nil {
		return "", fmt.Errorf("generate diff: %w", err)
	}
	return string(out), nil
}

// HeadRef returns the SHA of the current HEAD commit in the repo at cwd.
func HeadRef(cwd string) (string, error) {
	return HeadRefCtx(context.Background(), cwd)
}

// HeadRefCtx returns the SHA of the current HEAD commit in the repo at cwd,
// honouring ctx for cancellation/timeout.
func HeadRefCtx(ctx context.Context, cwd string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", gitHEAD)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	sha := strings.TrimSpace(string(out))
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
	return strings.TrimSpace(string(out))
}

// CreateBundle creates a git bundle from base..HEAD in the repo at cwd and returns the raw bytes.
// If base is empty, the full history up to HEAD is bundled.
func CreateBundle(base, cwd string) ([]byte, error) {
	var args []string
	if base == "" {
		args = []string{"bundle", "create", "-", gitHEAD}
	} else {
		args = []string{"bundle", "create", "-", base + "..HEAD"}
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("create bundle: %w", err)
	}
	return out, nil
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
