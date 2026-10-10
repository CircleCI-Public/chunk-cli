package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// maxPromptWidth keeps the prompt column readable in a terminal.
const maxPromptWidth = 40

func newFactoryListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's factory runs",
		Long: `List the factory runs of this project: their state, result, age, whether the
worktree has uncommitted changes, and the start of the prompt. Runs come from
their records, their worktrees and their branches, so a run is listed even when
one of them is gone.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			streams := iostream.FromCmd(cmd)
			_, entries, err := loadFactoryRuns(cmd.Context())
			if err != nil {
				return err
			}
			if jsonOut {
				return iostream.PrintJSON(streams.Out, entries)
			}
			if len(entries) == 0 {
				streams.Printf("No factory runs.\n")
				return nil
			}
			printRunTable(streams.Out, entries, time.Now())
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func newFactoryCleanCmd() *cobra.Command {
	var yes, force, dryRun bool
	cmd := &cobra.Command{
		Use:   "clean [run...]",
		Short: "Remove factory runs' worktrees, branches and records",
		Long: `Remove factory runs you are done with: each one's worktree, its branch and its
record. A run is a run ID or its branch. With none named, every run that is not
still running is selected.

Whether a run's work has landed cannot be told from git, since it is applied as
new commits, so the runs to remove are listed first and you are asked before
anything is removed. A run still in progress is never removed. A worktree with
uncommitted changes is skipped unless --force is given, which discards them.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFactoryClean(cmd, args, cleanFlags{yes: yes, force: force, dryRun: dryRun})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "remove without asking")
	cmd.Flags().BoolVar(&force, "force", false, "also remove worktrees with uncommitted changes, discarding them")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be removed and stop")
	return cmd
}

type cleanFlags struct{ yes, force, dryRun bool }

// loadFactoryRuns lists the project's runs. The daemon says which are active;
// when it is not reachable none can be.
func loadFactoryRuns(ctx context.Context) (string, []factory.RunEntry, error) {
	root, err := sessionProjectRoot(ctx)
	if err != nil {
		return "", nil, err
	}
	entries, err := factory.Inventory(ctx, root, activeRunIDs(root))
	if err != nil {
		return "", nil, &userError{msg: "Could not list the factory runs.", err: err}
	}
	if entries == nil {
		entries = []factory.RunEntry{}
	}
	return root, entries, nil
}

// activeRunIDs is the set of run IDs with a running daemon session.
func activeRunIDs(root string) map[string]bool {
	active := map[string]bool{}
	sessions, err := chunkd.ListSessions(root)
	if err != nil {
		return active
	}
	for _, s := range sessions {
		if s.State == chunkd.SessionRunning && s.Factory != nil && s.Factory.RunID != "" {
			active[s.Factory.RunID] = true
		}
	}
	return active
}

func runFactoryClean(cmd *cobra.Command, args []string, flags cleanFlags) error {
	ctx := cmd.Context()
	streams := iostream.FromCmd(cmd)
	root, entries, err := loadFactoryRuns(ctx)
	if err != nil {
		return err
	}
	sel, err := selectCleanRuns(entries, args)
	if err != nil {
		return err
	}
	var selected, skipped []factory.RunEntry
	for _, e := range sel {
		if e.Dirty && !flags.force {
			skipped = append(skipped, e)
			streams.ErrPrintf("skipped %s: worktree has uncommitted changes (use --force to discard them)\n", e.ID())
			continue
		}
		selected = append(selected, e)
	}
	if len(selected) == 0 {
		streams.Printf("Removed 0 run(s); skipped %d.\n", len(skipped))
		return nil
	}

	printRunTable(streams.Out, selected, time.Now())
	if flags.dryRun {
		return nil
	}
	if !flags.yes {
		if err := confirmClean(len(selected)); err != nil {
			return err
		}
	}

	removed, failures := removeRuns(ctx, streams, root, selected, flags.force)
	streams.Printf("Removed %d run(s); skipped %d.\n", removed, len(skipped))
	if failures != nil {
		return &userError{msg: "Could not remove every selected run.", err: failures}
	}
	return nil
}

