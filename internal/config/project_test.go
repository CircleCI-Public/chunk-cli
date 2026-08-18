package config

import (
	"errors"
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
	writeProjectConfig(t, dir, `{"commands":[{"name":"test","run":"task test"}]}`)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Assert(t, cfg.Environment == nil)
	assert.Equal(t, len(cfg.Commands), 1)
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
		Commands: []Command{
			{Name: CmdInstall, Run: "npm ci"},
			{Name: "test", Run: "npm test", Role: RoleGate},
			{Name: "format", Run: "npm run format", Role: RoleAutofix},
			{Name: "lint", Run: "npm run lint"},
			{Name: "test-changed", Run: "npm test --changed", Role: RoleGate, Remote: true},
		},
	}

	changed := cfg.MarkRemoteCommandsForSidecarSetup()
	assert.Assert(t, changed)
	assert.Assert(t, cfg.FindCommand(CmdInstall).Remote)
	assert.Assert(t, cfg.FindCommand("test").Remote)
	assert.Assert(t, !cfg.FindCommand("format").Remote)
	assert.Assert(t, !cfg.FindCommand("lint").Remote)
	assert.Assert(t, cfg.FindCommand("test-changed").Remote)

	assert.Assert(t, !cfg.MarkRemoteCommandsForSidecarSetup())
}

func TestMarkCommandRemote(t *testing.T) {
	newCfg := func() *ProjectConfig {
		return &ProjectConfig{
			Commands: []Command{
				{Name: CmdInstall, Run: "npm ci"},
				{Name: "test", Run: "npm test", Role: RoleGate},
				{Name: "format", Run: "npm run format", Role: RoleAutofix},
				{Name: "lint", Run: "npm run lint", Remote: true},
			},
		}
	}

	t.Run("named command", func(t *testing.T) {
		cfg := newCfg()
		changed, err := cfg.MarkCommandRemote("test")
		assert.NilError(t, err)
		assert.DeepEqual(t, changed, []string{"test"})
		assert.Assert(t, cfg.FindCommand("test").Remote)
		// Untargeted commands keep whatever they had.
		assert.Assert(t, !cfg.FindCommand(CmdInstall).Remote)
	})

	// A named autofix command is marked: the caller asked for that one by name.
	t.Run("named autofix command", func(t *testing.T) {
		cfg := newCfg()
		changed, err := cfg.MarkCommandRemote("format")
		assert.NilError(t, err)
		assert.DeepEqual(t, changed, []string{"format"})
		assert.Assert(t, cfg.FindCommand("format").Remote)
	})

	// The unnamed sweep skips autofix commands. A formatter running on the sidecar
	// rewrites files there and the edits never come back to the working tree.
	t.Run("empty name skips autofix", func(t *testing.T) {
		cfg := newCfg()
		changed, err := cfg.MarkCommandRemote("")
		assert.NilError(t, err)
		// "lint" is already remote, so it is not reported as changed.
		assert.DeepEqual(t, changed, []string{CmdInstall, "test"})
		assert.Assert(t, !cfg.FindCommand("format").Remote)
		assert.Assert(t, cfg.FindCommand("lint").Remote)
	})

	t.Run("already remote reports no change", func(t *testing.T) {
		cfg := newCfg()
		changed, err := cfg.MarkCommandRemote("lint")
		assert.NilError(t, err)
		assert.Equal(t, len(changed), 0)
	})

	t.Run("unknown name", func(t *testing.T) {
		cfg := newCfg()
		_, err := cfg.MarkCommandRemote("nope")
		assert.Assert(t, errors.Is(err, ErrNoSuchCommand))
		// Nothing is written when the name misses.
		assert.Assert(t, !cfg.FindCommand("test").Remote)
	})

	t.Run("nil config", func(t *testing.T) {
		var cfg *ProjectConfig
		_, err := cfg.MarkCommandRemote("test")
		assert.Assert(t, errors.Is(err, ErrNoSuchCommand))
	})
}
