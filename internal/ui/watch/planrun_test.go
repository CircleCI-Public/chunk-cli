package watch

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// fakePlans stands in for Claude and the factory on the planning screen:
// turns answer with reply once release is closed, when there is one, and
// sends answer with runID. The store's text is plan throughout.
type fakePlans struct {
	root    string
	err     error
	reply   string
	plan    string
	release chan struct{}
	runID   string
	sent    string
}

// fakePlanStore is a plan.Store whose text never changes; these tests do not
// exercise polling or comments.
type fakePlanStore struct{ text string }

func (f *fakePlanStore) Create(context.Context, string) error  { return nil }
func (f *fakePlanStore) State(context.Context) (string, error) { return "", nil }
func (f *fakePlanStore) Move(context.Context, string) error    { return nil }
func (f *fakePlanStore) Text(context.Context) (string, error)  { return f.text, nil }

func (f *fakePlanStore) Comments(context.Context) ([]plan.Comment, error) { return nil, nil }
func (f *fakePlanStore) AddComment(context.Context, string, string) (plan.Comment, error) {
	return plan.Comment{}, nil
}
func (f *fakePlanStore) EditComment(context.Context, string, string) error { return nil }
func (f *fakePlanStore) ResolveComment(context.Context, string) error      { return nil }
func (f *fakePlanStore) DeleteComment(context.Context, string) error       { return nil }

func (f *fakePlans) planner(_ context.Context, root string) (plan.Turner, plan.Submitter, plan.Store, error) {
	f.root = root
	if f.err != nil {
		return nil, nil, nil, f.err
	}
	turn := func(ctx context.Context, _ string, _ func(string)) (string, error) {
		if f.release != nil {
			select {
			case <-f.release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return f.reply, nil
	}
	submit := func(p string) (string, error) {
		f.sent = p
		return f.runID, nil
	}
	return turn, submit, &fakePlanStore{text: f.plan}, nil
}

func planModel(t *testing.T, f *fakePlans) Model {
	t.Helper()
	return searchModel().WithDrafts(t.Context(), "/src/chunk-cli", nil, nil).WithPlans(t.Context(), f.planner)
}

// openPlanScreen presses p and delivers the planner's answer.
func openPlanScreen(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := press(m, 'p')
	return deliver(t, m, cmd)
}

// pressKey sends a key to the model, and returns the command it gave back.
func pressKey(m Model, msg tea.KeyPressMsg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func screen(m Model) string { return stripANSI(m.View().Content) }

// typeInto types s as text, a key at a time, as a terminal sends it.
func typeInto(m Model, s string) Model {
	for _, r := range s {
		m, _ = pressKey(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func TestPOpensThePlanningScreenInTheProject(t *testing.T) {
	f := &fakePlans{}
	m := planModel(t, f)
	assert.Assert(t, strings.Contains(view(m), "p plan a run"), "planning is discoverable:\n%s", view(m))

	m = openPlanScreen(t, m)
	assert.Equal(t, f.root, "/src/chunk-cli")
	assert.Assert(t, m.planOpen)
	out := screen(m)
	assert.Assert(t, strings.Contains(out, "Chat") && strings.Contains(out, "No plan yet"), "the planning screen is shown:\n%s", out)
	assert.Assert(t, strings.Contains(out, "esc back to runs"), "the way back is in the footer:\n%s", out)
	assert.Assert(t, m.View().MouseMode == tea.MouseModeCellMotion, "the plan and chat scroll with the mouse")
}

func TestPlannerErrorIsNotedOnTheDashboard(t *testing.T) {
	f := &fakePlans{err: errors.New("this project has no git remote named origin")}
	m := openPlanScreen(t, planModel(t, f))
	assert.Assert(t, !m.HasPlan() && !m.planOpen)
	assert.Assert(t, strings.Contains(view(m), "could not start a plan: this project has no git remote named origin"), view(m))
}

// Esc goes back to the runs with the plan kept: Claude carries on while the
// dashboard is shown, and p opens the screen again as it was.
func TestThePlanIsKeptBehindTheDashboard(t *testing.T) {
	f := &fakePlans{reply: "Wrote a plan.", plan: "## 1. Outcome\n\nThe flag exists.", release: make(chan struct{})}
	m := openPlanScreen(t, planModel(t, f))
	m = typeInto(m, "add a flag")
	m, turn := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.Assert(t, turn != nil, "enter sends the message to Claude")

	// While Claude works, esc is for stopping it; ctrl+c goes back.
	m, back := pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = deliver(t, m, back)
	assert.Assert(t, !m.planOpen && m.HasPlan())
	assert.Assert(t, strings.Contains(view(m), "p back to plan"), view(m))

	// Keys are the dashboard's again.
	m, _ = press(m, 'j')
	assert.Equal(t, m.selRun, "bbb")

	// Claude answers while the dashboard is shown.
	close(f.release)
	m = deliver(t, m, turn)
	// The plan pane picks up the store's text on its next poll; this drives
	// one deterministically rather than waiting on the real timer.
	m = deliver(t, m, m.planScreen.Poll())

	m, _ = press(m, 'p')
	assert.Assert(t, m.planOpen)
	out := screen(m)
	assert.Assert(t, strings.Contains(out, "Wrote a plan.") && strings.Contains(out, "The flag exists."), "the reply that came while away is shown:\n%s", out)
}

func TestSendingThePlanSelectsItsRun(t *testing.T) {
	f := &fakePlans{reply: "ok", plan: "# The plan", runID: "aaa"}
	m := openPlanScreen(t, planModel(t, f))
	m = typeInto(m, "add a flag")
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = deliver(t, m, cmd)
	// The plan pane picks up the store's text on its next poll; this drives
	// one deterministically rather than waiting on the real timer.
	m = deliver(t, m, m.planScreen.Poll())

	m, _ = pressKey(m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m, cmd = pressKey(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	next, closeCmd := m.Update(cmd()) // the send
	m = deliver(t, next.(Model), closeCmd)
	assert.Equal(t, f.sent, "# The plan")

	assert.Assert(t, !m.planOpen && !m.HasPlan(), "a sent plan is done with")
	assert.Equal(t, m.pendingRun, "aaa")
	assert.Assert(t, strings.Contains(view(m), "sent the plan to the factory in chunk-cli"), view(m))
}

func TestWindowSizeReachesTheKeptPlan(t *testing.T) {
	m := openPlanScreen(t, planModel(t, &fakePlans{}))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 50})
	m = next.(Model)
	lines := strings.Split(screen(m), "\n")
	assert.Equal(t, len(lines), 50, "the screen fills the new height")
}
