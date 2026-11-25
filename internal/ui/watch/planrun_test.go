package watch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/flowlist"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// fakePlans stands in for factory on the planning screen: its store's text is
// plan throughout, and approving and claiming record that they were.
type fakePlans struct {
	root  string
	err   error
	plan  string
	title string
	// newID is the ID factory gives a new plan as it is created; "" leaves
	// it with none.
	newID string
	// sent is whether the plan was approved, and created the title and request
	// a new plan was created with.
	sent    bool
	created []string
	// units are the units the store serves, as factory reads them.
	units []plan.Unit
	// flow is the flow the store serves, when it is not the default.
	flow *plan.Flow
	// release, when set, holds each create until it is closed.
	release chan struct{}
}

// fakePlanStore is a plan.Store whose text never changes; these tests do not
// exercise polling or comments.
// A new plan's store takes newID as its ID on Create, when there is one.
// Its flow is flow when there is one, else a plan waiting on the developer.
type fakePlanStore struct {
	f                      *fakePlans
	text, id, newID, title string
	units                  []plan.Unit
	flow                   *plan.Flow
}

func (f *fakePlanStore) ID() string { return f.id }

func (f *fakePlanStore) Factories(context.Context) ([]plan.Factory, error) { return nil, nil }

func (f *fakePlanStore) Create(ctx context.Context, title, request, _ string) error {
	if f.f.release != nil {
		select {
		case <-f.f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.id == "" {
		f.id = f.newID
	}
	f.f.created = append(f.f.created, title+" | "+request)
	return nil
}

func (f *fakePlanStore) Claim(context.Context) (string, error) { return "scotts-mbp", nil }
func (f *fakePlanStore) Release(context.Context) error         { return nil }
func (f *fakePlanStore) Approve(context.Context) error {
	f.f.sent = true
	return nil
}
func (f *fakePlanStore) Flow(context.Context) (plan.Flow, error) {
	if f.flow != nil {
		return *f.flow, nil
	}
	// Factory requires the design's approval before the plan has units, and
	// the plan's once it has them.
	required := "approve-for-units"
	if len(f.units) > 0 {
		required = "approve-for-building"
	}
	events := []string{"address-comments", "approve-for-units", "approve-for-building"}
	var next []plan.Step
	for _, e := range events {
		next = append(next, plan.Step{Actor: "human", Event: e, Required: e == required})
	}
	return plan.Flow{State: "refine-proposal", WaitingOn: "human", Events: events, Next: next}, nil
}
func (f *fakePlanStore) Family(context.Context) (plan.Family, error) { return plan.Family{}, nil }
func (f *fakePlanStore) Structure(context.Context) (plan.Structure, error) {
	items := fakes.PlanItems(f.text)
	st := plan.Structure{Units: f.units}
	for _, sec := range []struct {
		heading string
		section *fakes.FactorySection
	}{{"1. Outcome", items.Outcome}, {"2. Design", items.Design}} {
		if sec.section != nil {
			st.Sections = append(st.Sections, plan.Section{Heading: sec.heading, Blocks: blocksOf(sec.section.Blocks)})
		}
	}
	return st, nil
}

// blocksOf is bs as the plan screen's blocks.
func blocksOf(bs []fakes.FactoryBlock) []plan.Block {
	out := make([]plan.Block, len(bs))
	for i, b := range bs {
		out[i] = plan.Block{Type: b.Type, Text: b.Text}
		if b.Blocks != nil {
			out[i].Blocks = blocksOf(*b.Blocks)
		}
	}
	return out
}
func (f *fakePlanStore) AddressComments(context.Context) error     { return nil }
func (f *fakePlanStore) AnswerOpenQuestions(context.Context) error { return nil }
func (f *fakePlanStore) StartWork(context.Context) error           { return nil }
func (f *fakePlanStore) End(context.Context, string) error         { return nil }
func (f *fakePlanStore) Delete(context.Context) error              { return nil }
func (f *fakePlanStore) ApproveDesign(context.Context) error       { return nil }
func (f *fakePlanStore) Text(context.Context) (string, error)      { return f.text, nil }
func (f *fakePlanStore) SetText(context.Context, string) error     { return nil }

func (f *fakePlanStore) Comments(context.Context) ([]plan.Comment, error) { return nil, nil }
func (f *fakePlanStore) AddComment(context.Context, string, string) (plan.Comment, error) {
	return plan.Comment{}, nil
}
func (f *fakePlanStore) EditComment(context.Context, string, string) error { return nil }
func (f *fakePlanStore) ResolveComment(context.Context, string) error      { return nil }
func (f *fakePlanStore) DeleteComment(context.Context, string) error       { return nil }

func (f *fakePlans) planner(_ context.Context, root, id string) (OpenedPlan, error) {
	f.root = root
	if f.err != nil {
		return OpenedPlan{}, f.err
	}
	// An existing plan needs no checkout; a new one is made in the project.
	if id != "" {
		root = ""
	}
	return OpenedPlan{
		Store: &fakePlanStore{f: f, text: f.plan, id: id, newID: f.newID, title: f.title, units: f.units, flow: f.flow},
		Root:  root,
	}, nil
}

// fakeDeleter is a Deleter that records each plan it is asked to delete and
// answers err. When gate is set, Delete waits for it to close.
type fakeDeleter struct {
	mu    sync.Mutex
	calls []string
	err   error
	gate  chan struct{}
}

func (d *fakeDeleter) Delete(_ context.Context, planID string) error {
	d.mu.Lock()
	d.calls = append(d.calls, planID)
	gate, err := d.gate, d.err
	d.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return err
}

func (d *fakeDeleter) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

// flowList is a flow list of flows, each previewed as an Outcome of its ID.
func flowList(t *testing.T, flows ...circleci.Flow) flowlist.Model {
	t.Helper()
	return flowlist.New(t.Context(), func(context.Context) ([]circleci.Flow, error) { return flows, nil },
		func(_ context.Context, id string) (plan.Structure, []circleci.PlanComment, error) {
			return plan.Structure{Sections: []plan.Section{{Heading: "1. Outcome", Blocks: []plan.Block{{Type: "paragraph", Text: id}}}}}, nil, nil
		})
}

// sized is m after a terminal of 110x40 reports its size.
func sized(m Model) Model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: 110, Height: 40})
	return next.(Model)
}

