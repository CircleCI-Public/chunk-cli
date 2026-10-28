package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// bootstrapImage is the image bootstrap's sidecar starts from unless --image
// names another: a system template with Claude Code installed, since chunk
// factory build runs Claude on sidecars made from the snapshot. The
// provisioner has no alias for the newest template, so this names one and
// needs bumping as they are added.
const bootstrapImage = "cimg-base:2026.09-claude"

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
			plan := planBootstrap(planInputs{Cfg: cfg, SkipSidecar: skipSidecar})
			printBootstrapPlan(streams, plan)
			if plan.empty() {
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
			var report bootstrapReport
			if plan.Config {
				if cfg, err = writeBootstrapConfig(ctx, workDir, rc, resolvedOrgID, statusFn); err != nil {
					return err
				}
				report.ConfigWritten = true
			}
			if plan.Sidecar {
				report.SidecarImage, err = setupSidecarImage(ctx, sidecarImageRun{
					WorkDir: workDir, Repo: repoName(ctx, workDir), Client: client,
					TokenSource: rc.CircleCITokenSource, OrgID: resolvedOrgID, Image: image,
					Cfg: cfg, SnapshotName: snapshotName, Now: time.Now(), Output: streams.Err,
				}, statusFn)
			}
			if jsonOut {
				// What the run made is reported even when the image failed, so a
				// script sees the config it wrote.
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
	cmd.Flags().StringVar(&image, "image", "", "Image the sidecar starts from (default: "+bootstrapImage+")")
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
		return nil, &userError{msg: "Could not read .chunk/config.json.", suggestion: "Fix it, or remove it to have bootstrap write a new one.", blocked: true, err: err}
	}
	return cfg, nil
}

// bootstrapPlan is which parts of setting the project up a bootstrap run does.
// A part already done is left out, so running bootstrap again finishes a setup
// that stopped partway.
type bootstrapPlan struct {
	// Config detects the validation commands and writes .chunk/config.json.
	Config bool
	// Sidecar sets up a sidecar with the project's environment, snapshots it
	// and records the snapshot as validation.sidecarImage.
	Sidecar bool
	// skipped says why each part left out is.
	skipped []string
}

// planInputs is what planBootstrap decides from. All of it is found locally,
// so the plan is shown before any prompt for a login or any sidecar.
type planInputs struct {
	// Cfg is the project's config, nil when it has none yet.
	Cfg         *config.ProjectConfig
	SkipSidecar bool
}

// planBootstrap works out what a bootstrap run does.
func planBootstrap(in planInputs) bootstrapPlan {
	p := bootstrapPlan{Config: in.Cfg == nil}
	if !p.Config {
		p.skipped = append(p.skipped, ".chunk/config.json already exists")
	}
	switch {
	case in.SkipSidecar:
		p.skipped = append(p.skipped, "sidecar image: --skip-sidecar")
	case in.Cfg.HasSidecarImage():
		p.skipped = append(p.skipped, "sidecar image: validation.sidecarImage is "+in.Cfg.Validation.SidecarImage)
	default:
		p.Sidecar = true
	}
	return p
}

// empty reports whether there is nothing for the run to do.
func (p bootstrapPlan) empty() bool {
	return !p.Config && !p.Sidecar
}

// printBootstrapPlan says what the run is about to do, and what it leaves out
// and why, before anything that costs time or money starts.
func printBootstrapPlan(streams iostream.Streams, p bootstrapPlan) {
	streams.ErrPrintln(ui.Bold("Setting up this project for chunk factory:"))
	if p.Config {
		streams.ErrPrintln("  • Detect validation commands and write .chunk/config.json")
	}
	if p.Sidecar {
		streams.ErrPrintln("  • Set up a sidecar with the project's environment, snapshot it, and record it as validation.sidecarImage")
	}
	for _, s := range p.skipped {
		streams.ErrPrintln(ui.Dim("  Skipping: " + s))
	}
	streams.ErrPrintln("")
}

