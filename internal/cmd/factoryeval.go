package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory/eval"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// evalPoolName names the pool eval runs on. Each case rewrites its members'
// workspaces and commits on them, so it is its own pool, deleted when the
// suite ends, rather than the review pool.
const evalPoolName = "factory-eval"

func newFactoryEvalCmd() *cobra.Command {
	var suiteDir, from, orgID, image, model, graderModel, output string
	var runs, parallelism, commits int
	var timeout, graderTimeout time.Duration
	var jsonOut, noGrade bool

	cmd := &cobra.Command{
		Use:   "eval [prompts-dir]",
		Short: "Score review prompts by reviewing past changes and grading what they find",
		Long: `Review a set of changes with a directory of review prompts, the way chunk
factory reviews a change in its first round, and score what they find. The
prompts directory defaults to .chunk/reviews.

The changes are either this repository's recent commits (--commits), each
reviewed as its diff against its parent with its message as the request, or
the cases of a suite (--suite). A suite is a directory with a suite.json
listing its cases: each a patch to this repository at a pinned revision and
the request it answers, optionally labeled with the problems a review should
find, by file and optionally by lines, or as clean.

A grader, a Claude run of its own, then judges every change: whether each
high or medium finding is a real problem that clears the bar reviews are
held to, whether it repeats another, and what the reviews missed. It is not
told which prompt reported which finding. Precision is the share of reported
problems it confirmed; coverage is the share of the problems it confirmed or
found that the reviews reported, so it measures against what the grader can
find. On labeled cases the grader is checked against the labels, and the
labels score the reviews too: recall is the share of expected problems
caught, and precision against labels is a floor, since an unlisted problem
may be a real one. --no-grade scores on labels alone.

High and medium findings are also grouped by file and nearby lines, so a
problem several prompts report counts once; overlap is how many problems
more than one prompt reported. Reviews vary between runs, so pass --runs to
review each change more than once.

The sidecars are deleted when the eval ends.`,
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)

			workDir, err := os.Getwd()
			if err != nil {
				return err
			}
			root, err := gitutil.RepoRoot(workDir)
			if err != nil {
				return &userError{msg: "chunk factory eval runs in a git repository.", err: err}
			}
			// The sidecars' copy of the repository is named from origin. Without a
			// GitHub one the pool fails after the credential and org prompts, so
			// say so before them.
			if _, _, err := gitremote.DetectOrgAndRepoCtx(ctx, root); err != nil {
				return &userError{
					msg:        "chunk factory eval needs a GitHub origin.",
					suggestion: "Point the origin remote at the project's GitHub repository.",
					blocked:    true,
					err:        err,
				}
			}
			suite, source, err := evalSuite(ctx, root, suiteDir, from, commits)
			if err != nil {
				return err
			}
			dir := filepath.Join(workDir, review.DefaultDir)
			if len(args) == 1 {
				dir = args[0]
			}
			prompts, err := review.LoadPrompts(dir)
			if err != nil {
				return &userError{msg: fmt.Sprintf("Could not read review prompts from %s.", dir), err: err}
			}

			cfg, err := config.LoadProjectConfig(workDir)
			if err != nil {
				return &userError{msg: msgValidateNotConfigured, blocked: true, suggestion: suggestionRunInit, err: err}
			}
			rc, _ := config.Resolve("", "", insecureStorageFlag(cmd))
			cred, credSource, credErr := reviewCredential(rc)
			if credErr != nil {
				if err := setupClaudeCredential(ctx, cmd, streams, rc, jsonOut, credErr); err != nil {
					return err
				}
				rc, _ = config.Resolve("", "", insecureStorageFlag(cmd))
				if cred, credSource, credErr = reviewCredential(rc); credErr != nil {
					return credErr
				}
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

			statusFn := newStatusFunc(streams)
			size := review.PoolSize(parallelism, len(prompts))
			statusFn(iostream.LevelStep, fmt.Sprintf("Preparing %d sidecar(s) for %d case(s) and %d prompt(s)...", size, len(suite.Cases), len(prompts)))
			pool, err := newPool(ctx, client, sidecar.PoolOptions{
				Size:    size,
				Name:    evalPoolName,
				OrgID:   resolvedOrgID,
				Image:   image,
				WorkDir: root,
			}, "eval sidecars", rc.CircleCITokenSource, statusFn)
			if err != nil {
				return err
			}
			defer closePool(ctx, pool, true, statusFn)
			if err := waitPoolReady(ctx, pool, "eval sidecars"); err != nil {
				return err
			}
			var entries []*sidecar.PoolEntry
			defer func() {
				for _, e := range entries {
					pool.Release(e)
				}
			}()
			for range size {
				e, err := pool.Acquire(ctx)
				if err != nil {
					return &userError{msg: "Could not get the eval sidecars.", err: err}
				}
				entries = append(entries, e)
			}

			var grade *eval.GradeOptions
			if !noGrade {
				grade = &eval.GradeOptions{Credential: cred, BaseURL: rc.AnthropicBaseURL, Model: graderModel, Timeout: graderTimeout}
			}
			results, runErr := eval.Run(ctx, eval.Options{
				Root:    root,
				Suite:   suite,
				Prompts: prompts,
				Runs:    runs,
				Entries: entries,
				Exec:    sidecar.ClientExec,
				Sync: func(ctx context.Context, e *sidecar.PoolEntry, dir string) error {
					return sidecar.RsyncSyncEphemeral(ctx, e.Client, e.ID, e.RepoPath, dir, func(iostream.Level, string) {})
				},
				Review: review.Options{Credential: cred, BaseURL: rc.AnthropicBaseURL, Model: model, Timeout: timeout},
				Grade:  grade,
				Status: statusFn,
				OnCase: func(r eval.CaseResult) { statusFn(caseLevel(r), caseLine(r)) },
			})

			names := make([]string, len(prompts))
			for i, p := range prompts {
				names[i] = p.Name
			}
			report := evalReport{Suite: source, Prompts: dir, Summary: eval.Summarize(names, results), Cases: results}
			if output != "" {
				data, err := json.MarshalIndent(report, "", "  ")
				if err == nil {
					err = os.WriteFile(output, append(data, '\n'), 0o644)
				}
				if err != nil {
					return &userError{msg: fmt.Sprintf("Could not write %s.", output), err: err}
				}
			}
			if jsonOut {
				if err := iostream.PrintJSON(streams.Out, report); err != nil {
					return fmt.Errorf("write report: %w", err)
				}
			} else {
				printEvalSummary(streams, report.Summary)
			}
			// The cases scored before the suite stopped are reported above.
			switch {
			case runErr == nil:
				return noneScored(report.Summary)
			case errors.Is(runErr, claudecode.ErrMissing):
				return agentNotInstalled("the eval sidecars", runErr)
			case errors.Is(runErr, claudecode.ErrCredentialRejected):
				return credentialRejected(cred, credSource, rc.AnthropicBaseURL, runErr)
			}
			return &userError{msg: "The suite stopped early.", err: runErr}
		},
	}

	cmd.Flags().StringVar(&suiteDir, "suite", "", "Directory of a suite of cases to review")
	cmd.Flags().IntVar(&commits, "commits", 0, "Review this many recent commits instead of a suite")
	cmd.Flags().StringVar(&from, "from", "HEAD", "Take --commits from the history of this revision")
	cmd.Flags().IntVar(&runs, "runs", 1, "times to review each case")
	cmd.Flags().IntVar(&parallelism, "parallelism", 0, "maximum number of sidecars (0: one per prompt)")
	cmd.Flags().StringVar(&output, "output", "", "Also write the full report as JSON to this file")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&image, "image", "", "Snapshot image ID (default: validation.sidecarImage from config)")
	cmd.Flags().StringVar(&model, "model", "", "Claude model for reviews (default: Claude Code's default)")
	cmd.Flags().DurationVar(&timeout, "timeout", review.DefaultTimeout, "max time for each review")
	cmd.Flags().BoolVar(&noGrade, "no-grade", false, "Score on the suite's labels alone, without the grader")
	cmd.Flags().StringVar(&graderModel, "grader-model", "", "Claude model for the grader (default: Claude Code's default)")
	cmd.Flags().DurationVar(&graderTimeout, "grader-timeout", eval.DefaultGradeTimeout, "max time for grading each case")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.MarkFlagsOneRequired("suite", "commits")
	cmd.MarkFlagsMutuallyExclusive("suite", "commits")
	cmd.MarkFlagsMutuallyExclusive("suite", "from")
	// Commits carry no labels, so without the grader nothing scores them.
	cmd.MarkFlagsMutuallyExclusive("commits", "no-grade")
	markExclusiveWith(cmd, "no-grade", "grader-model", "grader-timeout")
	return cmd
}

