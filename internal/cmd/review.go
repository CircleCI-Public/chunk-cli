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

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/keyring"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/reviewprogress"
)

func newReviewCmd() *cobra.Command {
	var parallelism int
	var destroyPool, jsonOut, detach bool
	var orgID, image, model, chunkBinary string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "review [prompts-dir]",
		Short: "Run a directory of review prompts on a sidecar pool",
		Long: `Run each prompt file (.md or .txt) in a directory as an independent review,
each on its own sidecar synced with the working tree. The directory defaults
to .chunk/reviews in the project.

The sidecars form one pool that is reused across review passes and across
runs; pass --destroy-pool to delete it when the run ends.

With --detach the whole review runs on a primary sidecar instead of here, so
you can close your laptop: the primary runs chunk review with its own pool, and
the report is left in a run directory on it. --detach installs the latest
released chunk on the primary; pass --chunk-binary with a Linux build to use
that instead. With --detach, --image and --destroy-pool apply to the primary's
reviewer sidecars; the primary itself is kept so that 'chunk review results' can
read the report.

A prompts directory named "results" must be passed as ./results, since
'chunk review results' reads a detached review's report.`,
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
					blocked:    true,
					suggestion: suggestionRunInit,
					err:        err,
				}
			}

			rc, _ := config.Resolve("", "", insecureStorageFlag(cmd))
			// Checked before any sidecar boots: without a credential every
			// review fails, and the pool would bill for nothing.
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

			size := review.PoolSize(parallelism, len(prompts))
			if detach {
				return runReviewDetached(ctx, newDetachRequest(args, detachFlags{
					client: client, streams: streams, workDir: workDir, orgID: resolvedOrgID,
					image: image, destroyPool: destroyPool, chunkBinary: chunkBinary, rc: rc, cred: cred,
					parallelism: size, model: model, timeout: timeout, jsonOut: jsonOut,
				}))
			}
			statusFn := newStatusFunc(streams)
			statusFn(iostream.LevelStep, fmt.Sprintf("Preparing pool of %d sidecar(s) for %d prompt(s)...", size, len(prompts)))

			pool, err := newPool(ctx, client, sidecar.PoolOptions{
				Size:    size,
				Name:    review.PoolName,
				OrgID:   resolvedOrgID,
				Image:   image,
				WorkDir: workDir,
			}, "review sidecar pool", rc.CircleCITokenSource, statusFn)
			if err != nil {
				return err
			}
			defer closePool(ctx, pool, destroyPool, statusFn)

			if err := waitPoolReady(ctx, pool, "review sidecar pool"); err != nil {
				return err
			}

			activity := newReviewActivity(ctx, workDir)
			opts := review.Options{
				Credential:  cred,
				BaseURL:     rc.AnthropicBaseURL,
				Model:       model,
				Timeout:     timeout,
				ProgressFn:  activity.progress,
				OnSubmitted: activity.submitted,
			}
			var (
				results []review.Result
				passErr error
			)
			if jsonOut || ui.RequireStdoutTTY() != nil {
				statusFn(iostream.LevelStep, fmt.Sprintf("Running %d review(s)...", len(prompts)))
				opts.StatusFn = statusFn
				results, passErr = review.RunPass(ctx, pool.Acquire, pool.Release, review.ClientExec, prompts, opts)
			} else {
				results, passErr = runReviewTUI(ctx, pool, prompts, size, opts)
			}
			activity.finish(passErr)
			if errors.Is(passErr, review.ErrClaudeMissing) {
				return agentNotInstalled("the review sidecars", passErr)
			}
			if errors.Is(passErr, review.ErrCredentialRejected) {
				return credentialRejected(cred, credSource, rc.AnthropicBaseURL, passErr)
			}
			if jsonOut {
				if jsonErr := iostream.PrintJSON(streams.Out, newReviewReport(results)); jsonErr != nil {
					return fmt.Errorf("write reviews: %w", jsonErr)
				}
			} else {
				printReviews(streams, results)
			}
			if passErr != nil {
				return &userError{msg: "The review pass stopped early.", err: passErr}
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
	cmd.Flags().BoolVar(&detach, "detach", false, "run the review on a primary sidecar and return, so this machine can close")
	cmd.AddCommand(newReviewResultsCmd())
	cmd.Flags().StringVar(&chunkBinary, "chunk-binary", "", "Linux chunk build to upload with --detach (default: install the latest release)")
	return cmd
}

