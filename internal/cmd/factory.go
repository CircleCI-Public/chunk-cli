package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/watch"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

func newFactoryCmd() *cobra.Command {
	var attempts, reviewers int
	var keepSidecars, noValidate, jsonOut bool
	var orgID, image, model, reviewsDir string
	var implementTimeout, reviewTimeout time.Duration

	cmd := &cobra.Command{
		Use:   "factory <prompt>",
		Short: "Implement a prompt on a sidecar, then review and validate it until it passes",
		Long: `Send a prompt to an implementer agent running on a sidecar, then loop:
review its work with each prompt in the reviews directory, each on its own
sidecar, and run the project's validation commands, feeding failures back to
the implementer until every check passes or attempts run out.

The run happens on the local watch daemon, as a session: watch it in
'chunk watch'. Ctrl-C stops the run, and what the implementer did so far is
still committed.

The run works in a git worktree of its own, on the branch
chunk/factory/<run id>, starting from your files as they are, uncommitted
changes included. Your checkout is never touched. The implementer's work is
synced into the worktree each round and committed there when the run ends;
the worktree is kept so you can look at it or carry on in it.`,
		// Hidden until the workshop build settles.
		Hidden:       true,
		SilenceUsage: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
				return nil
			}
			return newUserError("Pass the prompt as one argument.").
				withCode("command.invalid_args").
				withSuggestion(`Quote it: chunk factory "add a --verbose flag"`).
				withExitCode(ExitBadArgs).
				withoutDetail()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			if attempts < 1 {
				return newUserError("--attempts must be at least 1.").
					withCode("command.invalid_args").
					withExitCode(ExitBadArgs).
					withoutDetail()
			}
			if err := requireLocalDaemon(); err != nil {
				return err
			}
			root, err := sessionProjectRoot(ctx, "")
			if err != nil {
				return err
			}
			cfg, err := config.LoadProjectConfig(root)
			if err != nil {
				return &userError{msg: msgValidateNotConfigured, suggestion: suggestionRunInit, err: err}
			}
			// The daemon loads these itself; loading them here first explains a
			// mistake before anything starts.
			if _, _, err := factoryChecks(root, reviewsDir, cfg, noValidate); err != nil {
				return err
			}
			relReviews, err := factoryReviewsDir(root, reviewsDir)
			if err != nil {
				return err
			}

			if err := watchd.EnsureRunning([]string{watchCmdName, watchDaemonSubcmd}); err != nil {
				return &userError{msg: "Could not start the watch daemon.", err: err}
			}
			id, err := watchd.StartFactory(watchd.FactoryRequest{
				ProjectRoot:             root,
				Prompt:                  args[0],
				ReviewsDir:              relReviews,
				NoValidate:              noValidate,
				Attempts:                attempts,
				Reviewers:               reviewers,
				Model:                   model,
				ImplementTimeoutSeconds: int(implementTimeout / time.Second),
				ReviewTimeoutSeconds:    int(reviewTimeout / time.Second),
				KeepSidecars:            keepSidecars,
				OrgID:                   orgID,
				Image:                   image,
			})
			if err != nil {
				return sessionError(err)
			}
			streams.ErrPrintf("Factory run %s started on the watch daemon. Ctrl-C stops it.\n", id)
			if !jsonOut && ui.RequireStdoutTTY() == nil {
				return followFactoryTUI(ctx, streams, root, id)
			}
			return followSession(ctx, streams, id, jsonOut, true)
		},
	}

	cmd.Flags().IntVar(&attempts, "attempts", 3, "most rounds of review and validation")
	cmd.Flags().IntVar(&reviewers, "reviewers", 0, "reviewer sidecars (0: one per review prompt)")
	cmd.Flags().StringVar(&reviewsDir, "reviews", "", fmt.Sprintf("directory of review prompts (default: %s)", review.DefaultDir))
	cmd.Flags().BoolVar(&noValidate, "no-validate", false, "skip the project's validation commands")
	cmd.Flags().BoolVar(&keepSidecars, "keep-sidecars", false, "leave the sidecars running when the run ends")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&image, "image", "", "Snapshot image ID (default: validation.sidecarImage from config)")
	cmd.Flags().StringVar(&model, "model", "", "Claude model (default: Claude Code's default)")
	cmd.Flags().DurationVar(&implementTimeout, "implement-timeout", factory.DefaultImplementTimeout, "max time for each implementer turn")
	cmd.Flags().DurationVar(&reviewTimeout, "review-timeout", review.DefaultTimeout, "max time for each review")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// factoryChecks loads what the implementer's work is checked with: the review