// planModel is a dashboard opened in /src/chunk-cli, on an empty flow list.
func planModel(t *testing.T, f *fakePlans) Model {
	t.Helper()
	m := New().WithConnection(chunkd.Connection{}).
		WithPlans(t.Context(), "/src/chunk-cli", f.planner).WithFlowList(flowList(t))
	return sized(m)
}

// openPlanScreen presses n on the flow list and delivers the planner's
// answer.
func openPlanScreen(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	return deliverThen(t, m, cmd, 1)
}

// pressKey sends a key to the model, and returns the command it gave back.
func pressKey(m Model, msg tea.KeyPressMsg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func screen(m Model) string { return ansi.Strip(m.View().Content) }

// typeInto types s as text, a key at a time, as a terminal sends it.
func typeInto(m Model, s string) Model {
	for _, r := range s {
		m, _ = pressKey(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// deliver runs cmd and hands what it returns to the model, as the program
// would. A batch delivers each of its messages.
func deliver(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	assert.Assert(t, cmd != nil, "a command was returned")
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				m = deliver(t, m, c)
			}
		}
		return m
	}
	next, _ := m.Update(msg)
	return next.(Model)
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

func TestNOpensANewPlanInTheProject(t *testing.T) {
	f := &fakePlans{}
	m := planModel(t, f)
	assert.Assert(t, m.flowsOpen, "the dashboard opens on Flows")
	assert.Check(t, cmp.Contains(screen(m), "n new plan"), "planning is discoverable")

	m = openPlanScreen(t, m)
	assert.Equal(t, f.root, "/src/chunk-cli")
	assert.Assert(t, m.planOpen)
	out := screen(m)
	assert.Assert(t, strings.Contains(out, "Request") && strings.Contains(out, "No plan yet"), "the planning screen is shown:\n%s", out)
	assert.Check(t, cmp.Contains(out, "back to plans"), "the way back is in the footer")
	assert.Check(t, !strings.Contains(out, "runs"), "the footer names runs:\n%s", out)
	assert.Assert(t, m.View().MouseMode == tea.MouseModeCellMotion, "the plan and chat scroll with the mouse")
}

// The opener is asked for any plan with the project the dashboard was opened
// in; an existing plan comes back with no root of its own.
func TestAFlowOpensWithoutACheckout(t *testing.T) {
	f := &fakePlans{plan: "# Retry uploads"}
	m := planModel(t, f)
	next, cmd := m.Update(flowlist.OpenMsg{ID: "p1", Title: "Retry uploads"})
	m = deliverThen(t, next.(Model), cmd, 0)
	assert.Check(t, cmp.Equal(f.root, "/src/chunk-cli"))
	assert.Check(t, m.planOpen)
	assert.Check(t, cmp.Equal(m.planWorkspaces[m.planKey].root, ""))
}
func TestPlannerErrorIsNotedOnFlows(t *testing.T) {
	f := &fakePlans{err: errors.New("this project has no git remote named origin")}
	m := openPlanScreen(t, planModel(t, f))
	assert.Assert(t, !m.HasPlan() && !m.planOpen && m.flowsOpen)
	assert.Assert(t, strings.Contains(screen(m), "could not open the plan: this project has no git remote named origin"), screen(m))
}

// Flows is the only screen besides the plans: tab, 1 and 2 switch to
// nothing, and esc quits as q does.
func TestFlowsKeys(t *testing.T) {
	m := planModel(t, &fakePlans{})
	before := screen(m)
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyTab}, {Code: '1', Text: "1"}, {Code: '2', Text: "2"}} {
		next, cmd := pressKey(m, k)
		assert.Check(t, cmd == nil, "%s did something", k.String())
		assert.Check(t, next.flowsOpen && !next.planOpen, "%s left Flows", k.String())
		assert.Check(t, cmp.Equal(screen(next), before), "%s changed the screen", k.String())
	}
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: 'q', Text: "q"}} {
		_, ask := pressKey(m, k)
		assert.Assert(t, ask != nil, "%s did nothing", k.String())
		_, quit := m.Update(ask())
		assert.Assert(t, quit != nil, "%s did not quit", k.String())
		_, quits := quit().(tea.QuitMsg)
		assert.Check(t, quits, "%s did not quit", k.String())
	}
}

