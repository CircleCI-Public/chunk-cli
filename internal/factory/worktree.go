package factory

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// branchPrefix is where factory branches live.
const branchPrefix = "chunk/factory/"

const maxBranchSlugLen = 48

// Worktree is a run's local checkout: a git worktree of the developer's
// repository on a branch of its own. The implementer's work is pulled into it
// each round, so the developer's own checkout is never touched and they can
// look at the work while the run goes.
type Worktree struct {
	Path   string
	Branch string
	// Baseline is the commit the branch starts from: the developer's HEAD, or
	// their uncommitted work committed on top of it.
	Baseline string
	// Head is the developer's HEAD when the run started. It differs from
	// Baseline when their uncommitted work was committed as the baseline.
	Head string
}

// commitEnv commits as chunk, so a run never stops to ask for an identity or
// depends on the developer having one set.
func commitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=chunk", "GIT_AUTHOR_EMAIL=chunk@circleci.com",
		"GIT_COMMITTER_NAME=chunk", "GIT_COMMITTER_EMAIL=chunk@circleci.com")
}

// CreateWorktree checks out a new worktree of the repository at root into path,
// on a branch named chunk/factory/<prompt slug>/<runID>. It starts from the
// developer's files as they are now: uncommitted work, untracked files included,
// is committed on the branch first, so it is the baseline and not part of the
// change under review.
func CreateWorktree(ctx context.Context, root, path, runID, prompt string) (Worktree, error) {
	git := gitexec.Runner{Dir: root}
	headOut, err := git.Output(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Worktree{}, fmt.Errorf("the repository has no commit to start from: %w", err)
	}
	head := strings.TrimSpace(string(headOut))
	headTree, err := git.Output(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return Worktree{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	tree, err := gitutil.SnapshotTree(root)
	if err != nil {
		return Worktree{}, fmt.Errorf("snapshot your working tree: %w", err)
	}

	start := head
	if tree != strings.TrimSpace(string(headTree)) {
		out, err := (gitexec.Runner{Dir: root, Env: commitEnv()}).Output(ctx, "commit-tree", tree, "-p", head,
			"-m", "chunk factory baseline: uncommitted changes when run "+runID+" started")
		if err != nil {
			return Worktree{}, fmt.Errorf("commit your uncommitted changes as the baseline: %w", err)
		}
		start = strings.TrimSpace(string(out))
	}

	w := Worktree{Path: path, Branch: branchName(prompt, runID), Baseline: start, Head: head}
	if out, err := git.CombinedOutput(ctx, "worktree", "add", "--quiet", "-b", w.Branch, path, start); err != nil {
		return Worktree{}, fmt.Errorf("create worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return w, nil
}

func branchName(prompt, runID string) string {
	return branchPrefix + branchSlug(prompt) + "/" + runID
}

// branchSlug makes the request recognizable in branch listings while the run
// ID remains the stable identifier used for records, logs, and continuation.
func branchSlug(prompt string) string {
	subject, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	subject = strings.TrimSpace(strings.TrimLeft(strings.TrimSuffix(subject, "\r"), "#"))

	var b strings.Builder
	separator := false
	for _, r := range strings.ToLower(subject) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if b.Len() == maxBranchSlugLen {
				break
			}
			if separator && b.Len() < maxBranchSlugLen {
				b.WriteByte('-')
			}
			if b.Len() == maxBranchSlugLen {
				break
			}
			b.WriteRune(r)
			separator = false
			continue
		}
		if b.Len() > 0 {
			separator = true
		}
	}
	if slug := strings.TrimRight(b.String(), "-"); slug != "" {
		return slug
	}
	return "change"
}

// Remove deletes the worktree and its branch, for a run that ended before the
// implementer did anything. Nothing is lost: the baseline is the developer's
// own files, still in their checkout.
func (w Worktree) Remove(ctx context.Context, root string) error {
	git := gitexec.Runner{Dir: root}
	if out, err := git.CombinedOutput(ctx, "worktree", "remove", "--force", w.Path); err != nil {
		return fmt.Errorf("remove worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := git.CombinedOutput(ctx, "branch", "-D", w.Branch); err != nil {
		return fmt.Errorf("delete branch %s: %w: %s", w.Branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Commit commits everything in the worktree to its branch and returns the
// commit, or "" when nothing changed. The worktree is left in place for the
// developer to carry on in. Hooks and signing are skipped: the run cannot
// answer a prompt, and the work has already been checked.
func (w Worktree) Commit(ctx context.Context, message string) (string, error) {
	git := gitexec.Runner{Dir: w.Path, Env: commitEnv()}
	if out, err := git.CombinedOutput(ctx, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage the work: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if git.Run(ctx, "diff", "--cached", "--quiet") == nil {
		return "", nil
	}
	if out, err := git.CombinedOutput(ctx, "-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify", "-m", message); err != nil {
		return "", fmt.Errorf("commit the work: %w: %s", err, strings.TrimSpace(string(out)))
	}
	out, err := git.Output(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve the work's commit: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Stat is git's one-line summary of the branch's work since the baseline, such
// as "3 files changed, 40 insertions(+)", or "" when there is none.
func (w Worktree) Stat(ctx context.Context) (string, error) {
	out, err := (gitexec.Runner{Dir: w.Path}).Output(ctx, "diff", "--shortstat", w.Baseline, "HEAD")
	if err != nil {
		return "", fmt.Errorf("summarize the work: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitMessage is the work's commit message: the prompt's first line as the
// subject, the rest of the prompt below it, and how the run ended.
func CommitMessage(prompt, runID string, o Outcome) string {
	subject, rest, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	subject = strings.TrimSuffix(subject, "\r")
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", subject)
	if rest = strings.Trim(rest, "\r\n"); rest != "" {
		fmt.Fprintf(&b, "%s\n\n", rest)
	}
	fmt.Fprintf(&b, "Written by chunk factory run %s", runID)
	if o.Result != "" {
		fmt.Fprintf(&b, " (%s after %d round(s))", o.Result, o.Rounds)
	}
	b.WriteString(".\n")
	return b.String()
}