// evalSuite is the cases to review, from the suite in suiteDir or the commits
// most recent on from, and a name for where they came from. Every case is
// checked against the repository before any sidecar boots, so a stale one
// costs nothing.
func evalSuite(ctx context.Context, root, suiteDir, from string, commits int) (eval.Suite, string, error) {
	if suiteDir == "" {
		suite, err := eval.Commits(ctx, root, from, commits)
		if err != nil {
			return eval.Suite{}, "", &userError{msg: fmt.Sprintf("Could not make cases of the commits on %s.", from), err: err}
		}
		return suite, fmt.Sprintf("%d commit(s) on %s", len(suite.Cases), from), nil
	}
	suite, err := eval.LoadSuite(suiteDir)
	if err != nil {
		return eval.Suite{}, "", &userError{msg: fmt.Sprintf("Could not read the suite in %s.", suiteDir), err: err}
	}
	if err := suite.Check(ctx, root); err != nil {
		return eval.Suite{}, "", &userError{msg: "Some cases cannot run in this repository.", err: err}
	}
	return suite, suiteDir, nil
}

// noneScored fails a run in which every case errored: its report measures
// nothing. A run in which only some did still succeeds; the summary counts
// them.
func noneScored(s eval.Summary) error {
	if s.Runs == 0 || s.Scored > 0 {
		return nil
	}
	return &userError{
		msg:        "No case could be scored.",
		suggestion: "Every case errored; see the errors above.",
		errMsg:     fmt.Sprintf("all %d case run(s) errored", s.Runs),
	}
}