// createPlan sends the new plan screen's title and request, as typed, and
// delivers the create and the claim after it.
func createPlan(t *testing.T, m Model, title, request string) Model {
	t.Helper()
	m = typeInto(m, title)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // to Factory
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // to the request
	m = typeInto(m, request)
	m, create := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.Assert(t, create != nil, "enter sends the title and request")
	return deliverThen(t, m, create, 2) // the plan is created, then claimed and read
}

// Esc goes back to Flows with the plan kept: its worker carries on while the
// list is shown, and opening the plan again shows it as it was.
func TestThePlanIsKeptBehindFlows(t *testing.T) {
	f := &fakePlans{plan: "## 1. Outcome\n\nThe flag exists.", newID: "plan-1"}
	m := openPlanScreen(t, planModel(t, f))
	m = createPlan(t, m, "Add a flag", "add a flag")
	assert.Check(t, cmp.DeepEqual(f.created, []string{"Add a flag | add a flag"}))

	// Esc goes back while the worker works, which carries on.
	m, back := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = deliver(t, m, back)
	assert.Assert(t, !m.planOpen && m.flowsOpen && m.HasPlan())
	key := m.planKey

	// The plan pane picks up the store's text on its next poll; this drives
	// one deterministically rather than waiting on the real timer.
	m = deliver(t, m, workspaceCmd(key, m.planScreen.Poll()))

	next, _ := m.Update(flowlist.OpenMsg{ID: m.planWorkspaces[key].store.ID()})
	m = next.(Model)
	assert.Assert(t, m.planOpen)
	out := screen(m)
	assert.Assert(t, strings.Contains(out, "The flag exists."), "the plan is shown:\n%s", out)
}
func TestArrowsReachTheOpenFlowScreen(t *testing.T) {
	f := &fakePlans{newID: "plan-1", plan: "# The plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n\n### Unit 1: Add the flag\n"}
	f.units = []plan.Unit{{Name: "Unit 1: Add the flag"}}
	m := openPlanScreen(t, planModel(t, f))
	m = createPlan(t, m, "Add a flag", "add a flag")
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))
	s := screen(m)
	assert.Check(t, cmp.Contains(s, "ACTIVITY"), "the Overview is the left pane:\n%s", s)
	assert.Check(t, cmp.Contains(s, "The flag exists."))
	assert.Check(t, !strings.Contains(s, "PROGRESS"), "the plan pane shows the plan, not progress:\n%s", s)

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	for _, k := range []rune{tea.KeyLeft, tea.KeyRight} {
		m, _ = pressKey(m, tea.KeyPressMsg{Code: k})
		assert.Assert(t, m.planOpen, "an arrow left the flow screen")
	}
	s = screen(m)
	assert.Check(t, cmp.Contains(s, "ACTIVITY"))
	assert.Check(t, cmp.Contains(s, "The flag exists."))
}

