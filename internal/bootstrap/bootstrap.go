// Package bootstrap sets a project up for chunk factory: it writes the
// project's .chunk/config.json, and makes and records the sidecar image
// factory's builds run on. It reports progress as events, and leaves login,
// picking the org and phrasing errors for the user to its callers.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// DefaultImage is the image a sidecar starts from when Run.Image is unset: a
// system template with Claude Code installed, since a chunk factory plan's
// build runs Claude on sidecars made from the snapshot. The provisioner has no
// alias for the newest template, so this names one and needs bumping as they
// are added.
const DefaultImage = "cimg-base:2026.09-claude"

// cleanupTimeout bounds deleting the sidecar once the run is done with it.
const cleanupTimeout = 30 * time.Second

// Step is the part of Setup an event or error belongs to.
type Step string

// The parts of Setup, in the order it does them.
const (
	StepConfig      Step = "config"      // detecting validation commands, writing .chunk/config.json
	StepEnvironment Step = "environment" // provisioning the sidecar
	StepSnapshot    Step = "snapshot"    // snapshotting and recording validation.sidecarImage
)

// Event is one line of Setup's progress.
type Event struct {
	Step  Step
	Level iostream.Level
	Text  string
}

// Steps is which parts a run does. A part already done is left out, so running
// again finishes a setup that stopped partway.
type Steps struct {
	// Config detects the validation commands and writes .chunk/config.json.
	Config bool
	// Sidecar sets up a sidecar with the project's environment, snapshots it
	// and records the snapshot as validation.sidecarImage.
	Sidecar bool
	// Skipped says why each part left out is.
	Skipped []string
}

// Empty reports whether there is nothing for the run to do.
func (s Steps) Empty() bool {
	return !s.Config && !s.Sidecar
}

// Plan is the parts a run does: Config when cfg is nil; Sidecar unless
// skipSidecar or cfg.HasSidecarImage().
func Plan(cfg *config.ProjectConfig, skipSidecar bool) Steps {
	s := Steps{Config: cfg == nil}
	if !s.Config {
		s.Skipped = append(s.Skipped, ".chunk/config.json already exists")
	}
	switch {
	case skipSidecar:
		s.Skipped = append(s.Skipped, "sidecar image: --skip-sidecar")
	case cfg.HasSidecarImage():
		s.Skipped = append(s.Skipped, "sidecar image: validation.sidecarImage is "+cfg.Validation.SidecarImage)
	default:
		s.Sidecar = true
	}
	return s
}

// Run is what Setup needs.
type Run struct {
	WorkDir string
	// Repo names the sidecar's workspace and the snapshot.
	Repo   string
	OrgID  string
	Client *circleci.Client
	// Anthropic detects commands when the toolchain is unknown. Nil: detection
	// falls back without Claude.
	Anthropic *anthropic.Client
	// Image is the image the sidecar starts from, which may be any image.
	// Empty is DefaultImage.
	Image string
	// SnapshotName names the snapshot. Empty is Repo + "-" + Now in
	// snapshotTimeLayout.
	SnapshotName string
	// Now dates the snapshot's default name. Zero is the time Setup starts.
	Now time.Time
	// SkipSidecar leaves out the sidecar image, as Plan's skipSidecar does.
	SkipSidecar bool
	// Output receives provisioning's step output. Nil discards it.
	Output io.Writer
	// OnEvent is told each line of progress. Nil drops them.
	OnEvent func(Event)
	// OnSidecar is told the attempt's sidecar ID as soon as it is created.
	OnSidecar func(id string)
	// Provision provisions the sidecar; sidecar.Provision when nil. Tests
	// set it, since the real one syncs over SSH.
	Provision func(ctx context.Context, opts sidecar.ProvisionOptions, env *envbuilder.Environment) error
}

// Result is what a Setup run made.
type Result struct {
	ConfigWritten bool
	// Image is the recorded validation.sidecarImage; "" when none was made.
	// It is set too when the snapshot was made but could not be recorded.
	Image string
}

// StepError is an error from one part of Setup; Err is the underlying error,
// kept for errors.As (circleci.StatusError, sidecar.SetupStepError, ...).
type StepError struct {
	Step Step
	Err  error
}

func (e *StepError) Error() string { return fmt.Sprintf("%s: %v", e.Step, e.Err) }
func (e *StepError) Unwrap() error { return e.Err }

// CreateSidecarError is a failure to create the sidecar.
type CreateSidecarError struct{ Err error }

func (e *CreateSidecarError) Error() string { return fmt.Sprintf("create sidecar: %v", e.Err) }
func (e *CreateSidecarError) Unwrap() error { return e.Err }

