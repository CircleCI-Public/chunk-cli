package sidecar

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestSaveAndLoadActiveSnapshot(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

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
	setupXDGData(t)

	got, err := LoadActiveSnapshot()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "expected nil when no snapshot file")
}

func TestClearActiveSnapshot(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

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
	setupXDGData(t)

	assert.NilError(t, ClearActiveSnapshot())
}

func TestSnapshotSessionKeyed(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

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
