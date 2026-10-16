package sidecar

import (
	"context"
	"errors"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/envspec"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// Errors from the parts of Provision that do not reach the sidecar's setup
// steps, so a caller can say which part failed.
var (
	ErrSSHKey = errors.New("prepare SSH key")
	ErrSync   = errors.New("sync files to the sidecar")
)

// SetupStepError is a setup step that ran on the sidecar and failed.
type SetupStepError struct {
	Step     string
	ExitCode int
}

func (e *SetupStepError) Error() string {
	return fmt.Sprintf("setup step %q exited with status %d", e.Step, e.ExitCode)
}

// ProvisionOptions are the inputs to Provision.
type ProvisionOptions struct {
	Dir    string
	Client *circleci.Client
	// Sidecar returns the ID of the sidecar to set up, creating one if need
	// be.
	Sidecar func(context.Context) (string, error)
	// SkipSync leaves the sidecar's copy of the project as it is.
	SkipSync bool
	// Workspace is where the project is synced to and its setup steps run,
	// for a sidecar that is not the active one. Empty uses the active
	// sidecar's workspace, and saves the one the sync picks there.
	Workspace string
	// EnvVars are set for each setup step.
	EnvVars map[string]string
	// Streams receives the setup steps' output.
	Streams iostream.Streams
	Status  iostream.StatusFunc
}

// Provision gets a sidecar ready for the project: it syncs the project to it
// and runs env's setup steps there.
func Provision(ctx context.Context, opts ProvisionOptions, env *envbuilder.Environment) error {
	sidecarID, err := opts.Sidecar(ctx)
	if err != nil {
		return err
	}

	if err := ensureSSHKey(opts.Status); err != nil {
		return err
	}

	ws := opts.Workspace
	if opts.SkipSync {
		if ws == "" {
			ws = savedWorkspace(ctx, sidecarID)
		}
	} else {
		opts.Status(iostream.LevelStep, "Syncing files to sidecar...")
		// A given workspace is another sidecar's, so the sync must not touch
		// the active sidecar file.
		ws, err = rsyncTo(ctx, opts.Client, sidecarID, opts.Workspace, opts.Dir, opts.Workspace == "", false, opts.Status)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrSync, err)
		}
	}

	return runSetupSteps(ctx, opts, sidecarID, ws, env)
}

// savedWorkspace is the workspace saved for sidecarID by an earlier sync, or
// "" when there is none. Only the active sidecar has one saved, so any other
// sidecar has none.
func savedWorkspace(ctx context.Context, sidecarID string) string {
	active, err := LoadActive(ctx)
	if err != nil || active.ID() != sidecarID {
		return ""
	}
	return active.Workspace
}

func ensureSSHKey(status iostream.StatusFunc) error {
	keyPath, err := DefaultKeyPath()
	if err != nil {
		return fmt.Errorf("%w: determine key path: %w", ErrSSHKey, err)
	}
	generated, err := EnsureKeyPair(keyPath)
	if err != nil {
		return fmt.Errorf("%w: generate key: %w", ErrSSHKey, err)
	}
	if generated {
		status(iostream.LevelDone, "Generated SSH key at "+keyPath)
	}
	return nil
}

// runSetupSteps runs env's setup steps on the sidecar over SSH, one session
// each, stopping at the first that fails. They run in ws, the synced
// workspace, unless it is "".
func runSetupSteps(ctx context.Context, opts ProvisionOptions, sidecarID, ws string, env *envbuilder.Environment) error {
	if len(env.Setup) == 0 {
		opts.Status(iostream.LevelInfo, "No setup steps detected, skipping")
		return nil
	}

	for _, step := range env.Setup {
		// A freshly detected env still carries the test step (it feeds the
		// Dockerfile CMD); only the saved copy has it stripped. Skip it here so
		// setup never runs the project's test suite as a provisioning step.
		if step.Name == envspec.StepTest {
			continue
		}
		opts.Status(iostream.LevelStep, fmt.Sprintf("Running setup step %q: %s", step.Name, step.Command))
		session, err := OpenSession(ctx, opts.Client, sidecarID, false)
		if err != nil {
			return fmt.Errorf("open a session on the sidecar: %w", err)
		}
		// Run from the synced workspace directory so package managers can
		// find their manifest files (go.mod, package.json, etc.).
		workspaceCmd := step.Command
		if ws != "" {
			workspaceCmd = "cd " + ShellEscape(ws) + " && " + step.Command
		}
		// cimg images set PATH via Docker ENV which e2b does not propagate to SSH
		// sessions, so prepend the stack's binary locations explicitly.
		if paths := env.BinaryPaths(); paths != "" {
			workspaceCmd = "export PATH=" + paths + ":$PATH && " + workspaceCmd
		}
		loginCmd := "bash -l -c " + ShellEscape(workspaceCmd)
		result, err := ExecOverSSH(ctx, session, loginCmd, nil, opts.EnvVars)
		if err != nil {
			return fmt.Errorf("run setup step %q: %w", step.Name, err)
		}
		if result.Stdout != "" {
			opts.Streams.Printf("%s", result.Stdout)
		}
		if result.Stderr != "" {
			opts.Streams.ErrPrintf("%s", result.Stderr)
		}
		if result.ExitCode != 0 {
			return &SetupStepError{Step: step.Name, ExitCode: result.ExitCode}
		}
		opts.Status(iostream.LevelDone, fmt.Sprintf("Step %q complete", step.Name))
	}
	return nil
}
