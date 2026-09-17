package gitutil

import (
	"fmt"
	"os"
	"strings"
)

// shadowIdentity is the author git needs to write a commit object. The commit
// is a handle for a tree and nothing else — never pushed, never referenced,
// collected by gc — so it carries chunk's name rather than the developer's,
// which should never appear on an object they did not make.
var shadowIdentity = []string{
	"GIT_AUTHOR_NAME=chunk",
	"GIT_AUTHOR_EMAIL=chunk@local",
	"GIT_COMMITTER_NAME=chunk",
	"GIT_COMMITTER_EMAIL=chunk@local",
}

// MaterializeTree checks out a tree snapshot into its own directory and returns
// the path, along with a function that removes it.
//
// This is what lets checks run against a state that cannot move underneath
// them. A run against the live working tree is racing the developer and the
// agent: anything they edit while it runs makes its answer describe code that
// is no longer there, and that answer then has to be thrown away. A run against
// a checked-out snapshot is true about that snapshot however long it takes, and
// stays true afterwards.
//
// What it does not carry is everything git was told to ignore. Dependencies,
// build caches, local env files and anything else gitignored are absent, so
// checks that need them fail here for reasons that have nothing to do with the
// change. That is why the worktree mode is opt-in: for some projects it is
// exactly right, and for others every run would report an environment failure
// as a code failure.
//
// Unlike SnapshotTree this does touch repository metadata — git records the
// worktree under .git/worktrees — which the cleanup removes. A daemon killed
// before cleanup leaves an entry behind; `git worktree prune` clears those, and
// the next cleanup runs it.
func MaterializeTree(dir, tree string) (string, func(), error) {
	if tree == "" {
		return "", nil, fmt.Errorf("no snapshot to materialize")
	}

	commit, err := gitOutEnv(dir, append(os.Environ(), shadowIdentity...),
		"commit-tree", tree, "-m", "chunk snapshot")
	if err != nil {
		return "", nil, fmt.Errorf("commit snapshot: %w", err)
	}

	path, err := os.MkdirTemp("", "chunk-shadow-")
	if err != nil {
		return "", nil, fmt.Errorf("create shadow dir: %w", err)
	}
	// Removed first: worktree add refuses a directory that already exists, and
	// MkdirTemp has just made one. Taking the name rather than the directory is
	// what keeps two concurrent runs from choosing the same path.
	if err := os.Remove(path); err != nil {
		return "", nil, fmt.Errorf("clear shadow dir: %w", err)
	}

	if _, err := gitOut(dir, "worktree", "add", "--detach", path, strings.TrimSpace(commit)); err != nil {
		return "", nil, fmt.Errorf("add shadow worktree: %w", err)
	}
	return path, cleanupShadow(dir, path), nil
}

// cleanupShadow returns a function that removes a shadow worktree and the
// metadata git recorded for it. Best-effort throughout: a leftover temp
// directory is worth a warning, not a failed validate run.
func cleanupShadow(dir, path string) func() {
	return func() {
		_, _ = gitOut(dir, "worktree", "remove", "--force", path)
		_ = os.RemoveAll(path)
		// Clears entries left by a daemon that died before its own cleanup ran.
		_, _ = gitOut(dir, "worktree", "prune")
	}
}
