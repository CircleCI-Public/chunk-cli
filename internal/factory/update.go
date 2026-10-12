package factory

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/gitexec"
)

// UpdateResult is how bringing a run's branch up to date with its base came out.
type UpdateResult string

// The outcomes of an update.
const (
	// UpdateClean means the base merged without conflicts.
	UpdateClean UpdateResult = "clean"
	// UpdateConflicts means the merge conflicted and was committed with its
	// markers left in the files.
	UpdateConflicts UpdateResult = "conflicts"
	// UpdateUpToDate means the branch already contained the base.
	UpdateUpToDate UpdateResult = "up-to-date"
)

// UpdateReport is what UpdateBranch did. Its JSON form is what
// `smelt factory update --json` prints.
type UpdateReport struct {
	RunID         string       `json:"run_id"`
	Branch        string       `json:"branch"`
	Base          string       `json:"base"`
	Result        UpdateResult `json:"result"`
	MergedCommits int          `json:"merged_commits"`
	Conflicts     []string     `json:"conflicts"`
}

// UpdateOptions describes an update.
type UpdateOptions struct {
	// Base overrides the branch to merge; see ChooseBase.
	Base string
	// Author is who the merge commit is attributed to; see ResolveAuthor.
	Author config.CommitAuthor
}

// UpdateBranch merges the base branch into the branch of the finished run rec,
// in the run's worktree. The implementer cannot do this itself: its container
// has no history to merge against. A conflicted merge is committed with its
// markers in place and recorded in the run's record, so a continued run can
// resolve them and the worktree is left with nothing in progress.
func UpdateBranch(ctx context.Context, root string, rec Record, o UpdateOptions) (UpdateReport, error) {
	base := ChooseBase(o.Base, rec.BaseBranch, rec.DefaultBranch)
	if base == "" {
		return UpdateReport{}, errors.New("cannot tell which branch to merge: the run recorded no base branch; pass --base")
	}
	rep := UpdateReport{RunID: rec.RunID, Branch: rec.Branch, Base: base, Conflicts: []string{}}
	git := gitexec.Runner{Dir: rec.Worktree, Env: commitEnv(o.Author)}
	if err := checkUpdatable(ctx, git, rec); err != nil {
		return rep, err
	}
	if out, err := git.CombinedOutput(ctx, "fetch", "--quiet", DefaultRemote, base); err != nil {
		return rep, fmt.Errorf("fetch %s from %s: %w: %s", base, DefaultRemote, err, strings.TrimSpace(string(out)))
	}
	target := DefaultRemote + "/" + base
	count, err := git.Output(ctx, "rev-list", "--count", "HEAD.."+target)
	if err != nil {
		return rep, fmt.Errorf("count the commits of %s to merge: %w", target, err)
	}
	if rep.MergedCommits, err = strconv.Atoi(strings.TrimSpace(string(count))); err != nil {
		return rep, fmt.Errorf("read the commit count of %s: %w", target, err)
	}
	if rep.MergedCommits == 0 {
		rep.Result = UpdateUpToDate
		return rep, nil
	}

	out, mergeErr := git.CombinedOutput(ctx, "-c", "commit.gpgsign=false", "merge", "--no-ff", "--no-edit", "--no-verify", target)
	if mergeErr == nil {
		rep.Result = UpdateClean
		return rep, nil
	}
	conflicts, err := conflictedFiles(ctx, git)
	if err != nil || len(conflicts) == 0 {
		_ = git.Run(context.WithoutCancel(ctx), "merge", "--abort")
		return rep, fmt.Errorf("merge %s into %s: %w: %s", target, rec.Branch, mergeErr, strings.TrimSpace(string(out)))
	}
	if err := commitConflicts(ctx, git, base, rec.Branch, conflicts); err != nil {
		return rep, err
	}
	rep.Result, rep.Conflicts = UpdateConflicts, conflicts
	rec.Conflicts = conflicts
	if err := recordConflicts(root, rec); err != nil {
		return rep, err
	}
	return rep, nil
}

// checkUpdatable refuses a worktree the merge cannot safely run in: not on the
// run's branch, or holding changes the merge would trip over.
func checkUpdatable(ctx context.Context, git gitexec.Runner, rec Record) error {
	head, err := git.Output(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != rec.Branch {
		return fmt.Errorf("the worktree %s is not on the branch %s", rec.Worktree, rec.Branch)
	}
	status, err := git.Output(ctx, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("check the worktree %s: %w", rec.Worktree, err)
	}
	if strings.TrimSpace(string(status)) != "" {
		return fmt.Errorf("the worktree %s has uncommitted changes; commit or discard them first", rec.Worktree)
	}
	return nil
}

// conflictedFiles lists the paths a merge in progress left unmerged.
func conflictedFiles(ctx context.Context, git gitexec.Runner) ([]string, error) {
	out, err := git.Output(ctx, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, fmt.Errorf("list the conflicted files: %w", err)
	}
	return strings.Fields(string(out)), nil
}

// commitConflicts commits the merge in progress as it is, markers included.
func commitConflicts(ctx context.Context, git gitexec.Runner, base, branch string, conflicts []string) error {
	if out, err := git.CombinedOutput(ctx, "add", "-A"); err != nil {
		return fmt.Errorf("stage the conflicted files: %w: %s", err, strings.TrimSpace(string(out)))
	}
	msg := fmt.Sprintf("Merge %s into %s (conflicts to resolve: %s)", base, branch, strings.Join(conflicts, ", "))
	if out, err := git.CombinedOutput(ctx, "-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify", "-m", msg); err != nil {
		return fmt.Errorf("commit the conflicted merge: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// recordConflicts saves rec, whose Conflicts were just set, in the project's
// data directory.
func recordConflicts(root string, rec Record) error {
	dataDir, err := config.ProjectDataDir(root)
	if err != nil {
		return fmt.Errorf("find smelt's data directory for the project: %w", err)
	}
	return saveRecord(dataDir, rec)
}
