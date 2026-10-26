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
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/factory/eval"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/reviewhistory"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newFactoryBootstrapCmd() *cobra.Command {
	var orgID, image, model, output, since, template, snapshotName string
	var timeout time.Duration
	var top, evalCommits int
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
the snapshot as validation.sidecarImage. The snapshot is named after the repo
and the time, as <repo>-<YYYY-MM-DD>-<HHMMSS>; --snapshot-name picks another.
--skip-sidecar leaves it out.

Then Claude Code writes the review prompts on a sidecar of its own, one file
per review, in .chunk/reviews. chunk factory reviews every change with them.
The prompts are written even when the sidecar image could not be set up.

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
little for Claude to read yet.

--eval-commits scores the new prompts once they are written, as chunk
factory eval --commits does: each of this many recent commits is reviewed
with them on the project's sidecar image, and a grader judges what they
find. It costs far more than writing the prompts. Prompts already in
.chunk/reviews are not scored; chunk factory eval scores them. Prompts
written from the team's review comments are scored on the changes those
comments were left on, so with history the scores flatter them.`,
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
			historyOpts, err := checkBootstrapFlags(since, top, snapshotName, cmd.Flags().Changed("eval-commits"), evalCommits)
			if err != nil {
				return err
			}
			// Checked before any sidecar boots, so a project that has prompts
			// costs nothing.
			promptsExist, err := checkBootstrapPrompts(workDir, dir, output != "" || cmd.Flags().Changed("template"))
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("template") {
				return writeTemplate(streams, workDir, dir, template, jsonOut)
			}

			cfg, err := loadBootstrapConfig(workDir)
			if err != nil {
				return err
			}
			rc, _ := config.Resolve("", "", insecureStorageFlag(cmd))
			plan := planBootstrap(planInputs{
				Cfg:          cfg,
				PromptsExist: promptsExist,
				PromptsDir:   relPath(workDir, dir),
				SkipSidecar:  skipSidecar,
				NoHistory:    noHistory,
				GitHubToken:  rc.GitHubToken != "",
				EvalCommits:  evalCommits,
			})
			if plan.empty() {
				return nothingToDoError(dir, evalCommits)
			}
			// Checked before any prompt or sidecar, so a project without a
			// GitHub origin costs nothing.
			var owner, repo string
			if plan.Prompts {
				if owner, repo, err = bootstrapOrigin(ctx, workDir); err != nil {
					return err
				}
			}
			// The commits are made into cases before any prompt or sidecar, so
			// a history with none to score costs nothing.
			scoring, err := bootstrapEval(ctx, workDir, plan.Score)
			if err != nil {
				return err
			}
			printBootstrapPlan(streams, plan, relPath(workDir, dir))

			// Asked for even when the prompts exist: chunk factory build runs
			// Claude on every change, so a run that cannot is better found out
			// now than after a sidecar image has been set up for it.
			rc, cred, credSource, err := claudeCredential(ctx, cmd, streams, jsonOut)
			if err != nil {
				return err
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
					WorkDir:      workDir,
					Repo:         repoName(ctx, workDir),
					Client:       client,
					TokenSource:  rc.CircleCITokenSource,
					OrgID:        resolvedOrgID,
					Image:        image,
					Cfg:          cfg,
					SnapshotName: snapshotName,
					Now:          time.Now(),
					Output:       streams.Err,
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
					Commands:    cfg.Commands,
					History:     history,
				}
			}
			out, err := runBootstrapParts(ctx, streams, sidecarRun, prompts, statusFn)
			// Read after the parts: the sidecar image part may just have
			// recorded the image the eval runs on.
			scoring = readyScoring(scoring, out, err, evalRun{
				PromptsDir:  dir,
				Client:      client,
				TokenSource: rc.CircleCITokenSource,
				OrgID:       resolvedOrgID,
				Image:       resolveImage("", cfg),
				Credential:  cred,
				CredSource:  credSource,
				BaseURL:     rc.AnthropicBaseURL,
				Review:      review.Options{Credential: cred, BaseURL: rc.AnthropicBaseURL},
				Grade:       &eval.GradeOptions{Credential: cred, BaseURL: rc.AnthropicBaseURL},
			})
			if jsonOut {
				// A part that failed does not hide what the other made: a
				// rerun skips that part, so this is the only report of it.
				if err != nil && out.paths == nil && out.snapshotID == "" {
					return err
				}
				report := newBootstrapReport(workDir, out.paths, out.result, history)
				report.SidecarImage = out.snapshotID
				report.Eval = scorePrompts(ctx, streams, scoring, statusFn)
				if jsonErr := iostream.PrintJSON(streams.Out, report); jsonErr != nil {
					return jsonErr
				}
				return err
			}
			printBootstrapPrompts(streams, workDir, out, statusFn)
			if err != nil {
				return err
			}
			printBootstrapScore(ctx, streams, scoring, statusFn)
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
	cmd.Flags().StringVar(&snapshotName, "snapshot-name", "", "Name for the sidecar image's snapshot (default: <repo>-<YYYY-MM-DD>-<HHMMSS>)")
	cmd.MarkFlagsMutuallyExclusive("skip-sidecar", "snapshot-name")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().IntVar(&evalCommits, "eval-commits", 0, "Run factory eval on the new prompts, reviewing this many recent commits")
	// Hidden while factory eval is.
	_ = cmd.Flags().MarkHidden("eval-commits")
	cmd.Flags().StringVar(&template, "template", "", "Write a standard set of prompts instead ("+strings.Join(factory.Templates(), ", ")+")")
	// A template is written locally, so the sidecar, Claude and history flags do
	// nothing with it.
	markExclusiveWith(cmd, "template", "org-id", "image", "model", "timeout", "no-history", "since", "top", "skip-sidecar", "snapshot-name", "eval-commits")
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

// checkBootstrapPrompts reports whether dir has review prompts. When the run
// was asked to write to dir, with --output or --template, prompts there are an
// error rather than a part done. A directory that cannot be read is an error
// either way, not one taken to have prompts.
func checkBootstrapPrompts(workDir, dir string, asked bool) (exist bool, err error) {
	err = factory.CheckPromptsDir(dir)
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, factory.ErrPromptsExist):
		return false, &userError{msg: fmt.Sprintf("Could not read %s.", relPath(workDir, dir)), suggestion: suggestionCheckPerms, err: err}
	case asked:
		return true, promptsExistError(dir, err)
	}
	return true, nil
}

// loadBootstrapConfig loads the project's config, or nil when it has none
// yet, which bootstrap writes.
func loadBootstrapConfig(workDir string) (*config.ProjectConfig, error) {
	cfg, err := config.LoadProjectConfig(workDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, &userError{msg: "Could not read .chunk/config.json.", suggestion: "Fix it, or remove it to have bootstrap write a new one.", blocked: true, err: err}
	}
	return cfg, nil
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
				statusFn(iostream.LevelInfo, a.Line())
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

// checkBootstrapFlags checks the flags whose values can be wrong on their
// own, and returns the history options --since and --top make.
func checkBootstrapFlags(since string, top int, snapshotName string, evalCommitsSet bool, evalCommits int) (reviewhistory.Options, error) {
	opts, err := historyOptions(since, top)
	switch {
	case err != nil:
		return opts, err
	case len(snapshotName) > 255:
		return opts, &userError{msg: fmt.Sprintf("--snapshot-name must be 255 characters or fewer, not %d.", len(snapshotName)), errMsg: "invalid --snapshot-name"}
	case evalCommitsSet && evalCommits < 1:
		return opts, &userError{msg: fmt.Sprintf("--eval-commits must be at least 1, not %d.", evalCommits), errMsg: "invalid --eval-commits"}
	}
	return opts, nil
}

// nothingToDoError is the error for a project bootstrap has already set up.
// The plan, which says why scoring is skipped, is not shown then, so with
// --eval-commits the error says it instead.
func nothingToDoError(dir string, evalCommits int) error {
	if evalCommits == 0 {
		return promptsExistError(dir, factory.ErrPromptsExist)
	}
	return &userError{
		msg:        fmt.Sprintf("%s already has review prompts, and --eval-commits only scores the ones bootstrap writes.", dir),
		suggestion: fmt.Sprintf("Score them with chunk factory eval --commits %d.", evalCommits),
		hideDetail: true,
		blocked:    true,
		err:        factory.ErrPromptsExist,
	}
}

// bootstrapEval makes cases of the n most recent commits in the repository
// workDir is in, for scoring the prompts bootstrap writes, or returns nil
// when n is zero. The rest of the run is filled in by readyScoring.
func bootstrapEval(ctx context.Context, workDir string, n int) (*evalRun, error) {
	if n == 0 {
		return nil, nil
	}
	root, err := gitutil.RepoRoot(workDir)
	if err != nil {
		return nil, &userError{msg: "--eval-commits needs a git repository.", err: err}
	}
	suite, err := eval.Commits(ctx, root, "HEAD", n)
	if errors.Is(err, eval.ErrNoCases) {
		return nil, &userError{msg: "--eval-commits found no commits on HEAD to score the prompts with.", err: err}
	}
	if err != nil {
		return nil, &userError{msg: "--eval-commits could not read the commits on HEAD.", err: err}
	}
	return &evalRun{Root: root, Suite: suite, Source: fmt.Sprintf("%d commit(s) on HEAD", len(suite.Cases))}, nil
}

// readyScoring fills in the rest of the scoring run from run, or returns nil
// when there is nothing to score: none was asked for, or a part of bootstrap
// failed or wrote no prompts.
func readyScoring(scoring *evalRun, out bootstrapOutcome, partsErr error, run evalRun) *evalRun {
	if scoring == nil || partsErr != nil || out.paths == nil {
		return nil
	}
	run.Root, run.Suite, run.Source = scoring.Root, scoring.Suite, scoring.Source
	return &run
}

// printBootstrapScore scores the prompts bootstrap wrote, when scoring is set,
// and shows the summary.
func printBootstrapScore(ctx context.Context, streams iostream.Streams, scoring *evalRun, statusFn iostream.StatusFunc) {
	if scoring == nil {
		return
	}
	streams.ErrPrintln("")
	if report := scorePrompts(ctx, streams, scoring, statusFn); report != nil {
		printEvalSummary(streams, report.Summary)
	}
}

// scorePrompts scores the prompts bootstrap wrote, as factory eval reads them,
// or returns nil when scoring is nil. The prompts are written whatever the
// score, so a failure is a warning, and the report has whatever cases were
// scored before it.
func scorePrompts(ctx context.Context, streams iostream.Streams, scoring *evalRun, statusFn iostream.StatusFunc) *evalReport {
	if scoring == nil {
		return nil
	}
	r := *scoring
	// An environment with no setup steps records no image, and the
	// provisioner's default has no Claude Code to review with.
	if r.Image == "" {
		statusFn(iostream.LevelWarn, "Could not score the prompts: validation.sidecarImage is not set. Set it, then run chunk factory eval --commits.")
		return nil
	}
	prompts, err := review.LoadPrompts(r.PromptsDir)
	if err != nil {
		warnUserError(streams, "Could not score the prompts: ", err)
		return nil
	}
	r.Prompts = prompts
	report, err := runEval(ctx, r, statusFn)
	if err == nil && report != nil {
		err = noneScored(report.Summary)
	}
	if err != nil {
		warnUserError(streams, "Could not score the prompts: ", err)
	}
	return report
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
	// Eval scores the prompts, with --eval-commits.
	Eval *evalReport `json:"eval,omitempty"`
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
