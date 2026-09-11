package gitutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SnapshotTree captures the whole working state at dir as a git tree object and
// returns its SHA: tracked files, staged edits and untracked files alike, minus
// whatever .gitignore excludes.
//
// It exists so a caller can ask "what has changed since this state" later on,
// about a state that was never committed. A fingerprint cannot answer that — it
// says whether the tree is the same tree, not what moved — and HEAD cannot
// either, since the state worth comparing against is usually the last one that
// passed its checks rather than the last one somebody committed.
//
// Nothing about the developer's repository moves. The index is a throwaway copy
// in a temp file (GIT_INDEX_FILE), so no staging is touched, and HEAD and the
// working tree are never written. The copy is seeded from the real index for its
// stat cache: without it every file would be re-hashed on every call, which on a
// large repo is the difference between milliseconds and seconds.
//
// It does write blob and tree objects into .git/objects. They are unreferenced,
// so git's own gc collects them in its own time — which is also why a snapshot
// is not a durable handle. A caller holding one that has since been collected
// gets an error from ChangesBetween and should fall back to measuring against
// HEAD.
func SnapshotTree(dir string) (string, error) {
	gitDir, err := gitOut(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git dir: %w", err)
	}

	tmp, err := os.CreateTemp("", "chunk-index-")
	if err != nil {
		return "", fmt.Errorf("create temp index: %w", err)
	}
	indexPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(indexPath) }()

	// Best-effort: a missing or unreadable index costs speed, not correctness.
	if data, err := os.ReadFile(filepath.Join(strings.TrimSpace(gitDir), "index")); err == nil {
		_ = os.WriteFile(indexPath, data, 0o600)
	}

	env := append(os.Environ(), "GIT_INDEX_FILE="+indexPath)
	if _, err := gitOutEnv(dir, env, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage working tree: %w", err)
	}
	out, err := gitOutEnv(dir, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write tree: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// ChangesBetween measures the working tree at dir against an earlier snapshot.
//
// Both sides are tree objects, so the comparison is exact and says nothing about
// commits: a commit landing in between moves HEAD but not the content, and this
// reports the same change either way. That is the point. Measuring against HEAD
// means the number only ever grows until something commits — five small turns
// read as one large change by the fifth — and resets to nothing the moment
// anything commits, validated or not.
//
// Renames are not detected. A renamed file reports as one path gone and another
// arrived, which counts double and reads as less inert than it is — the
// conservative direction, and the cheaper one.
//
// The error is non-nil when base cannot be diffed: it has been garbage collected,
// or the tree cannot be snapshotted now. Callers fall back to WorkingChanges.
func ChangesBetween(dir, base string) (Changes, error) {
	now, err := SnapshotTree(dir)
	if err != nil {
		return Changes{}, err
	}
	if now == base {
		return Changes{Baseline: base}, nil
	}

	names, err := gitOutEnv(dir, nil, "diff", "--name-only", "-z", base, now)
	if err != nil {
		return Changes{}, fmt.Errorf("diff %s: %w", base, err)
	}
	var paths []string
	for _, p := range strings.Split(names, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return Changes{Baseline: base}, nil
	}

	stat, err := gitOutEnv(dir, nil, "diff", "--shortstat", base, now)
	if err != nil {
		return Changes{}, fmt.Errorf("diff %s: %w", base, err)
	}
	return Changes{Paths: paths, Lines: countShortstat(stat), Baseline: base}, nil
}

// gitOutEnv is gitOut with an explicit environment. A nil env inherits this
// process's, as exec does.
func gitOutEnv(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
