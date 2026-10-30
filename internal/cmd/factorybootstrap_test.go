package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
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

func TestPlanBootstrap(t *testing.T) {
	withImage := &config.ProjectConfig{Validation: &config.ValidationConfig{SidecarImage: "snap-1"}}
	tests := []struct {
		name        string
		in          planInputs
		want        bootstrapPlan
		wantSkipped []string
		empty       bool
	}{
		{
			name: "a new project gets everything",
			in:   planInputs{},
			want: bootstrapPlan{Config: true, Sidecar: true},
		},
		{
			name:        "an existing config is kept",
			in:          planInputs{Cfg: &config.ProjectConfig{}},
			want:        bootstrapPlan{Sidecar: true},
			wantSkipped: []string{".chunk/config.json already exists"},
		},
		{
			name:        "--skip-sidecar leaves the image out",
			in:          planInputs{SkipSidecar: true},
			want:        bootstrapPlan{Config: true},
			wantSkipped: []string{"sidecar image: --skip-sidecar"},
		},
		{
			name:        "a config and --skip-sidecar leave nothing to do",
			in:          planInputs{Cfg: &config.ProjectConfig{}, SkipSidecar: true},
			empty:       true,
			wantSkipped: []string{".chunk/config.json already exists", "sidecar image: --skip-sidecar"},
		},
		{
			name:        "a project set up already has nothing to do",
			in:          planInputs{Cfg: withImage},
			empty:       true,
			wantSkipped: []string{".chunk/config.json already exists", "sidecar image: validation.sidecarImage is snap-1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planBootstrap(tt.in)
			assert.Check(t, cmp.DeepEqual(got.skipped, tt.wantSkipped))
			assert.Check(t, cmp.Equal(got.Config, tt.want.Config))
			assert.Check(t, cmp.Equal(got.Sidecar, tt.want.Sidecar))
			assert.Check(t, cmp.Equal(got.empty(), tt.empty))
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
