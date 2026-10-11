package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/smeltdev/smelt/internal/factory"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/smeltd"
)

// detachWait is how long --detach waits for the daemon to set the run up far
// enough to name its branch and worktree.
const detachWait = 30 * time.Second

// detachPollInterval is how often --detach asks the daemon whether it has.
const detachPollInterval = 100 * time.Millisecond

// detachedFactoryRun is what `smelt factory build --detach --json` prints.
type detachedFactoryRun struct {
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id"`
	Branch    string `json:"branch"`
	Worktree  string `json:"worktree"`
}

// refuseDetachPublish explains that a run that is not followed cannot be
// published when it ends.
func refuseDetachPublish() error {
	return newUserError("--detach cannot be combined with --pr or --push.").
		withCode("command.invalid_flags").
		withSuggestion("Publish the run when it has finished: smelt factory publish <run-id> --pr").
		withExitCode(ExitBadArgs).
		withoutDetail()
}

// detachRun waits until the daemon has made the run's worktree, which names its
// run, branch and worktree, and reports them. The run carries on without us.
func detachRun(ctx context.Context, streams iostream.Streams, sessionID string, jsonOut bool) error {
	ctx, cancel := context.WithTimeout(ctx, detachWait)
	defer cancel()
	ticker := time.NewTicker(detachPollInterval)
	defer ticker.Stop()
	for {
		detail, err := smeltd.FetchSession(sessionID)
		if err != nil {
			return sessionError(err)
		}
		if f := detail.Factory; f != nil && f.RunID != "" && f.Branch != "" {
			run := detachedFactoryRun{RunID: f.RunID, SessionID: sessionID, Branch: f.Branch, Worktree: f.Worktree}
			if jsonOut {
				return iostream.PrintJSON(streams.Out, run)
			}
			streams.Printf("Started run %s on %s\n", run.RunID, run.Branch)
			return nil
		}
		if detail.State.Finished() {
			return finishFactory(streams, detail, jsonOut)
		}
		select {
		case <-ctx.Done():
			return newUserError("The run was started but has not set up its branch yet.").
				withSuggestion("Follow it in 'smelt watch' (press r), or with 'smelt factory status'.").
				withoutDetail()
		case <-ticker.C:
		}
	}
}

// runSession is the daemon's session for the run in the project at root, or
// nil when the daemon has none: it has restarted, or is not running.
func runSession(root, runID string) *smeltd.Session {
	sessions, err := smeltd.ListSessions(root)
	if err != nil {
		return nil
	}
	for _, s := range sessions {
		if s.ID == runID || s.Factory != nil && s.Factory.RunID == runID {
			return &s
		}
	}
	return nil
}

// noSuchRun is the error for a run ID the project has no trace of.
func noSuchRun(runID string) error {
	return newUserError(fmt.Sprintf("No factory run %s in this project.", runID)).
		withCode("command.invalid_args").
		withSuggestion("List the runs with: smelt factory list").
		withExitCode(ExitNotFound).
		withoutDetail()
}

// loadRunRecord is the record of the run runID, or the error to show.
func loadRunRecord(root, runID string) (factory.Record, error) {
	rec, err := factory.LoadRecord(root, runID)
	switch {
	case err == nil:
		return rec, nil
	case errors.Is(err, factory.ErrNoRecord):
		return factory.Record{}, noSuchRun(runID)
	}
	return factory.Record{}, &userError{msg: fmt.Sprintf("Could not read factory run %s.", runID), err: err}
}

// resolveRunID is the run a command is about: the one named, as its ID or its
// branch, else the project's newest.
func resolveRunID(ctx context.Context, root string, args []string) (string, error) {
	if len(args) > 0 {
		return factory.ParseRunID(args[0]), nil
	}
	entries, err := factory.Inventory(ctx, root, activeRunIDs(root))
	if err != nil {
		return "", &userError{msg: "Could not list the factory runs.", err: err}
	}
	if len(entries) == 0 {
		return "", newUserError("No factory runs in this project.").
			withCode("command.invalid_args").
			withExitCode(ExitNotFound).
			withoutDetail()
	}
	return entries[0].ID(), nil
}

// recordStatus is the status of a run the daemon no longer has. A record is
// only left by a run that ended, so its state is done.
type recordStatus struct {
	factory.Record
	State smeltd.SessionState `json:"state"`
}

func newFactoryStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status [run-id]",
		Short: "Show how a factory run is going, or how it ended",
		Long: `Show a factory run: its state, rounds, reviews, result and cost. The run is a
run ID or its branch; with none, the newest run of this project.

A run the daemon still knows is shown in full. One it no longer knows, after
a restart, is shown from its record, with state "done".`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			runID, err := resolveRunID(ctx, root, args)
			if err != nil {
				return err
			}
			if s := runSession(root, runID); s != nil {
				detail, err := smeltd.FetchSession(s.ID)
				if err != nil {
					return sessionError(err)
				}
				return printStatus(streams, detail, jsonOut)
			}
			rec, err := loadRunRecord(root, runID)
			if err != nil {
				return err
			}
			if jsonOut {
				return iostream.PrintJSON(streams.Out, recordStatus{Record: rec, State: smeltd.SessionDone})
			}
			streams.Printf("Run %s: %s\n  Branch: %s\n  Worktree: %s\n  Result: %s after %d round(s)\n",
				rec.RunID, smeltd.SessionDone, rec.Branch, rec.Worktree, rec.Result, rec.Rounds)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// printStatus writes a daemon session's status: all of it as JSON, or a
