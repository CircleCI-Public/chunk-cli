package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestSnapshotCreateNameTooLong(t *testing.T) {
	cmd := newSidecarSnapshotCreateCmd()
	cmd.SetOut(nil)
	cmd.SetErr(nil)

	longName := strings.Repeat("a", 256)
	cmd.SetArgs([]string{"--name", longName})

	err := cmd.Execute()
	assert.ErrorContains(t, err, "255 characters or fewer")
	assert.ErrorContains(t, err, "256")
}

func TestSnapshotCreateNameAtLimit(t *testing.T) {
	cmd := newSidecarSnapshotCreateCmd()

	exactName := strings.Repeat("a", 255)
	cmd.SetArgs([]string{"--name", exactName})

	// Passes name validation; fails later on sidecar ID resolution (no active sidecar).
	// We just confirm it does NOT return the length error.
	err := cmd.Execute()
	if err != nil {
		assert.Assert(t, !strings.Contains(err.Error(), "255 characters or fewer"),
			"unexpected length validation error for 255-char name: %v", err)
	}
}

func TestResolveOrgID(t *testing.T) {
	pickerErr := errors.New("picker should not be called")
	failPicker := func() (string, error) { return "", pickerErr }
	okPicker := func() (string, error) { return "picked-org", nil }

	writeConfig := func(t *testing.T, dir, orgID string) {
		t.Helper()
		assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".chunk"), 0o755))
		body := `{"orgID": "` + orgID + `"}`
		assert.NilError(t, os.WriteFile(filepath.Join(dir, ".chunk", "config.json"), []byte(body), 0o644))
	}

	t.Run("flag wins over everything", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "config-org")
		t.Setenv(config.EnvCircleCIOrgID, "env-org")
		got, err := resolveOrgID("flag-org", dir, failPicker)
		assert.NilError(t, err)
		assert.Equal(t, got, "flag-org")
	})

	t.Run("env wins over config and picker", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "config-org")
		t.Setenv(config.EnvCircleCIOrgID, "env-org")
		got, err := resolveOrgID("", dir, failPicker)
		assert.NilError(t, err)
		assert.Equal(t, got, "env-org")
	})

	t.Run("config wins over picker", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "config-org")
		t.Setenv(config.EnvCircleCIOrgID, "")
		got, err := resolveOrgID("", dir, failPicker)
		assert.NilError(t, err)
		assert.Equal(t, got, "config-org")
	})

	t.Run("picker called when no other source", func(t *testing.T) {
		dir := t.TempDir() // no config written
		t.Setenv(config.EnvCircleCIOrgID, "")
		got, err := resolveOrgID("", dir, okPicker)
		assert.NilError(t, err)
		assert.Equal(t, got, "picked-org")
	})

	t.Run("empty workDir skips config lookup", func(t *testing.T) {
		t.Setenv(config.EnvCircleCIOrgID, "")
		got, err := resolveOrgID("", "", okPicker)
		assert.NilError(t, err)
		assert.Equal(t, got, "picked-org")
	})

	t.Run("missing config falls through to picker", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvCircleCIOrgID, "")
		got, err := resolveOrgID("", dir, okPicker)
		assert.NilError(t, err)
		assert.Equal(t, got, "picked-org")
	})

	t.Run("config without orgID falls through to picker", func(t *testing.T) {
		dir := t.TempDir()
		assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".chunk"), 0o755))
		assert.NilError(t, os.WriteFile(filepath.Join(dir, ".chunk", "config.json"), []byte(`{}`), 0o644))
		t.Setenv(config.EnvCircleCIOrgID, "")
		got, err := resolveOrgID("", dir, okPicker)
		assert.NilError(t, err)
		assert.Equal(t, got, "picked-org")
	})
}
