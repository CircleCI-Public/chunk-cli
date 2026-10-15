package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// The parts of setting a project up for chunk factory that bootstrap does
// besides writing review prompts: the project's config, and the sidecar image
// factory runs the implementer and the validation commands on.

// bootstrapPlan is which parts of setting the project up a bootstrap run does.
// A part already done is left out, so running bootstrap again finishes a setup
// that stopped partway.
type bootstrapPlan struct {
	// Config detects the validation commands and writes .chunk/config.json.
	Config bool
	// Sidecar sets up a sidecar with the project's environment, snapshots it
	// and records the snapshot as validation.sidecarImage.
	Sidecar bool
	// Prompts writes review prompts.
	Prompts bool
	// History gives Claude the team's review comments for the prompts.
	History bool
	// skipped says why each part left out is.
	skipped []string
}

// planBootstrap works out what a bootstrap run does. cfg is nil when the
// project has no config yet. promptsErr is from checking the prompts
// directory, which is non-nil when it has prompts already.
func planBootstrap(cfg *config.ProjectConfig, promptsErr error, skipSidecar, noHistory bool, promptsDir string) bootstrapPlan {
	p := bootstrapPlan{Config: cfg == nil, Prompts: promptsErr == nil, History: !noHistory}
	if !p.Config {
		p.skipped = append(p.skipped, ".chunk/config.json already exists")
	}
	switch {
	case skipSidecar:
		p.skipped = append(p.skipped, "sidecar image: --skip-sidecar")
	case cfg.HasSidecarImage():
		p.skipped = append(p.skipped, "sidecar image: validation.sidecarImage is "+cfg.Validation.SidecarImage)
	default:
		p.Sidecar = true
	}
	if !p.Prompts {
		p.skipped = append(p.skipped, promptsDir+" already has review prompts")
	}
	return p
}

// empty reports whether there is nothing for the run to do.
func (p bootstrapPlan) empty() bool {
	return !p.Config && !p.Sidecar && !p.Prompts
}

