package watch

import (
	"context"
	"errors"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// tracking is a context whose events are recorded, as one sitting of
// `chunk watch` would have.
func tracking(t *testing.T) (context.Context, *fakes.Events) {
	t.Helper()
	ctx, ev := fakes.Tracking(t.Context(), t)
	return telemetry.WithProperties(ctx, map[string]any{"watch_session_id": "sitting-1"}), ev
}

// trackedPlanModel is planModel, with its plans opened on ctx.
func trackedPlanModel(ctx context.Context, t *testing.T, f *fakePlans) Model {
	t.Helper()
	m := New().WithConnection(chunkd.Connection{}).
		WithPlans(ctx, "/src/chunk-cli", f.planner).WithFlowList(flowList(t))
	return sized(m)
}

// Opening a plan, new or existing, is tracked once it has loaded, with
// whether it is new, so a funnel can tell starting a plan from returning to
// one.
func TestOpeningAPlanIsTracked(t *testing.T) {
	ctx, ev := tracking(t)
	m := trackedPlanModel(ctx, t, &fakePlans{})

	m = openPlanScreen(t, m)
	assert.Assert(t, cmp.DeepEqual(ev.Names(), []string{"plan_opened"}))
	p := ev.Last().Properties
	assert.Check(t, cmp.Equal(p["is_new"], true))
	assert.Check(t, cmp.Equal(p["watch_session_id"], "sitting-1"))
	_, hasID := p["plan_id"]
	assert.Check(t, !hasID, "a new plan has no ID yet: %+v", p)

	next, cmd := m.openPlanID("plan-7", "Add a flag")
	_ = deliver(t, next.(Model), cmd)
	assert.Assert(t, cmp.Len(ev.Names(), 2))
	p = ev.Last().Properties
	assert.Check(t, cmp.Equal(p["is_new"], false))
	assert.Check(t, cmp.Equal(p["plan_id"], "plan-7"))
}

// A plan that fails to load was never opened.
func TestAPlanThatFailsToLoadIsNotTracked(t *testing.T) {
	ctx, ev := tracking(t)
	m := trackedPlanModel(ctx, t, &fakePlans{err: errors.New("factory is down")})

	next, cmd := m.openPlanID("plan-7", "Add a flag")
	_ = deliver(t, next.(Model), cmd)
	assert.Check(t, cmp.Len(ev.Names(), 0))
}
