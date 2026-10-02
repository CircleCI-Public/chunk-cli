package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newFactoryCmd() *cobra.Command {
	var attempts, reviewers int
	var keepSidecars, noValidate bool
	var orgID, image, model, reviewsDir string
	var implementTimeout, reviewTimeout time.Duration

	cmd := &cobra.Command{
		Use:   "factory <prompt>",
		Short: "Implement a prompt on a sidecar, then review and validate it until it passes",
		Long: `Send a prompt to an implementer agent running on a sidecar, then loop:
review its work with each prompt in the reviews directory, each on its own
sidecar, and run the project's validation commands, feeding failures back to
the implementer until every check passes or attempts run out.

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
			status := newStatusFunc(streams)
			if attempts < 1 {
				return newUserError("--attempts must be at least 1.").
					withCode("command.invalid_args").
					withExitCode(ExitBadArgs).
					withoutDetail()
			}

			workDir, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, err := config.LoadProjectConfig(workDir)
			if err != nil {
				return &userError{msg: msgValidateNotConfigured, suggestion: suggestionRunInit, err: err}
			}
			prompts, commands, err := factoryChecks(workDir, reviewsDir, cfg, noValidate)
			if err != nil {
				return err
			}

			rc, _ := config.Resolve("", "", insecureStorageFlag(cmd))
			cred, credSource, credErr := reviewCredential(rc)
			if credErr != nil {
				return credErr
			}
			client, err := ensureCircleCIClient(ctx, cmd, rc, streams, ui.PromptHidden)
			if err != nil {
				return err
			}
			if orgID == "" {
				orgID = cfg.OrgID
			}
			resolvedOrgID, err := resolveOrgID(orgID, workDir, orgPicker(ctx, client, rc.CircleCITokenSource, streams))
			if err != nil {
				return err
			}
			if image == "" {
				image = resolveImage("", cfg)
			}
			// The pool would find this out too, but only after sidecars started
			// booting.
			if _, _, err := gitremote.DetectOrgAndRepoCtx(ctx, workDir); err != nil {
				return &userError{msg: "Could not tell which repository this is from its origin remote.", err: err}
			}

			if len(prompts) == 0 {
				reviewers = 0
			} else if reviewers <= 0 || reviewers > len(prompts) {
				reviewers = len(prompts)
			}
			runID := time.Now().UTC().Format("20060102-150405")
			root, wt, err := createFactoryWorktree(ctx, workDir, runID)
			if err != nil {
				return err
			}
			// Until the implementer starts, the worktree holds nothing the
			// developer does not already have.
			started := false
			defer func() {
				if !started {
					removeFactoryWorktree(ctx, root, wt, status)
				}
			}()

			status(iostream.LevelStep, fmt.Sprintf("Preparing an implementer sidecar and %d reviewer sidecar(s)...", reviewers))
			pool, err := newPool(ctx, client, sidecar.PoolOptions{
				Size:  factory.PoolSize(reviewers),
				Name:  factory.PoolName(runID),
				OrgID: resolvedOrgID,
				Image: image,
				// The sidecars start from the worktree, and its state stays in
				// the project, where the dashboard finds it.
				WorkDir:  wt.Path,
				StateDir: root,
			}, "factory's sidecars", rc.CircleCITokenSource, status)
			if err != nil {
				return err
			}
			defer closeFactoryPool(ctx, pool, keepSidecars, status)
			if err := waitPoolReady(ctx, pool, "factory's sidecars"); err != nil {
				return err
			}
			// The implementer holds its member for the whole run; reviews are
			// handed the rest.
			impl, err := pool.Acquire(ctx)
			if err != nil {
				return &userError{msg: "Could not check out the implementer's sidecar.", err: err}
			}
			activity := newFactoryActivity(ctx, root, wt.Branch, impl.ID)

			steps := &factory.Sidecars{
				Exec: review.ClientExec,
				Implementer: &factory.Implementer{
					Exec: review.ClientExec, Entry: impl, Credential: cred, BaseURL: rc.AnthropicBaseURL,
					Model: model, Timeout: implementTimeout,
					OnActivity: func(a factory.Activity) {
						printActivity(status, a)
						activity.toolUsed(a)
					},
				},
				Acquire:   pool.Acquire,
				Release:   pool.Release,
				Reviewers: factory.Members(impl, pool.IDs()),
				Relay:     factory.NewRelay(client, wt.Path, status),
				Prompts:   prompts,
				Review: review.Options{
					Credential: cred, BaseURL: rc.AnthropicBaseURL, Model: model, Timeout: reviewTimeout,
					StructuredFindings: true,
					ProgressFn: func(e review.ProgressEvent) {
						printReviewProgress(status, e)
						activity.reviewProgress(e)
					},
					OnSubmitted: activity.reviewSubmitted,
				},
				Commands:    commands,
				OnSubmitted: activity.commandSubmitted,
				OnCheck: func(c factory.Check) {
					printCheck(status, c)
					activity.checked(c)
				},
			}
			if err := steps.Prepare(ctx); err != nil {
				return &userError{msg: "Could not set up the implementer's workspace.", err: err}
			}

			started = true
			loop := factory.Loop{Attempts: attempts, OnEvent: func(e factory.Event) { printEvent(status, attempts, e) }}
			outcome, loopErr := loop.Run(ctx, recordedSteps{Steps: steps, activity: activity}, args[0])
			activity.finish(loopErr)
			// Whatever the implementer got to is kept, even when the loop
			// failed partway, so the work is not lost with the sidecars.
			commitFactoryWork(ctx, steps, wt, factory.CommitMessage(args[0], runID, outcome), status, streams)
			if errors.Is(loopErr, review.ErrCredentialRejected) {
				return credentialRejected(cred, credSource, rc.AnthropicBaseURL, loopErr)
			}
			if loopErr != nil {
				return factoryLoopError(loopErr)
			}
			return reportOutcome(status, outcome)
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
	prompts, err := review.LoadPrompts(reviewsDir)
	switch {
	case err == nil:
	case !explicit && (errors.Is(err, review.ErrNoPrompts) || errors.Is(err, os.ErrNotExist)):
		// The default directory is optional: validation commands alone may do.
	case errors.Is(err, review.ErrNoPrompts):
		return nil, nil, &userError{msg: fmt.Sprintf("No review prompts found in %s.", reviewsDir), suggestion: "Add one .md or .txt file per review.", err: err}
	default:
		return nil, nil, &userError{msg: fmt.Sprintf("Could not read review prompts from %s.", reviewsDir), err: err}
	}
	var commands []config.Command
	if !noValidate {
		commands = factory.ValidationCommands(cfg.Commands)
	}
	if len(prompts) == 0 && len(commands) == 0 {
		return nil, nil, &userError{
			msg:        "Nothing to check the implementer's work with.",
			suggestion: fmt.Sprintf("Add review prompts to %s, or validation commands with 'chunk init'.", review.DefaultDir),
			hideDetail: true,
		}
	}
	return prompts, commands, nil
}

// createFactoryWorktree makes the run's worktree in the project's chunk data
// directory, outside the repository, so nothing that syncs or scans the
// developer's checkout finds it. It returns the repository root with it.
func createFactoryWorktree(ctx context.Context, workDir, runID string) (string, factory.Worktree, error) {
	root := gitutil.TopLevelCtx(ctx, workDir)
	if root == "" {
		return "", factory.Worktree{}, &userError{msg: "chunk factory must be run inside a git repository.", hideDetail: true}
	}
	dataDir, err := config.ProjectDataDir(root)
	if err != nil {
		return "", factory.Worktree{}, &userError{msg: "Could not find chunk's data directory for this project.", err: err}
	}
	wt, err := factory.CreateWorktree(ctx, root, filepath.Join(dataDir, "factory", runID), runID)
	if err != nil {
		return "", factory.Worktree{}, &userError{msg: "Could not create the run's worktree.", err: err}
	}
	return root, wt, nil
}

// removeFactoryWorktree removes the worktree of a run that ended before the
// implementer started. It has its own deadline, since it runs on the way out.
func removeFactoryWorktree(ctx context.Context, root string, wt factory.Worktree, status iostream.StatusFunc) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := wt.Remove(ctx, root); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not remove the run's worktree %s: %v", wt.Path, err))
	}
}

// commitFactoryWork brings the implementer's last work into the worktree and
// commits it on the run's branch. It runs on the way out, after a failure too,
// so it gets its own deadlines rather than the run's possibly canceled context:
// one for the pull, and a fresh one for the commit, so a pull that hangs cannot
// use up the commit's time. What an earlier round pulled is committed even if
// the last pull fails.
func commitFactoryWork(ctx context.Context, steps *factory.Sidecars, wt factory.Worktree, message string, status iostream.StatusFunc, streams iostream.Streams) {
	base := context.WithoutCancel(ctx)
	pullCtx, cancelPull := context.WithTimeout(base, 2*time.Minute)
	err := steps.Pull(pullCtx)
	cancelPull()
	if err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not bring back the implementer's last changes: %v", err))
	}
	ctx, cancel := context.WithTimeout(base, cleanupTimeout)
	defer cancel()
	if _, err := wt.Commit(ctx, message); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not commit the work in %s: %v", wt.Path, err))
		return
	}
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

// closeFactoryPool deletes the run's sidecars, or with keep leaves them
// running. A kept pool stays in its state file, which is how the dashboard and
// 'chunk sidecar' still find its sidecars; no later run reuses it, since the
// name is the run's own.
func closeFactoryPool(ctx context.Context, pool *sidecar.Pool, keep bool, status iostream.StatusFunc) {
	closePool(ctx, pool, !keep, status)
	if keep {
		status(iostream.LevelInfo, "kept sidecars: "+strings.Join(pool.IDs(), " "))
	}
}

func factoryLoopError(err error) error {
	if errors.Is(err, review.ErrClaudeMissing) {
		return agentNotInstalled("the factory's sidecars", err)
	}
	return &userError{msg: "The factory stopped early.", err: err}
}

func printEvent(status iostream.StatusFunc, attempts int, e factory.Event) {
	switch e.Kind {
	case factory.EventImplementing:
		if e.Round == 1 {
			status(iostream.LevelStep, "Implementing...")
			return
		}
		status(iostream.LevelStep, fmt.Sprintf("Round %d/%d: fixing failed checks...", e.Round, attempts))
	case factory.EventImplemented:
		status(iostream.LevelDone, fmt.Sprintf("implementer finished in %s ($%.2f)", e.Turn.Duration.Round(time.Second), e.Turn.CostUSD))
		if e.Turn.Summary != "" {
			status(iostream.LevelInfo, oneLineSummary(e.Turn.Summary))
		}
	case factory.EventCollected:
		if e.Change.Empty() {
			status(iostream.LevelWarn, "no changes")
			return
		}
		status(iostream.LevelInfo, e.Change.Stat)
	case factory.EventChecking:
		status(iostream.LevelStep, fmt.Sprintf("Round %d/%d: reviewing and validating...", e.Round, attempts))
	case factory.EventChecked:
		passed := 0
		for _, c := range e.Checks {
			if c.Status == factory.StatusPassed {
				passed++
			}
		}
		status(iostream.LevelInfo, fmt.Sprintf("%d of %d checks passed", passed, len(e.Checks)))
	}
}

func printActivity(status iostream.StatusFunc, a factory.Activity) {
	if a.Tool == "" {
		return
	}
	status(iostream.LevelInfo, fmt.Sprintf("  %s %s", a.Tool, oneLineSummary(a.Detail)))
}

func printReviewProgress(status iostream.StatusFunc, e review.ProgressEvent) {
	switch e.State {
	case review.StateQueued:
	case review.StateRunning:
		status(iostream.LevelInfo, fmt.Sprintf("  review %s started on %s", e.Prompt, e.SidecarID))
	case review.StateFailed:
		status(iostream.LevelWarn, fmt.Sprintf("  review %s could not run: %s", e.Prompt, e.Error))
	case review.StateDone:
		status(iostream.LevelInfo, fmt.Sprintf("  review %s finished in %s", e.Prompt, e.Duration.Round(time.Second)))
	}
}

func printCheck(status iostream.StatusFunc, c factory.Check) {
	switch c.Status {
	case factory.StatusPassed:
		status(iostream.LevelDone, fmt.Sprintf("  %s passed in %s", c.Name, c.Duration.Round(time.Second)))
	case factory.StatusFailed:
		status(iostream.LevelError, fmt.Sprintf("  %s failed in %s", c.Name, c.Duration.Round(time.Second)))
	case factory.StatusErrored:
		status(iostream.LevelWarn, fmt.Sprintf("  %s could not run: %s", c.Name, c.Error))
	}
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