// evalReport is the --json and --output report of an eval.
type evalReport struct {
	Suite   string            `json:"suite"`
	Prompts string            `json:"prompts"`
	Summary eval.Summary      `json:"summary"`
	Cases   []eval.CaseResult `json:"cases"`
}

func caseLevel(r eval.CaseResult) iostream.Level {
	if r.Errored() {
		return iostream.LevelWarn
	}
	return iostream.LevelDone
}

// caseLine is one case run's outcome, for the status stream.
func caseLine(r eval.CaseResult) string {
	name := fmt.Sprintf("%s (run %d)", r.Case, r.Run)
	if r.Errored() {
		return fmt.Sprintf("%s: %d review(s) failed: %s: %s", name, len(r.Errors), r.Errors[0].Prompt, r.Errors[0].Error)
	}
	var line string
	switch {
	case r.Clean:
		line = fmt.Sprintf("%s: clean, %d problem(s) reported", name, len(r.Problems))
	case r.Labeled:
		line = fmt.Sprintf("%s: caught %d of %d, %d unexpected problem(s), %d repeat(s)",
			name, r.Caught(), r.Caught()+len(r.Missed), r.Unexpected(), r.Repeats())
	default:
		line = fmt.Sprintf("%s: %d problem(s) reported", name, len(r.Problems))
	}
	switch {
	case r.GradeError != "":
		line += "; grading failed: " + r.GradeError
	case r.Grade != nil:
		var confirmed int
		for _, i := range r.Grade.Problems() {
			if r.Grade.Findings[i].Verdict == eval.VerdictConfirmed {
				confirmed++
			}
		}
		line += fmt.Sprintf("; grader confirmed %d, found %d missed", confirmed, len(r.Grade.Missed))
	}
	return line
}

func printEvalSummary(streams iostream.Streams, s eval.Summary) {
	streams.Println()
	streams.Printf("Scored %d of %d case run(s)", s.Scored, s.Runs)
	if s.Errored > 0 {
		streams.Printf("; %d errored and are left out", s.Errored)
	}
	streams.Println()
	if g := s.Graded; g != nil {
		streams.Printf("Graded %d run(s)", g.Runs)
		if g.Errors > 0 {
			streams.Printf("; grading failed on %d, left out of these", g.Errors)
		}
		streams.Printf(" ($%.2f)\n", g.CostUSD)
		streams.Printf("Precision:  %s of %d problem(s) reported confirmed (%d not blocking, %d wrong)\n",
			share(g.Confirmed, g.Confirmed+g.NotBlocking+g.Wrong), g.Problems, g.NotBlocking, g.Wrong)
		streams.Printf("Coverage:   %s of %d problem(s) confirmed or found were reported (%d missed)\n",
			share(g.Confirmed, g.Confirmed+g.Missed), g.Confirmed+g.Missed, g.Missed)
		if g.Ungraded > 0 {
			streams.Printf("Ungraded:   %d finding(s) the grader gave no verdict\n", g.Ungraded)
		}
	}
	if s.Labeled > 0 {
		streams.Printf("Labeled:    %d run(s)\n", s.Labeled)
		streams.Printf("Recall:     %s of %d expected problem(s) caught\n", share(s.Caught, s.Expected), s.Expected)
		streams.Printf("Listed:     %s of %d problem(s) reported are listed (%d unexpected)\n",
			share(s.Caught, s.ReportedLabeled), s.ReportedLabeled, s.Unexpected)
		streams.Printf("Clean:      %s of %d clean run(s) with no blocking finding\n", share(s.CleanPassed, s.Clean), s.Clean)
		if g := s.Graded; g != nil {
			c := g.Calibration
			streams.Printf("Grader:     confirmed %d of %d finding(s) on expected problems, %d of %d on clean cases; found %d of %d expected problem(s) the reviews missed; %d miss(es) on clean cases\n",
				c.OnExpectedConfirmed, c.OnExpected, c.OnCleanConfirmed, c.OnClean, c.ExpectedFound, c.ExpectedMissed, c.MissedOnClean)
		}
	}
	streams.Printf("Overlap:    %d of %d problem(s) reported by more than one prompt; %d of %d blocking finding(s) repeat another\n",
		s.Shared, s.Reported, s.Repeats, s.Blocking)
	streams.Println()
	streams.Printf("%-24s %8s %9s %12s %5s %6s %10s %8s %6s %6s\n",
		"prompt", "blocking", "confirmed", "not blocking", "wrong", "caught", "unexpected", "on clean", "shared", "errors")
	for _, name := range slices.Sorted(maps.Keys(s.Prompts)) {
		p := s.Prompts[name]
		streams.Printf("%-24s %8d %9d %12d %5d %6d %10d %8d %6d %6d\n",
			name, p.Blocking, p.Confirmed, p.NotBlocking, p.Wrong, p.Caught, p.Unexpected, p.OnClean, p.Shared, p.Errors)
	}
}

// share is n of d as a percentage, or "-" when d is zero.
func share(n, d int) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", n*100/d)
}
