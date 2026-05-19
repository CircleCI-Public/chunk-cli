package sidecar

import (
	"context"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/session"
)

func TestSaveAndLoadActiveSnapshot(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	ctx := context.Background()
	want := ActiveSnapshot{ID: "snap-abc", Name: "my-snap"}
	assert.NilError(t, SaveActiveSnapshot(ctx, want))

	got, err := LoadActiveSnapshot(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil, "expected non-nil ActiveSnapshot")
	assert.Equal(t, got.ID, want.ID)
	assert.Equal(t, got.Name, want.Name)
}

func TestLoadActiveSnapshotReturnsNilWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	got, err := LoadActiveSnapshot(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "expected nil when no snapshot file")
}

func TestClearActiveSnapshot(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	ctx := context.Background()
	assert.NilError(t, SaveActiveSnapshot(ctx, ActiveSnapshot{ID: "snap-xyz"}))

	got, err := LoadActiveSnapshot(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil)

	assert.NilError(t, ClearActiveSnapshot(ctx))

	got, err = LoadActiveSnapshot(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got == nil)
}

func TestClearActiveSnapshotNoopWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	assert.NilError(t, ClearActiveSnapshot(context.Background()))
}

func TestSnapshotSessionKeyed(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	ctx := context.Background()
	sessCtx := session.WithID(ctx, "sess-abc")

	// Save without a session — generic file.
	assert.NilError(t, SaveActiveSnapshot(ctx, ActiveSnapshot{ID: "snap-generic"}))

	// Session-keyed load should not see the generic file.
	got, err := LoadActiveSnapshot(sessCtx)
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "session-keyed load should not see generic file")

	// Save under the session.
	assert.NilError(t, SaveActiveSnapshot(sessCtx, ActiveSnapshot{ID: "snap-session"}))

	got, err = LoadActiveSnapshot(sessCtx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.ID, "snap-session")

	// Without the session, the original generic file is still intact.
	got, err = LoadActiveSnapshot(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.ID, "snap-generic")
}