// printBootstrapPlan says what the run is about to do, and what it leaves out
// and why, before anything that costs time or money starts.
func printBootstrapPlan(streams iostream.Streams, p bootstrapPlan, promptsDir string) {
	streams.ErrPrintln(ui.Bold("Setting up this project for chunk factory:"))
	if p.Config {
		streams.ErrPrintln("  • Detect validation commands and write .chunk/config.json")
	}
	if p.Sidecar {
		streams.ErrPrintln("  • Set up a sidecar with the project's environment, snapshot it, and record it as validation.sidecarImage")
	}
	if p.Prompts {
		from := "the code"
		if p.History {
			from = "the code and the team's review comments"
		}
		streams.ErrPrintln(fmt.Sprintf("  • Write review prompts to %s from %s", promptsDir, from))
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
	// Image is the image the sidecar starts from, which must have Claude
	// Code: chunk factory runs Claude on sidecars made from the snapshot.
	// Empty is factory.BootstrapImage.
	Image string
	// Cfg is the project's config. The detected environment and the snapshot
	// are recorded in it, and it is saved.
	Cfg *config.ProjectConfig
	// Now dates the snapshot's name.
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
// The sidecar starts from an image with Claude Code rather than the
// provisioner's default, which has none: chunk factory's implementer and
// reviewers run Claude on sidecars made from the snapshot. Claude is checked
// for before the snapshot is taken, so an image without it is never recorded.
//
// An environment with no setup steps has nothing to put in a snapshot, so no
// sidecar is created for it and snapshotID is empty: recording the default
// image would only send chunk validate's commands to a sidecar.
func setupSidecarImage(ctx context.Context, run sidecarImageRun, status iostream.StatusFunc) (snapshotID string, err error) {
	env, cached, err := loadEnvironment(ctx, run.WorkDir, run.Cfg, false, status)
	if err != nil {
		return "", err
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

	image := run.Image
	if image == "" {
		image = factory.BootstrapImage
	}
	status(iostream.LevelStep, fmt.Sprintf("Creating a sidecar from %s...", image))
	sc, err := sidecar.Create(ctx, run.Client, run.OrgID, randomSidecarName(), image)
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
		// All of it to stderr: stdout is for the prompts bootstrap writes,
		// or its --json report.
		Streams: iostream.Streams{Out: run.Output, Err: run.Output},
		Status:  status,
	}, env)
	if err != nil {
		return "", sidecarSetupError(err, run.TokenSource)
	}

	if err := checkClaudeInstalled(ctx, run.Client, run.TokenSource, sc.ID, image); err != nil {
		return "", err
	}

	name := run.Repo + "-" + run.Now.Format(time.DateOnly)
	status(iostream.LevelStep, fmt.Sprintf("Snapshotting the sidecar as %s...", name))
	snap, err := run.Client.CreateSnapshot(ctx, sc.ID, name)
	if err != nil {
		return "", &userError{msg: "Could not snapshot the sidecar.", err: err}
	}

	if run.Cfg.Validation == nil {
		run.Cfg.Validation = &config.ValidationConfig{}
	}
	run.Cfg.Validation.SidecarImage = snap.ID
	run.Cfg.MarkRemoteCommandsForSidecarSetup()
	if err := config.SaveProjectConfig(run.WorkDir, run.Cfg); err != nil {
		return snap.ID, &userError{
			msg:        fmt.Sprintf("Created snapshot %s, but could not record it in .chunk/config.json.", snap.ID),
			suggestion: "Record it with: chunk config set validation.sidecarImage " + snap.ID,
			err:        err,
		}
	}
	status(iostream.LevelDone, fmt.Sprintf("Recorded snapshot %s as validation.sidecarImage", snap.ID))
	return snap.ID, nil
}

// checkClaudeInstalled reports whether Claude Code is installed on the
// sidecar, which chunk factory needs on every sidecar made from its snapshot.
func checkClaudeInstalled(ctx context.Context, client *circleci.Client, tokenSource, sidecarID, image string) error {
	session, err := sidecar.OpenSession(ctx, client, sidecarID, false)
	if err != nil {
		return sidecarSetupError(fmt.Errorf("open a session on the sidecar: %w", err), tokenSource)
	}
	// The sidecar's SSH server runs a command without a shell, so the script
	// gets one of its own, as the setup steps do.
	result, err := sidecar.ExecOverSSH(ctx, session, "bash -l -c "+sidecar.ShellEscape(claudecode.InstalledScript()), nil, nil)
	if err != nil {
		return &userError{msg: "Could not check for Claude Code on the sidecar.", err: err}
	}
	if result.ExitCode == claudecode.ExitMissing {
		return &userError{
			msg:        fmt.Sprintf("Claude Code is not installed in the image %s, so chunk factory could not run on a snapshot of it. Nothing was recorded.", image),
			suggestion: fmt.Sprintf("Omit --image to use %s, or pass an image that has Claude Code.", factory.BootstrapImage),
			hideDetail: true,
			err:        claudecode.ErrMissing,
		}
	}
	if result.ExitCode != 0 {
		return &userError{msg: "Could not check for Claude Code on the sidecar.", err: fmt.Errorf("check exited with status %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))}
	}
	return nil
}

// repoName names the project's repository from its git origin, or from its
// directory when it has none.
func repoName(ctx context.Context, workDir string) string {
	if repo, err := gitremote.RepoName(ctx, workDir); err == nil && repo != "" {
		return repo
	}
	return filepath.Base(workDir)
}

// bootstrapOutcome is what a bootstrap run's parts made.
type bootstrapOutcome struct {
	// snapshotID is the sidecar image recorded, if any.
	snapshotID string
	// paths are the review prompts written, if any.
	paths  []string
	result factory.Bootstrap
}

// runBootstrapParts runs the sidecar image part of a bootstrap run and then
// the review prompts part, whichever are not nil, and returns what they made.
// The sidecar image comes first: it is the slower part, the likelier to fail,
// and the one chunk factory cannot run without. Its failing does not stop the
// prompts, so a rerun has only the image left to do; it is warned of, and
// returned when the prompts succeed.
func runBootstrapParts(ctx context.Context, streams iostream.Streams, sidecarRun *sidecarImageRun, prompts *promptsRun, status iostream.StatusFunc) (bootstrapOutcome, error) {
	var out bootstrapOutcome
	var sidecarErr error
	if sidecarRun != nil {
		out.snapshotID, sidecarErr = setupSidecarImage(ctx, *sidecarRun, status)
	}
	if prompts == nil {
		return out, sidecarErr
	}
	if sidecarErr != nil {
		warnUserError(streams, "", sidecarErr)
		streams.ErrPrintln(ui.Dim("Writing the review prompts anyway; run bootstrap again to set up the sidecar image."))
	}
	if sidecarRun != nil {
		streams.ErrPrintln("")
	}
	var err error
	out.paths, out.result, err = writeBootstrapPrompts(ctx, *prompts, status)
	if err != nil {
		return out, err
	}
	return out, sidecarErr
}

// bootstrapNextStep closes a run that finished setting the project up.
func bootstrapNextStep(streams iostream.Streams, workDir string) {
	streams.ErrPrintln("")
	streams.ErrPrintln(ui.Bold("Next: ") + ui.Cyan(`chunk factory build "<what to change>"`))
	if _, err := os.Stat(filepath.Join(workDir, ".claude", "settings.json")); err != nil {
		streams.ErrPrintln(ui.Dim("  chunk init also adds the hooks and skills that run chunk validate in your agent."))
	}
}