// SnapshotNameTakenError is a snapshot name the org already has. ExistingID is
// the snapshot that has it, when it is known.
type SnapshotNameTakenError struct {
	Name, ExistingID string
	Err              error
}

func (e *SnapshotNameTakenError) Error() string {
	return fmt.Sprintf("snapshot name %s is taken: %v", e.Name, e.Err)
}
func (e *SnapshotNameTakenError) Unwrap() error { return e.Err }

// SaveConfigError is a failure to write .chunk/config.json.
type SaveConfigError struct{ Err error }

func (e *SaveConfigError) Error() string { return fmt.Sprintf("write config: %v", e.Err) }
func (e *SaveConfigError) Unwrap() error { return e.Err }

// RecordImageError is a snapshot made but not recorded as
// validation.sidecarImage.
type RecordImageError struct {
	ID  string
	Err error
}

func (e *RecordImageError) Error() string {
	return fmt.Sprintf("record snapshot %s: %v", e.ID, e.Err)
}
func (e *RecordImageError) Unwrap() error { return e.Err }

// setup is one Setup run: the run, and the step its events belong to.
type setup struct {
	Run
	step Step
}

func (s *setup) status(level iostream.Level, text string) {
	if s.OnEvent != nil {
		s.OnEvent(Event{Step: s.step, Level: level, Text: text})
	}
}

func (s *setup) fail(step Step, err error) error {
	return &StepError{Step: step, Err: err}
}

// Setup does the parts Plan(cfg, r.SkipSidecar) gives for the config at
// WorkDir: it writes .chunk/config.json when there is none (keeping one that
// exists), then provisions a sidecar, snapshots it, records
// validation.sidecarImage and deletes the sidecar. Every error it returns is a
// *StepError.
func Setup(ctx context.Context, r Run) (Result, error) {
	s := &setup{Run: r, step: StepConfig}
	if s.Output == nil {
		s.Output = io.Discard
	}
	if s.Now.IsZero() {
		s.Now = time.Now()
	}
	if s.Provision == nil {
		s.Provision = sidecar.Provision
	}

	cfg, err := config.LoadProjectConfig(s.WorkDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		cfg = nil
	case err != nil:
		return Result{}, s.fail(StepConfig, err)
	}
	steps := Plan(cfg, s.SkipSidecar)

	var res Result
	if steps.Config {
		if cfg, err = s.writeConfig(ctx); err != nil {
			return res, err
		}
		res.ConfigWritten = true
	}
	if steps.Sidecar {
		res.Image, err = s.sidecarImage(ctx, cfg)
	}
	return res, err
}

// writeConfig detects what chunk init would write to .chunk/config.json and
// saves it. Init's hooks, skills and completions are for chunk validate, and
// are left to it.
func (s *setup) writeConfig(ctx context.Context) (*config.ProjectConfig, error) {
	cfg := &config.ProjectConfig{OrgID: s.OrgID}
	vcs, err := DetectVCS(ctx, s.WorkDir, s.status)
	if err != nil {
		return nil, s.fail(StepConfig, err)
	}
	cfg.VCS = vcs
	if commands, ok := DetectCommands(ctx, s.Anthropic, s.WorkDir, s.status); ok {
		cfg.Commands = commands
	}
	if err := config.SaveProjectConfig(s.WorkDir, cfg); err != nil {
		return nil, s.fail(StepConfig, &SaveConfigError{Err: err})
	}
	s.status(iostream.LevelDone, "Wrote .chunk/config.json")
	UpdateGitignore(s.WorkDir, s.status)
	return cfg, nil
}

