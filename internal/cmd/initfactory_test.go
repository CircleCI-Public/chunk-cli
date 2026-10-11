package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sandboximage"
	"github.com/smeltdev/smelt/internal/ui"
)

// withFakeGenerator replaces Docker-backed rendering for one test.
func withFakeGenerator(t *testing.T, content string) *int {
	t.Helper()
	calls := 0
	orig := generateInitDockerfile
	generateInitDockerfile = func(_ context.Context, _ string, cfg *config.ProjectConfig) (sandboximage.Generated, error) {
		calls++
		return sandboximage.Generated{
			Recipe:     sandboximage.NewRecipe(cfg.Environment, nil, nil),
			Dockerfile: content,
		}, nil
	}
	t.Cleanup(func() { generateInitDockerfile = orig })
	return &calls
}

func pnpmRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"a"}`), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), nil, 0o644))
	return dir
}

func TestInitManagedImageWritesDockerfileAndRecordsIt(t *testing.T) {
	calls := withFakeGenerator(t, "FROM generated\n")
	dir := pnpmRepo(t)
	cfg := &config.ProjectConfig{Factory: &config.FactoryConfig{Backend: config.BackendDocker}}

	initManagedImage(context.Background(), dir, discardStreams(), cfg, initFactoryOptions{SkipImageBuild: true})

	got, err := os.ReadFile(filepath.Join(dir, sandboximage.DefaultDockerfile))
	assert.NilError(t, err)
	assert.Equal(t, string(got), "FROM generated\n")
	assert.Equal(t, cfg.Factory.Dockerfile, sandboximage.DefaultDockerfile)
	assert.Equal(t, *calls, 1)
	// The runtime install step needs the environment init detected.
	assert.Equal(t, cfg.InstallCommand(), "pnpm install")
}

func TestInitManagedImageKeepsAnExistingDockerfile(t *testing.T) {
	calls := withFakeGenerator(t, "FROM generated\n")
	dir := pnpmRepo(t)
	path := filepath.Join(dir, sandboximage.DefaultDockerfile)
	assert.NilError(t, os.WriteFile(path, []byte("FROM mine\n# hand edited\n"), 0o644))
	cfg := &config.ProjectConfig{Factory: &config.FactoryConfig{Backend: config.BackendDocker}}

	initManagedImage(context.Background(), dir, discardStreams(), cfg, initFactoryOptions{SkipImageBuild: true})

	got, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, string(got), "FROM mine\n# hand edited\n")
	assert.Equal(t, *calls, 0)
	assert.Equal(t, cfg.Factory.Dockerfile, sandboximage.DefaultDockerfile)
	assert.Equal(t, cfg.InstallCommand(), "pnpm install")
}

func TestInitManagedImageLeavesAnExplicitImageAlone(t *testing.T) {
	calls := withFakeGenerator(t, "FROM generated\n")
	dir := pnpmRepo(t)
	cfg := &config.ProjectConfig{Factory: &config.FactoryConfig{Backend: config.BackendDocker, Image: "smelt-sandbox:local"}}

	initManagedImage(context.Background(), dir, discardStreams(), cfg, initFactoryOptions{SkipImageBuild: true})

	_, err := os.Stat(filepath.Join(dir, sandboximage.DefaultDockerfile))
	assert.Assert(t, os.IsNotExist(err))
	assert.Equal(t, *calls, 0)
	assert.Equal(t, cfg.Factory.Dockerfile, "")
	assert.Assert(t, cfg.Environment == nil)
}

func TestInitManagedImageIgnoresTheCircleCIBackend(t *testing.T) {
	calls := withFakeGenerator(t, "FROM generated\n")
	dir := pnpmRepo(t)
	cfg := &config.ProjectConfig{Factory: &config.FactoryConfig{Backend: config.BackendCircleCI}}

	initManagedImage(context.Background(), dir, discardStreams(), cfg, initFactoryOptions{SkipImageBuild: true})

	assert.Equal(t, *calls, 0)
	assert.Equal(t, cfg.Factory.Dockerfile, "")
}

func TestInitManagedImageKeepsASavedEnvironment(t *testing.T) {
	withFakeGenerator(t, "FROM generated\n")
	dir := pnpmRepo(t)
	cfg := cfgWith(t, `{"factory":{"backend":"docker"},"environment":{"stack":"javascript","setup":[{"name":"install","command":"yarn"}]}}`)

	initManagedImage(context.Background(), dir, discardStreams(), cfg, initFactoryOptions{SkipImageBuild: true})

	assert.Equal(t, cfg.InstallCommand(), "yarn")
}

func TestManagesImage(t *testing.T) {
	tests := []struct {
		name    string
		factory *config.FactoryConfig
		want    bool
	}{
		{"no section", nil, false},
		{"docker, no image", &config.FactoryConfig{Backend: "docker"}, true},
		{"docker, explicit image", &config.FactoryConfig{Backend: "docker", Image: "x:1"}, false},
		{"circleci", &config.FactoryConfig{Backend: "circleci"}, false},
		{"default backend is docker", &config.FactoryConfig{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, managesImage(&config.ProjectConfig{Factory: tt.factory}), tt.want)
		})
	}
}

func TestWarnPromptError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		warn bool
	}{
		// A confirm answered "no" returns a nil error, which is no failure.
		{"answered", nil, false},
		{"no terminal", ui.ErrNoTTY, false},
		{"cancelled", fmt.Errorf("prompt: %w", ui.ErrCancelled), false},
		{"broken", errors.New("terminal went away"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var errb bytes.Buffer
			warnPromptError(iostream.Streams{Out: &bytes.Buffer{}, Err: &errb}, tt.err)
			assert.Equal(t, errb.Len() > 0, tt.warn, errb.String())
		})
	}
}
