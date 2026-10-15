package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

// ErrNothingToRefine is returned when a run whose record needs work, such as
// one made by Start, is continued before anything was done in its worktree:
// the factory refines a diff, and there is none.
var ErrNothingToRefine = errors.New("nothing was done in the run's worktree to refine")

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

// prompt is the continued run's first implementer prompt. The implementer is
// a new session that did not write the work, so it is told what was asked and
// where the work is.
func (c *Continuation) prompt() string {
	var b strings.Builder
	if c.From.Brief != "" {
		b.WriteString(c.From.Brief)
	} else {
		b.WriteString(retryBrief(c.From.Prompt))
	}
	if g := strings.TrimSpace(c.Guidance); g != "" {
		b.WriteString("\n\nThe developer adds:\n\n")
		b.WriteString(g)
	}
	return b.String()
}

// retryBrief is how a continued run's implementer is told about an earlier
// run's unfinished work.
func retryBrief(prompt string) string {
	return "You are continuing a change that an earlier session started and did not finish. It was asked:\n\n" +
		strings.TrimSpace(prompt) +
		"\n\nIts work is in the working tree as uncommitted changes; `git diff HEAD` shows it. Build on it rather than starting over."
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

// handOff opens the worktree of the run c continues and summarizes the work it
// hands over: the whole change since the baseline, as git's one-line stat.
func (c *Continuation) handOff(ctx context.Context, root, runID string) (Worktree, string, error) {
	wt, err := openWorktree(ctx, root, c.From, runID)
	if err != nil {
		return Worktree{}, "", fmt.Errorf("open the worktree of run %s: %w", c.From.RunID, err)
	}
	stat, err := wt.Stat(ctx)
	if err != nil {
		return Worktree{}, "", err
	}
	if stat == "" && c.From.NeedsWork {
		return Worktree{}, "", fmt.Errorf("run %s: %w", c.From.RunID, ErrNothingToRefine)
	}
	return wt, stat, nil
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
