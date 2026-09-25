package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newReviewCmd() *cobra.Command {
	var parallelism int
	var destroyPool, jsonOut bool
	var orgID, image, model string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "review [prompts-dir]",
		Short: "Run a directory of review prompts on a sidecar pool",
		Long: `Run each prompt file (.md or .txt) in a directory as an independent review,
each on its own sidecar synced with the working tree. The directory defaults
to .chunk/reviews in the project.

The sidecars form one pool that is reused across review passes and across
runs; pass --destroy-pool to delete it when the run ends.`,
		// Hidden until passes repeat and check for convergence.
		Hidden:       true,
		SilenceUsage: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) <= 1 {
				return nil
			}
			return newUserError("Pass at most one directory of review prompts.").
				withCode("command.invalid_args").
				withSuggestion(fmt.Sprintf("Omit it to use %s, or run: chunk review ./prompts", review.DefaultDir)).
				withExitCode(ExitBadArgs).
				withoutDetail()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)

			workDir, err := os.Getwd()
			if err != nil {
				return err
			}
			dir := filepath.Join(workDir, review.DefaultDir)
			if len(args) == 1 {
				dir = args[0]
			}

			prompts, err := review.LoadPrompts(dir)
			switch {
			case errors.Is(err, review.ErrNoPrompts):
				return &userError{
					msg:        fmt.Sprintf("No prompts found in %s.", dir),
					suggestion: "Add one .md or .txt file per review.",
					err:        err,
				}
			case errors.Is(err, os.ErrNotExist) && len(args) == 0:
				return &userError{
					msg:        fmt.Sprintf("No %s directory in this project.", review.DefaultDir),
					suggestion: fmt.Sprintf("Create %s with one .md or .txt file per review, or pass a directory.", review.DefaultDir),
					hideDetail: true,
					err:        err,
				}
			case err != nil:
				return &userError{msg: fmt.Sprintf("Could not read prompts from %s.", dir), err: err}
			}

			cfg, err := config.LoadProjectConfig(workDir)
			if err != nil {
				return &userError{
					msg:        msgValidateNotConfigured,
					suggestion: "Run 'chunk init' first.",
					err:        err,
				}
			}

			rc, _ := config.Resolve("", "", insecureStorageFlag(cmd))
			// Checked before any sidecar boots: without a key every review
			// fails, and the pool would bill for nothing.
			if rc.AnthropicAPIKey == "" {
				return &userError{
					msg:        "No Anthropic API key found; reviews run Claude on each sidecar.",
					suggestion: "Run 'chunk auth set anthropic' or set ANTHROPIC_API_KEY.",
					errMsg:     "anthropic api key not found",
					hideDetail: true,
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

			size := review.PoolSize(parallelism, len(prompts))
			statusFn := newStatusFunc(streams)
			statusFn(iostream.LevelStep, fmt.Sprintf("Preparing pool of %d sidecar(s) for %d prompt(s)...", size, len(prompts)))

			pool, err := sidecar.NewPool(ctx, client, sidecar.PoolOptions{
				Size:    size,
				Name:    review.PoolName,
				OrgID:   resolvedOrgID,
				Image:   image,
				WorkDir: workDir,
			}, statusFn)
			if err != nil {
				if authErr := notAuthorized("create sidecars", rc.CircleCITokenSource, err); authErr != nil {
					return authErr
				}
				return &userError{msg: "Could not prepare the review sidecar pool.", err: err}
			}
			defer func() {
				if destroyPool {
					cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
					defer cancel()
					if err := pool.Destroy(cleanupCtx); err != nil {
						statusFn(iostream.LevelWarn, fmt.Sprintf("could not destroy pool: %v", err))
					}
					return
				}
				closeCtx, cancel := context.WithTimeout(ctx, poolCloseTimeout)
				defer cancel()
				pool.Close(closeCtx)
			}()

			// Waiting also keeps the pool's "Synced" line, reported from
			// another goroutine, from landing among the reviews' progress.
			if err := review.WaitReady(ctx, pool.WaitSynced); err != nil {
				return &userError{msg: "The review sidecar pool did not become ready.", err: err}
			}

			statusFn(iostream.LevelStep, fmt.Sprintf("Running %d review(s)...", len(prompts)))
			results, err := review.RunPass(ctx, pool.Acquire, pool.Release, review.ClientExec, prompts, review.Options{
				APIKey:   rc.AnthropicAPIKey,
				Model:    model,
				Timeout:  timeout,
				StatusFn: statusFn,
			})
			if errors.Is(err, review.ErrClaudeMissing) {
				return &userError{
					msg:        "Claude Code is not installed on the review sidecars.",
					suggestion: "Install it in the sidecar image (see 'chunk sidecar env build') and pass --image, or set validation.sidecarImage.",
					hideDetail: true,
					err:        err,
				}
			}
			if jsonOut {
				if jsonErr := iostream.PrintJSON(streams.Out, newReviewReport(results)); jsonErr != nil {
					return fmt.Errorf("write reviews: %w", jsonErr)
				}
			} else {
				printReviews(streams, results)
			}
			if err != nil {
				return &userError{msg: "The review pass stopped early.", err: err}
			}
			if failed := countFailedReviews(results); failed > 0 {
				return &userError{
					msg:        fmt.Sprintf("%d of %d review(s) failed.", failed, len(results)),
					errMsg:     "reviews failed",
					hideDetail: true,
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&parallelism, "parallelism", 5, "maximum number of sidecars (0: one per prompt)")
	cmd.Flags().BoolVar(&destroyPool, "destroy-pool", false, "delete sidecars and clear pool state after run")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&model, "model", "", "Claude model for reviews (default: Claude Code's default)")
	cmd.Flags().DurationVar(&timeout, "timeout", review.DefaultTimeout, "max time for each review")
	cmd.Flags().StringVar(&image, "image", "", "Snapshot image ID (default: validation.sidecarImage from config)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// printReviews writes each review to stdout as a markdown section, in prompt
// order, so the output of a pass reads as one document.
func printReviews(streams iostream.Streams, results []review.Result) {
	for i, r := range results {
		if i > 0 {
			streams.Println()
		}
		streams.Printf("## %s\n\n", r.Prompt)
		if r.Error != "" {
			streams.Printf("_Review failed on %s: %s_\n\n", r.SidecarID, r.Error)
		}
		if r.Output != "" {
			streams.Println(r.Output)
		}
	}
}

// reviewReport is the --json output of a review pass. It is an object rather
// than a bare list so later passes can report alongside the reviews without
// changing their shape.
type reviewReport struct {
	Reviews []reviewJSON `json:"reviews"`
	Failed  int          `json:"failed"`
}

type reviewJSON struct {
	Prompt          string  `json:"prompt"`
	SidecarID       string  `json:"sidecar_id"`
	Output          string  `json:"output"`
	Error           string  `json:"error,omitempty"`
	DurationSeconds float64 `json:"duration_seconds"`
}

func newReviewReport(results []review.Result) reviewReport {
	report := reviewReport{Reviews: make([]reviewJSON, 0, len(results)), Failed: countFailedReviews(results)}
	for _, r := range results {
		report.Reviews = append(report.Reviews, reviewJSON{
			Prompt:          r.Prompt,
			SidecarID:       r.SidecarID,
			Output:          r.Output,
			Error:           r.Error,
			DurationSeconds: r.Duration.Round(time.Millisecond).Seconds(),
		})
	}
	return report
}

func countFailedReviews(results []review.Result) int {
	n := 0
	for _, r := range results {
		if r.Error != "" {
			n++
		}
	}
	return n
}
