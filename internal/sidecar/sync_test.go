package sidecar_test

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// TestResolveWorkspace covers the workspace resolution logic used by RsyncSync.
func TestResolveWorkspace(t *testing.T) {
	t.Run("explicit workdir wins", func(t *testing.T) {
		t.Setenv(config.EnvXDGDataHome, t.TempDir())
		path, err := sidecar.ResolveWorkspace(t.Context(), "/explicit", "repo")
		assert.NilError(t, err)
		assert.Equal(t, path, "/explicit")
	})

	t.Run("falls back to repo name", func(t *testing.T) {
		t.Setenv(config.EnvXDGDataHome, t.TempDir())
		path, err := sidecar.ResolveWorkspace(t.Context(), "", "my-repo")
		assert.NilError(t, err)
		assert.Equal(t, path, "/home/user/my-repo")
	})

	t.Run("errors when repo empty and no saved workspace", func(t *testing.T) {
		t.Setenv(config.EnvXDGDataHome, t.TempDir())
		_, err := sidecar.ResolveWorkspace(t.Context(), "", "")
		assert.Assert(t, err != nil)
	})
}