// writeBootstrapConfig detects what chunk init would write to
// .chunk/config.json and saves it. Init's hooks, skills and completions are
// for chunk validate, and are left to it.
func writeBootstrapConfig(ctx context.Context, workDir string, rc config.ResolvedConfig, orgID string, status iostream.StatusFunc) (*config.ProjectConfig, error) {
	cfg := &config.ProjectConfig{OrgID: orgID}
	vcs, err := detectVCS(ctx, workDir, status)
	if err != nil {
		return nil, err
	}
	cfg.VCS = vcs
	claude, _ := anthropic.New(anthropic.Config{APIKey: rc.AnthropicAPIKey, BaseURL: rc.AnthropicBaseURL})
	if commands, ok := detectCommands(ctx, claude, workDir, status); ok {
		cfg.Commands = commands
	}
	if err := config.SaveProjectConfig(workDir, cfg); err != nil {
		return nil, &userError{
			msg:        "Could not write .chunk/config.json.",
			suggestion: suggestionCheckPerms,
			err:        fmt.Errorf("write config: %w", err),
		}
	}
	status(iostream.LevelDone, "Wrote .chunk/config.json")
	updateGitignore(workDir, status)
	return cfg, nil
}

// sidecarImageRun is what setting up the project's sidecar image needs.
type sidecarImageRun struct {
	WorkDir string
	// Repo names the sidecar's workspace and the snapshot.
	Repo        string
	Client      *circleci.Client
	TokenSource string
	OrgID       string
	// Image is the image the sidecar starts from, which may be any image.
	// Empty is bootstrapImage.
	Image string
	// Cfg is the project's config. The detected environment and the snapshot
	// are recorded in it, and it is saved.
	Cfg *config.ProjectConfig
	// SnapshotName names the snapshot. Empty is snapshotName's default.
	SnapshotName string
	// Now dates the snapshot's default name.
	Now time.Time
	// Output receives the setup steps' output.
	Output io.Writer
}