// selectCleanRuns picks the runs to remove: those named, or with none named,
// every run that is not running. Naming a run that is running or unknown is an
// error.
func selectCleanRuns(entries []factory.RunEntry, args []string) ([]factory.RunEntry, error) {
	if len(args) == 0 {
		var all []factory.RunEntry
		for _, e := range entries {
			if e.State != factory.StateRunning {
				all = append(all, e)
			}
		}
		return all, nil
	}
	var picked []factory.RunEntry
	seen := map[int]bool{}
	for _, arg := range args {
		id := factory.ParseRunID(arg)
		i := indexOfRun(entries, id)
		if i < 0 {
			return nil, newUserError(fmt.Sprintf("No factory run %s in this project.", id)).
				withCode("command.invalid_args").
				withSuggestion("See the runs with: chunk factory list").
				withExitCode(ExitBadArgs).
				withoutDetail()
		}
		if entries[i].State == factory.StateRunning {
			return nil, newUserError(fmt.Sprintf("Factory run %s is still running.", id)).
				withCode("command.invalid_args").
				withSuggestion("Stop it in 'chunk watch' first.").
				withExitCode(ExitBadArgs).
				withoutDetail()
		}
		if !seen[i] {
			seen[i] = true
			picked = append(picked, entries[i])
		}
	}
	return picked, nil
}

func indexOfRun(entries []factory.RunEntry, id string) int {
	for i, e := range entries {
		if e.Has(id) {
			return i
		}
	}
	return -1
}

// confirmClean asks before removing, defaulting to no. Without a terminal it
// cannot ask, which is a mistake to explain rather than a reason to remove.
func confirmClean(n int) error {
	label := fmt.Sprintf("Remove these %d run(s)? Their branches and any work not applied elsewhere will be deleted.", n)
	ok := false
	err := ui.ErrNoTTY
	if !nonInteractive() {
		ok, err = ui.Confirm(label, false)
	}
	switch {
	case errors.Is(err, ui.ErrNoTTY):
		return newUserError("Cannot ask before removing runs without a terminal.").
			withCode("command.invalid_args").
			withSuggestion("Pass --yes to remove them, or --dry-run to only list them.").
			withExitCode(ExitBadArgs).
			withoutDetail()
	case errors.Is(err, ui.ErrCancelled):
		return newUserError("Nothing was removed.").withoutDetail()
	case err != nil:
		return fmt.Errorf("ask to remove the runs: %w", err)
	case !ok:
		return newUserError("Nothing was removed.").withoutDetail()
	}
	return nil
}

// removeRuns removes each run, going on past a failure, and returns how many
// were removed and the failures joined.
func removeRuns(ctx context.Context, streams iostream.Streams, root string, runs []factory.RunEntry, force bool) (int, error) {
	var errs []error
	removed := 0
	for _, e := range runs {
		r, err := factory.RemoveRun(ctx, root, e, force)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
		streams.Printf("removed %s (%s)\n", e.ID(), removalParts(r))
	}
	return removed, errors.Join(errs...)
}

// removalParts names what a removal took away.
func removalParts(r factory.Removal) string {
	var parts []string
	if r.Branch {
		parts = append(parts, "branch")
	}
	if r.Worktree {
		parts = append(parts, "worktree")
	}
	if r.Records > 0 {
		parts = append(parts, "record")
	}
	if len(parts) == 0 {
		return "already gone"
	}
	return strings.Join(parts, ", ")
}

func printRunTable(w io.Writer, entries []factory.RunEntry, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "RUN\tSTATE\tRESULT\tROUNDS\tAGE\tWORKTREE\tPROMPT")
	for _, e := range entries {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			runLabel(e), e.State, dashIfEmpty(string(e.Result)), roundsCell(e), runAge(e.StartedAt, now),
			worktreeCell(e), dashIfEmpty(shorten(e.Prompt, maxPromptWidth)))
	}
	_ = tw.Flush()
}

// runLabel is the run's ID, with how many earlier runs of a chain it continues.
func runLabel(e factory.RunEntry) string {
	if n := len(e.RunIDs) - 1; n > 0 {
		return fmt.Sprintf("%s (+%d)", e.ID(), n)
	}
	return e.ID()
}

func roundsCell(e factory.RunEntry) string {
	if e.Rounds == 0 {
		return "-"
	}
	return fmt.Sprint(e.Rounds)
}

func worktreeCell(e factory.RunEntry) string {
	switch {
	case !e.WorktreeExists:
		return "missing"
	case e.Dirty:
		return "dirty"
	}
	return "clean"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// shorten cuts s to at most width characters, marking the cut.
func shorten(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}

// runAge is how long before now the run started, as 5m, 2h or 3d, or "-" when
// its start is not known.
func runAge(start, now time.Time) string {
	if start.IsZero() {
		return "-"
	}
	d := max(now.Sub(start), 0)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}
