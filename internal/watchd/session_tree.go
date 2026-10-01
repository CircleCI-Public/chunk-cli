package watchd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// This file is the part of a session that writes to the user's files, and so the
// part that has to be right. Three guarantees, each tested against real git:
//
//   - Nothing is changed before a restore point exists (saveRestorePoint), and a
//     restore point is a full snapshot of the working tree that survives the
//     daemon: a git ref.
//   - Nothing is overwritten that the session did not see: the caller compares
//     treeNow with the tree it last saw, and pauses on any difference.
//   - A patch is checked before it is applied, so one that does not fit changes
//     nothing at all.

// restoreRefPrefix is where restore points live. Outside refs/heads and
// refs/tags, so they appear in no branch list, yet still keep the snapshot alive
// through garbage collection.
const restoreRefPrefix = "refs/chunk/restore/"

// restoreRef names a session's restore ref.
func restoreRef(sessionID string) string { return restoreRefPrefix + sessionID }

// treeNow snapshots the user's working tree as a git tree object: every tracked
// file as it is on disk (staged or not) and every untracked file that is not
// ignored. Two equal trees are identical content, so comparing them is how a
// session notices the files moved under it. It touches neither the index nor the
// files (see gitutil.SnapshotTree).
func treeNow(root string) (string, error) {
	tree, err := gitutil.SnapshotTree(root)
	if err != nil {
		return "", fmt.Errorf("snapshot working tree: %w", err)
	}
	return tree, nil
}

