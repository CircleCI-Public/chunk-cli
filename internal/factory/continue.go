package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

// Continuation is a run that picks up the work an earlier run left on its
// branch, typically one whose checks still failed when its attempts ran out.
// It works in that run's worktree and adds a commit to its branch, and its
// reviewers see the whole change since the original baseline, not only what
// the continuation adds.
type Continuation struct {
	From Record
	// Guidance is what the developer adds to the original request, or "".
	// Without it the run checks the work as it is before the implementer's
	// first turn, so that turn is sent what failed.
	Guidance string
}

// checkFirst reports whether the run checks the work before the implementer's
// first turn: with no guidance there is nothing to implement until the checks
// say what is wrong.
func (c *Continuation) checkFirst() bool {
	return strings.TrimSpace(c.Guidance) == ""
}

// Rounds is the most rounds a continued run checks when attempts were asked
// for. Checking first takes a round of its own, so the developer still gets
// the implementer turns they asked for.
func (c *Continuation) Rounds(attempts int) int {
	if c.checkFirst() {
		return attempts + 1
	}
	return attempts
}

// prompt is the continued run's first implementer prompt. The implementer is
// a new session that did not write the work, so it is told what was asked and
// where the work is.
func (c *Continuation) prompt() string {
	var b strings.Builder
	b.WriteString("You are continuing a change that an earlier session started and did not finish. It was asked:\n\n")
	b.WriteString(strings.TrimSpace(c.From.Prompt))
	b.WriteString("\n\nIts work is in the working tree as uncommitted changes; `git diff HEAD` shows it. Build on it rather than starting over.")
	if g := strings.TrimSpace(c.Guidance); g != "" {
		b.WriteString("\n\nThe developer adds:\n\n")
		b.WriteString(g)
	}
	return b.String()
}

// request is what the reviewers check the work against: the original request
// and whatever the developer added to it.
func (c *Continuation) request() string {
	r := strings.TrimSpace(c.From.Prompt)
	if g := strings.TrimSpace(c.Guidance); g != "" {
		r += "\n\nFollow-up from the developer:\n\n" + g
	}
	return r
}

// commitMessage is the continued run's commit message: the original request's
// subject, the guidance if there was any, and which run it continued.
func (c *Continuation) commitMessage(runID string, o Outcome) string {
	subject, _, _ := strings.Cut(strings.TrimSpace(c.From.Prompt), "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", subject)
	if g := strings.TrimSpace(c.Guidance); g != "" {
		fmt.Fprintf(&b, "%s\n\n", g)
	}
	fmt.Fprintf(&b, "Written by chunk factory run %s, continuing run %s", runID, c.From.RunID)
	if o.Result != "" {
		fmt.Fprintf(&b, " (%s after %d round(s))", o.Result, o.Rounds)
	}
	b.WriteString(".\n")
	return b.String()
}

// openWorktree returns the worktree of the run rec describes, adding it back
// from the run's branch if it was removed. Edits made in it since that run
// ended are committed on the branch first, so they are part of the work the
// continued run picks up and survive the run swapping the worktree's files.
func openWorktree(ctx context.Context, root string, rec Record, runID string) (Worktree, error) {
	git := gitexec.Runner{Dir: root}
	if err := git.Run(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+rec.Branch); err != nil {
		return Worktree{}, fmt.Errorf("the run's branch %s is gone", rec.Branch)
	}
	if err := git.Run(ctx, "merge-base", "--is-ancestor", rec.Baseline, "refs/heads/"+rec.Branch); err != nil {
		return Worktree{}, fmt.Errorf("the branch %s no longer starts from the run's baseline %s", rec.Branch, shortSHA(rec.Baseline))
	}
	wt := Worktree{Path: rec.Worktree, Branch: rec.Branch, Baseline: rec.Baseline, Head: rec.Head}
	if _, err := os.Stat(wt.Path); errors.Is(err, os.ErrNotExist) {
		// git still lists a worktree whose directory was deleted, and refuses
		// to check its branch out again until it forgets it.
		_ = git.Run(ctx, "worktree", "prune")
		if out, err := git.CombinedOutput(ctx, "worktree", "add", "--quiet", wt.Path, wt.Branch); err != nil {
			return Worktree{}, fmt.Errorf("add the worktree back: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	out, err := (gitexec.Runner{Dir: wt.Path}).Output(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(string(out)) != wt.Branch {
		return Worktree{}, fmt.Errorf("the worktree %s is not on the branch %s", wt.Path, wt.Branch)
	}
	if _, err := wt.Commit(ctx, "Edits made in the worktree before chunk factory run "+runID+" continued it\n"); err != nil {
		return Worktree{}, err
	}
	return wt, nil
}

// showBaseline puts the baseline's files in the worktree, leaving its branch
// where it is, so sidecars synced from it start from the baseline and the
// work can be laid on top of it as uncommitted changes. restoreWork undoes it.
func (w Worktree) showBaseline(ctx context.Context) error {
	if out, err := (gitexec.Runner{Dir: w.Path}).CombinedOutput(ctx, "read-tree", "-u", "--reset", w.Baseline); err != nil {
		return fmt.Errorf("check out the baseline's files: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// restoreWork puts the branch's files back in the worktree after showBaseline.
func (w Worktree) restoreWork(ctx context.Context) error {
	if out, err := (gitexec.Runner{Dir: w.Path}).CombinedOutput(ctx, "read-tree", "-u", "--reset", "HEAD"); err != nil {
		return fmt.Errorf("put the work's files back: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