// Approving keeps the plan screen open on its Overview, which follows the
// build: it sends the approval to factory and names the worker building it.
func TestApprovingThePlanKeepsItsScreen(t *testing.T) {
	f := &fakePlans{newID: "plan-1", plan: "# The plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"}
	f.units = []plan.Unit{{Name: "Unit 1: Add the flag"}}
	f.flow = &plan.Flow{
		State: "refine-units", WaitingOn: "human",
		Events: []string{"address-comments", "approve-for-building"},
		Next: []plan.Step{
			{Actor: "human", Event: "address-comments"},
			{Actor: "human", Event: "approve-for-building", Required: true},
		},
		Worker: &plan.Worker{ID: "w1", Hostname: "scotts-mbp", Live: true},
	}
	m := openPlanScreen(t, planModel(t, f))
	m = createPlan(t, m, "Add a flag", "add a flag")
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))

	m, _ = pressKey(m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	// y starts the approval and the spinner; the spinner's ticks are left out.
	batch, ok := cmd().(tea.BatchMsg)
	assert.Assert(t, ok && len(batch) == 2, "y did not start the approval")
	next, buildingCmd := m.Update(batch[0]()) // the approval
	// The screen answers with BuildingMsg, and a refresh, as workspaceMsgs.
	m = deliver(t, next.(Model), buildingCmd)
	assert.Assert(t, f.sent, "the plan was not approved")
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))

	assert.Check(t, m.planOpen && m.HasPlan(), "an approved plan's screen stays open")
	out := screen(m)
	assert.Check(t, cmp.Contains(out, "Approved · scotts-mbp is building it"))
	assert.Check(t, !strings.Contains(out, "Runs"), "the screen names Runs:\n%s", out)
	// The Overview has the keyboard, so its cursor is on the Building stage.
	assert.Check(t, cmp.Contains(out, "▶ ◉ Building"), "the Overview does not have the keyboard:\n%s", out)
}

// A ClosedMsg for an approved plan goes back to Flows, as one for a plan
// left does, with the plan selected.
func TestApprovedPlanStaysOnFlows(t *testing.T) {
	f := &fakePlans{plan: "# Retry uploads"}
	m := New().WithConnection(chunkd.Connection{}).WithPlans(t.Context(), "/src/chunk-cli", f.planner).
		WithFlowList(flowList(t, circleci.Flow{ID: "a", Title: "Alpha"}, circleci.Flow{ID: "b1", Title: "Retry uploads"}))
	m = sized(m)
	m = deliverThen(t, m, m.flows.Init(), 1)
	next, cmd := m.Update(flowlist.OpenMsg{ID: "b1", Title: "Retry uploads"})
	m = deliverThen(t, next.(Model), cmd, 0)
	assert.Assert(t, m.planOpen)

	next, _ = m.Update(workspaceMsg{key: m.planKey, msg: plan.ClosedMsg{Approved: true, Worker: "scotts-mbp"}})
	m = next.(Model)
	next, _ = m.Update(workspaceMsg{key: m.planKey, msg: plan.BuildingMsg{Worker: "scotts-mbp"}})
	m = next.(Model)
	view := screen(m)
	assert.Check(t, m.flowsOpen && !m.planOpen)
	assert.Check(t, cmp.Contains(view, "▶ Retry uploads"))
	assert.Check(t, !strings.Contains(view, "Runs"), view)
	assert.Check(t, !strings.Contains(view, "appears in the list"), view)
}

func TestWindowSizeReachesTheKeptPlan(t *testing.T) {
	m := openPlanScreen(t, planModel(t, &fakePlans{}))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 50})
	m = next.(Model)
	lines := strings.Split(screen(m), "\n")
	assert.Equal(t, len(lines), 50, "the screen fills the new height")
}