// changedBetween lists the paths whose content differs between two trees.
func changedBetween(ctx context.Context, root, from, to string) ([]string, error) {
	out, err := (gitexec.Runner{Dir: root}).Output(ctx, "diff", "--name-only", "--no-renames", "-z", from, to)
	if err != nil {
		return nil, fmt.Errorf("compare working trees: %w", err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// saveRestorePoint records tree — the working tree as the session is about to
// find it — under a ref, so that everything the session later changes can be put
// back. The snapshot holds all tracked content, staged and unstaged edits alike,
// and all untracked files that are not ignored. Ignored files are not covered,
// and neither are the index entries themselves: a session writes to files only,
// never to the index, so staging is left exactly as the user had it.
func saveRestorePoint(ctx context.Context, root, sessionID, tree string) (RestorePoint, error) {
	git := gitexec.Runner{Dir: root}
	headOut, err := git.Output(ctx, "rev-parse", "HEAD")
	if err != nil {
		return RestorePoint{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	head := strings.TrimSpace(string(headOut))

	// The snapshot becomes a commit so a ref can hold it; the commit's parent is
	// the user's HEAD so `git log` on the ref reads naturally.
	commit := gitexec.Runner{Dir: root, Env: append(os.Environ(),
		"GIT_AUTHOR_NAME=chunk", "GIT_AUTHOR_EMAIL=chunk@localhost",
		"GIT_COMMITTER_NAME=chunk", "GIT_COMMITTER_EMAIL=chunk@localhost")}
	out, err := commit.Output(ctx, "commit-tree", tree, "-p", head, "-m", "chunk session "+sessionID+": working tree before any fix")
	if err != nil {
		return RestorePoint{}, fmt.Errorf("save restore point: %w", err)
	}
	sha := strings.TrimSpace(string(out))
	ref := restoreRef(sessionID)
	if err := git.Run(ctx, "update-ref", ref, sha); err != nil {
		return RestorePoint{}, fmt.Errorf("save restore point ref: %w", err)
	}
	return RestorePoint{Ref: ref, HeadSHA: head, SavedAt: time.Now()}, nil
}

// applyPatchToTree applies a patch to the user's working tree, and only the
// working tree: the index is left alone. The patch is checked first, so a patch
// that does not fit changes nothing. It returns the files the patch changed.
func applyPatchToTree(ctx context.Context, root, patchPath string) ([]FileChange, error) {
	git := gitexec.Runner{Dir: root}
	if out, err := git.CombinedOutput(ctx, "apply", "--check", patchPath); err != nil {
		return nil, fmt.Errorf("the fixes do not apply to your files any more: %w: %s", err, strings.TrimSpace(string(out)))
	}
	stat, err := git.Output(ctx, "apply", "--numstat", "-z", patchPath)
	if err != nil {
		return nil, fmt.Errorf("measure the fixes: %w", err)
	}
	files := parseNumstat(string(stat))
	if out, err := git.CombinedOutput(ctx, "apply", patchPath); err != nil {
		return nil, fmt.Errorf("apply the fixes: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return files, nil
}

// parseNumstat reads `git apply --numstat -z` output: "added\tdeleted\tpath" NUL
// records. A binary file reports "-" for both counts.
func parseNumstat(s string) []FileChange {
	var files []FileChange
	for _, rec := range strings.Split(s, "\x00") {
		fields := strings.SplitN(rec, "\t", 3)
		if len(fields) != 3 || fields[2] == "" {
			continue
		}
		ins, _ := strconv.Atoi(fields[0])
		del, _ := strconv.Atoi(fields[1])
		files = append(files, FileChange{Path: fields[2], Insertions: ins, Deletions: del})
	}
	return files
}

// ErrEditedSince is returned by restoreSession when files the session changed
// have been edited since, and restoring would overwrite those edits.
var ErrEditedSince = errors.New("files were edited after the session changed them")

// EditedSinceError names the files, and wraps ErrEditedSince.
type EditedSinceError struct{ Paths []string }

func (e *EditedSinceError) Error() string {
	return fmt.Sprintf("%v: %s (restore with force to overwrite them)", ErrEditedSince, strings.Join(e.Paths, ", "))
}

func (e *EditedSinceError) Unwrap() error { return ErrEditedSince }

// restoreSession puts back every file the session changed, as it was in the
// restore point, and touches nothing else. paths are the files the session's
// fixes changed; leftTree is the working tree as the session last left it.
//
// A file the user has edited since the session left it is not overwritten unless
// force is set: restoring would silently discard their work. Files the session
// created are removed; files it modified or deleted are written back.
func restoreSession(ctx context.Context, root string, rp RestorePoint, paths []string, leftTree string, force bool) ([]string, error) {
	if !force {
		now, err := treeNow(root)
		if err != nil {
			return nil, err
		}
		changed, err := changedBetween(ctx, root, leftTree, now)
		if err != nil {
			return nil, err
		}
		if edited := intersect(paths, changed); len(edited) > 0 {
			return nil, &EditedSinceError{Paths: edited}
		}
	}

	git := gitexec.Runner{Dir: root}
	var present, absent []string
	for _, p := range paths {
		if !filepath.IsLocal(filepath.FromSlash(p)) {
			return nil, fmt.Errorf("refusing to restore %q: not a path inside the project", p)
		}
		if git.Run(ctx, "cat-file", "-e", rp.Ref+":"+p) == nil {
			present = append(present, p)
		} else {
			absent = append(absent, p)
		}
	}

	if len(present) > 0 {
		args := []string{"restore", "--source=" + rp.Ref, "--worktree", "--"}
		for _, p := range present {
			// :(literal) so a file name is never read as a pathspec pattern.
			args = append(args, ":(literal)"+p)
		}
		if out, err := git.CombinedOutput(ctx, args...); err != nil {
			return nil, fmt.Errorf("restore files: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	for _, p := range absent {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove %s: %w", p, err)
		}
		removeEmptyParents(root, filepath.Dir(full))
	}
	return append(present, absent...), nil
}

// removeEmptyParents removes dir and its parents while they are empty, stopping
// at root. A fix that created a file in a new directory leaves that directory
// behind otherwise.
func removeEmptyParents(root, dir string) {
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// intersect returns the members of a that are also in b, in a's order.
func intersect(a, b []string) []string {
	in := make(map[string]bool, len(b))
	for _, p := range b {
		in[p] = true
	}
	var out []string
	for _, p := range a {
		if in[p] {
			out = append(out, p)
		}
	}
	return out
}

// deniedPatchPrefixes are paths a fix may never change. The .git directory is
// git's own, and CI definitions run with secrets: text that came from a model
// reading the user's code has no business editing either on its own.
var deniedPatchPrefixes = []string{".git/", ".github/workflows/", ".github/actions/", ".circleci/"}

// checkPatchPaths refuses a patch that touches somewhere it must not: outside
// the repository, in git's own directory, or in CI definitions.
func checkPatchPaths(patch string) error {
	for _, line := range strings.Split(patch, "\n") {
		rest, ok := strings.CutPrefix(line, "diff --git ")
		if !ok {
			continue
		}
		for _, field := range strings.Fields(rest) {
			p, ok := strings.CutPrefix(field, "a/")
			if !ok {
				p, ok = strings.CutPrefix(field, "b/")
			}
			if !ok {
				continue
			}
			clean := path.Clean(p)
			if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
				return fmt.Errorf("the fixes touch %q, outside the repository", p)
			}
			for _, denied := range deniedPatchPrefixes {
				if strings.HasPrefix(clean+"/", denied) || strings.HasPrefix(clean, denied) {
					return fmt.Errorf("the fixes touch %q, which a review fix may not edit", clean)
				}
			}
		}
	}
	return nil
}
