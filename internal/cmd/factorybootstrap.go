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

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/reviewhistory"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newFactoryBootstrapCmd() *cobra.Command {
	var orgID, image, model, output, since, template string
	var timeout time.Duration
	var top int
	var jsonOut, noHistory bool

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Write a first set of review prompts for this project",
		Long: `Have Claude Code read the project on a sidecar and write review prompts for
it, one file per review, in .chunk/reviews. chunk factory reviews every change
with them.

Claude reads what the project says about itself, such as AGENTS.md, CLAUDE.md,
docs, CI and linter configuration, and its code and tests. The project's
validation commands are left out of the reviews, since chunk factory runs them
on every change anyway.

When a GitHub token is set and origin is a GitHub repository, bootstrap also
fetches the review comments its most active reviewers left on its pull
requests, and Claude makes what they push back on into rules. --since and --top
choose which comments; --no-history leaves them out.

Bootstrap only reads the project, so it runs on a system image with Claude Code
installed rather than the project's own sidecar image. --image picks another;
it must have Claude Code.

Existing review prompts are never overwritten: bootstrap refuses a directory
that has any. Pass --output to write somewhere else, for example to compare
with the prompts you have. The prompts are a starting point; read them and
edit them before relying on them.

--template writes a standard set of prompts instead, such as python or
go-service, without a sidecar or Claude. It suits a new project that has
little for Claude to read yet.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)

			workDir, err := os.Getwd()
			if err != nil {
				return err
			}
			dir := output
			if dir == "" {
				dir = filepath.Join(workDir, review.DefaultDir)
			}
			// Checked before any sidecar boots, so a project that has prompts
			// costs nothing.
			if err := factory.CheckPromptsDir(dir); err != nil {
				return promptsExistError(dir, err)
			}
			if cmd.Flags().Changed("template") {
				return writeTemplate(streams, workDir, dir, template, jsonOut)
			}

			cfg, err := config.LoadProjectConfig(workDir)
			if err != nil {
				return &userError{msg: msgValidateNotConfigured, blocked: true, suggestion: suggestionRunInit, err: err}
			}
			historyOpts := reviewhistory.Options{Top: top}
			if since != "" {
				if historyOpts.Since, err = time.Parse(time.DateOnly, since); err != nil {
					return &userError{msg: fmt.Sprintf("--since must be a date such as 2026-01-31, not %q.", since), err: err}
				}
			}
			if top < 1 {
				return &userError{msg: fmt.Sprintf("--top must be at least 1, not %d.", top), errMsg: "invalid --top"}
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
				image = factory.BootstrapImage
			}

			statusFn := newStatusFunc(streams)
			// Fetched before any sidecar boots, so a GitHub failure costs
			// nothing.
			var history *reviewhistory.History
			if !noHistory {
				if history, err = fetchReviewHistory(ctx, rc, workDir, historyOpts, statusFn); err != nil {
					return err
				}
			}

			statusFn(iostream.LevelStep, "Preparing a sidecar...")
			pool, err := newPool(ctx, client, sidecar.PoolOptions{
				Size:    1,
				Name:    factory.BootstrapPoolName,
				OrgID:   resolvedOrgID,
				Image:   image,
				WorkDir: workDir,
			}, "bootstrap sidecar", rc.CircleCITokenSource, statusFn)
			if err != nil {
				return err
			}
			// Bootstrap runs once per project, so its sidecar is not kept.
			defer closePool(ctx, pool, true, statusFn)
			if err := waitPoolReady(ctx, pool, "bootstrap sidecar"); err != nil {
				return err
			}
			entry, err := pool.Acquire(ctx)
			if err != nil {
				return &userError{msg: "Could not get the bootstrap sidecar.", err: err}
			}
			defer pool.Release(entry)

			statusFn(iostream.LevelStep, fmt.Sprintf("Reading the project and writing review prompts on %s...", entry.ID))
			result, err := factory.RunBootstrap(ctx, factory.BootstrapOptions{
				Exec:       sidecar.ClientExec,
				WriteFile:  sidecar.WriteFile,
				Entry:      entry,
				Credential: cred,
				BaseURL:    rc.AnthropicBaseURL,
				Model:      model,
				Timeout:    timeout,
				Commands:   cfg.Commands,
				History:    history,
				OnActivity: func(a claudecode.Activity) {
					// Only what claude reads and runs: its passing remarks would
					// bury the answer when it lands.
					if a.Tool != "" {
						statusFn(iostream.LevelInfo, activityLine(a))
					}
				},
			})
			switch {
			case errors.Is(err, claudecode.ErrMissing):
				return &userError{
					msg:        fmt.Sprintf("Claude Code is not installed in the image %s.", image),
					suggestion: fmt.Sprintf("Omit --image to use %s, or pass an image that has Claude Code.", factory.BootstrapImage),
					hideDetail: true,
					err:        err,
				}
			case errors.Is(err, claudecode.ErrCredentialRejected):
				return credentialRejected(cred, credSource, rc.AnthropicBaseURL, err)
			case err != nil:
				return &userError{msg: "Could not write review prompts.", err: err}
			}

			paths, err := factory.WritePrompts(dir, result.Prompts)
			if errors.Is(err, factory.ErrPromptsExist) {
				return promptsExistError(dir, err)
			}
			if err != nil {
				return &userError{msg: "Could not save the review prompts.", err: err}
			}
			if result.Dropped > 0 {
				statusFn(iostream.LevelWarn, fmt.Sprintf("left out %d unusable prompt(s) from Claude's answer", result.Dropped))
			}

			if jsonOut {
				return iostream.PrintJSON(streams.Out, newBootstrapReport(workDir, paths, result, history))
			}
			statusFn(iostream.LevelDone, fmt.Sprintf("Wrote %d review prompt(s) ($%.2f)", len(paths), result.CostUSD))
			for _, p := range paths {
				streams.Println("  " + relPath(workDir, p))
			}
			if result.Summary != "" {
				streams.Println()
				streams.Println(result.Summary)
			}
			streams.Println()
			streams.Println(ui.Dim("Read and edit them before relying on them. chunk factory uses them on its next run."))
			return nil
		},
	}

	cmd.Flags().StringVar(&output, "output", "", "Directory to write the prompts to (default: .chunk/reviews)")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&image, "image", "", "Sidecar image with Claude Code installed (default: "+factory.BootstrapImage+")")
	cmd.Flags().StringVar(&model, "model", "", "Claude model (default: Claude Code's default)")
	cmd.Flags().DurationVar(&timeout, "timeout", factory.DefaultBootstrapTimeout, "max time for writing the prompts")
	cmd.Flags().BoolVar(&noHistory, "no-history", false, "Write the prompts without the team's review comments from GitHub")
	cmd.Flags().StringVar(&since, "since", "", "Use review comments from this date on, as YYYY-MM-DD (default: 90 days ago)")
	cmd.Flags().IntVar(&top, "top", reviewhistory.DefaultTop, "Use review comments from this many of the most active reviewers")
	cmd.MarkFlagsMutuallyExclusive("no-history", "since")
	cmd.MarkFlagsMutuallyExclusive("no-history", "top")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&template, "template", "", "Write a standard set of prompts instead ("+strings.Join(factory.Templates(), ", ")+")")
	// A template is written locally, so the sidecar, Claude and history flags do
	// nothing.
	for _, f := range []string{"org-id", "image", "model", "timeout", "no-history", "since", "top"} {
		cmd.MarkFlagsMutuallyExclusive("template", f)
	}
	return cmd
}

// writeTemplate writes the named standard set of review prompts to dir.
func writeTemplate(streams iostream.Streams, workDir, dir, name string, jsonOut bool) error {
	prompts, err := factory.Template(name)
	if errors.Is(err, factory.ErrUnknownTemplate) {
		return &userError{
			msg:        fmt.Sprintf("No review template is named %q.", name),
			suggestion: "Pick one of: " + strings.Join(factory.Templates(), ", ") + ".",
			hideDetail: true,
			err:        err,
		}
	}
	if err != nil {
		return &userError{msg: "Could not read the review template.", err: err}
	}
	paths, err := factory.WritePrompts(dir, prompts)
	if errors.Is(err, factory.ErrPromptsExist) {
		return promptsExistError(dir, err)
	}
	if err != nil {
		return &userError{msg: "Could not save the review prompts.", err: err}
	}

	if jsonOut {
		return iostream.PrintJSON(streams.Out, newBootstrapReport(workDir, paths, factory.Bootstrap{}, nil))
	}
	newStatusFunc(streams)(iostream.LevelDone, fmt.Sprintf("Wrote %d review prompt(s) from the %s template", len(paths), name))
	for _, p := range paths {
		streams.Println("  " + relPath(workDir, p))
	}
	streams.Println()
	streams.Println(ui.Dim("Read and edit them before relying on them. chunk factory uses them on its next run."))
	return nil
}

// fetchReviewHistory gathers the review comments on the project's GitHub
// repository. Without a GitHub token or a GitHub origin there is no history to
// fetch, which is not an error: bootstrap says so and goes on without it. A
// failed fetch is, since the user asked for history by having a token.
func fetchReviewHistory(ctx context.Context, rc config.ResolvedConfig, workDir string, opts reviewhistory.Options, statusFn iostream.StatusFunc) (*reviewhistory.History, error) {
	client, err := authprompt.ResolveGitHubClient(rc, func(msg string) { statusFn(iostream.LevelInfo, msg) })
	if errors.Is(err, authprompt.ErrNeedsAuth) {
		statusFn(iostream.LevelWarn, "No GitHub token, so the team's review comments are left out. Run 'chunk auth set github' to include them, or pass --no-history.")
		return nil, nil
	}
	if err != nil {
		return nil, &userError{msg: "Could not set up the GitHub client.", err: err}
	}
	owner, repo, err := gitremote.DetectOrgAndRepoCtx(ctx, workDir)
	if err != nil {
		statusFn(iostream.LevelWarn, "origin is not a GitHub repository, so the team's review comments are left out.")
		return nil, nil
	}
	statusFn(iostream.LevelStep, fmt.Sprintf("Fetching review comments on %s/%s...", owner, repo))
	history, err := reviewhistory.Fetch(ctx, client, owner, repo, opts)
	if err != nil {
		return nil, &userError{
			msg:        fmt.Sprintf("Could not fetch the review comments on %s/%s.", owner, repo),
			suggestion: "Check the GitHub token can read the repository, or pass --no-history to write the prompts without them.",
			err:        err,
		}
	}
	if history.Comments() == 0 {
		statusFn(iostream.LevelWarn, fmt.Sprintf("No review comments on %s/%s since %s; the prompts come from the code alone.", owner, repo, history.Since.Format(time.DateOnly)))
		return nil, nil
	}
	statusFn(iostream.LevelInfo, fmt.Sprintf("%d comments from %d reviewers", history.Comments(), len(history.Reviewers)))
	return &history, nil
}

// maxActivityDetail caps what an activity line shows of a tool's target, such
// as a long shell command, so each stays on one line.
const maxActivityDetail = 100

// activityLine is one tool call claude made, for the status stream.
func activityLine(a claudecode.Activity) string {
	detail := strings.Join(strings.Fields(a.Detail), " ")
	if r := []rune(detail); len(r) > maxActivityDetail {
		detail = string(r[:maxActivityDetail]) + "…"
	}
	return strings.TrimSpace(a.Tool + " " + detail)
}

func promptsExistError(dir string, err error) error {
	return &userError{
		msg:        fmt.Sprintf("%s already has review prompts.", dir),
		suggestion: "Bootstrap never overwrites them. Pass --output to write new prompts to another directory.",
		hideDetail: true,
		blocked:    true,
		err:        err,
	}
}

// relPath is path relative to dir when it is inside it, and path otherwise.
func relPath(dir, path string) string {
	if rel, err := filepath.Rel(dir, path); err == nil && filepath.IsLocal(rel) {
		return rel
	}
	return path
}

// bootstrapReport is the --json output of bootstrap.
type bootstrapReport struct {
	Prompts []string `json:"prompts"`
	Summary string   `json:"summary"`
	Dropped int      `json:"dropped"`
	CostUSD float64  `json:"cost_usd"`
	// ReviewComments counts the team's review comments Claude was given.
	ReviewComments int `json:"review_comments"`
}

func newBootstrapReport(workDir string, paths []string, result factory.Bootstrap, history *reviewhistory.History) bootstrapReport {
	report := bootstrapReport{Prompts: make([]string, 0, len(paths)), Summary: result.Summary, Dropped: result.Dropped, CostUSD: result.CostUSD}
	if history != nil {
		report.ReviewComments = history.Comments()
	}
	for _, p := range paths {
		report.Prompts = append(report.Prompts, relPath(workDir, p))
	}
	return report
}