// short summary.
func printStatus(streams iostream.Streams, detail smeltd.SessionDetail, jsonOut bool) error {
	if jsonOut {
		return iostream.PrintJSON(streams.Out, detail)
	}
	f := detail.Factory
	if f == nil {
		f = &smeltd.FactoryRun{}
	}
	cost, turns := implementerCost(detail)
	streams.Printf("Run %s: %s\n  Branch: %s\n  Worktree: %s\n  Round: %d of %d\n",
		f.RunID, detail.State, f.Branch, f.Worktree, len(detail.Rounds), f.Attempts)
	if f.Result != "" {
		streams.Printf("  Result: %s\n", f.Result)
	}
	if detail.Error != "" {
		streams.Printf("  Error: %s\n", detail.Error)
	}
	streams.Printf("  Cost: $%.2f over %d implementer turn(s)\n", cost, turns)
	return nil
}

func newFactoryStopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <run-id>",
		Short: "Stop a running factory run",
		Long: `Stop a factory run that is running on the daemon, as Ctrl-C does: the work so
far is still committed on the run's branch. The run is a run ID or its branch.
A run that has finished is reported, not an error. Follow the stop with
'smelt factory status'.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			runID := factory.ParseRunID(args[0])
			s := runSession(root, runID)
			if s == nil {
				if _, err := loadRunRecord(root, runID); err != nil {
					return err
				}
			}
			if s == nil || s.State.Finished() {
				streams.Printf("Run %s has already finished.\n", runID)
				return nil
			}
			if err := smeltd.CancelSession(s.ID); err != nil {
				return sessionError(err)
			}
			streams.Printf("Stopping run %s. The work so far is committed on its branch as it winds down.\n", runID)
			return nil
		},
	}
	return cmd
}

func newFactoryPublishCmd() *cobra.Command {
	var pub publishFlags
	cmd := &cobra.Command{
		Use:   "publish <run-id> [--pr|--push]",
		Short: "Push a finished run's branch, or open a PR for it",
		Long: `Publish the branch of a finished factory run as the prompt at the end of
'smelt factory build' would: --push pushes it, --pr pushes it and opens a PR
into the branch the run started from, or updates the branch's PR. The run is
a run ID or its branch. With neither flag, in a terminal, it asks.

Only smelt/* branches are ever pushed, never forced, and never the repository's
default branch. A PR is opened with the GitHub CLI (gh); without it the branch
is pushed and a compare URL is printed.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			mode, err := pub.mode()
			if err != nil {
				return err
			}
			prompt := canPrompt(cmd.InOrStdin(), false)
			if mode == factory.PublishPrompt && !prompt {
				return newUserError("Say how to publish: --push or --pr.").
					withCode("command.invalid_args").
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
			root, err := sessionProjectRoot(ctx)
			if err != nil {
				return err
			}
			runID := factory.ParseRunID(args[0])
			if s := runSession(root, runID); s != nil && !s.State.Finished() {
				return newUserError(fmt.Sprintf("Run %s is still running.", runID)).
					withSuggestion("Wait for it, or stop it with: smelt factory stop " + runID).
					withoutDetail()
			}
			rec, err := loadRunRecord(root, runID)
			if err != nil {
				return err
			}
			f, err := publishableRun(rec)
			if err != nil {
				return err
			}
			pub.bodySet = cmd.Flags().Changed("pr-body")
			return publishRun(ctx, streams, f, pub, prompt, hostFor(f.Worktree))
		},
	}
	cmd.Flags().BoolVar(&pub.push, "push", false, "push the run's branch")
	cmd.Flags().BoolVar(&pub.pr, "pr", false, "push the run's branch and open a PR (or update the branch's PR)")
	cmd.Flags().StringVar(&pub.base, "base", "", "branch the PR targets (default: the branch the run started from, else the default branch)")
	cmd.Flags().StringVar(&pub.title, "pr-title", "", "PR title (default: the run's notes)")
	cmd.Flags().StringVar(&pub.body, "pr-body", "", "PR description (default: the run's What / Why / How notes)")
	cmd.Flags().BoolVar(&pub.draft, "draft", false, "open the PR as a draft")
	return cmd
}

// publishableRun is rec as the session record publishing works from, or an error
// when the run has nothing to publish. A record is written when a run
// commits, so a record with a result and a worktree is committed work.
func publishableRun(rec factory.Record) (*smeltd.FactoryRun, error) {
	switch {
	case rec.Result == "":
		return nil, newUserError(fmt.Sprintf("Run %s has not finished.", rec.RunID)).
			withSuggestion("Continue it with: smelt factory build --continue " + rec.RunID).
			withoutDetail()
	case rec.Worktree == "" || !dirExists(rec.Worktree):
		return nil, newUserError(fmt.Sprintf("The worktree of run %s is gone, so its branch cannot be published from it.", rec.RunID)).
			withoutDetail()
	}
	f := &smeltd.FactoryRun{
		Prompt: rec.Prompt, RunID: rec.RunID, Worktree: rec.Worktree, Branch: rec.Branch,
		Baseline: rec.Baseline, Head: rec.Head, BaseBranch: rec.BaseBranch, DefaultBranch: rec.DefaultBranch,
		Result: string(rec.Result), Rounds: rec.Rounds, Committed: true,
	}
	if rec.Notes != nil {
		n := smeltd.Notes(*rec.Notes)
		f.Notes = &n
	}
	return f, nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