func TestSwitchingPlansKeepsDraftsAndRoutesBackgroundResults(t *testing.T) {
	a := &fakePlans{plan: "# Upload retries\n\n## 1. Outcome\n\nRetry transient failures."}
	b := &fakePlans{plan: "# Cache installs\n\n## 1. Outcome\n\nCache dependencies."}
	opened := map[string]int{}
	planner := func(ctx context.Context, root, id string) (OpenedPlan, error) {
		opened[id]++
		if id == "uploads" {
			return a.planner(ctx, root, id)
		}
		return b.planner(ctx, root, id)
	}
	m := sized(New().WithConnection(chunkd.Connection{}).
		WithPlans(t.Context(), "/src/chunk-cli", planner).WithFlowList(flowList(t)))
	open := func(id string) {
		t.Helper()
		next, cmd := m.Update(flowlist.OpenMsg{ID: id, Title: id})
		m = next.(Model)
		if cmd != nil { // switching to an open workspace has nothing to load
			m = deliver(t, m, cmd)
		}
	}
	// comment queues a comment on the open plan's first part, which an
	// existing plan opens on.
	comment := func(text string) {
		t.Helper()
		m, _ = pressKey(m, tea.KeyPressMsg{Code: 'c', Text: "c"})
		m = typeInto(m, text)
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	open("uploads")
	keyA := m.planKey
	latePoll := workspaceCmd(keyA, m.planScreen.Poll())
	m = deliver(t, m, workspaceCmd(keyA, m.planScreen.Poll()))
	comment("unsent upload note")
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	assert.Assert(t, m.flowsOpen && !m.planOpen)
	open("cache")
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))
	comment("unsent cache note")

	// A late poll for A may not change B.
	m = deliver(t, m, latePoll)
	out := screen(m)
	assert.Assert(t, strings.Contains(out, "Cache dependencies.") && strings.Contains(out, "unsent cache note"), out)
	assert.Assert(t, !strings.Contains(out, "Retry transient failures.") && !strings.Contains(out, "unsent upload note"), out)

	open("uploads")
	out = screen(m)
	for _, want := range []string{"Retry transient failures.", "unsent upload note"} {
		assert.Assert(t, strings.Contains(out, want), "%q missing:\n%s", want, out)
	}
	assert.Equal(t, opened["uploads"], 1, "switching must reuse the session")
	open("cache")
	assert.Assert(t, strings.Contains(screen(m), "unsent cache note"), screen(m))
	assert.Equal(t, opened["cache"], 1)

	// Hidden workspaces' drafts also participate in the quit warning.
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = deliver(t, m, cmd)
	next, cmd := m.Update(flowlist.QuitMsg{})
	m = next.(Model)
	assert.Assert(t, cmd == nil)
	assert.Assert(t, strings.Contains(screen(m), "2 plan workspace(s) have unsent input"), screen(m))
	m, cmd = pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	_, quits := cmd().(tea.QuitMsg)
	assert.Assert(t, quits)
}

// A new plan's header says New plan until it is created, and the typed title
// goes to the workspace that created it, not the one showing.
func TestANewPlansHeaderTakesItsTitle(t *testing.T) {
	fresh := &fakePlans{release: make(chan struct{})}
	other := &fakePlans{plan: "# Cache installs"}
	planner := func(ctx context.Context, root, id string) (OpenedPlan, error) {
		if id == "" {
			return fresh.planner(ctx, root, id)
		}
		return other.planner(ctx, root, id)
	}
	m := New().WithConnection(chunkd.Connection{}).
		WithPlans(t.Context(), "/src/chunk-cli", planner).WithFlowList(flowList(t))
	// Wide enough that the header does not cut the titles down.
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)

	next, cmd := m.Update(flowlist.NewMsg{})
	m = deliver(t, next.(Model), cmd)
	newKey := m.planKey
	assert.Check(t, cmp.Contains(screen(m), "chunk plan / New plan"))

	m = typeInto(m, "retry-uploads")
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // past Factory
	m = typeInto(m, "retry uploads")
	m, create := pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	// Switch to another plan before factory answers.
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	next, cmd = m.Update(flowlist.OpenMsg{ID: "cache", Title: "cache-installs"})
	m = deliver(t, next.(Model), cmd)
	close(fresh.release)
	m = deliverThen(t, m, create, 0) // the create lands, for the new plan
	assert.Check(t, cmp.Contains(screen(m), "chunk plan / cache-installs"))
	assert.Check(t, !strings.Contains(screen(m), "retry-uploads"), screen(m))

	next, _ = m.Update(flowlist.OpenMsg{ID: "", Title: "New plan"})
	m = next.(Model)
	assert.Check(t, cmp.Equal(m.planKey, newKey))
	assert.Check(t, cmp.Contains(screen(m), "chunk plan / retry-uploads"))
}

