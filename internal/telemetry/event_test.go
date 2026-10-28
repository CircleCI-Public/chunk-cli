package telemetry

import (
	"context"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestTrack_CarriesTheContextsProperties(t *testing.T) {
	fake := &fakeDestination{}
	s, err := NewSender(Config{TestDestination: fake})
	assert.NilError(t, err)

	ctx := WithSender(context.Background(), s)
	ctx = WithProperties(ctx, map[string]any{"watch_session_id": "sitting-1", "plan_id": "outer"})
	ctx = WithProperties(ctx, map[string]any{"plan_id": "inner"})
	Track(ctx, "plan_approved", map[string]any{"run_id": "run-1"})

	assert.Assert(t, cmp.Len(fake.tracks, 1))
	tr := fake.tracks[0]
	assert.Check(t, cmp.Equal(tr.Event, "plan_approved"))
	assert.Check(t, cmp.Equal(tr.Properties["watch_session_id"], "sitting-1"))
	assert.Check(t, cmp.Equal(tr.Properties["plan_id"], "inner"))
	assert.Check(t, cmp.Equal(tr.Properties["run_id"], "run-1"))
	assert.Check(t, cmp.Equal(tr.Properties["app_name"], "chunk-cli"))
}

// Properties attached to a derived context do not leak back into the one it
// came from.
func TestWithProperties_LeavesTheParentAlone(t *testing.T) {
	fake := &fakeDestination{}
	s, err := NewSender(Config{TestDestination: fake})
	assert.NilError(t, err)

	parent := WithProperties(WithSender(context.Background(), s), map[string]any{"watch_session_id": "sitting-1"})
	_ = WithProperties(parent, map[string]any{"plan_id": "plan-1"})
	Track(parent, "watch_opened", nil)

	assert.Assert(t, cmp.Len(fake.tracks, 1))
	_, leaked := fake.tracks[0].Properties["plan_id"]
	assert.Check(t, !leaked)
}

func TestTrack_WithoutASenderDoesNothing(t *testing.T) {
	Track(context.Background(), "watch_opened", nil)
	// A screen built without a context has a nil one.
	var unset context.Context
	Track(unset, "watch_opened", nil)
}

// app_name is chunk-cli's, whatever a caller passes: Amplitude tells the
// applications apart by it.
func TestSenderTrack_AppNameCannotBeOverridden(t *testing.T) {
	fake := &fakeDestination{}
	s, err := NewSender(Config{TestDestination: fake})
	assert.NilError(t, err)

	err = s.Track("watch_opened", map[string]any{"app_name": "other"})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(fake.tracks, 1))
	assert.Check(t, cmp.Equal(fake.tracks[0].Properties["app_name"], "chunk-cli"))
}
