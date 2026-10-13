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
	var suiteDir, orgID, image, model, output string
	var runs, parallelism int
	var timeout time.Duration
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "eval [prompts-dir]",
		Short: "Score review prompts against a suite of changes with known problems",
		Long: `Review every case in a suite with a directory of review prompts, the way
chunk factory reviews a change in its first round, and score what they find
against what each case expects. The prompts directory defaults to
.chunk/reviews.

A suite is a directory with a suite.json listing its cases. Each case is a
patch to this repository at a pinned revision, the request it answers, and
the problems a review should find in it, by file and optionally by lines. A
case that expects none is clean: any high or medium finding on it would send
sound work back.

High and medium findings are grouped into the problems they are about, by
file and nearby lines, so a problem several prompts report counts once.
Recall is the share of expected problems caught. Precision is the share of
reported problems the suite lists; it is a floor, since an unlisted problem
may be a real one. Overlap is how many problems more than one prompt
reported. Reviews vary between runs, so pass --runs to review
each case more than once.

The sidecars are deleted when the suite ends.`,
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
			suite, err := eval.LoadSuite(suiteDir)
			if err != nil {
				return &userError{msg: fmt.Sprintf("Could not read the suite in %s.", suiteDir), err: err}
			}
			// Checked before any sidecar boots, so a stale case costs nothing.
			if err := suite.Check(ctx, root); err != nil {
				return &userError{msg: "Some cases cannot run in this repository.", err: err}
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
				Status: statusFn,
				OnCase: func(r eval.CaseResult) { statusFn(caseLevel(r), caseLine(r)) },
			})
			switch {
			case errors.Is(runErr, claudecode.ErrMissing):
				return agentNotInstalled("the eval sidecars", runErr)
			case errors.Is(runErr, claudecode.ErrCredentialRejected):
				return credentialRejected(cred, credSource, rc.AnthropicBaseURL, runErr)
			}

			names := make([]string, len(prompts))
			for i, p := range prompts {
				names[i] = p.Name
			}
			report := evalReport{Suite: suiteDir, Prompts: dir, Summary: eval.Summarize(names, results), Cases: results}
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
			if runErr != nil {
				return &userError{msg: "The suite stopped early.", err: runErr}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&suiteDir, "suite", "", "Directory of the suite to score against (required)")
	cmd.Flags().IntVar(&runs, "runs", 1, "times to review each case")
	cmd.Flags().IntVar(&parallelism, "parallelism", 0, "maximum number of sidecars (0: one per prompt)")
	cmd.Flags().StringVar(&output, "output", "", "Also write the full report as JSON to this file")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&image, "image", "", "Snapshot image ID (default: validation.sidecarImage from config)")
	cmd.Flags().StringVar(&model, "model", "", "Claude model for reviews (default: Claude Code's default)")
	cmd.Flags().DurationVar(&timeout, "timeout", review.DefaultTimeout, "max time for each review")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("suite")
	return cmd
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
	switch {
	case r.Errored():
		return fmt.Sprintf("%s: %d review(s) failed: %s: %s", name, len(r.Errors), r.Errors[0].Prompt, r.Errors[0].Error)
	case r.Clean:
		return fmt.Sprintf("%s: clean, %d problem(s) reported", name, len(r.Problems))
	}
	return fmt.Sprintf("%s: caught %d of %d, %d unexpected problem(s), %d repeat(s)",
		name, r.Caught(), r.Caught()+len(r.Missed), r.Unexpected(), r.Repeats())
}

func printEvalSummary(streams iostream.Streams, s eval.Summary) {
	streams.Println()
	streams.Printf("Scored %d of %d case run(s)", s.Scored, s.Runs)
	if s.Errored > 0 {
		streams.Printf("; %d errored and are left out", s.Errored)
	}
	streams.Println()
	streams.Printf("Recall:     %s of %d expected problem(s) caught\n", share(s.Caught, s.Expected), s.Expected)
	streams.Printf("Precision:  %s of %d problem(s) reported are listed (%d unexpected)\n",
		share(s.Caught, s.Reported), s.Reported, s.Unexpected)
	streams.Printf("Overlap:    %d of %d problem(s) reported by more than one prompt; %d of %d blocking finding(s) repeat another\n",
		s.Shared, s.Reported, s.Repeats, s.Blocking)
	streams.Printf("Clean:      %s of %d clean run(s) with no blocking finding\n", share(s.CleanPassed, s.Clean), s.Clean)
	streams.Println()
	streams.Printf("%-24s %8s %6s %10s %8s %6s %6s\n", "prompt", "blocking", "caught", "unexpected", "on clean", "shared", "errors")
	for _, name := range slices.Sorted(maps.Keys(s.Prompts)) {
		p := s.Prompts[name]
		streams.Printf("%-24s %8d %6d %10d %8d %6d %6d\n", name, p.Blocking, p.Caught, p.Unexpected, p.OnClean, p.Shared, p.Errors)
	}
}

// share is n of d as a percentage, or "-" when d is zero.
func share(n, d int) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", n*100/d)
}