// newPool creates or reuses a pool, turning a rejected creation into the
// not-authorized error. what names the pool in messages.
func newPool(ctx context.Context, client *circleci.Client, opts sidecar.PoolOptions, what, tokenSource string, statusFn iostream.StatusFunc) (*sidecar.Pool, error) {
	pool, err := sidecar.NewPool(ctx, client, opts, statusFn)
	if err != nil {
		if authErr := notAuthorized("create sidecars", tokenSource, err); authErr != nil {
			return nil, authErr
		}
		return nil, &userError{msg: fmt.Sprintf("Could not prepare the %s.", what), err: err}
	}
	return pool, nil
}

// closePool ends a command's use of its pool: with destroy it deletes the
// sidecars and clears the pool's state, and otherwise keeps them for reuse.
// Keeping waits for pending creates to land in pool state; an interrupt
// cancels them instead.
func closePool(ctx context.Context, pool *sidecar.Pool, destroy bool, status iostream.StatusFunc) {
	if destroy {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if err := pool.Destroy(cleanupCtx); err != nil {
			status(iostream.LevelWarn, fmt.Sprintf("could not destroy pool: %v", err))
		}
		return
	}
	closeCtx, cancel := context.WithTimeout(ctx, poolCloseTimeout)
	defer cancel()
	pool.Close(closeCtx)
}

// agentNotInstalled explains a pass that stopped because the sidecars in where
// have no coding agent to run.
func agentNotInstalled(where string, err error) error {
	return &userError{
		msg:        fmt.Sprintf("Claude Code is not installed on %s.", where),
		suggestion: "Install it in the sidecar image (see 'chunk sidecar env build') and pass --image, or set validation.sidecarImage.",
		hideDetail: true,
		err:        err,
	}
}

// waitPoolReady blocks until the pool's sidecars are created and synced.
// sidecar.NewPool returns as soon as its members exist, while reused members
// may still be syncing in the background; waiting makes a failed sync surface
// before any work starts, rather than as one task failing partway through.
// It also keeps the pool's "Synced" line, reported from another goroutine,
// from landing among later output.
func waitPoolReady(ctx context.Context, pool *sidecar.Pool, what string) error {
	if err := pool.WaitSynced(ctx); err != nil {
		return &userError{msg: fmt.Sprintf("The %s did not become ready.", what), err: err}
	}
	return nil
}

