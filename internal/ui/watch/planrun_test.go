package watch

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/planlist"
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
type fakePlanStore struct{ text, id string }

func (f *fakePlanStore) ID() string                            { return f.id }
func (f *fakePlanStore) Create(context.Context, string) error  { return nil }
func (f *fakePlanStore) State(context.Context) (string, error) { return plan.ReadyState, nil }
func (f *fakePlanStore) Flow(context.Context) (plan.Flow, error) {
	return plan.Flow{Events: []string{"address-comments", "approve-for-units", "approve-for-building"}}, nil
}
func (f *fakePlanStore) AddressComments(context.Context) error { return nil }
func (f *fakePlanStore) ApproveDesign(context.Context) error   { return nil }
func (f *fakePlanStore) Text(context.Context) (string, error)  { return f.text, nil }
func (f *fakePlanStore) SetText(context.Context, string) error { return nil }

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

func (f *fakePlans) planner(_ context.Context, root, id string) (plan.Agent, plan.Approver, plan.Store, error) {
	f.root = root
	if f.err != nil {
		return nil, nil, nil, f.err
	}
	approve := func(func(string)) (string, error) {
		f.sent = true
		return "build-1", nil
	}
	return &fakePlanAgent{f: f, events: make(chan plan.Event, 8)}, approve, &fakePlanStore{text: f.plan, id: id}, nil
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
	assert.Assert(t, strings.Contains(view(m), "could not open the plan: this project has no git remote named origin"), view(m))
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
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Next()))
	// The plan pane picks up the store's text on its next poll; this drives
	// one deterministically rather than waiting on the real timer.
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))

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
	m = deliverThen(t, m, cmd, 1)                                   // the plan is created, then the message sent
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Next())) // chunk's reply
	// The plan pane picks up the store's text on its next poll; this drives
	// one deterministically rather than waiting on the real timer.
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))

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

func TestSwitchingPlansKeepsDraftsAndRoutesBackgroundResults(t *testing.T) {
	a := &fakePlans{plan: "# Upload retries\n\nRetry transient failures.", reply: "Reply only for uploads."}
	b := &fakePlans{plan: "# Cache installs\n\nCache dependencies."}
	opened := map[string]int{}
	planner := func(ctx context.Context, root, id string) (plan.Agent, plan.Approver, plan.Store, error) {
		opened[id]++
		if id == "uploads" {
			return a.planner(ctx, root, id)
		}
		return b.planner(ctx, root, id)
	}
	inbox := planlist.New(t.Context(), func(context.Context) ([]planlist.Plan, error) { return nil, nil },
		func(context.Context, string) (string, []circleci.PlanComment, error) { return "", nil, nil })
	m := searchModel().WithDrafts(t.Context(), "/src/chunk-cli", nil, nil).
		WithPlans(t.Context(), planner).WithPlanInbox(inbox)
	open := func(id string) {
		t.Helper()
		next, cmd := m.Update(planlist.OpenMsg{ID: id, Title: id})
		m = next.(Model)
		if cmd != nil { // switching to an open workspace has nothing to load
			m = deliver(t, m, cmd)
		}
	}
	open("uploads")
	keyA := m.planKey
	latePoll := workspaceCmd(keyA, m.planScreen.Poll())()
	m = typeInto(m, "explain retries")
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = deliver(t, m, cmd)
	lateReply := workspaceCmd(keyA, m.planScreen.Next())()
	m = typeInto(m, "unsent upload message")
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	assert.Assert(t, m.inboxOpen && !m.planOpen)
	open("cache")
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))
	m = typeInto(m, "unsent cache message")

	// Neither a late poll nor a late agent reply for A may change B.
	next, _ := m.Update(latePoll)
	m = next.(Model)
	next, _ = m.Update(lateReply)
	m = next.(Model)
	out := screen(m)
	assert.Assert(t, strings.Contains(out, "Cache dependencies.") && strings.Contains(out, "unsent cache message"), out)
	assert.Assert(t, !strings.Contains(out, "Reply only for uploads.") && !strings.Contains(out, "Retry transient failures."), out)

	open("uploads")
	out = screen(m)
	for _, want := range []string{"Retry transient failures.", "Reply only for uploads.", "unsent upload message"} {
		assert.Assert(t, strings.Contains(out, want), "%q missing:\n%s", want, out)
	}
	assert.Equal(t, opened["uploads"], 1, "switching must reuse the session")
	open("cache")
	assert.Assert(t, strings.Contains(screen(m), "unsent cache message"), screen(m))
	assert.Equal(t, opened["cache"], 1)

	// Hidden workspaces' drafts also participate in the quit warning.
	m, cmd = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = deliver(t, m, cmd)
	next, cmd = m.Update(planlist.QuitMsg{})
	m = next.(Model)
	assert.Assert(t, cmd == nil)
	assert.Assert(t, strings.Contains(screen(m), "2 plan workspace(s) have unsent input"), screen(m))
	m, cmd = pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	_, quits := cmd().(tea.QuitMsg)
	assert.Assert(t, quits)
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

// The inbox is drawn under the dashboard's own header, with tabs naming the
// one showing, and the two together fit the terminal.
func TestInboxSharesTheDashboardHeader(t *testing.T) {
	plans := []planlist.Plan{{Plan: circleci.Plan{ID: "p1", Title: "Retry uploads"}, FlowState: "Review - Human"}}
	inbox := planlist.New(t.Context(), func(context.Context) ([]planlist.Plan, error) { return plans, nil },
		func(context.Context, string) (string, []circleci.PlanComment, error) {
			return "# Retry uploads", nil, nil
		})
	m := searchModel().WithPlans(t.Context(), (&fakePlans{}).planner).WithPlanInbox(inbox)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = next.(Model)
	// Up to the list; deeper waits out the inbox's poll.
	m = deliverThen(t, m, m.inbox.Init(), 1)

	lines := strings.Split(stripANSI(m.View().Content), "\n")
	assert.Assert(t, strings.Contains(lines[0], "chunk watch  Plans  Runs  1 plan"), lines[0])
	assert.Assert(t, strings.HasPrefix(lines[1], "╭─ plans"), lines[1])
	assert.Equal(t, len(lines), 20)

	m, cmd := pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = deliver(t, m, cmd)
	assert.Assert(t, strings.Contains(stripANSI(m.render()), "chunk watch  Plans  Runs  2 runs"))
}
