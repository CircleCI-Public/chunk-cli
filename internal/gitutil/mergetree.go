package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrMergeTreeUnsupported reports a git too old for `merge-tree --write-tree`,
// which landed in git 2.38. Callers treat it as "cannot answer" rather than
// "no conflicts": the two are opposite advice, and reporting a clean merge from
// a git that never performed one is the worse of the two mistakes.
var ErrMergeTreeUnsupported = errors.New("git merge-tree --write-tree is unavailable (requires git 2.38+)")

// MergePreview is the result of merging two commits without touching the
// working tree, index, or HEAD.
type MergePreview struct {
	// Clean is true when the merge produced a tree with no conflicts.
	Clean bool
	// Paths lists the conflicted paths, empty when Clean. Only paths git
	// reported as conflicted appear here — a file merged automatically is not
	// listed even though the merge rewrote it.
	Paths []string
}

// PreviewMerge reports whether ours and theirs merge cleanly, using
// `git merge-tree --write-tree`.
//
// Nothing in the repository is modified: merge-tree resolves the merge entirely
// in the object database and writes the result as a loose tree that nothing
// references, so this is safe to run against a checkout somebody is actively
// working in. That is the whole reason this is the primitive rather than a
// throwaway worktree and a real `git merge` — a background process must never
// be able to disturb a developer's index.
//
// Both arguments are commit-ish. The merge is of committed history only:
// uncommitted work in the tree at dir is invisible to it, so a conflict that
// exists only in unstaged edits is not reported. Callers surfacing this to a
// person need to say so, or the silence reads as an all-clear it did not check.
//
// A conflicted merge is a successful call, not an error — the answer is in
// Clean. An error means the question could not be answered at all.
func PreviewMerge(ctx context.Context, dir, ours, theirs string) (MergePreview, error) {
	// --name-only reduces the conflict section to bare paths. Without it each
	// path arrives as a mode/oid/stage tuple that would have to be parsed apart
	// again, and no caller here wants the stages.
	cmd := exec.CommandContext(ctx, "git", "-C", dir,
		"merge-tree", "--write-tree", "--name-only", ours, theirs)
	out, err := cmd.Output()

	if err == nil {
		// Exit 0: merged cleanly. Output is the tree OID alone.
		return MergePreview{Clean: true}, nil
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return MergePreview{}, fmt.Errorf("merge-tree %s %s: %w", ours, theirs, err)
	}

	stderr := strings.TrimSpace(string(exitErr.Stderr))

	if isUnsupportedMergeTree(stderr) {
		return MergePreview{}, ErrMergeTreeUnsupported
	}

	// Exit 1 is git's documented "merged with conflicts" — but it is also what
	// an unmergeable argument produces: `merge-tree --write-tree main nope`
	// exits 1 with "nope - not something we can merge". The exit code alone
	// therefore cannot tell a conflict from a bad revision, and reading the
	// latter as the former would report a phantom conflict, with no paths, for
	// any ref that failed to resolve.
	//
	// Stdout is what separates them: a merge that ran writes the resulting tree
	// OID there whether or not it conflicted, and a merge that never started
	// writes nothing.
	merged := strings.TrimSpace(string(out)) != ""
	if exitErr.ExitCode() != 1 || !merged {
		if stderr != "" {
			return MergePreview{}, fmt.Errorf("merge-tree %s %s: %s", ours, theirs, stderr)
		}
		return MergePreview{}, fmt.Errorf("merge-tree %s %s: %w", ours, theirs, err)
	}

	return MergePreview{Clean: false, Paths: parseConflictPaths(string(out))}, nil
}

// isUnsupportedMergeTree recognises the way git refuses an option it does not
// have. Matching the message is unavoidable: git offers no capability query,
// and parsing `git --version` would mean maintaining a version comparison for
// a question the error already answers.
func isUnsupportedMergeTree(stderr string) bool {
	return strings.Contains(stderr, "unknown option") ||
		strings.Contains(stderr, "unknown switch") ||
		strings.Contains(stderr, "usage: git merge-tree")
}

// parseConflictPaths pulls the conflicted paths out of merge-tree's output.
//
// The output is sections separated by a blank line: the OID of the merged tree
// first, then (under --name-only) one conflicted path per line, then git's
// human-readable "CONFLICT (content): ..." messages. Only the second section is
// wanted; the third is prose that would read as a filename if included.
func parseConflictPaths(out string) []string {
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return nil
	}
	var paths []string
	// Skip line 0, the tree OID.
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			break // end of the path section
		}
		paths = append(paths, line)
	}
	return paths
}

// DefaultRemoteBranchIn returns the remote and branch name of the default
// branch for the repo rooted at dir — the branch a PR merges into, and the
// remote it lives on.
//
// The remote matters to anything that needs to refresh the ref rather than just
// name it: `origin/main` and `upstream/main` are different commits in a fork,
// and fetching the wrong one leaves the comparison stale in exactly the
// checkout where being stale is most misleading.
//
// Like DefaultBranchIn, an error is a routine answer: a repo with no remote HEAD
// recorded has no default branch to find, and callers fall back rather than fail.
func DefaultRemoteBranchIn(dir string) (remote, branch string, err error) {
	for _, r := range []string{"origin", "upstream"} {
		out, cmdErr := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "refs/remotes/"+r+"/HEAD").Output()
		if cmdErr != nil {
			continue
		}
		// The symref reads back qualified — "origin/main" — and only the branch
		// name is usable on its own.
		if b := strings.TrimPrefix(strings.TrimSpace(string(out)), r+"/"); b != "" {
			return r, b, nil
		}
	}
	return "", "", fmt.Errorf("no remote HEAD is set for %s", dir)
}

// FetchRemoteBranch updates the remote-tracking ref for one branch.
//
// The refspec is explicit rather than relying on `git fetch <remote> <branch>`
// opportunistically updating refs/remotes/<remote>/<branch>: that update
// depends on the remote's configured fetch refspec matching, and a checkout
// with a narrowed refspec would fetch successfully and leave the tracking ref
// untouched — a silent no-op that looks like a fetch.
//
// Only the one branch is fetched, and no tags, because this runs on a timer in
// the background. The cost of a full fetch is somebody else's bandwidth and a
// pack the developer did not ask for.
func FetchRemoteBranch(ctx context.Context, dir, remote, branch string) error {
	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", branch, remote, branch)
	cmd := exec.CommandContext(ctx, "git", "-C", dir,
		"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", remote, refspec)
	// A fetch must never turn into a credential prompt: this process has no
	// terminal to prompt on, and a git that blocks waiting for input would hang
	// the timer it runs on until the context expires.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("fetch %s %s: %s", remote, branch, msg)
		}
		return fmt.Errorf("fetch %s %s: %w", remote, branch, err)
	}
	return nil
}

// RevParseCtx resolves a revision to its SHA in the repo at dir. An error means
// the revision does not exist, which for a remote-tracking ref is the ordinary
// state of a repo that has never fetched it.
func RevParseCtx(ctx context.Context, dir, rev string) (string, error) {
	// --verify with a ^{commit} peel refuses anything that is not a commit,
	// so a tag or a tree cannot resolve here and reach merge-tree as a surprise.
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", rev, err)
	}
	sha := trimNewline(out)
	if sha == "" {
		return "", fmt.Errorf("resolve %s: empty output", rev)
	}
	return sha, nil
}