// runReviewTUI runs the review pass with a BubbleTea progress display.
// It blocks until the pass completes or the user quits.
func runReviewTUI(ctx context.Context, pool *sidecar.Pool, prompts []review.Prompt, poolSize int, opts review.Options) ([]review.Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := reviewprogress.New(prompts, poolSize, cancel)
	prog := tea.NewProgram(m, tea.WithContext(ctx))

	// Chains rather than replaces: the caller's ProgressFn files the pass in the
	// event log, and dropping it here would make a pass run under the display
	// invisible to the dashboard.
	record := opts.ProgressFn
	opts.ProgressFn = func(e review.ProgressEvent) {
		if record != nil {
			record(e)
		}
		prog.Send(reviewprogress.ProgressMsg(e))
	}

	type passResult struct {
		results []review.Result
		err     error
	}
	doneCh := make(chan passResult, 1)

	go func() {
		r, err := review.RunPass(ctx, pool.Acquire, pool.Release, review.ClientExec, prompts, opts)
		doneCh <- passResult{r, err}
		prog.Send(reviewprogress.DoneMsg{Err: err})
	}()

	// Run also returns an error when the user quits, which cancels ctx
	// first, so only an error with ctx still live is a display failure.
	_, tuiErr := prog.Run()
	quit := ctx.Err() != nil || errors.Is(tuiErr, tea.ErrInterrupted)
	cancel()
	done := <-doneCh
	if tuiErr != nil && !quit {
		return done.results, fmt.Errorf("run review progress display: %w", tuiErr)
	}
	return done.results, done.err
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

// reviewCredential picks the credential reviews authenticate with, preferring
// an API key so nothing changes for anyone who already has one. The source is
// returned so a rejected credential can be cleared from wherever it came from.
func reviewCredential(rc config.ResolvedConfig) (review.Credential, string, error) {
	switch {
	case rc.AnthropicAPIKey != "":
		return review.Credential{EnvVar: config.EnvAnthropicAPIKey, Value: rc.AnthropicAPIKey}, rc.AnthropicAPIKeySource, nil
	case rc.ClaudeOAuthToken != "":
		return review.Credential{EnvVar: config.EnvClaudeOAuthToken, Value: rc.ClaudeOAuthToken}, rc.ClaudeOAuthTokenSource, nil
	}
	return review.Credential{}, "", &userError{
		msg:        "No Claude credential found; reviews run Claude on each sidecar.",
		suggestion: "Run 'chunk auth set anthropic-oauth' to use your Claude subscription, or 'chunk auth set anthropic' for an API key.",
		errMsg:     "no claude credential found",
		hideDetail: true,
	}
}

// credentialRejected reports a credential Anthropic refused, clearing it first
// when it was one we stored in the keychain. A credential from the environment
// is the user's to fix, so it is only named, and a key in the config file is
// pointed at rather than rewritten.
func credentialRejected(cred review.Credential, source, baseURL string, err error) error {
	if strings.HasPrefix(source, "Environment") {
		return &userError{
			msg:        fmt.Sprintf("Anthropic rejected the credential in %s.", cred.EnvVar),
			suggestion: "Replace it, or unset it to use a stored credential instead.",
			exitCode:   ExitAuthError,
			hideDetail: true,
			err:        err,
		}
	}

	if source != keyring.SourceKeychain {
		return &userError{
			msg:        fmt.Sprintf("Anthropic rejected the API key in %s.", strings.ToLower(source[:1])+source[1:]),
			suggestion: "Run 'chunk auth set anthropic --insecure-storage' to replace it, or 'chunk auth remove anthropic --insecure-storage' to remove it.",
			exitCode:   ExitAuthError,
			hideDetail: true,
			err:        err,
		}
	}

	service := keyring.ServiceAnthropic
	suggestion := "Run 'chunk auth set anthropic' to store a new key, then run the review again."
	if cred.EnvVar == config.EnvClaudeOAuthToken {
		service = keyring.ServiceAnthropicOAuth
		suggestion = "Run 'chunk auth set anthropic-oauth' to mint a new token, then run the review again."
	}
	if delErr := keyring.Delete(service(baseURL)); delErr != nil {
		return &userError{
			msg:        "Anthropic rejected the stored credential, which could not be removed.",
			suggestion: suggestion,
			exitCode:   ExitAuthError,
			err:        delErr,
		}
	}
	return &userError{
		msg:        "Anthropic rejected the stored credential, so it has been removed.",
		suggestion: suggestion,
		exitCode:   ExitAuthError,
		hideDetail: true,
		err:        err,
	}
}

// setupClaudeCredential offers to mint a subscription token when nothing is
// stored. A non-interactive run gets credErr back untouched: there is nobody to
// complete the browser flow. So does a --json run, whose stdout the setup's
// output would corrupt, and an --insecure-storage run, which cannot store the
// token.
func setupClaudeCredential(ctx context.Context, cmd *cobra.Command, streams iostream.Streams, rc config.ResolvedConfig, jsonOut bool, credErr error) error {
	if nonInteractive() || jsonOut || insecureStorageFlag(cmd) {
		return credErr
	}
	streams.ErrPrintln(ui.Warning("No Claude credential found."))
	mint, err := ui.Confirm("Mint one from your Claude subscription now?", true)
	if err != nil || !mint {
		return credErr
	}
	return authSetAnthropicOAuth(ctx, streams, rc.AnthropicBaseURL, false, false, false)
}