// sidecarImage does what chunk sidecar setup and then chunk sidecar snapshot
// create do, on a sidecar of its own: it creates a sidecar, syncs the project
// to it and runs the environment's setup steps there, snapshots it and records
// the snapshot as validation.sidecarImage in cfg, which it saves. The sidecar
// is deleted afterwards, as snapshot create does, and the active sidecar is
// left alone.
//
// An environment with no setup steps has nothing to put in a snapshot, so no
// sidecar is created for it and snapshotID is empty: recording the default
// image would only send chunk validate's commands to a sidecar.
func (s *setup) sidecarImage(ctx context.Context, cfg *config.ProjectConfig) (snapshotID string, err error) {
	s.step = StepEnvironment
	env, cached, err := loadEnvironment(ctx, s.WorkDir, cfg, s.status)
	if err != nil {
		return "", s.fail(StepEnvironment, err)
	}
	if cached {
		_, _ = fmt.Fprintln(s.Output, msgUsingSavedEnvironment)
	}
	// Saved before the sidecar boots, so a setup step that fails does not
	// lose the detected environment, as with sidecar setup.
	if !cached {
		saveConfigOrWarn(s.WorkDir, cfg, s.status)
	}
	if env.ProvisioningSteps() == 0 {
		s.status(iostream.LevelWarn, "No setup steps detected, so there is no sidecar image to make; chunk factory uses the default image.")
		s.status(iostream.LevelInfo, "To set up the environment yourself, ask your agent to run the chunk-sidecar-setup skill.")
		return "", nil
	}
	vars, err := envVars(ctx, s.WorkDir)
	if err != nil {
		return "", s.fail(StepEnvironment, err)
	}
	if err := s.checkSnapshotNameFree(ctx); err != nil {
		return "", s.fail(StepSnapshot, err)
	}

	image := s.Image
	if image == "" {
		image = DefaultImage
	}
	s.status(iostream.LevelStep, fmt.Sprintf("Creating a sidecar from %s...", image))
	sc, err := sidecar.Create(ctx, s.Client, s.OrgID, randomSidecarName(), image, "")
	if err != nil {
		return "", s.fail(StepEnvironment, &CreateSidecarError{Err: err})
	}
	if s.OnSidecar != nil {
		s.OnSidecar(sc.ID)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if err := s.Client.DeleteSidecar(cleanupCtx, sc.ID); err != nil {
			s.status(iostream.LevelWarn, fmt.Sprintf("Could not delete sidecar %s: %v. Delete it with 'chunk sidecar delete', or wait for it to expire.", sc.ID, err))
		}
	}()

	err = s.Provision(ctx, sidecar.ProvisionOptions{
		Dir:       s.WorkDir,
		Client:    s.Client,
		Sidecar:   func(context.Context) (string, error) { return sc.ID, nil },
		Workspace: sidecar.DefaultWorkspace(s.Repo),
		EnvVars:   vars,
		// All of it to Output: a caller's stdout may be for a report.
		Streams: iostream.Streams{Out: s.Output, Err: s.Output},
		Status:  s.status,
	}, env)
	if err != nil {
		return "", s.fail(StepEnvironment, err)
	}

	s.step = StepSnapshot
	name := s.snapshotName()
	s.status(iostream.LevelStep, fmt.Sprintf("Snapshotting the sidecar as %s...", name))
	snap, err := s.Client.CreateSnapshot(ctx, sc.ID, name)
	if se, ok := errors.AsType[*circleci.StatusError](err); ok && se.StatusCode == http.StatusConflict {
		return "", s.fail(StepSnapshot, &SnapshotNameTakenError{Name: name, Err: err})
	}
	if err != nil {
		return "", s.fail(StepSnapshot, err)
	}

	if cfg.Validation == nil {
		cfg.Validation = &config.ValidationConfig{}
	}
	cfg.Validation.SidecarImage = snap.ID
	marked := cfg.MarkRemoteCommandsForSidecarSetup()
	if err := config.SaveProjectConfig(s.WorkDir, cfg); err != nil {
		return snap.ID, s.fail(StepSnapshot, &RecordImageError{ID: snap.ID, Err: err})
	}
	s.status(iostream.LevelDone, fmt.Sprintf("Recorded snapshot %s as validation.sidecarImage", snap.ID))
	if marked {
		// chunk validate now runs these on a sidecar, which is more than
		// chunk factory asked for, so say so as sidecar setup does.
		s.status(iostream.LevelInfo, "Marked install and gate commands as remote in .chunk/config.json")
	}
	return snap.ID, nil
}

// checkSnapshotNameFree stops a run whose SnapshotName the org already has
// before it makes a sidecar, syncs the project and runs every setup step only
// to be refused at the snapshot. The default name has the time in it and needs
// no check. Failing to list the snapshots does not stop the run: the snapshot
// call reports a taken name too.
func (s *setup) checkSnapshotNameFree(ctx context.Context) error {
	if s.SnapshotName == "" {
		return nil
	}
	snaps, err := s.Client.ListSnapshots(ctx, s.OrgID)
	if err != nil {
		return nil
	}
	for _, snap := range snaps {
		if !snap.IsSystem && snap.Name == s.SnapshotName {
			return &SnapshotNameTakenError{
				Name: s.SnapshotName, ExistingID: snap.ID,
				Err: fmt.Errorf("snapshot %s has the name %s", snap.ID, snap.Name),
			}
		}
	}
	return nil
}

// snapshotTimeLayout dates a snapshot's default name to the second, so a
// rerun on the same day does not ask for a name that is taken.
const snapshotTimeLayout = "2006-01-02-150405"

// snapshotName is the name the run's snapshot is taken with: the one asked
// for, or the repo and when the run started.
func (s *setup) snapshotName() string {
	if s.SnapshotName != "" {
		return s.SnapshotName
	}
	return s.Repo + "-" + s.Now.Format(snapshotTimeLayout)
}