// The flow list is drawn under the dashboard's own header, and the two
// together fit the terminal.
func TestFlowListSharesTheDashboardHeader(t *testing.T) {
	flows := flowList(t, circleci.Flow{ID: "p1", Title: "Retry uploads", State: "Review - Human"})
	m := New().WithConnection(chunkd.Connection{}).WithPlans(t.Context(), "", (&fakePlans{}).planner).WithFlowList(flows)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = next.(Model)
	// Up to the list; deeper waits out the flow list's poll.
	m = deliverThen(t, m, m.flows.Init(), 1)

	lines := strings.Split(screen(m), "\n")
	assert.Check(t, cmp.Contains(lines[0], "chunk  1 flow"))
	assert.Check(t, !strings.Contains(lines[0], "Runs"), lines[0])
	assert.Assert(t, strings.HasPrefix(lines[1], "╭─ flows"), lines[1])
	assert.Equal(t, len(lines), 20)
}

// A relative the plan screen opens goes the way of a plan picked in the flow
// list: to its workspace when one is open, or to a new one.
func TestOpenMsgOpensOrReusesAWorkspace(t *testing.T) {
	f := &fakePlans{plan: "# A plan"}
	calls := 0
	planner := func(ctx context.Context, root, id string) (OpenedPlan, error) {
		calls++
		return f.planner(ctx, root, id)
	}
	m := sized(New().WithPlans(t.Context(), "/src/chunk-cli", planner).WithFlowList(flowList(t)))
	next, cmd := m.Update(flowlist.OpenMsg{ID: "parent", Title: "Parent"})
	m = deliver(t, next.(Model), cmd)
	parentKey := m.planKey
	assert.Assert(t, cmp.Equal(calls, 1))

	// The parent's screen opens its sub-plan, which is not open yet.
	next, cmd = m.Update(workspaceMsg{key: parentKey, msg: plan.OpenMsg{ID: "child", Title: "Child"}})
	m = deliver(t, next.(Model), cmd)
	assert.Check(t, cmp.Equal(calls, 2))
	assert.Check(t, cmp.Len(m.planWorkspaces, 2))
	assert.Check(t, m.planKey != parentKey)
	assert.Check(t, cmp.Equal(m.planWorkspaces[m.planKey].store.ID(), "child"))

	// The child's screen opens its parent, whose workspace is open.
	next, _ = m.Update(workspaceMsg{key: m.planKey, msg: plan.OpenMsg{ID: "parent", Title: "Parent"}})
	m = next.(Model)
	assert.Check(t, cmp.Equal(calls, 2), "the planner was called again for an open workspace")
	assert.Check(t, cmp.Equal(m.planKey, parentKey))
	assert.Check(t, m.planOpen)
}

// claimer is a claim func that records each plan it is asked to claim and
// answers err, or the worker scotts-mbp.
type claimer struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (c *claimer) claim(_ context.Context, planID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, planID)
	if c.err != nil {
		return "", c.err
	}
	return "scotts-mbp", nil
}

func (c *claimer) called() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// R on the flow list has the default worker claim the selected flow's plan,
// and the list stays where it is.
func TestResumeFromListClaims(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "claimed",
			want: "claimed by scotts-mbp",
		},
		{
			name: "refused",
			err:  errors.New("no live worker can claim this plan"),
			want: "could not resume Alpha: no live worker can claim this plan",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &claimer{err: tc.err}
			flow := circleci.Flow{ID: "a", Title: "Alpha", State: "build",
				Next: []circleci.Transition{{Actor: "session", TaskCategory: "build-unit"}}}
			m := sized(New().WithConnection(chunkd.Connection{}).
				WithPlans(t.Context(), "/src/chunk-cli", (&fakePlans{}).planner).
				WithPlanClaim(c.claim).WithFlowList(flowList(t, flow)))
			m = deliverThen(t, m, m.flows.Init(), 1)

			next, cmd := m.Update(flowlist.ResumeMsg{ID: "a", Title: "Alpha"})
			m = next.(Model)
			assert.Check(t, cmp.Equal(m.note, "resuming Alpha…"))
			assert.Assert(t, cmd != nil, "the claim was not started")
			msg := cmd()
			assert.Check(t, cmp.DeepEqual(c.called(), []string{"a"}))

			next, refresh := m.Update(msg)
			m = next.(Model)
			assert.Check(t, cmp.Equal(m.note, tc.want))
			assert.Check(t, refresh != nil, "the list was not refreshed")
			assert.Check(t, cmp.Contains(screen(m), "▶ Alpha"), "the list lost its selection:\n%s", screen(m))
			assert.Check(t, cmp.Contains(screen(m), tc.want))
		})
	}
}