// prompts and, unless noValidate, the validation commands. Having neither is an
// error, since the loop would then pass whatever the implementer wrote.
func factoryChecks(workDir, reviewsDir string, cfg *config.ProjectConfig, noValidate bool) ([]review.Prompt, []config.Command, error) {
	explicit := reviewsDir != ""
	if !explicit {
		reviewsDir = filepath.Join(workDir, review.DefaultDir)
	}
	prompts, commands, err := factory.LoadChecks(reviewsDir, !explicit, cfg, noValidate)
	switch {
	case err == nil:
		return prompts, commands, nil
	case errors.Is(err, factory.ErrNothingToCheck):
		return nil, nil, &userError{
			msg:        "Nothing to check the implementer's work with.",
			suggestion: fmt.Sprintf("Add review prompts to %s, or validation commands with 'chunk init'.", review.DefaultDir),
			hideDetail: true,
		}
	case errors.Is(err, review.ErrNoPrompts):
		return nil, nil, &userError{msg: fmt.Sprintf("No review prompts found in %s.", reviewsDir), suggestion: "Add one .md or .txt file per review.", err: err}
	}
	return nil, nil, &userError{msg: fmt.Sprintf("Could not read review prompts from %s.", reviewsDir), err: err}
}

// printFactoryWork summarizes the work committed on the run's branch and says
// how to keep it.
func printFactoryWork(ctx context.Context, wt factory.Worktree, status iostream.StatusFunc, streams iostream.Streams) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	stat, err := wt.Stat(ctx)
	if err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not summarize the work on %s: %v", wt.Branch, err))
		streams.ErrPrintf("  Worktree: %s\n", wt.Path)
		return
	}
	if stat == "" {
		status(iostream.LevelInfo, "No changes to keep. The worktree is at "+wt.Path)
		return
	}
	status(iostream.LevelDone, fmt.Sprintf("Committed %s to %s", stat, wt.Branch))
	streams.ErrPrintf("  Worktree: %s\n  %s\n", wt.Path, keepWorkHint(wt))
}

// keepWorkHint says how to bring the run's work into the developer's checkout.
// A merge only works when the branch starts at their HEAD: when it starts from
// their uncommitted work committed as the baseline, a merge would collide with
// that same work still uncommitted in their checkout, so they apply the run's
// own changes on top of it instead.
func keepWorkHint(wt factory.Worktree) string {
	if wt.Baseline == wt.Head {
		return "Merge it with: git merge " + wt.Branch
	}
	return fmt.Sprintf("Apply it with: git diff --binary %s %s | git apply", wt.Baseline, wt.Branch)
}

// factoryReviewsDir is the --reviews directory relative to the project root,
// which is how the daemon is told it, or "" for the default.
func factoryReviewsDir(root, dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	// The root has its symlinks resolved, so the directory must too.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", newUserError("--reviews must be a directory inside the project.").
			withCode("command.invalid_args").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return rel, nil
}

// finishFactory prints where a factory run ended up and maps anything short of
// every check passing to an error.
func finishFactory(ctx context.Context, streams iostream.Streams, detail watchd.SessionDetail, jsonOut bool) error {
	status := newStatusFunc(streams)
	f := detail.Factory
	if jsonOut {
		if err := iostream.PrintJSON(streams.Out, detail); err != nil {
			return err
		}
		// The JSON is the report; the exit code still says whether it passed.
		status = func(iostream.Level, string) {}
	} else {
		printFactoryLeftovers(ctx, f, status, streams)
	}
	switch detail.State {
	case watchd.SessionCancelled:
		return newUserError("The factory run was cancelled.").withoutDetail()
	case watchd.SessionFailed:
		return &userError{msg: "The factory run failed: " + detail.Error, hideDetail: true, errMsg: "factory run failed"}
	case watchd.SessionRunning, watchd.SessionPaused, watchd.SessionDone:
	}
	return reportOutcome(status, factoryOutcome(detail))
}

