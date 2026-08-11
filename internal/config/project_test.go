package config

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/envspec"
)

func writeProjectConfig(t *testing.T, workDir, data string) {
	t.Helper()
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o700))
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(data), 0o600))
}

// Configs written before the test command stopped being persisted still carry a
// "test" step; loading must drop it so it is never run as a setup step.
func TestLoadProjectConfigDropsTestStep(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{
	  "environment": {
	    "stack": "go",
	    "setup": [
	      {"name": "install", "command": "go mod download"},
	      {"name": "test", "command": "go test -p 1 ./..."}
	    ],
	    "image": "cimg/go",
	    "image_version": "1.26.2"
	  }
	}`)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(cfg.Environment.Setup), 1)
	assert.Equal(t, cfg.Environment.Setup[0].Command, "go mod download")
	assert.Equal(t, cfg.Environment.SetupStep(envspec.StepTest), "")

	// And it is not written back out on the next save.
	assert.NilError(t, SaveProjectConfig(dir, cfg))
	reloaded, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(reloaded.Environment.Setup), 1)
	assert.Equal(t, reloaded.Environment.ImageVersion, "1.26.2")
}

func TestLoadProjectConfigWithoutEnvironment(t *testing.T) {
	dir := t.TempDir()
	// New format.
	writeProjectConfig(t, dir, `{"validate":[{"name":"test","run":"task test"}]}`)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Assert(t, cfg.Environment == nil)
	assert.Equal(t, len(cfg.Validate), 1)
}

func TestLoadProjectConfigMigratesLegacyCommands(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{
	  "commands": [
	    {"name": "fmt",  "run": "gofmt -w .", "role": "autofix"},
	    {"name": "test", "run": "go test ./..."},
	    {"name": "lint", "run": "golangci-lint run"}
	  ]
	}`)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(cfg.Fix), 1)
	assert.Equal(t, cfg.Fix[0].Name, "fmt")
	assert.Equal(t, len(cfg.Validate), 2)
	assert.Equal(t, cfg.Validate[0].Name, "test")
	assert.Equal(t, cfg.Validate[1].Name, "lint")
}

func TestHasSidecarImage(t *testing.T) {
	assert.Assert(t, !(*ProjectConfig)(nil).HasSidecarImage())

	cfg := &ProjectConfig{}
	assert.Assert(t, !cfg.HasSidecarImage())

	cfg.Validation = &ValidationConfig{}
	assert.Assert(t, !cfg.HasSidecarImage())

	cfg.Validation.SidecarImage = "snap-123"
	assert.Assert(t, cfg.HasSidecarImage())
}

func TestMarkRemoteCommandsForSidecarSetup(t *testing.T) {
	cfg := &ProjectConfig{
		Validate: []ValidateCommand{
			{Name: "install", Run: "npm ci"},
			{Name: "test", Run: "npm test"},
			{Name: "lint", Run: "npm run lint"},
			{Name: "test-changed", Run: "npm test --changed", Remote: true},
		},
		Fix: []FixCommand{
			{Name: "format", Run: "npm run format"},
		},
	}

	changed := cfg.MarkRemoteCommandsForSidecarSetup()
	assert.Assert(t, changed)
	assert.Assert(t, cfg.FindValidateCommand("install").Remote)
	assert.Assert(t, cfg.FindValidateCommand("test").Remote)
	assert.Assert(t, cfg.FindValidateCommand("lint").Remote)
	assert.Assert(t, cfg.FindValidateCommand("test-changed").Remote)

	// Fix commands are never marked remote.
	assert.Equal(t, len(cfg.Fix), 1)

	assert.Assert(t, !cfg.MarkRemoteCommandsForSidecarSetup())
}