// Without a claim func the flow list's resume does nothing.
func TestFlowListResumeWithoutAClaim(t *testing.T) {
	m := planModel(t, &fakePlans{})
	next, cmd := m.Update(flowlist.ResumeMsg{ID: "a", Title: "Alpha"})
	assert.Check(t, cmd == nil)
	assert.Check(t, cmp.Equal(next.(Model).note, ""))
}

// X on the flow list asks, and the confirmed delete goes through the deleter;
// the result is noted and the list refreshed.
func TestFlowListDeleteCallsTheDeleter(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "deleted",
			want: "deleted Alpha",
		},
		{
			name: "refused",
			err:  errors.New("plan has sub-plans"),
			want: "could not delete Alpha: plan has sub-plans",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDeleter{err: tc.err}
			flow := circleci.Flow{ID: "a", Title: "Alpha", State: "refine-proposal"}
			m := sized(New().WithConnection(chunkd.Connection{}).
				WithPlans(t.Context(), "/src/chunk-cli", (&fakePlans{}).planner).
				WithPlanDelete(d).WithFlowList(flowList(t, flow)))
			m = deliverThen(t, m, m.flows.Init(), 1)
			assert.Check(t, cmp.Contains(screen(m), "X delete plan"), "X is not offered:\n%s", screen(m))

			next, cmd := m.Update(flowlist.DeleteMsg{ID: "a", Title: "Alpha"})
			m = next.(Model)
			assert.Check(t, cmp.Equal(m.note, "deleting Alpha…"))
			assert.Assert(t, cmd != nil, "the delete was not started")
			msg := cmd()
			assert.Check(t, cmp.DeepEqual(d.called(), []string{"a"}))

			next, refresh := m.Update(msg)
			m = next.(Model)
			assert.Check(t, cmp.Equal(m.note, tc.want))
			assert.Check(t, refresh != nil, "the list was not refreshed")
			assert.Check(t, cmp.Equal(m.deleting, ""))
		})
	}
}

// X then y, pressed on the dashboard, reach the flow list and delete the
// selected flow through the deleter.
func TestDeleteKeysReachTheListThroughTheDashboard(t *testing.T) {
	d := &fakeDeleter{}
	flow := circleci.Flow{ID: "a", Title: "Alpha", State: "refine-proposal"}
	m := sized(New().WithConnection(chunkd.Connection{}).
		WithPlans(t.Context(), "/src/chunk-cli", (&fakePlans{}).planner).
		WithPlanDelete(d).WithFlowList(flowList(t, flow)))
	m = deliverThen(t, m, m.flows.Init(), 1)

	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'X', Text: "X"})
	assert.Check(t, cmp.Contains(screen(m), "Delete this plan"), "X did not ask:\n%s", screen(m))
	m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	assert.Assert(t, cmd != nil, "y did not confirm")

	next, cmd := m.Update(cmd())
	m = next.(Model)
	assert.Assert(t, cmd != nil, "the delete was not started")
	next, _ = m.Update(cmd())
	m = next.(Model)
	assert.Check(t, cmp.DeepEqual(d.called(), []string{"a"}))
	assert.Check(t, cmp.Equal(m.note, "deleted Alpha"))
}

// Without a deleter the flow list's delete does nothing.
func TestFlowListDeleteWithoutADeleter(t *testing.T) {
	m := planModel(t, &fakePlans{})
	next, cmd := m.Update(flowlist.DeleteMsg{ID: "a", Title: "Alpha"})
	assert.Check(t, cmd == nil)
	assert.Check(t, cmp.Equal(next.(Model).note, ""))
}

