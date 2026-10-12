package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/factory"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/smeltd"
)

func newFactoryUpdateCmd() *cobra.Command {
	var base string
	var push, jsonOut bool
	cmd := &cobra.Command{
		Use:   "update <run-id> [--base BRANCH] [--push] [--json]",
		Short: "Merge a run's base branch into the run's branch",
		Long: `Bring the branch of a finished factory run up to date with its base branch,
usually main, by merging origin/<base> into it in the run's worktree. The run is
a run ID or its branch. The implementer cannot do this itself: it works on a
copy of the files with no history to merge against.

A clean merge just lands. A merge that conflicts is committed with the conflict
markers left in the files, and the files are listed; resolve them with
'smelt factory build --continue <run-id> "..."', which has the implementer
remove the markers. The 'markers' check fails while any are left.

Nothing is pushed unless --push is given and the merge was clean. Only smelt/*
branches are ever pushed, never forced.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFactoryUpdate(cmd.Context(), iostream.FromCmd(cmd), args[0], base, push, jsonOut)
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "branch to merge (default: the branch the run started from, else the default branch)")
	cmd.Flags().BoolVar(&push, "push", false, "push the run's branch after a clean update")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func runFactoryUpdate(ctx context.Context, streams iostream.Streams, arg, base string, push, jsonOut bool) error {
	root, err := sessionProjectRoot(ctx)
	if err != nil {
		return err
	}
	runID := factory.ParseRunID(arg)
	if err := refuseActiveRun(runSession(root, runID), runID); err != nil {
		return err
	}
	rec, err := loadRunRecord(root, runID)
	if err != nil {
		return err
	}
	return updateRun(ctx, streams, root, rec, updateFlags{base: base, push: push, json: jsonOut})
}

// updateFlags are the options of `smelt factory update`.
type updateFlags struct {
	base       string
	push, json bool
}

// refuseActiveRun is the error for a run the daemon is still running, whose
// worktree is in use. s is the run's session, or nil when the daemon has none.
func refuseActiveRun(s *smeltd.Session, runID string) error {
	if s == nil || s.State.Finished() {
		return nil
	}
	return newUserError(fmt.Sprintf("Run %s is still running.", runID)).
		withSuggestion("Wait for it to end, or stop it with: smelt factory stop " + runID).
		withoutDetail()
}

// updateRun merges the base into the branch of the finished run rec and
// reports it. With flags.push it then pushes the branch, but only after a
// clean merge: a branch holding conflict markers is never published.
func updateRun(ctx context.Context, streams iostream.Streams, root string, rec factory.Record, flags updateFlags) error {
	if rec.Worktree == "" || !dirExists(rec.Worktree) {
		return newUserError(fmt.Sprintf("The worktree of run %s is gone, so its branch cannot be updated.", rec.RunID)).
			withoutDetail()
	}
	// Refused before the merge rather than after, so a branch that cannot be
	// pushed is not changed for nothing.
	if flags.push {
		if err := factory.CheckHeadBranch(rec.Branch, rec.DefaultBranch); err != nil {
			return &userError{msg: "Not updating " + rec.Branch + ".", err: err}
		}
	}
	author, err := commitAuthorFor(ctx, root)
	if err != nil {
		return err
	}
	rep, err := factory.UpdateBranch(ctx, root, rec, factory.UpdateOptions{Base: flags.base, Author: author})
	if err != nil {
		return &userError{msg: "Could not update " + rec.Branch + ".", err: err}
	}
	if flags.json {
		if err := iostream.PrintJSON(streams.Out, rep); err != nil {
			return err
		}
	} else {
		printUpdateReport(streams, rep)
	}
	if !flags.push {
		return nil
	}
	if rep.Result != factory.UpdateClean {
		streams.ErrPrintf("Not pushing %s: the update was %s.\n", rec.Branch, rep.Result)
		return nil
	}
	pub := factory.Publication{Dir: rec.Worktree, Remote: factory.DefaultRemote, Head: rec.Branch, DefaultBranch: rec.DefaultBranch}
	if err := pub.Push(ctx); err != nil {
		return &userError{msg: "Could not push " + rec.Branch + ".", err: err}
	}
	newStatusFunc(streams)(iostream.LevelDone, "Pushed "+rec.Branch+" to "+pub.Remote)
	return nil
}

// commitAuthorFor is who a merge in the project's runs is committed as.
func commitAuthorFor(ctx context.Context, root string) (config.CommitAuthor, error) {
	cfg, err := config.LoadProjectConfig(root)
	switch {
	case errors.Is(err, os.ErrNotExist):
		cfg = &config.ProjectConfig{}
	case err != nil:
		return config.CommitAuthor{}, &userError{msg: "Could not read .smelt/config.json.", err: err}
	}
	configured, err := cfg.Factory.CommitAuthor()
	if err != nil {
		return config.CommitAuthor{}, &userError{msg: "factory.author in .smelt/config.json is invalid.", err: err}
	}
	return factory.ResolveAuthor(ctx, root, configured).CommitAuthor, nil
}

// printUpdateReport says how the update came out, and how to resolve conflicts.
func printUpdateReport(streams iostream.Streams, r factory.UpdateReport) {
	target := factory.DefaultRemote + "/" + r.Base
	switch r.Result {
	case factory.UpdateUpToDate:
		streams.Printf("%s is already up to date with %s.\n", r.Branch, target)
	case factory.UpdateClean:
		streams.Printf("Updated %s with %s: clean (%d commits merged).\n", r.Branch, target, r.MergedCommits)
	case factory.UpdateConflicts:
		streams.Printf("Merged %s into %s with conflicts in %d file(s):\n", target, r.Branch, len(r.Conflicts))
		for _, f := range r.Conflicts {
			streams.Printf("  %s\n", f)
		}
		streams.Printf("Resolve them with:\n  smelt factory build --continue %s %q\n", r.RunID,
			"Resolve the merge conflict markers in "+joinAnd(r.Conflicts))
	}
}

// joinAnd writes items as "a", "a and b" or "a, b and c".
func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
