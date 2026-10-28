package watch

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

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

// n starts a draft, and a draft counts as sent once the factory takes it,
// with the run it started.
func TestDraftsAreTrackedFromNToTheFactory(t *testing.T) {
	orig := draftTickEvery
	draftTickEvery = time.Millisecond
	t.Cleanup(func() { draftTickEvery = orig })
	ctx, ev := tracking(t)
	f := &fakeDrafts{draft: sampleDraft, runID: "factory-run"}
	m := searchModel().WithDrafts(ctx, "/src/chunk-cli", f.makeDraft, f.send)

	m, _ = press(m, 'n')
	assert.Assert(t, cmp.DeepEqual(ev.Names(), []string{"draft_started"}))
	assert.Check(t, cmp.Equal(ev.Last().Properties["watch_session_id"], "sitting-1"))

	m = typeText(m, "add a --verbose flag")
	m, cmd := press(m, tea.KeyEnter)
	m = deliver(t, m, cmd)
	m, cmd = press(m, tea.KeyEnter)
	_ = deliver(t, m, cmd)
	assert.Assert(t, cmp.DeepEqual(ev.Names(), []string{"draft_started", "draft_sent"}))
	sent := ev.Last().Properties
	assert.Check(t, cmp.Equal(sent["run_id"], "factory-run"))
	assert.Check(t, cmp.Equal(sent["watch_session_id"], "sitting-1"))
}

func TestADraftTheFactoryRefusesIsNotTrackedAsSent(t *testing.T) {
	ctx, ev := tracking(t)
	m := searchModel().WithDrafts(ctx, "/src/chunk-cli", nil, nil)
	m.draft, m.draftResult = draftReady, sampleDraft
	_ = m.withRunStarted(runStartedMsg{err: errors.New("the chunk daemon is not reachable")})
	assert.Check(t, cmp.Len(ev.Names(), 0))
}

// Opening a plan, new or existing, is tracked once it has loaded, with
// whether it is new, so a funnel can tell starting a plan from returning to
// one.
func TestOpeningAPlanIsTracked(t *testing.T) {
	ctx, ev := tracking(t)
	f := &fakePlans{}
	m := searchModel().WithDrafts(ctx, "/src/chunk-cli", nil, nil).WithPlans(ctx, f.planner)

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
	f := &fakePlans{err: errors.New("factory is down")}
	m := searchModel().WithDrafts(ctx, "/src/chunk-cli", nil, nil).WithPlans(ctx, f.planner)

	next, cmd := m.openPlanID("plan-7", "Add a flag")
	_ = deliver(t, next.(Model), cmd)
	assert.Check(t, cmp.Len(ev.Names(), 0))
}
