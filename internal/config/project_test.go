package config

import (
	"testing"

	"gotest.tools/v3/assert"
)

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
			{Name: "install", Run: "npm ci"},
			{Name: "test", Run: "npm test", Role: RoleGate},
			{Name: "format", Run: "npm run format", Role: RoleAutofix},
			{Name: "lint", Run: "npm run lint", Role: RolePrecheck},
			{Name: "test-changed", Run: "npm test --changed", Role: RoleGate, Remote: true},
		},
	}

	changed := cfg.MarkRemoteCommandsForSidecarSetup()
	assert.Assert(t, changed)
	assert.Assert(t, cfg.FindCommand("install").Remote)
	assert.Assert(t, cfg.FindCommand("test").Remote)
	assert.Assert(t, !cfg.FindCommand("format").Remote)
	assert.Assert(t, !cfg.FindCommand("lint").Remote)
	assert.Assert(t, cfg.FindCommand("test-changed").Remote)

	assert.Assert(t, !cfg.MarkRemoteCommandsForSidecarSetup())
}
