package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/bootstrap"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func TestFactoryBootstrapIsRegisteredUnderFactory(t *testing.T) {
	cmd, _, err := newFactoryCmd().Find([]string{"bootstrap"})
	assert.NilError(t, err)
	assert.Equal(t, cmd.Name(), "bootstrap")
}

// Each of these flags is an unknown flag to bootstrap.
func TestFactoryBootstrapRemovedFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--output", "x"}, {"--model", "x"}, {"--timeout", "1m"}, {"--no-history"},
		{"--since", "2026-01-01"}, {"--top", "1"}, {"--template", "python"}, {"--eval-commits", "1"},
	} {
		t.Run(args[0], func(t *testing.T) {
			t.Chdir(t.TempDir())
			cmd := newFactoryBootstrapCmd()
			cmd.SetArgs(args)
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			err := cmd.Execute()
			assert.Check(t, cmp.ErrorContains(err, "unknown flag: "+args[0]))
		})
	}
}

// --image and --snapshot-name only say how to make the sidecar image, which
// --skip-sidecar leaves out.
func TestFactoryBootstrapSkipSidecarExcludesTheImageFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		flag string
	}{
		{
			name: "--image",
			args: []string{"--skip-sidecar", "--image", "x"},
			flag: "image",
		},
		{
			name: "--snapshot-name",
			args: []string{"--skip-sidecar", "--snapshot-name", "x"},
			flag: "snapshot-name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			cmd := newFactoryBootstrapCmd()
			cmd.SetArgs(tt.args)
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			err := cmd.Execute()
			assert.Check(t, cmp.ErrorContains(err, "skip-sidecar"))
			assert.Check(t, cmp.ErrorContains(err, tt.flag))
		})
	}
}

// Each error Setup returns reads as a user error that says what failed and
// what to do about it.
func TestBootstrapError(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name           string
		err            error
		wantMsg        string
		wantSuggestion string
	}{
		{
			name: "a setup step that fails",
			err: &bootstrap.StepError{
				Step: bootstrap.StepEnvironment,
				Err: &sidecar.SetupStepError{
					Step:     "install",
					ExitCode: 2,
				},
			},
			wantMsg:        `Setup step "install" exited with status 2.`,
			wantSuggestion: "Check the output above for details.",
		},
		{
			name: "a sidecar the token may not create",
			err: &bootstrap.StepError{
				Step: bootstrap.StepEnvironment,
				Err: &bootstrap.CreateSidecarError{
					Err: fmt.Errorf("create sidecar: %w", circleci.ErrNotAuthorized),
				},
			},
			wantMsg:        "Not authorized to create sidecars.",
			wantSuggestion: suggestionReauth,
		},
		{
			name: "a sidecar the network would not create",
			err: &bootstrap.StepError{
				Step: bootstrap.StepEnvironment,
				Err: &bootstrap.CreateSidecarError{
					Err: errors.New("dial tcp: connection refused"),
				},
			},
			wantMsg:        msgCouldNotCreateSidecar,
			wantSuggestion: suggestionNetworkRetry,
		},
		{
			name: "a snapshot name the org has",
			err: &bootstrap.StepError{
				Step: bootstrap.StepSnapshot,
				Err: &bootstrap.SnapshotNameTakenError{
					Name:       "n",
					ExistingID: "s1",
					Err:        errors.New("taken"),
				},
			},
			wantMsg:        "A snapshot named n already exists in the org.",
			wantSuggestion: "Pass --snapshot-name with another name. To use the existing one instead, run: chunk config set validation.sidecarImage s1",
		},
		{
			name: "a snapshot the token may not create",
			err: &bootstrap.StepError{
				Step: bootstrap.StepSnapshot,
				Err:  fmt.Errorf("create snapshot: %w", circleci.ErrNotAuthorized),
			},
			wantMsg:        "Not authorized to create snapshots.",
			wantSuggestion: suggestionReauth,
		},
		{
			name: "a config that cannot be written",
			err: &bootstrap.StepError{
				Step: bootstrap.StepConfig,
				Err:  &bootstrap.SaveConfigError{Err: os.ErrPermission},
			},
			wantMsg:        "Could not write .chunk/config.json.",
			wantSuggestion: suggestionCheckPerms,
		},
		{
			name: "an image that cannot be recorded",
			err: &bootstrap.StepError{
				Step: bootstrap.StepSnapshot,
				Err: &bootstrap.RecordImageError{
					ID:  "snap-1",
					Err: os.ErrPermission,
				},
			},
			wantMsg:        "Created snapshot snap-1, but could not record it in .chunk/config.json.",
			wantSuggestion: "Record it with: chunk config set validation.sidecarImage snap-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ue, ok := errors.AsType[*userError](bootstrapError(ctx, tt.err, ""))
			assert.Assert(t, ok)
			assert.Check(t, cmp.Equal(ue.msg, tt.wantMsg))
			assert.Check(t, cmp.Equal(ue.suggestion, tt.wantSuggestion))
		})
	}
}

// The skills hint shows until chunk init has installed chunk's skills, and an
// agent settings file is no sign that it has.
func TestBootstrapNextStepSkillsHint(t *testing.T) {
	ui.SetColorEnabled(false)
	t.Cleanup(func() { ui.SetColorEnabled(true) })
	const hint = "  chunk init also installs the skills that help your agent use chunk.\n"
	tests := []struct {
		name     string
		setup    func(t *testing.T, dir string)
		wantHint bool
	}{
		{
			name: "only an agent settings file",
			setup: func(t *testing.T, dir string) {
				err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
				assert.NilError(t, err)
				err = os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte("{}\n"), 0o644)
				assert.NilError(t, err)
			},
			wantHint: true,
		},
		{
			name: "chunk's skills installed",
			setup: func(t *testing.T, dir string) {
				err := os.MkdirAll(filepath.Join(dir, ".claude", "skills", "chunk-sidecar"), 0o755)
				assert.NilError(t, err)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			streams, out, errOut := testStreams()

			bootstrapNextStep(streams, dir)

			assert.Check(t, cmp.Equal(out.String(), ""))
			assert.Check(t, cmp.Equal(strings.HasSuffix(errOut.String(), hint), tt.wantHint), errOut.String())
			assert.Check(t, cmp.Contains(errOut.String(), `Next: chunk factory plan "<what to change>"`))
		})
	}
}
