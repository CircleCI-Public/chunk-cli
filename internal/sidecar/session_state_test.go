package sidecar

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestSaveAndLoadSessionID(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	assert.Equal(t, LoadSessionID(dir), "", "no session stored yet")

	assert.NilError(t, SaveSessionID(dir, "sess-abc"))
	assert.Equal(t, LoadSessionID(dir), "sess-abc")
}

func TestLoadSessionID_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	assert.Equal(t, LoadSessionID(dir), "")
}

func TestSaveSessionID_OverwritesPreviousSession(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	assert.NilError(t, SaveSessionID(dir, "sess-1"))
	assert.NilError(t, SaveSessionID(dir, "sess-2"))
	assert.Equal(t, LoadSessionID(dir), "sess-2")
}

func TestSaveSessionID_ResolvesThroughGitRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	// Create a fake .git directory so findGitRootFrom recognises root as a repo.
	assert.NilError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))

	// Save from a subdirectory — should resolve to root.
	sub := filepath.Join(root, "pkg", "foo")
	assert.NilError(t, os.MkdirAll(sub, 0o755))

	assert.NilError(t, SaveSessionID(sub, "sess-sub"))
	// Load from a different subdirectory — same git root, should see the same ID.
	sub2 := filepath.Join(root, "internal")
	assert.NilError(t, os.MkdirAll(sub2, 0o755))
	assert.Equal(t, LoadSessionID(sub2), "sess-sub")
}
