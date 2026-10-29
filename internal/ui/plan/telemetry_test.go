package plan

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// tracking is a context whose events are recorded.
func tracking(t *testing.T) (context.Context, *fakes.Events) {
	t.Helper()
	return fakes.Tracking(context.Background(), t)
}

// The plan counts as sent to the factory once the developer confirms and
// factory takes it, not when ctrl+s asks them to, and carries the run it
// started. A declined confirmation, or an approval factory refuses, started
// no run and is not counted.
func TestApprovalIsTrackedOnceFactoryTakesIt(t *testing.T) {
	tests := []struct {
		name    string
		answer  rune
		approve func(func(string)) (string, error)
		tracked bool
	}{
		{
			name:    "confirmed",
			answer:  'y',
			approve: func(func(string)) (string, error) { return "build-1", nil },
			tracked: true,
		},
		{
			name:    "declined",
			answer:  'n',
			approve: func(func(string)) (string, error) { return "build-1", nil },
		},
		{
			name:    "refused by factory",
			answer:  'y',
			approve: func(func(string)) (string, error) { return "", errors.New("factory is down") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, ev := tracking(t)
			store := newFakeStore()
			store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
			m := New(ctx, turnReturning("ok"), tt.approve, store, "do it")
			m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

			m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			assert.Check(t, cmp.Len(ev.Named("plan_approved"), 0), "ctrl+s alone tracked an approval")
			_ = drive(t, m, tea.KeyPressMsg{Code: tt.answer, Text: string(tt.answer)})

			got := ev.Named("plan_approved")
			if !tt.tracked {
				assert.Check(t, cmp.Len(got, 0))
				return
			}
			assert.Assert(t, cmp.Len(got, 1))
			assert.Check(t, cmp.Equal(got[0].Properties["plan_id"], "saved-plan"))
			assert.Check(t, cmp.Equal(got[0].Properties["run_id"], "build-1"))
		})
	}
}

// Saved comments are a round of feedback on the plan, with its ID. The
// request that creates the plan is not feedback. Nothing the developer wrote
// is sent.
func TestFeedbackIsTrackedWithThePlanID(t *testing.T) {
	ctx, ev := tracking(t)
	store := newFakeStore()
	store.TextV = twoParts
	agent := turnReturning("ok")
	m := New(ctx, agent, nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	agent.reset()

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))
	_ = drive(t, m, press("s"))

	got := ev.Named("plan_feedback_sent")
	assert.Assert(t, cmp.Len(got, 1), "want the saved comment only")
	p := got[0].Properties
	assert.Check(t, cmp.Equal(p["feedback_type"], "comments"))
	assert.Check(t, cmp.Equal(p["comment_count"], 1))
	assert.Check(t, cmp.Equal(p["plan_id"], "saved-plan"))
	for _, v := range p {
		wrote := v == "plan it" || v == "too vague"
		assert.Check(t, !wrote, "the event carries what the developer wrote: %+v", p)
	}
}
