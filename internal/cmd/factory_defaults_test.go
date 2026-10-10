package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar/docker"
)

func TestResolveFactorySettings(t *testing.T) {
	t.Setenv(docker.EnvImage, "")
	fc := &config.ProjectConfig{
		Factory:    &config.FactoryConfig{Backend: "docker", Image: "cfg:img", MaxAttempts: 5, Log: true},
		Validation: &config.ValidationConfig{SidecarImage: "snapshot-id"},
	}

	got, err := resolveFactorySettings(factorySettings{Attempts: 3}, factoryExplicit{}, fc)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, factorySettings{Backend: "docker", Image: "cfg:img", Attempts: 5, Log: true})

	got, err = resolveFactorySettings(factorySettings{Backend: "circleci", Image: "flag:img", Attempts: 2, Log: false},
		factoryExplicit{Backend: true, Image: true, Attempts: true, Log: true}, fc)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, factorySettings{Backend: "circleci", Image: "flag:img", Attempts: 2})

	// An explicit 3 beats config even though 3 is the default.
	got, err = resolveFactorySettings(factorySettings{Attempts: 3}, factoryExplicit{Attempts: true}, fc)
	assert.NilError(t, err)
	assert.Equal(t, got.Attempts, 3)

	// Built-in defaults.
	got, err = resolveFactorySettings(factorySettings{Attempts: 3}, factoryExplicit{}, &config.ProjectConfig{})
	assert.NilError(t, err)
	assert.DeepEqual(t, got, factorySettings{Backend: "circleci", Attempts: 3})

	// circleci takes validation.sidecarImage; docker never does.
	got, err = resolveFactorySettings(factorySettings{Attempts: 3}, factoryExplicit{},
		&config.ProjectConfig{Validation: &config.ValidationConfig{SidecarImage: "snapshot-id"}})
	assert.NilError(t, err)
	assert.Equal(t, got.Image, "snapshot-id")

	_, err = resolveFactorySettings(factorySettings{Backend: "docker", Attempts: 3}, factoryExplicit{Backend: true},
		&config.ProjectConfig{Validation: &config.ValidationConfig{SidecarImage: "snapshot-id"}})
	assert.Assert(t, strings.Contains(userMsg(err), "No Docker image is set"), userMsg(err))

	t.Setenv(docker.EnvImage, "env:img")
	got, err = resolveFactorySettings(factorySettings{Backend: "docker", Attempts: 3}, factoryExplicit{Backend: true},
		&config.ProjectConfig{Validation: &config.ValidationConfig{SidecarImage: "snapshot-id"}})
	assert.NilError(t, err)
	assert.Equal(t, got.Image, "")
}

func TestFactoryPromptPlan(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	assert.NilError(t, os.WriteFile(spec, []byte("# Spec\n"), 0o644))
	empty := filepath.Join(dir, "empty.md")
	assert.NilError(t, os.WriteFile(empty, []byte(" \n"), 0o644))

	got, err := factoryPrompt(strings.NewReader(""), nil, spec, false)
	assert.NilError(t, err)
	assert.Equal(t, got, "# Spec\n")

	planArg := filepath.Join(dir, "missing.md")
	_, err = factoryPrompt(strings.NewReader(""), nil, planArg, false)
	assert.Equal(t, userMsg(err), "Could not read the plan file "+planArg+".")
	planArg = dir
	_, err = factoryPrompt(strings.NewReader(""), nil, dir, false)
	assert.Equal(t, userMsg(err), "Could not read the plan file "+planArg+".")
	_, err = factoryPrompt(strings.NewReader(""), nil, empty, false)
	assert.Assert(t, strings.Contains(userMsg(err), "is empty"), userMsg(err))
	_, err = factoryPrompt(strings.NewReader(""), []string{"do it"}, spec, false)
	assert.Assert(t, strings.Contains(userMsg(err), "along with another prompt"), userMsg(err))

	f, err := os.Open(spec)
	assert.NilError(t, err)
	defer func() { _ = f.Close() }()
	_, err = factoryPrompt(f, nil, spec, false)
	assert.Assert(t, strings.Contains(userMsg(err), "along with another prompt"), userMsg(err))
}

func TestDecideInitFactory(t *testing.T) {
	tests := []struct {
		name                                 string
		interactive, flags, skip, hasSection bool
		want                                 initFactoryAction
	}{
		{"interactive fresh", true, false, false, false, factoryAsk},
		{"non-interactive fresh", false, false, false, false, factorySkipped},
		{"interactive existing", true, false, false, true, factoryReportExists},
		{"non-interactive existing", false, false, false, true, factoryReportExists},
		{"flags interactive", true, true, false, false, factoryFromFlags},
		{"flags non-interactive", false, true, false, false, factoryFromFlags},
		{"flags override existing", false, true, false, true, factoryFromFlags},
		{"skip", true, false, true, false, factorySkipped},
		{"skip beats flags", true, true, true, false, factorySkipped},
		{"skip beats existing", true, false, true, true, factorySkipped},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decideInitFactory(initFactoryState{
				Interactive: tc.interactive, FlagsGiven: tc.flags, Skip: tc.skip, HasSection: tc.hasSection,
			})
			assert.Equal(t, got, tc.want)
		})
	}
}

func TestCheckFactoryBackend(t *testing.T) {
	assert.NilError(t, checkFactoryBackend(""))
	assert.NilError(t, checkFactoryBackend("docker"))
	assert.Assert(t, strings.Contains(userMsg(checkFactoryBackend("podman")), "podman"))
}

// userMsg is the message a userError shows, which its Error() leaves out.
func userMsg(err error) string {
	var ue *userError
	if errors.As(err, &ue) {
		return ue.msg
	}
	return ""
}
