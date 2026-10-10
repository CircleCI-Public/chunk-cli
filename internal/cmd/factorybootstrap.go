package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newFactoryBootstrapCmd() *cobra.Command {
	var orgID, image, model, output string
	var timeout time.Duration
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Write a first set of review prompts for this project",
		Long: `Have Claude Code read the project on a sidecar and write review prompts for
it, one file per review, in .chunk/reviews. chunk factory reviews every change
with them.

Claude reads what the project says about itself, such as AGENTS.md, CLAUDE.md,
docs, CI and linter configuration, and its code and tests. It also reads the
team's review standards in .chunk/context/review-prompt.md when
'chunk build-prompt' has written them. The project's validation commands are
left out of the reviews, since chunk factory runs them on every change anyway.

Bootstrap only reads the project, so it runs on a system image with Claude Code
installed rather than the project's own sidecar image. --image picks another;
it must have Claude Code.

Existing review prompts are never overwritten: bootstrap refuses a directory
that has any. Pass --output to write somewhere else, for example to compare
with the prompts you have. The prompts are a starting point; read them and
edit them before relying on them.`,
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
				image = factory.BootstrapImage
			}

			statusFn := newStatusFunc(streams)
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

			standards, err := os.ReadFile(filepath.Join(workDir, factory.StandardsPath))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return &userError{msg: fmt.Sprintf("Could not read %s.", factory.StandardsPath), err: err}
			}

			statusFn(iostream.LevelStep, fmt.Sprintf("Reading the project and writing review prompts on %s...", entry.ID))
			result, err := factory.RunBootstrap(ctx, factory.BootstrapOptions{
				Exec:       sidecar.ClientExec,
				Entry:      entry,
				Credential: cred,
				BaseURL:    rc.AnthropicBaseURL,
				Model:      model,
				Timeout:    timeout,
				Commands:   cfg.Commands,
				Standards:  string(standards),
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
				return iostream.PrintJSON(streams.Out, newBootstrapReport(workDir, paths, result))
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
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
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
}

func newBootstrapReport(workDir string, paths []string, result factory.Bootstrap) bootstrapReport {
	report := bootstrapReport{Prompts: make([]string, 0, len(paths)), Summary: result.Summary, Dropped: result.Dropped, CostUSD: result.CostUSD}
	for _, p := range paths {
		report.Prompts = append(report.Prompts, relPath(workDir, p))
	}
	return report
}