// printFactoryLeftovers says what a run left behind: its committed work, or
// where its uncommitted work still is, and any sidecars kept running.
func printFactoryLeftovers(ctx context.Context, f *watchd.FactoryRun, status iostream.StatusFunc, streams iostream.Streams) {
	switch {
	case f.Committed:
		printFactoryWork(ctx, factory.Worktree{Path: f.Worktree, Branch: f.Branch, Baseline: f.Baseline, Head: f.Head}, status, streams)
	case f.Worktree != "":
		// A worktree removed after an early failure held no work.
		if _, err := os.Stat(f.Worktree); err == nil {
			status(iostream.LevelWarn, "The work was not committed. It is in the worktree "+f.Worktree)
		}
	}
	if len(f.KeptSidecars) > 0 {
		status(iostream.LevelInfo, "kept sidecars: "+strings.Join(f.KeptSidecars, " "))
	}
}

// factoryOutcome rebuilds a run's outcome from its record: why it stopped,
// and how the last round it checked came out. A run that stopped because the
// implementer changed nothing new ends on a round that was never checked.
func factoryOutcome(detail watchd.SessionDetail) factory.Outcome {
	o := factory.Outcome{Result: factory.Result(detail.Factory.Result), Rounds: detail.Factory.Rounds}
	for _, rd := range detail.Details {
		if rd.Number != o.Rounds {
			continue
		}
		for _, res := range rd.Results {
			o.Checks = append(o.Checks, factory.Check{
				Name: res.Prompt, Kind: factory.KindReview, Status: factory.Status(res.Status),
				SidecarID: res.SidecarID, Error: res.Error, Findings: res.Findings,
			})
		}
	}
	return o
}

// reportOutcome prints how the last round's checks came out and returns an
// error unless they all passed.
func reportOutcome(status iostream.StatusFunc, o factory.Outcome) error {
	for _, c := range o.Checks {
		if c.Kind != factory.KindReview {
			continue
		}
		switch c.Status {
		case factory.StatusPassed:
			status(iostream.LevelDone, fmt.Sprintf("review %s: no findings", c.Name))
		case factory.StatusFailed:
			status(iostream.LevelError, fmt.Sprintf("review %s: %d finding(s)", c.Name, len(c.Findings)))
			for _, f := range c.Findings {
				status(iostream.LevelInfo, fmt.Sprintf("  [%s] %s %s", f.Severity, f.Location(), oneLineSummary(f.Body)))
			}
		case factory.StatusErrored:
			status(iostream.LevelWarn, fmt.Sprintf("review %s could not run: %s", c.Name, c.Error))
		}
	}
	switch o.Result {
	case factory.ResultPassed:
		status(iostream.LevelDone, fmt.Sprintf("All checks passed after %d round(s).", o.Rounds))
		return nil
	case factory.ResultNoChange:
		return &userError{msg: "The implementer made no changes.", hideDetail: true, errMsg: "no changes"}
	case factory.ResultStuck:
		return &userError{msg: fmt.Sprintf("The implementer stopped changing the code after round %d, with checks still failing.", o.Rounds), hideDetail: true, errMsg: "stuck"}
	case factory.ResultExhausted:
	}
	return &userError{msg: fmt.Sprintf("Checks still failed after %d round(s).", o.Rounds), hideDetail: true, errMsg: "attempts exhausted"}
}

// oneLineSummary collapses text to one line short enough for a status line.
func oneLineSummary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}

// followFactoryTUI shows a factory run in the watch dashboard, opened on the
// run's session. Quitting the dashboard only detaches (x twice cancels the
// run), so what happens next depends on where the run is: a finished run is
// reported as it is without the dashboard, and one still going is left to the
// daemon.
func followFactoryTUI(ctx context.Context, streams iostream.Streams, root, id string) error {
	dataDir, err := config.ProjectDataDir(root)
	if err != nil {
		return fmt.Errorf("find chunk's data directory for the project: %w", err)
	}
	entries := []watch.ProjectEntry{{DataDir: dataDir, ProjectRoot: root}}
	daemonArgs := []string{watchCmdName, watchDaemonSubcmd}
	m := watch.New(entries, false).WithDaemonArgs(daemonArgs).WithSession(id)
	if _, err := tea.NewProgram(m, tea.WithContext(ctx)).Run(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("factory dashboard: %w", err)
	}

	detail, err := watchd.FetchSession(id)
	if err != nil {
		return sessionError(err)
	}
	if detail.State.Finished() {
		return finishFactory(ctx, streams, detail, false)
	}
	streams.ErrPrintf("Detached. Run %s keeps going on the watch daemon (chunk watch to see it, chunk session cancel %s to stop it).\n", id, id)
	return nil
}
