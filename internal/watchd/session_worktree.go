package watchd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

// A session works in a git worktree of its own, on a branch of its own, so the
// user's checkout is never touched and they can keep working while it runs.
// The worktree starts from the user's files as they were when the session
// started, uncommitted work included, and when the session ends its work is
// committed on the branch and the worktree is removed.

// workBranchPrefix is where session branches live.
const workBranchPrefix = "chunk/factory/"

// worktreeFinishTimeout bounds committing the work and removing the worktree.
// It runs on the way out, after a cancel too, when the session's own context is
// already done.
const worktreeFinishTimeout = 2 * time.Minute

// sessionDir is the daemon's directory for one session: its worktree, its pool
// state and the patches it applied.
func sessionDir(id string) (string, error) {
	base, err := EnsureDir()
	if err != nil {
		return "", fmt.Errorf("daemon directory: %w", err)
	}
	dir := filepath.Join(base, "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create session directory: %w", err)
	}
	return dir, nil
}

// workBranch names a session's branch.
func workBranch(id string) string {
	return workBranchPrefix + id[:min(8, len(id))]
}

// commitEnv commits as chunk. A commit made in the daemon cannot stop to ask
// for an identity, so the user's is not relied on.
func commitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=chunk", "GIT_AUTHOR_EMAIL=chunk@localhost",
		"GIT_COMMITTER_NAME=chunk", "GIT_COMMITTER_EMAIL=chunk@localhost")
}

// createWorktree makes the session's worktree under dir, on a new branch. When
// the user has uncommitted work, it is committed on that branch first, as the
// baseline: the reviewers are shown what the session changed, not what the
// user had in progress.
func createWorktree(ctx context.Context, root, id, dir string) (path, branch string, err error) {
	git := gitexec.Runner{Dir: root}
	headOut, err := git.Output(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("the project has no commit to start from: %w", err)
	}
	head := strings.TrimSpace(string(headOut))
	headTreeOut, err := git.Output(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", "", fmt.Errorf("resolve HEAD: %w", err)
	}
	tree, err := treeNow(root)
	if err != nil {
		return "", "", err
	}

	start := head
	if tree != strings.TrimSpace(string(headTreeOut)) {
		out, err := (gitexec.Runner{Dir: root, Env: commitEnv()}).Output(ctx, "commit-tree", tree, "-p", head,
			"-m", "chunk factory baseline: uncommitted changes when session "+id+" started")
		if err != nil {
			return "", "", fmt.Errorf("commit your uncommitted changes as the baseline: %w", err)
		}
		start = strings.TrimSpace(string(out))
	}

	path = filepath.Join(dir, "worktree")
	branch = workBranch(id)
	if out, err := git.CombinedOutput(ctx, "worktree", "add", "--quiet", "-b", branch, path, start); err != nil {
		return "", "", fmt.Errorf("create worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return path, branch, nil
}

// finishWorktree commits the work in the worktree to its branch and removes
// the worktree. It returns the commit, or "" when there was nothing to commit,
// and whether the branch was kept: with nothing to commit it is deleted, unless
// it holds the user's uncommitted work as a baseline, which is theirs.
func finishWorktree(ctx context.Context, root, path, branch, message string) (commit string, kept bool, err error) {
	wt := gitexec.Runner{Dir: path, Env: commitEnv()}
	if err := wt.Run(ctx, "add", "-A"); err != nil {
		return "", true, fmt.Errorf("stage the work: %w", err)
	}
	if wt.Run(ctx, "diff", "--cached", "--quiet") != nil {
		// No hooks and no signing: the daemon cannot answer a prompt, and the
		// work has been checked already.
		if out, err := wt.CombinedOutput(ctx, "-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify", "-m", message); err != nil {
			return "", true, fmt.Errorf("commit the work: %w: %s", err, strings.TrimSpace(string(out)))
		}
		out, err := wt.Output(ctx, "rev-parse", "HEAD")
		if err != nil {
			return "", true, fmt.Errorf("resolve the work's commit: %w", err)
		}
		commit = strings.TrimSpace(string(out))
	}

	git := gitexec.Runner{Dir: root}
	if out, err := git.CombinedOutput(ctx, "worktree", "remove", "--force", path); err != nil {
		return commit, true, fmt.Errorf("remove worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if commit != "" {
		return commit, true, nil
	}
	// Nothing was done. A branch that only points at the user's HEAD is noise;
	// one holding their uncommitted work as a baseline commit is not.
	tip, err := git.Output(ctx, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return "", false, nil
	}
	head, err := git.Output(ctx, "rev-parse", "HEAD")
	if err != nil || string(head) != string(tip) {
		return "", true, nil
	}
	if out, err := git.CombinedOutput(ctx, "branch", "-D", branch); err != nil {
		return "", true, fmt.Errorf("delete the empty branch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return "", false, nil
}

// commitMessage is the work commit's message: the task's first line as the
// subject, the task in full below it, and how the session ended. The subject
// is the developer's own words, so it is not cut.
func commitMessage(s Session) string {
	subject := strings.TrimSpace(strings.SplitN(strings.TrimSpace(s.Task), "\n", 2)[0])
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n\n", subject, strings.TrimSpace(s.Task))
	fmt.Fprintf(&b, "Written by chunk factory session %s", s.ID)
	if s.Outcome != "" {
		fmt.Fprintf(&b, " (%s after %d round(s))", s.Outcome, len(s.Rounds))
	}
	b.WriteString(".\n")
	return b.String()
}
