package sidecar

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestSaveAndLoadActiveSnapshot(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	want := ActiveSnapshot{ID: "snap-abc", Name: "my-snap"}
	assert.NilError(t, SaveActiveSnapshot(want))

	got, err := LoadActiveSnapshot()
	assert.NilError(t, err)
	assert.Assert(t, got != nil, "expected non-nil ActiveSnapshot")
	assert.Equal(t, got.ID, want.ID)
	assert.Equal(t, got.Name, want.Name)
}

func TestLoadActiveSnapshotReturnsNilWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	got, err := LoadActiveSnapshot()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "expected nil when no snapshot file")
}

func TestClearActiveSnapshot(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	assert.NilError(t, SaveActiveSnapshot(ActiveSnapshot{ID: "snap-xyz"}))

	got, err := LoadActiveSnapshot()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)

	assert.NilError(t, ClearActiveSnapshot())

	got, err = LoadActiveSnapshot()
	assert.NilError(t, err)
	assert.Assert(t, got == nil)
}

func TestClearActiveSnapshotNoopWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	assert.NilError(t, ClearActiveSnapshot())
}

func TestSnapshotSessionKeyed(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// Save without a session — generic file.
	assert.NilError(t, SaveActiveSnapshot(ActiveSnapshot{ID: "snap-generic"}))

	// With a session ID set, load should return nil (isolated from the generic file).
	t.Setenv(config.EnvClaudeSession, "sess-abc")
	got, err := LoadActiveSnapshot()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "session-keyed load should not see generic file")

	// Save under the session.
	assert.NilError(t, SaveActiveSnapshot(ActiveSnapshot{ID: "snap-session"}))

	got, err = LoadActiveSnapshot()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.ID, "snap-session")

	// Without the session env var, the original generic file is still intact.
	t.Setenv(config.EnvClaudeSession, "")
	got, err = LoadActiveSnapshot()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.ID, "snap-generic")
}

func TestLoadActiveSnapshotWalksUpToParent(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "sub", "dir")
	assert.NilError(t, os.MkdirAll(child, 0o755))

	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".git"), 0o755))
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".chunk"), 0o755))
	data := []byte(`{"id":"snap-parent","name":"parent-snap"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(parent, ".chunk", "snapshot.json"), data, 0o644))

	t.Chdir(child)

	got, err := LoadActiveSnapshot()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.ID, "snap-parent")
}

func TestLoadActiveSnapshotStopsAtGitRoot(t *testing.T) {
	grandparent := t.TempDir()
	parent := filepath.Join(grandparent, "repo")
	child := filepath.Join(parent, "sub")
	assert.NilError(t, os.MkdirAll(child, 0o755))
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".git"), 0o755))
	assert.NilError(t, os.MkdirAll(filepath.Join(grandparent, ".chunk"), 0o755))
	data := []byte(`{"id":"snap-grandparent"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(grandparent, ".chunk", "snapshot.json"), data, 0o644))

	t.Chdir(child)

	got, err := LoadActiveSnapshot()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "walk should not cross the git root boundary")
}