// While a delete is in flight, the same plan is not deleted twice, another
// is refused, and the list stops offering X on the one being deleted until
// the result arrives.
func TestDeleteInFlight(t *testing.T) {
	d := &fakeDeleter{gate: make(chan struct{})}
	flow := circleci.Flow{ID: "a", Title: "Alpha", State: "refine-proposal"}
	m := sized(New().WithConnection(chunkd.Connection{}).
		WithPlans(t.Context(), "/src/chunk-cli", (&fakePlans{}).planner).
		WithPlanDelete(d).WithFlowList(flowList(t, flow)))
	m = deliverThen(t, m, m.flows.Init(), 1)

	next, cmd := m.Update(flowlist.DeleteMsg{ID: "a", Title: "Alpha"})
	m = next.(Model)
	assert.Assert(t, cmd != nil)
	assert.Check(t, !strings.Contains(ansi.Strip(m.flows.Content()), "X delete plan"), "X is offered mid-delete:\n%s", ansi.Strip(m.flows.Content()))
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()

	next, again := m.Update(flowlist.DeleteMsg{ID: "a", Title: "Alpha"})
	m = next.(Model)
	assert.Check(t, again == nil, "the same plan was deleted twice")

	next, other := m.Update(flowlist.DeleteMsg{ID: "b", Title: "Beta"})
	m = next.(Model)
	assert.Check(t, other == nil)
	assert.Check(t, cmp.Equal(m.note, "a delete is in progress"))

	close(d.gate)
	<-result
	assert.Check(t, cmp.DeepEqual(d.called(), []string{"a"}))
	next, _ = m.Update(planDeletedMsg{id: "a", title: "Alpha", err: errors.New("refused")})
	m = next.(Model)
	assert.Check(t, cmp.Equal(m.deleting, ""))
	assert.Check(t, cmp.Contains(ansi.Strip(m.flows.Content()), "X delete plan"), "X is still hidden:\n%s", ansi.Strip(m.flows.Content()))

	_, cmd = m.Update(flowlist.DeleteMsg{ID: "b", Title: "Beta"})
	assert.Check(t, cmd != nil, "a new delete did not start")
}

// A plan screen open for a deleted plan is dropped, so opening the plan again
// asks for it anew; one for another plan stays.
func TestDeleteDropsTheOpenPlanScreen(t *testing.T) {
	d := &fakeDeleter{}
	f := &fakePlans{plan: "# Plan"}
	m := planModel(t, f).WithPlanDelete(d)
	for _, id := range []string{"p2", "p1"} {
		next, cmd := m.Update(flowlist.OpenMsg{ID: id, Title: id})
		m = deliverThen(t, next.(Model), cmd, 0)
	}
	assert.Assert(t, m.HasPlan())
	m, _ = m.showFlows()

	next, cmd := m.Update(flowlist.DeleteMsg{ID: "p1", Title: "p1"})
	m = next.(Model)
	m = deliver(t, m, cmd)
	assert.Check(t, cmp.Equal(m.note, "deleted p1"))
	assert.Check(t, cmp.Len(m.planWorkspaces, 1))
	for _, ws := range m.planWorkspaces {
		assert.Check(t, cmp.Equal(ws.store.ID(), "p2"))
	}

	// Opening p1 again asks the planner, instead of reusing the screen.
	f.root = ""
	_, cmd = m.Update(flowlist.OpenMsg{ID: "p1", Title: "p1"})
	assert.Assert(t, cmd != nil, "p1 was not opened anew")
	_ = cmd()
	assert.Check(t, cmp.Equal(f.root, "/src/chunk-cli"), "the planner was not asked")
}

// If the plan shown is the one deleted, the dashboard falls back to the list.
func TestDeleteOfTheShownPlanShowsTheList(t *testing.T) {
	f := &fakePlans{plan: "# Plan"}
	m := planModel(t, f)
	next, cmd := m.Update(flowlist.OpenMsg{ID: "p1", Title: "p1"})
	m = deliverThen(t, next.(Model), cmd, 0)
	assert.Assert(t, m.planOpen)

	next, _ = m.Update(planDeletedMsg{id: "p1", title: "p1"})
	m = next.(Model)
	assert.Check(t, !m.planOpen)
	assert.Check(t, m.flowsOpen)
	assert.Check(t, m.planScreen == nil)
	assert.Check(t, !m.HasPlan())
}