// setupSidecarImage does what chunk sidecar setup and then chunk sidecar
// snapshot create do, on a sidecar of its own: it creates a sidecar, syncs the
// project to it and runs the environment's setup steps there, snapshots it and
// records the snapshot as validation.sidecarImage. The sidecar is deleted
// afterwards, as snapshot create does, and the active sidecar is left alone.
//
// The sidecar starts from run.Image, or bootstrapImage when it is empty.
//
// An environment with no setup steps has nothing to put in a snapshot, so no
// sidecar is created for it and snapshotID is empty: recording the default
// image would only send chunk validate's commands to a sidecar.
func setupSidecarImage(ctx context.Context, run sidecarImageRun, status iostream.StatusFunc) (snapshotID string, err error) {
	env, cached, err := loadEnvironment(ctx, run.WorkDir, run.Cfg, false, status)
	if err != nil {
		return "", err
	}
	if cached {
		_, _ = fmt.Fprintln(run.Output, msgUsingSavedEnvironment)
	}
	// Saved before the sidecar boots, so a setup step that fails does not
	// lose the detected environment, as with sidecar setup.
	if !cached {
		saveConfigOrWarn(run.WorkDir, run.Cfg, status)
	}
	if env.ProvisioningSteps() == 0 {
		status(iostream.LevelWarn, "No setup steps detected, so there is no sidecar image to make; chunk factory uses the default image.")
		status(iostream.LevelInfo, "To set up the environment yourself, ask your agent to run the chunk-sidecar-setup skill.")
		return "", nil
	}
	envVars, err := resolveEnvVars(ctx, run.WorkDir, defaultEnvFile, nil)
	if err != nil {
		return "", err
	}
	if err := checkSnapshotNameFree(ctx, run); err != nil {
		return "", err
	}

	image := run.Image
	if image == "" {
		image = bootstrapImage
	}
	status(iostream.LevelStep, fmt.Sprintf("Creating a sidecar from %s...", image))
	sc, err := sidecar.Create(ctx, run.Client, run.OrgID, randomSidecarName(), image, "")
	if err != nil {
		if authErr := notAuthorized("create sidecars", run.TokenSource, err); authErr != nil {
			return "", authErr
		}
		return "", &userError{msg: msgCouldNotCreateSidecar, suggestion: suggestionNetworkRetry, err: err}
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if err := run.Client.DeleteSidecar(cleanupCtx, sc.ID); err != nil {
			status(iostream.LevelWarn, fmt.Sprintf("Could not delete sidecar %s: %v. Delete it with 'chunk sidecar delete', or wait for it to expire.", sc.ID, err))
		}
	}()

	err = sidecar.Provision(ctx, sidecar.ProvisionOptions{
		Dir:       run.WorkDir,
		Client:    run.Client,
		Sidecar:   func(context.Context) (string, error) { return sc.ID, nil },
		Workspace: sidecar.DefaultWorkspace(run.Repo),
		EnvVars:   envVars,
		// All of it to stderr: stdout is for bootstrap's --json report.
		Streams: iostream.Streams{Out: run.Output, Err: run.Output},
		Status:  status,
	}, env)
	if err != nil {
		return "", sidecarSetupError(ctx, err, run.TokenSource)
	}

	name := snapshotName(run)
	status(iostream.LevelStep, fmt.Sprintf("Snapshotting the sidecar as %s...", name))
	snap, err := run.Client.CreateSnapshot(ctx, sc.ID, name)
	if se, ok := errors.AsType[*circleci.StatusError](err); ok && se.StatusCode == http.StatusConflict {
		return "", snapshotNameTaken(name, "", err)
	}
	if err != nil {
		if authErr := notAuthorized("create snapshots", run.TokenSource, err); authErr != nil {
			return "", authErr
		}
		return "", &userError{msg: "Could not snapshot the sidecar.", err: err}
	}

	if run.Cfg.Validation == nil {
		run.Cfg.Validation = &config.ValidationConfig{}
	}
	run.Cfg.Validation.SidecarImage = snap.ID
	marked := run.Cfg.MarkRemoteCommandsForSidecarSetup()
	if err := config.SaveProjectConfig(run.WorkDir, run.Cfg); err != nil {
		return snap.ID, &userError{
			msg:        fmt.Sprintf("Created snapshot %s, but could not record it in .chunk/config.json.", snap.ID),
			suggestion: "Record it with: chunk config set validation.sidecarImage " + snap.ID,
			err:        err,
		}
	}
	status(iostream.LevelDone, fmt.Sprintf("Recorded snapshot %s as validation.sidecarImage", snap.ID))
	if marked {
		// chunk validate now runs these on a sidecar, which is more than
		// chunk factory asked for, so say so as sidecar setup does.
		status(iostream.LevelInfo, "Marked install and gate commands as remote in .chunk/config.json")
	}
	return snap.ID, nil
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

// checkSnapshotNameFree stops a run whose --snapshot-name the org already has
// before it makes a sidecar, syncs the project and runs every setup step only
// to be refused at the snapshot. The default name has the time in it and needs
// no check. Failing to list the snapshots does not stop the run: the snapshot
// call reports a taken name too.
func checkSnapshotNameFree(ctx context.Context, run sidecarImageRun) error {
	if run.SnapshotName == "" {
		return nil
	}
	snaps, err := run.Client.ListSnapshots(ctx, run.OrgID)
	if err != nil {
		return nil
	}
	for _, s := range snaps {
		if !s.IsSystem && s.Name == run.SnapshotName {
			return snapshotNameTaken(run.SnapshotName, s.ID, fmt.Errorf("snapshot %s has the name %s", s.ID, s.Name))
		}
	}
	return nil
}

// snapshotTimeLayout dates a snapshot's default name to the second, so a
// rerun on the same day does not ask for a name that is taken.
const snapshotTimeLayout = "2006-01-02-150405"

// snapshotName is the name the run's snapshot is taken with: the one asked
// for, or the repo and when the run started.
func snapshotName(run sidecarImageRun) string {
	if run.SnapshotName != "" {
		return run.SnapshotName
	}
	return run.Repo + "-" + run.Now.Format(snapshotTimeLayout)
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
	if _, err := os.Stat(filepath.Join(workDir, ".claude", "settings.json")); err != nil {
		streams.ErrPrintln(ui.Dim("  chunk init also adds the hooks and skills that run chunk validate in your agent."))
	}
}
