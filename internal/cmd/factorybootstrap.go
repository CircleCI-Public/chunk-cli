package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
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
	var jsonOut, noHistory, skipSidecar bool

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Set up this project for chunk factory",
		Long: `Set up the project for chunk factory: its config, the sidecar image factory
runs on, and review prompts to check each change with. Each part already done
is left out, so running bootstrap again finishes a setup that stopped partway.

Without .chunk/config.json, bootstrap detects the validation commands and
writes it, as chunk init does. Init's hooks and skills, which run
chunk validate in your agent, are left to chunk init.

Without validation.sidecarImage, bootstrap sets up a sidecar with the
project's environment, as chunk sidecar setup does, snapshots it, and records
the snapshot as validation.sidecarImage. --skip-sidecar leaves it out.

It runs at the same time as the review prompts, which Claude Code writes on a
sidecar of its own, one file per review, in .chunk/reviews. chunk factory
reviews every change with them.

Claude reads what the project says about itself, such as AGENTS.md, CLAUDE.md,
docs, CI and linter configuration, and its code and tests. The project's
validation commands are left out of the reviews, since chunk factory runs them
on every change anyway.

When a GitHub token is set and origin is a GitHub repository, bootstrap also
fetches the review comments its most active reviewers left on its pull
requests, and Claude makes what they push back on into rules. --since and --top
choose which comments; --no-history leaves them out.

Both sidecars start from a system image with Claude Code installed rather than
the provisioner's default, which has none: the review prompts are written by
Claude, and chunk factory runs Claude on sidecars made from the snapshot. The
snapshot is not taken unless Claude Code is there. --image picks another image
for both; it must have Claude Code.

Existing review prompts are never overwritten: bootstrap leaves them out of
the run, and refuses an --output directory that has any. Pass --output to
write somewhere else, for example to compare with the prompts you have. The
prompts are a starting point; read them and edit them before relying on them.

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
			// Flags are checked before anything that can fail on the project,
			// so a bad flag is reported as one rather than behind whatever the
			// project happens to be missing.
			historyOpts, err := historyOptions(since, top)
			if err != nil {
				return err
			}
			// Checked before any sidecar boots, so a project that has prompts
			// costs nothing.
			promptsErr := factory.CheckPromptsDir(dir)
			if promptsErr != nil && (output != "" || cmd.Flags().Changed("template")) {
				return promptsExistError(dir, promptsErr)
			}
			if cmd.Flags().Changed("template") {
				return writeTemplate(streams, workDir, dir, template, jsonOut)
			}

			cfg, err := config.LoadProjectConfig(workDir)
			switch {
			case errors.Is(err, os.ErrNotExist):
				cfg = nil
			case err != nil:
				return &userError{msg: "Could not read .chunk/config.json.", suggestion: "Fix it, or remove it to have bootstrap write a new one.", blocked: true, err: err}
			}
			plan := planBootstrap(cfg, promptsErr, skipSidecar, noHistory, relPath(workDir, dir))
			if plan.empty() {
				return promptsExistError(dir, promptsErr)
			}
			// Checked before any prompt or sidecar, so a project without a
			// GitHub origin costs nothing.
			var owner, repo string
			if plan.Prompts {
				if owner, repo, err = bootstrapOrigin(ctx, workDir); err != nil {
					return err
				}
			}
			printBootstrapPlan(streams, plan, relPath(workDir, dir))

			var rc config.ResolvedConfig
			var cred claudecode.Credential
			var credSource string
			if plan.Prompts {
				if rc, cred, credSource, err = claudeCredential(ctx, cmd, streams, jsonOut); err != nil {
					return err
				}
			} else {
				rc, _ = config.Resolve("", "", insecureStorageFlag(cmd))
			}
			client, err := ensureCircleCIClient(ctx, cmd, rc, streams, ui.PromptHidden)
			if err != nil {
				return err
			}
			if orgID == "" && cfg != nil {
				orgID = cfg.OrgID
			}
			resolvedOrgID, err := resolveOrgID(orgID, workDir, orgPicker(ctx, client, rc.CircleCITokenSource, streams))
			if err != nil {
				return err
			}

			statusFn := newStatusFunc(streams)
			if plan.Config {
				if cfg, err = writeBootstrapConfig(ctx, workDir, rc, resolvedOrgID, statusFn); err != nil {
					return err
				}
			}
			// Fetched before any sidecar boots, so a GitHub failure costs
			// nothing.
			var history *reviewhistory.History
			if plan.Prompts && plan.History {
				if history, err = fetchReviewHistory(ctx, rc, owner, repo, historyOpts, statusFn); err != nil {
					return err
				}
			}

			var sidecarRun *sidecarImageRun
			if plan.Sidecar {
				sidecarRun = &sidecarImageRun{
					WorkDir:     workDir,
					Repo:        repoName(ctx, workDir),
					Client:      client,
					TokenSource: rc.CircleCITokenSource,
					OrgID:       resolvedOrgID,
					Image:       image,
					Cfg:         cfg,
					Now:         time.Now(),
				}
			}
			var prompts *promptsRun
			if plan.Prompts {
				prompts = &promptsRun{
					WorkDir:     workDir,
					Dir:         dir,
					Client:      client,
					TokenSource: rc.CircleCITokenSource,
					OrgID:       resolvedOrgID,
					Image:       image,
					Model:       model,
					Timeout:     timeout,
					Cred:        cred,
					CredSource:  credSource,
					BaseURL:     rc.AnthropicBaseURL,
					// A copy, since the sidecar part marks the commands remote
					// in cfg while the prompts are written.
					Commands: slices.Clone(cfg.Commands),
					History:  history,
				}
			}
			out, err := runBootstrapParts(ctx, streams, sidecarRun, prompts, statusFn)
			if jsonOut {
				if err != nil {
					return err
				}
				report := newBootstrapReport(workDir, out.paths, out.result, history)
				report.SidecarImage = out.snapshotID
				return iostream.PrintJSON(streams.Out, report)
			}
			printBootstrapPrompts(streams, workDir, out, statusFn)
			if err != nil {
				return err
			}
			bootstrapNextStep(streams, workDir)
			return nil
		},
	}

	cmd.Flags().StringVar(&output, "output", "", "Directory to write the prompts to (default: .chunk/reviews)")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&image, "image", "", "Image with Claude Code installed, for both sidecars (default: "+factory.BootstrapImage+")")
	cmd.Flags().StringVar(&model, "model", "", "Claude model (default: Claude Code's default)")
	cmd.Flags().DurationVar(&timeout, "timeout", factory.DefaultBootstrapTimeout, "max time for writing the prompts")
	cmd.Flags().BoolVar(&noHistory, "no-history", false, "Write the prompts without the team's review comments from GitHub")
	cmd.Flags().StringVar(&since, "since", "", "Use review comments from this date on, as YYYY-MM-DD (default: 90 days ago)")
	cmd.Flags().IntVar(&top, "top", reviewhistory.DefaultTop, "Use review comments from this many of the most active reviewers")
	cmd.MarkFlagsMutuallyExclusive("no-history", "since")
	cmd.MarkFlagsMutuallyExclusive("no-history", "top")
	cmd.Flags().BoolVar(&skipSidecar, "skip-sidecar", false, "Leave out setting up and recording the project's sidecar image")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&template, "template", "", "Write a standard set of prompts instead ("+strings.Join(factory.Templates(), ", ")+")")
	// A template is written locally, so the sidecar, Claude and history flags do
	// nothing with it.
	markExclusiveWith(cmd, "template", "org-id", "image", "model", "timeout", "no-history", "since", "top", "skip-sidecar")
	return cmd
}

// markExclusiveWith makes flag exclusive with each of others, which stay
// usable together.
func markExclusiveWith(cmd *cobra.Command, flag string, others ...string) {
	for _, f := range others {
		cmd.MarkFlagsMutuallyExclusive(flag, f)
	}
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
	printPromptPaths(streams, workDir, paths)
	streams.Println()
	streams.Println(ui.Dim(promptsReadHint))
	return nil
}

// promptsReadHint closes the output of a run that wrote review prompts.
const promptsReadHint = "Read and edit them before relying on them. chunk factory uses them on its next run."

// printPromptPaths lists the review prompts that were written.
func printPromptPaths(streams iostream.Streams, workDir string, paths []string) {
	for _, p := range paths {
		streams.Println("  " + relPath(workDir, p))
	}
}

// printBootstrapPrompts lists the review prompts a run wrote, if it wrote any,
// with Claude's summary of the project.
func printBootstrapPrompts(streams iostream.Streams, workDir string, out bootstrapOutcome, statusFn iostream.StatusFunc) {
	if out.paths == nil {
		return
	}
	streams.ErrPrintln("")
	statusFn(iostream.LevelDone, fmt.Sprintf("Wrote %d review prompt(s) ($%.2f)", len(out.paths), out.result.CostUSD))
	printPromptPaths(streams, workDir, out.paths)
	if out.result.Summary != "" {
		streams.Println()
		streams.Println(out.result.Summary)
	}
	streams.Println()
	streams.Println(ui.Dim(promptsReadHint))
}

// claudeCredential returns the resolved config and the credential Claude runs
// with on the sidecar, offering to mint one when there is none. The config is
// resolved again after minting, so it carries the new credential.
func claudeCredential(ctx context.Context, cmd *cobra.Command, streams iostream.Streams, jsonOut bool) (rc config.ResolvedConfig, cred claudecode.Credential, source string, err error) {
	rc, _ = config.Resolve("", "", insecureStorageFlag(cmd))
	cred, source, credErr := reviewCredential(rc)
	if credErr == nil {
		return rc, cred, source, nil
	}
	if err := setupClaudeCredential(ctx, cmd, streams, rc, jsonOut, credErr); err != nil {
		return rc, cred, source, err
	}
	rc, _ = config.Resolve("", "", insecureStorageFlag(cmd))
	cred, source, err = reviewCredential(rc)
	return rc, cred, source, err
}

// promptsRun is what writing review prompts on a sidecar needs. All of it is
// gathered before the sidecar boots, so a missing credential, org or history
// costs nothing.
type promptsRun struct {
	WorkDir string
	// Dir is where the prompts are written. It must have none yet.
	Dir         string
	Client      *circleci.Client
	TokenSource string
	OrgID       string
	// Image is the sidecar image, which must have Claude Code. Empty is
	// factory.BootstrapImage.
	Image      string
	Model      string
	Timeout    time.Duration
	Cred       claudecode.Credential
	CredSource string
	BaseURL    string
	// Commands are the project's validation commands, which the prompts leave
	// out since chunk factory runs them anyway.
	Commands []config.Command
	History  *reviewhistory.History
}

// writeBootstrapPrompts boots a sidecar, has Claude Code read the project on
// it and write review prompts, and saves them to run.Dir. The sidecar is
// deleted afterwards: bootstrap runs once per project.
func writeBootstrapPrompts(ctx context.Context, run promptsRun, statusFn iostream.StatusFunc) (paths []string, result factory.Bootstrap, err error) {
	image := run.Image
	if image == "" {
		image = factory.BootstrapImage
	}

	statusFn(iostream.LevelStep, "Preparing a sidecar...")
	pool, err := newPool(ctx, run.Client, sidecar.PoolOptions{
		Size:    1,
		Name:    factory.BootstrapPoolName,
		OrgID:   run.OrgID,
		Image:   image,
		WorkDir: run.WorkDir,
	}, "bootstrap sidecar", run.TokenSource, statusFn)
	if err != nil {
		return nil, result, err
	}
	defer closePool(ctx, pool, true, statusFn)
	if err := waitPoolReady(ctx, pool, "bootstrap sidecar"); err != nil {
		return nil, result, err
	}
	entry, err := pool.Acquire(ctx)
	if err != nil {
		return nil, result, &userError{msg: "Could not get the bootstrap sidecar.", err: err}
	}
	defer pool.Release(entry)

	statusFn(iostream.LevelStep, fmt.Sprintf("Reading the project and writing review prompts on %s...", entry.ID))
	result, err = factory.RunBootstrap(ctx, factory.BootstrapOptions{
		Exec:       sidecar.ClientExec,
		WriteFile:  sidecar.WriteFile,
		Entry:      entry,
		Credential: run.Cred,
		BaseURL:    run.BaseURL,
		Model:      run.Model,
		Timeout:    run.Timeout,
		Commands:   run.Commands,
		History:    run.History,
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
		return nil, result, &userError{
			msg:        fmt.Sprintf("Claude Code is not installed in the image %s.", image),
			suggestion: fmt.Sprintf("Omit --image to use %s, or pass an image that has Claude Code.", factory.BootstrapImage),
			hideDetail: true,
			err:        err,
		}
	case errors.Is(err, claudecode.ErrCredentialRejected):
		return nil, result, credentialRejected(run.Cred, run.CredSource, run.BaseURL, err)
	case err != nil:
		return nil, result, &userError{msg: "Could not write review prompts.", err: err}
	}

	paths, err = factory.WritePrompts(run.Dir, result.Prompts)
	if errors.Is(err, factory.ErrPromptsExist) {
		return nil, result, promptsExistError(run.Dir, err)
	}
	if err != nil {
		return nil, result, &userError{msg: "Could not save the review prompts.", err: err}
	}
	if result.Dropped > 0 {
		statusFn(iostream.LevelWarn, fmt.Sprintf("left out %d unusable prompt(s) from Claude's answer", result.Dropped))
	}
	return paths, result, nil
}

// historyOptions checks the --since and --top flags.
func historyOptions(since string, top int) (reviewhistory.Options, error) {
	opts := reviewhistory.Options{Top: top}
	if since != "" {
		var err error
		if opts.Since, err = time.Parse(time.DateOnly, since); err != nil {
			return opts, &userError{msg: fmt.Sprintf("--since must be a date such as 2026-01-31, not %q.", since), err: err}
		}
	}
	if top < 1 {
		return opts, &userError{msg: fmt.Sprintf("--top must be at least 1, not %d.", top), errMsg: "invalid --top"}
	}
	return opts, nil
}

// bootstrapOrigin returns the owner and name of the project's GitHub
// repository. The sidecar's copy of the repository is named from origin, so
// bootstrap needs a GitHub origin even without history.
func bootstrapOrigin(ctx context.Context, workDir string) (owner, repo string, err error) {
	owner, repo, err = gitremote.DetectOrgAndRepoCtx(ctx, workDir)
	if err != nil {
		return "", "", &userError{
			msg:        "chunk factory bootstrap needs a GitHub origin.",
			suggestion: "Point the origin remote at the project's GitHub repository.",
			blocked:    true,
			err:        err,
		}
	}
	return owner, repo, nil
}

// fetchReviewHistory gathers the review comments on the project's GitHub
// repository. Without a GitHub token there is no history to fetch, which is
// not an error: bootstrap says so and goes on without it. A failed fetch is,
// since the user asked for history by having a token.
func fetchReviewHistory(ctx context.Context, rc config.ResolvedConfig, owner, repo string, opts reviewhistory.Options, statusFn iostream.StatusFunc) (*reviewhistory.History, error) {
	client, err := authprompt.ResolveGitHubClient(rc, func(msg string) { statusFn(iostream.LevelInfo, msg) })
	if errors.Is(err, authprompt.ErrNeedsAuth) {
		statusFn(iostream.LevelWarn, "No GitHub token, so the team's review comments are left out. Run 'chunk auth set github' to include them, or pass --no-history.")
		return nil, nil
	}
	if err != nil {
		return nil, &userError{msg: "Could not set up the GitHub client.", err: err}
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
	// SidecarImage is the snapshot recorded as validation.sidecarImage, when
	// the run set one up.
	SidecarImage string `json:"sidecar_image,omitempty"`
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
