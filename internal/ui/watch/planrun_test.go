package watch

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// fakePlans stands in for the planning agent and the factory on the planning
// screen: the agent answers each message with reply once release is closed,
// when there is one, and approving records that it was. The store's text is
// plan throughout.
type fakePlans struct {
	root    string
	err     error
	reply   string
	plan    string
	release chan struct{}
	sent    bool
}

// fakePlanStore is a plan.Store whose text never changes; these tests do not
// exercise polling or comments.
type fakePlanStore struct{ text string }

func (f *fakePlanStore) Create(context.Context, string) error  { return nil }
func (f *fakePlanStore) State(context.Context) (string, error) { return plan.ReadyState, nil }
func (f *fakePlanStore) AddressComments(context.Context) error { return nil }
func (f *fakePlanStore) ApproveDesign(context.Context) error   { return nil }
func (f *fakePlanStore) Text(context.Context) (string, error)  { return f.text, nil }

func (f *fakePlanStore) Comments(context.Context) ([]plan.Comment, error) { return nil, nil }
func (f *fakePlanStore) AddComment(context.Context, string, string) (plan.Comment, error) {
	return plan.Comment{}, nil
}
func (f *fakePlanStore) EditComment(context.Context, string, string) error { return nil }
func (f *fakePlanStore) ResolveComment(context.Context, string) error      { return nil }
func (f *fakePlanStore) DeleteComment(context.Context, string) error       { return nil }

// fakePlanAgent is a plan.Agent that answers every message with its
// fakePlans' reply.
type fakePlanAgent struct {
	f      *fakePlans
	events chan plan.Event
}

func (a *fakePlanAgent) Send(ctx context.Context, _ string) error {
	go func() {
		if a.f.release != nil {
			select {
			case <-a.f.release:
			case <-ctx.Done():
				return
			}
		}
		a.events <- plan.Event{Reply: &plan.Reply{Text: a.f.reply}}
	}()
	return nil
}

func (a *fakePlanAgent) Events() <-chan plan.Event { return a.events }

func (f *fakePlans) planner(_ context.Context, root string) (plan.Agent, plan.Approver, plan.Store, error) {
	f.root = root
	if f.err != nil {
		return nil, nil, nil, f.err
	}
	approve := func(func(string)) (string, error) {
		f.sent = true
		return "build-1", nil
	}
	return &fakePlanAgent{f: f, events: make(chan plan.Event, 8)}, approve, &fakePlanStore{text: f.plan}, nil
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

// Esc goes back to the runs with the plan kept: chunk carries on while the
// dashboard is shown, and p opens the screen again as it was.
func TestThePlanIsKeptBehindTheDashboard(t *testing.T) {
	f := &fakePlans{reply: "Wrote a plan.", plan: "## 1. Outcome\n\nThe flag exists.", release: make(chan struct{})}
	m := openPlanScreen(t, planModel(t, f))
	m = typeInto(m, "add a flag")
	m, turn := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.Assert(t, turn != nil, "enter sends the message to chunk")
	m = deliverThen(t, m, turn, 1) // the plan is created, then the message sent

	// Esc goes back while chunk works, which carries on.
	m, back := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = deliver(t, m, back)
	assert.Assert(t, !m.planOpen && m.HasPlan())
	assert.Assert(t, strings.Contains(view(m), "p back to plan"), view(m))

	// Keys are the dashboard's again.
	m, _ = press(m, 'j')
	assert.Equal(t, m.selRun, "bbb")

	// chunk answers while the dashboard is shown.
	close(f.release)
	m = deliver(t, m, m.planScreen.Next())
	// The plan pane picks up the store's text on its next poll; this drives
	// one deterministically rather than waiting on the real timer.
	m = deliver(t, m, m.planScreen.Poll())

	m, _ = press(m, 'p')
	assert.Assert(t, m.planOpen)
	out := screen(m)
	assert.Assert(t, strings.Contains(out, "Wrote a plan.") && strings.Contains(out, "The flag exists."), "the reply that came while away is shown:\n%s", out)
}

func TestApprovingThePlanSelectsItsBuild(t *testing.T) {
	f := &fakePlans{reply: "ok", plan: "# The plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"}
	m := openPlanScreen(t, planModel(t, f))
	m = typeInto(m, "add a flag")
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = deliverThen(t, m, cmd, 1)          // the plan is created, then the message sent
	m = deliver(t, m, m.planScreen.Next()) // chunk's reply
	// The plan pane picks up the store's text on its next poll; this drives
	// one deterministically rather than waiting on the real timer.
	m = deliver(t, m, m.planScreen.Poll())

	m, _ = pressKey(m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m, cmd = pressKey(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	// y starts the approval and the spinner; the spinner's ticks are left out.
	batch, ok := cmd().(tea.BatchMsg)
	assert.Assert(t, ok && len(batch) == 2, "y did not start the approval")
	next, closeCmd := m.Update(batch[0]()) // the approval
	m = deliver(t, next.(Model), closeCmd)
	assert.Assert(t, f.sent, "the plan was not approved")

	assert.Assert(t, !m.planOpen && !m.HasPlan(), "an approved plan is done with")
	assert.Check(t, cmp.Equal(m.pendingRun, "build-1"))
	assert.Check(t, cmp.Contains(view(m), "approved the plan; it is building in chunk-cli"))
}

func TestWindowSizeReachesTheKeptPlan(t *testing.T) {
	m := openPlanScreen(t, planModel(t, &fakePlans{}))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 50})
	m = next.(Model)
	lines := strings.Split(screen(m), "\n")
	assert.Equal(t, len(lines), 50, "the screen fills the new height")
}

// deliverThen is deliver, also delivering what the commands each message
// brings back return, n deep, as the program would: the planning screen's
// first message creates the plan in factory before it is sent.
func deliverThen(t *testing.T, m Model, cmd tea.Cmd, n int) Model {
	t.Helper()
	assert.Assert(t, cmd != nil, "a command was returned")
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				m = deliverThen(t, m, c, n)
			}
		}
		return m
	}
	next, follow := m.Update(msg)
	m = next.(Model)
	if n > 0 && follow != nil {
		m = deliverThen(t, m, follow, n-1)
	}
	return m
}
