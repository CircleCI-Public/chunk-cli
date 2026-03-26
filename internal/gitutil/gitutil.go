package gitutil

import (
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

// CurrentBranch returns the current git branch name.
// Returns an error if in detached HEAD state or not in a git repo.
func CurrentBranch() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("get current branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
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
			args := append([]string{"reset", "HEAD", "--"}, untracked...)
			_ = exec.Command("git", args...).Run()
		}()
	}

	out, err := exec.Command("git", "diff", base, "--binary").Output()
	if err != nil {
		return "", fmt.Errorf("generate diff: %w", err)
	}
	return string(out), nil
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
