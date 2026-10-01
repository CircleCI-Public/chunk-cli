package watchd

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// This file is the part of a session that writes to files on this machine: the
// session's own worktree. A patch from a sandbox is checked before it is
// applied, so one that does not fit, or touches somewhere it must not, changes
// nothing at all.

// treeNow snapshots a working tree as a git tree object: every tracked file as
// it is on disk (staged or not) and every untracked file that is not ignored.
// It is how a session takes the user's uncommitted work as its starting point.
// It touches neither the index nor the files (see gitutil.SnapshotTree).
func treeNow(root string) (string, error) {
	tree, err := gitutil.SnapshotTree(root)
	if err != nil {
		return "", fmt.Errorf("snapshot working tree: %w", err)
	}
	return tree, nil
}

// applyPatchToTree applies a patch to a working tree, and only the working
// tree: the index is left alone. The patch is checked first, so a patch that
// does not fit changes nothing. It returns the files the patch changed.
func applyPatchToTree(ctx context.Context, root, patchPath string) ([]FileChange, error) {
	git := gitexec.Runner{Dir: root}
	if out, err := git.CombinedOutput(ctx, "apply", "--check", patchPath); err != nil {
		return nil, fmt.Errorf("the changes do not apply to the worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	stat, err := git.Output(ctx, "apply", "--numstat", "-z", patchPath)
	if err != nil {
		return nil, fmt.Errorf("measure the changes: %w", err)
	}
	files := parseNumstat(string(stat))
	// The deny list is enforced here as well as on the diff header, because -z
	// gives the real paths: git quotes a name with a space in the header, and a
	// quoted name is not something header parsing can be trusted to read.
	for _, f := range files {
		if err := deniedPath(f.Path); err != nil {
			return nil, err
		}
	}
	if out, err := git.CombinedOutput(ctx, "apply", patchPath); err != nil {
		return nil, fmt.Errorf("apply the changes: %w: %s", err, strings.TrimSpace(string(out)))
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

// deniedPatchPrefixes are paths the implementer's work may never change: git's
// own directory, whose config and hooks would run on this machine. CI
// definitions are allowed, since the task may well be about CI, and the work
// lands on a branch of its own for the developer to read before it goes
// anywhere.
var deniedPatchPrefixes = []string{".git/"}

// checkPatchPaths refuses a patch that touches somewhere it must not: outside
// the repository, or in git's own directory.
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
			if err := deniedPath(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// deniedPath refuses one path the work must not change.
func deniedPath(p string) error {
	clean := path.Clean(filepath.ToSlash(p))
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return fmt.Errorf("the changes touch %q, outside the repository", p)
	}
	for _, denied := range deniedPatchPrefixes {
		if strings.HasPrefix(clean+"/", denied) || strings.HasPrefix(clean, denied) {
			return fmt.Errorf("the changes touch %q, which the implementer may not edit", clean)
		}
	}
	return nil
}
