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

The implementer's work is written as a patch under .chunk/factory, to be
applied with git apply to the tree the run started from.`,
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
			_, repo, err := gitremote.DetectOrgAndRepoCtx(ctx, workDir)
			if err != nil {
				return &userError{msg: "Could not tell which repository this is from its origin remote.", err: err}
			}

			if len(prompts) == 0 {
				reviewers = 0
			} else if reviewers <= 0 || reviewers > len(prompts) {
				reviewers = len(prompts)
			}
			status(iostream.LevelStep, fmt.Sprintf("Creating an implementer sidecar and %d reviewer sidecar(s)...", reviewers))
			impl, revs, err := factory.Provision(ctx, client, resolvedOrgID, image, sidecar.DefaultWorkspace(repo), reviewers, status)
			if err != nil {
				if authErr := notAuthorized("create sidecars", rc.CircleCITokenSource, err); authErr != nil {
					return authErr
				}
				return &userError{msg: "Could not create the factory's sidecars.", err: err}
			}
			all := append([]*sidecar.PoolEntry{impl}, revs...)
			defer func() {
				if keepSidecars {
					ids := make([]string, len(all))
					for i, e := range all {
						ids[i] = e.ID
					}
					status(iostream.LevelInfo, "kept sidecars: "+strings.Join(ids, " "))
					return
				}
				if err := factory.Teardown(client, all); err != nil {
					status(iostream.LevelWarn, fmt.Sprintf("could not delete sidecars: %v", err))
					status(iostream.LevelInfo, "Delete them with 'chunk sidecar delete', or wait for them to expire.")
				}
			}()

			relay, err := factory.NewRelay(client, status)
			if err != nil {
				return err
			}
			defer func() { _ = relay.Close() }()

			steps := &factory.Sidecars{
				Client: client,
				Exec:   review.ClientExec,
				Implementer: &factory.Implementer{
					Exec: review.ClientExec, Entry: impl, Credential: cred, BaseURL: rc.AnthropicBaseURL,
					Model: model, Timeout: implementTimeout,
					OnActivity: func(a factory.Activity) { printActivity(status, a) },
				},
				Reviewers: revs,
				Relay:     relay,
				Prompts:   prompts,
				Review: review.Options{
					Credential: cred, BaseURL: rc.AnthropicBaseURL, Model: model, Timeout: reviewTimeout,
					StructuredFindings: true,
					ProgressFn:         func(e review.ProgressEvent) { printReviewProgress(status, e) },
				},
				Commands: commands,
				Status:   status,
				OnCheck:  func(c factory.Check) { printCheck(status, c) },
			}

			status(iostream.LevelStep, "Syncing your working tree to the implementer...")
			if err := steps.Prepare(ctx, workDir); err != nil {
				return &userError{msg: "Could not set up the implementer's workspace.", err: err}
			}

			loop := factory.Loop{Attempts: attempts, OnEvent: func(e factory.Event) { printEvent(status, attempts, e) }}
			outcome, loopErr := loop.Run(ctx, steps, args[0])
			// Whatever the implementer got to is kept, even when the loop
			// failed partway, so the work is not lost with the sidecars.
			saveFactoryPatch(ctx, steps, workDir, status, streams)
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

// saveFactoryPatch writes the implementer's work, if any, to a patch under
// .chunk/factory. It runs on the way out, after a failure too, so it gets its
// own deadline rather than the run's possibly canceled context.
func saveFactoryPatch(ctx context.Context, steps *factory.Sidecars, workDir string, status iostream.StatusFunc, streams iostream.Streams) {
	path := filepath.Join(workDir, ".chunk", "factory", time.Now().UTC().Format("20060102-150405")+".patch")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	change, err := steps.WritePatch(ctx, path)
	if err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("could not save the implementer's work: %v", err))
		return
	}
	if change.Empty() {
		return
	}
	status(iostream.LevelDone, fmt.Sprintf("Saved %s to %s", change.Stat, path))
	streams.ErrPrintf("  Apply it with: git apply %s\n", path)
}

func factoryLoopError(err error) error {
	if errors.Is(err, review.ErrClaudeMissing) {
		return &userError{
			msg:        "Claude Code is not installed on the factory's sidecars.",
			suggestion: "Install it in the sidecar image (see 'chunk sidecar env build') and pass --image, or set validation.sidecarImage.",
			hideDetail: true,
			err:        err,
		}
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
				status(iostream.LevelInfo, fmt.Sprintf("  [%s] %s:%d %s", f.Severity, f.File, f.Line, oneLineSummary(f.Body)))
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
