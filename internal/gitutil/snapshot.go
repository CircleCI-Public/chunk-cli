package gitutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/changeset"
	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
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
// Nothing about the developer's repository moves. The index is a throwaway one
// in a temp directory (GIT_INDEX_FILE), so no staging is touched, and HEAD and
// the working tree are never written.
//
// That index is never seeded from the repository's own. Copying it would bring
// its stat cache along, and git trusts that cache for any file whose size and
// mtime still match — so a snapshot could report content the file no longer has,
// two snapshots of one unchanged tree could disagree, and an edit that keeps a
// file's size inside one mtime tick could go unseen. Hashing from cold is also
// no slower in practice, since seeding costs a copy of the whole index.
//
// It does write blob and tree objects into .git/objects. They are unreferenced,
// so git's own gc collects them in its own time — which is also why a snapshot
// is not a durable handle. A caller holding one that has since been collected
// gets an error from ChangesBetween and should fall back to measuring against
// HEAD.
func SnapshotTree(dir string) (string, error) {
	// The index must not exist yet: git rejects an empty file as a malformed one.
	tmp, err := os.MkdirTemp("", "chunk-index-")
	if err != nil {
		return "", fmt.Errorf("create temp index dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"))
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
func ChangesBetween(dir, base string) (changeset.Changes, error) {
	now, err := SnapshotTree(dir)
	if err != nil {
		return changeset.Changes{}, err
	}
	if now == base {
		return changeset.Changes{Baseline: base}, nil
	}

	names, err := gitOutEnv(dir, nil, "diff", "--name-only", "-z", base, now)
	if err != nil {
		return changeset.Changes{}, fmt.Errorf("diff %s: %w", base, err)
	}
	var paths []string
	for _, p := range strings.Split(names, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return changeset.Changes{Baseline: base}, nil
	}

	stat, err := gitOutEnv(dir, nil, "diff", "--shortstat", base, now)
	if err != nil {
		return changeset.Changes{}, fmt.Errorf("diff %s: %w", base, err)
	}
	return changeset.Changes{Paths: paths, Lines: countShortstat(stat), Baseline: base}, nil
}

// gitOutEnv is gitOut with an explicit environment. A nil env inherits this
// process's, as exec does.
func gitOutEnv(dir string, env []string, args ...string) (string, error) {
	out, err := (gitexec.Runner{Dir: dir, Env: env}).Output(context.Background(), args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
