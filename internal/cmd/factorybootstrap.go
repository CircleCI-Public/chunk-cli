package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/bootstrap"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newFactoryBootstrapCmd() *cobra.Command {
	var orgID, image, snapshotName string
	var jsonOut, skipSidecar bool

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Set up this project for chunk factory",
		Long: `Set up the project for chunk factory: its config, and the sidecar image
factory runs on. Each part already done is left out, so running bootstrap
again finishes a setup that stopped partway.

Without .chunk/config.json, bootstrap detects the validation commands and
writes it, as chunk init does. Init's hooks and skills, which run
chunk validate in your agent, are left to chunk init.

Without validation.sidecarImage, bootstrap sets up a sidecar with the
project's environment, as chunk sidecar setup does, snapshots it, and records
the snapshot as validation.sidecarImage. The snapshot is named after the repo
and the time, as <repo>-<YYYY-MM-DD>-<HHMMSS>; --snapshot-name picks another.
--skip-sidecar leaves it out.

The sidecar starts from a system image with Claude Code installed; --image
picks another.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			streams := iostream.FromCmd(cmd)
			if len(snapshotName) > 255 {
				return &userError{msg: fmt.Sprintf("--snapshot-name must be 255 characters or fewer, not %d.", len(snapshotName)), errMsg: "invalid --snapshot-name"}
			}
			workDir, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, err := loadBootstrapConfig(workDir)
			if err != nil {
				return err
			}
			plan := bootstrap.Plan(cfg, skipSidecar)
			printBootstrapPlan(streams, plan)
			if plan.Empty() {
				return alreadySetUpError()
			}

			rc, _ := config.Resolve("", "", insecureStorageFlag(cmd))
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
			claude, _ := anthropic.New(anthropic.Config{APIKey: rc.AnthropicAPIKey, BaseURL: rc.AnthropicBaseURL})
			res, setupErr := bootstrap.Setup(ctx, bootstrap.Run{
				WorkDir: workDir, Repo: repoName(ctx, workDir), OrgID: resolvedOrgID,
				Client: client, Anthropic: claude, Image: image,
				SnapshotName: snapshotName, Now: time.Now(), SkipSidecar: skipSidecar,
				Output:  streams.Err,
				OnEvent: func(ev bootstrap.Event) { statusFn(ev.Level, ev.Text) },
			})
			err = bootstrapError(ctx, setupErr, rc.CircleCITokenSource)
			// A failure writing the config ends the run before anything is
			// made, so there is no report for it.
			if se, ok := errors.AsType[*bootstrap.StepError](setupErr); ok && se.Step == bootstrap.StepConfig {
				return err
			}
			if jsonOut {
				// What the run made is reported even when the image failed, so a
				// script sees the config it wrote.
				report := bootstrapReport{ConfigWritten: res.ConfigWritten, SidecarImage: res.Image}
				if jsonErr := iostream.PrintJSON(streams.Out, report); jsonErr != nil {
					return jsonErr
				}
				return err
			}
			if err != nil {
				return err
			}
			bootstrapNextStep(streams, workDir)
			return nil
		},
	}

	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization ID")
	cmd.Flags().StringVar(&image, "image", "", "Image the sidecar starts from (default: "+bootstrap.DefaultImage+")")
	cmd.Flags().BoolVar(&skipSidecar, "skip-sidecar", false, "Leave out setting up and recording the project's sidecar image")
	cmd.Flags().StringVar(&snapshotName, "snapshot-name", "", "Name for the sidecar image's snapshot (default: <repo>-<YYYY-MM-DD>-<HHMMSS>)")
	cmd.MarkFlagsMutuallyExclusive("skip-sidecar", "snapshot-name")
	// --image only picks the sidecar's image.
	cmd.MarkFlagsMutuallyExclusive("skip-sidecar", "image")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// bootstrapReport is the --json output of bootstrap.
type bootstrapReport struct {
	ConfigWritten bool `json:"config_written"`
	// SidecarImage is the snapshot recorded as validation.sidecarImage, or
	// empty when the run recorded none.
	SidecarImage string `json:"sidecar_image"`
}

// alreadySetUpError is the error for a project bootstrap has nothing left to
// do for.
func alreadySetUpError() error {
	return &userError{
		msg:        "This project is already set up for chunk factory.",
		suggestion: `Next: chunk factory plan "<what to change>"`,
		hideDetail: true,
		blocked:    true,
		err:        errors.New("nothing to set up"),
	}
}

// loadBootstrapConfig loads the project's config, or nil when it has none
// yet, which bootstrap writes.
func loadBootstrapConfig(workDir string) (*config.ProjectConfig, error) {
	cfg, err := config.LoadProjectConfig(workDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, configReadError(err)
	}
	return cfg, nil
}

// configReadError is the error for a .chunk/config.json bootstrap cannot read.
func configReadError(err error) error {
	return &userError{msg: "Could not read .chunk/config.json.", suggestion: "Fix it, or remove it to have bootstrap write a new one.", blocked: true, err: err}
}

// printBootstrapPlan says what the run is about to do, and what it leaves out
// and why, before anything that costs time or money starts.
func printBootstrapPlan(streams iostream.Streams, p bootstrap.Steps) {
	streams.ErrPrintln(ui.Bold("Setting up this project for chunk factory:"))
	if p.Config {
		streams.ErrPrintln("  • Detect validation commands and write .chunk/config.json")
	}
	if p.Sidecar {
		streams.ErrPrintln("  • Set up a sidecar with the project's environment, snapshot it, and record it as validation.sidecarImage")
	}
	for _, s := range p.Skipped {
		streams.ErrPrintln(ui.Dim("  Skipping: " + s))
	}
	streams.ErrPrintln("")
}

// snapshotNameTaken explains a snapshot name the org already has, with the
// ways out. existingID is the snapshot that has the name, when it is known.
func snapshotNameTaken(name, existingID string, err error) error {
	use := "find its ID with 'chunk sidecar snapshot list' and run: chunk config set validation.sidecarImage <id>"
	if existingID != "" {
		use = "run: chunk config set validation.sidecarImage " + existingID
	}
	return &userError{
		msg:        fmt.Sprintf("A snapshot named %s already exists in the org.", name),
		suggestion: "Pass --snapshot-name with another name. To use the existing one instead, " + use,
		err:        err,
	}
}

// repoName names the project's repository from its git origin, or from its
// directory when it has none.
func repoName(ctx context.Context, workDir string) string {
	if repo, err := gitremote.RepoName(ctx, workDir); err == nil && repo != "" {
		return repo
	}
	return filepath.Base(workDir)
}

// bootstrapNextStep closes a run that finished setting the project up.
func bootstrapNextStep(streams iostream.Streams, workDir string) {
	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("Next: ") + ui.Cyan(`chunk factory plan "<what to change>"`))
	if _, err := os.Stat(filepath.Join(workDir, ".claude", "skills", "chunk-sidecar")); err != nil {
		streams.ErrPrintln(ui.Dim("  chunk init also installs the skills that help your agent use chunk."))
	}
}

// bootstrapError turns an error from bootstrap.Setup into a user error that
// says what failed and what to do next. An error that is not a
// *bootstrap.StepError passes through.
func bootstrapError(ctx context.Context, err error, tokenSource string) error {
	se, ok := errors.AsType[*bootstrap.StepError](err)
	if !ok {
		return err
	}
	if e, ok := errors.AsType[*bootstrap.SnapshotNameTakenError](se.Err); ok {
		return snapshotNameTaken(e.Name, e.ExistingID, e.Err)
	}
	if e, ok := errors.AsType[*bootstrap.CreateSidecarError](se.Err); ok {
		if authErr := notAuthorized("create sidecars", tokenSource, e.Err); authErr != nil {
			return authErr
		}
		return &userError{msg: msgCouldNotCreateSidecar, suggestion: suggestionNetworkRetry, err: e.Err}
	}
	if e, ok := errors.AsType[*bootstrap.DetectEnvironmentError](se.Err); ok {
		return detectEnvironmentError(e.Err)
	}
	if e, ok := errors.AsType[*bootstrap.EnvFileError](se.Err); ok {
		return &userError{msg: fmt.Sprintf("load %s: %s", e.Path, e.Err), err: e.Err}
	}
	if e, ok := errors.AsType[*bootstrap.SecretsError](se.Err); ok {
		return secretResolveError(e.Err)
	}
	if e, ok := errors.AsType[*bootstrap.SaveConfigError](se.Err); ok {
		return &userError{
			msg:        "Could not write .chunk/config.json.",
			suggestion: suggestionCheckPerms,
			err:        fmt.Errorf("write config: %w", e.Err),
		}
	}
	if e, ok := errors.AsType[*bootstrap.RecordImageError](se.Err); ok {
		return &userError{
			msg:        fmt.Sprintf("Created snapshot %s, but could not record it in .chunk/config.json.", e.ID),
			suggestion: "Record it with: chunk config set validation.sidecarImage " + e.ID,
			err:        e.Err,
		}
	}
	switch se.Step {
	case bootstrap.StepEnvironment:
		return sidecarSetupError(ctx, se.Err, tokenSource)
	case bootstrap.StepSnapshot:
		if authErr := notAuthorized("create snapshots", tokenSource, se.Err); authErr != nil {
			return authErr
		}
		return &userError{msg: "Could not snapshot the sidecar.", err: se.Err}
	case bootstrap.StepConfig:
		// Other than saving it, the config step fails only on a cancelled ctx
		// or a config Setup could not read, which RunE reads first too.
		if ctx.Err() == nil && !errors.Is(se.Err, context.Canceled) && !errors.Is(se.Err, context.DeadlineExceeded) {
			return configReadError(se.Err)
		}
	}
	return se.Err
}
