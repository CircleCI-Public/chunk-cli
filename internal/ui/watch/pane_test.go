package watch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/flowlist"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

var (
	down  = tea.KeyPressMsg{Code: tea.KeyDown}
	ctrlP = tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
)

// paneModel is a dashboard of 110x40 on flows, its list read and no
// workspace open.
func paneModel(t *testing.T, f *fakePlans, flows ...circleci.Flow) Model {
	t.Helper()
	m := sized(New().WithConnection(chunkd.Connection{}).
		WithPlans(t.Context(), "/src/chunk-cli", f.planner).WithFlowList(flowList(t, flows...)))
	return listed(t, m)
}

// flowsOf returns one flow per id, in the order given.
func flowsOf(ids ...string) []circleci.Flow {
	var out []circleci.Flow
	for _, id := range ids {
		out = append(out, circleci.Flow{ID: id, Title: "Flow " + id, State: "refine-proposal", WaitingOn: "human"})
	}
	return out
}

// Selecting a flow opens its workspace in the pane, without the keyboard
// and without resuming its planner; the pane shows its tabs.
func TestSelectedFlowOpensInThePane(t *testing.T) {
	f := &fakePlans{plan: "## 1. Outcome\n\nThe flag exists."}
	m := paneModel(t, f, circleci.Flow{ID: "p1", Title: "Add a flag"})
	assert.Check(t, cmp.Contains(screen(m), "Opening the plan…"))
	assert.Check(t, cmp.Len(f.openedIDs(), 0), "the plan was opened before the selection settled")

	m = settle(t, m)
	s := screen(m)
	assert.Check(t, cmp.Contains(s, "Add a flag")) // the list
	assert.Check(t, cmp.Contains(s, "Overview"))   // the pane's tabs
	assert.Check(t, cmp.Contains(s, "The flag exists."))
	assert.Check(t, m.focus == focusList && !m.fullScreen())
	assert.Check(t, cmp.DeepEqual(f.openedIDs(), []string{"p1"}))
	assert.Check(t, !m.HasPlan(), "a workspace opened to show is not one the developer opened")

	// A note takes a row from the pane, and the workspace is drawn at the
	// size left.
	next, _ := m.Update(planResumedMsg{title: "x", err: errors.New("boom")})
	m = next.(Model)
	_, h := m.flows.DetailSize()
	content := m.planWorkspaces["p1"].screen.Content()
	assert.Check(t, cmp.Equal(strings.Count(content, "\n")+1, h), "the workspace is not as tall as the pane")
	assert.Check(t, strings.Count(screen(m), "\n")+1 <= 40, "the screen is taller than the terminal")
}

// Enter gives the keyboard to the pane, which still shows the list beside it;
// esc and ctrl+p give it back, with the same flow selected.
func TestEnterFocusesThePane(t *testing.T) {
	f := &fakePlans{plan: "## 1. Outcome\n\nThe flag exists."}
	m := settle(t, paneModel(t, f, circleci.Flow{ID: "p1", Title: "Add a flag"}))

	m, cmd := pressKey(m, enter)
	m = deliver(t, m, cmd)
	assert.Assert(t, m.focus == focusPane && !m.fullScreen())
	assert.Check(t, cmp.Contains(screen(m), "Add a flag"), "the list is still drawn")
	before := screen(m)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	assert.Check(t, screen(m) != before, "tab did not reach the workspace")

	m, back := pressKey(m, esc)
	m = deliver(t, m, back)
	assert.Check(t, m.focus == focusList)
	id, _ := m.flows.Selected()
	assert.Check(t, cmp.Equal(id, "p1"))
	assert.Check(t, cmp.Len(m.planWorkspaces, 1), "the workspace stays open")

	m, cmd = pressKey(m, enter)
	m = deliver(t, m, cmd)
	assert.Assert(t, m.focus == focusPane)
	m, _ = pressKey(m, ctrlP)
	assert.Check(t, m.focus == focusList)
	id, _ = m.flows.Selected()
	assert.Check(t, cmp.Equal(id, "p1"))
}

// ctrl+p on the list is not a way back to the last plan: it leaves the
// selection where it is.
func TestCtrlPOnTheListKeepsTheSelection(t *testing.T) {
	f := &fakePlans{plan: "# Plan"}
	m := settle(t, paneModel(t, f, flowsOf("p1", "p2")...))
	m, cmd := pressKey(m, enter)
	m = deliver(t, m, cmd)
	m, _ = pressKey(m, ctrlP)
	m, _ = pressKey(m, down)
	m, _ = pressKey(m, ctrlP)
	id, _ := m.flows.Selected()
	assert.Check(t, cmp.Equal(id, "p2"))
}

// Arrowing past flows opens only the one the selection rests on: a settle
// tick of an old selection does nothing.
func TestArrowingPastFlowsOpensOnlyWhereItSettles(t *testing.T) {
	f := &fakePlans{plan: "# Plan"}
	m := paneModel(t, f, flowsOf("p1", "p2", "p3")...)
	gens := []uint64{m.settleGen}
	for range 2 {
		m, _ = pressKey(m, down)
		gens = append(gens, m.settleGen)
	}
	assert.Check(t, cmp.Equal(m.planKey, "p3"))
	for _, gen := range gens[:2] {
		_, cmd := m.Update(settledMsg{gen: gen})
		assert.Check(t, cmd == nil, "a stale settle did something")
	}
	m = settle(t, m)
	assert.Check(t, cmp.DeepEqual(f.openedIDs(), []string{"p3"}))

	// Opens run independently: the settle of a selection opens its flow
	// while another's open is in flight, and that one, dropped meanwhile, is
	// closed when it lands.
	held := func(t *testing.T, f *fakePlans) (Model, tea.Cmd) {
		t.Helper()
		m := paneModel(t, f, flowsOf("p1", "p2")...)
		next, cmd := m.Update(settledMsg{gen: m.settleGen})
		m = next.(Model)
		assert.Assert(t, cmd != nil && m.planWorkspaces["p1"] != nil)
		return m, cmd
	}
	t.Run("a settle during an open opens its flow too", func(t *testing.T) {
		f := &fakePlans{plan: "# Plan"}
		m, opening := held(t, f)
		m, _ = pressKey(m, down)
		next, cmd := m.Update(settledMsg{gen: m.settleGen})
		m = next.(Model)
		assert.Assert(t, cmd != nil, "a settle did not open a flow while another opened")
		m = deliverThen(t, m, cmd, 0)
		assert.Assert(t, m.planWorkspaces["p2"] != nil && m.planWorkspaces["p2"].state == wsReady, "p2's open did not land")

		m = deliverThen(t, m, opening, 0)
		assert.Check(t, cmp.DeepEqual(f.openedIDs(), []string{"p2", "p1"}))
		assert.Check(t, cmp.DeepEqual(f.closedIDs(), []string{"p1"}))
		assert.Check(t, cmp.Len(m.planWorkspaces, 1))
	})
	t.Run("a flow asked for with enter is kept", func(t *testing.T) {
		f := &fakePlans{plan: "# Plan"}
		m, opening := held(t, f)
		m, cmd := pressKey(m, enter)
		m = deliver(t, m, cmd)
		m, _ = pressKey(m, ctrlP)
		m, _ = pressKey(m, down)
		next, settled := m.Update(settledMsg{gen: m.settleGen})
		m = next.(Model)
		assert.Check(t, settled != nil)

		m = deliverThen(t, m, opening, 0)
		assert.Check(t, cmp.Len(f.closedIDs(), 0))
		ws := m.planWorkspaces["p1"]
		assert.Assert(t, ws != nil && ws.active && ws.state == wsReady)
		id, _ := m.flows.Selected()
		assert.Check(t, cmp.Equal(id, "p2"), "the list's selection moved on with the keyboard")
	})
}

// A workspace opened only to show goes when the selection leaves it, unless
// the developer used it.
func TestLeavingAPassiveWorkspaceClosesIt(t *testing.T) {
	t.Run("never used", func(t *testing.T) {
		f := &fakePlans{plan: "# Plan"}
		m := settle(t, paneModel(t, f, flowsOf("p1", "p2")...))
		m, _ = pressKey(m, down)
		assert.Check(t, cmp.DeepEqual(f.closedIDs(), []string{"p1"}), "the workspace outlived the selection")
		m = settle(t, m)
		assert.Assert(t, cmp.Len(m.planWorkspaces, 1))
		assert.Check(t, m.planWorkspaces["p2"] != nil)
	})
	t.Run("focused once", func(t *testing.T) {
		f := &fakePlans{plan: "# Plan"}
		m := settle(t, paneModel(t, f, flowsOf("p1", "p2")...))
		m, cmd := pressKey(m, enter)
		m = deliver(t, m, cmd)
		m, back := pressKey(m, esc)
		m = deliver(t, m, back)
		m, _ = pressKey(m, down)
		m = settle(t, m)
		assert.Check(t, cmp.Len(f.closedIDs(), 0))
		assert.Check(t, cmp.Len(m.planWorkspaces, 2))
	})
}

// Only a pane that has had the keyboard resumes its flow's planner; one the
// selection merely rests on does not.
func TestOnlyAnActivePaneResumesItsPlanner(t *testing.T) {
	f := &fakePlans{plan: "# Plan", flow: &plan.Flow{State: "refine-proposal", WaitingOn: "session"}}
	m := settle(t, paneModel(t, f, circleci.Flow{ID: "p1", Title: "Flow p1", State: "refine-proposal", WaitingOn: "session"}))
	m = deliverThen(t, m, pollOf(m), 1)
	assert.Check(t, cmp.Equal(f.resumed(), 0), "a workspace resumed before it had the keyboard")

	m, cmd := pressKey(m, enter)
	_ = deliverThen(t, m, cmd, 2)
	assert.Check(t, cmp.Equal(f.resumed(), 1))
}

// Where no pane fits, enter shows the workspace across the screen, and
// crossing the width moves it between there and the pane, keeping what was
// typed.
func TestNarrowTerminalOpensFullScreen(t *testing.T) {
	f := &fakePlans{plan: "# Retry uploads\n\n## 1. Outcome\n\nRetry transient failures."}
	m := paneModel(t, f, circleci.Flow{ID: "p1", Title: "Retry uploads"})
	m = resized(m, 60)
	w, h := m.flows.DetailSize()
	assert.Check(t, w == 0 && h == 0, "the list draws a pane at 60 columns")
	assert.Check(t, !strings.Contains(screen(m), "Opening the plan"), screen(m))

	m, cmd := pressKey(m, enter)
	m = deliverThen(t, m, cmd, 1)
	assert.Assert(t, m.focus == focusPane && m.fullScreen())
	m = deliver(t, m, pollOf(m))
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeInto(m, "half a comment")
	assert.Assert(t, cmp.Equal(m.planScreen().Unsent(), "1 unsent comment"))
	key := m.planKey

	m = resized(m, 110)
	assert.Check(t, m.focus == focusPane && !m.fullScreen())
	assert.Check(t, cmp.Equal(m.planKey, key))
	assert.Check(t, cmp.Equal(m.planScreen().Unsent(), "1 unsent comment"))
	assert.Check(t, cmp.Contains(screen(m), "half a comment"))
	assert.Check(t, cmp.Contains(screen(m), "Retry uploads"), "the list is drawn beside the pane")

	m = resized(m, 60)
	assert.Check(t, m.focus == focusPane && m.fullScreen())
	assert.Check(t, cmp.Equal(m.planScreen().Unsent(), "1 unsent comment"))

	// esc and ctrl+p leave the full-screen workspace for the list.
	m, _ = pressKey(m, ctrlP)
	assert.Check(t, m.focus == focusList && !m.fullScreen())
}

// Enter before the workspace has opened opens it at once; the settle that
// follows has nothing to do.
func TestEnterSkipsTheSettle(t *testing.T) {
	f := &fakePlans{plan: "# Plan"}
	m := paneModel(t, f, flowsOf("p1")...)
	m, cmd := pressKey(m, enter)
	m = deliverThen(t, m, cmd, 1)
	assert.Check(t, m.focus == focusPane)
	_, late := m.Update(settledMsg{gen: m.settleGen})
	assert.Check(t, late == nil, "the settle opened the workspace again")
	assert.Check(t, cmp.DeepEqual(f.openedIDs(), []string{"p1"}))
}

// An open that fails is shown in the pane, not the note, and Enter tries
// again.
func TestAFailedOpenIsShownInThePaneAndRetried(t *testing.T) {
	f := &fakePlans{plan: "# Plan", err: errors.New("no git remote named origin")}
	m := settle(t, paneModel(t, f, flowsOf("p1")...))
	s := screen(m)
	assert.Check(t, cmp.Contains(s, "Could not open the plan: no git remote named origin"))
	assert.Check(t, cmp.Equal(m.note, ""))
	assert.Check(t, m.planScreen() == nil)

	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	m, cmd := pressKey(m, enter)
	m = deliverThen(t, m, cmd, 1)
	assert.Check(t, m.focus == focusPane)
	assert.Check(t, cmp.Equal(m.planScreen().ID(), "p1"))
	assert.Check(t, !strings.Contains(screen(m), "Could not open the plan"), screen(m))

	// Selecting the flow again retries it too, without the keyboard.
	t.Run("selecting the flow again retries", func(t *testing.T) {
		f := &fakePlans{plan: "# Plan", err: errors.New("no git remote named origin")}
		m := settle(t, paneModel(t, f, flowsOf("p1", "p2")...))
		assert.Assert(t, cmp.Contains(screen(m), "Could not open the plan"))

		f.setErr(nil)
		m, _ = pressKey(m, down)
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
		m = settle(t, m)
		ws := m.planWorkspaces["p1"]
		assert.Assert(t, ws != nil && ws.state == wsReady, "the plan was not opened again")
		assert.Check(t, m.focus == focusList, "a retry on selection took the keyboard")
		assert.Check(t, !ws.active)
		assert.Check(t, !strings.Contains(screen(m), "Could not open the plan"), screen(m))
	})

	// One flow's failure does not stop the open asked for after it.
	t.Run("another flow still opens", func(t *testing.T) {
		f := &fakePlans{plan: "# Plan", errFor: map[string]error{"p1": errors.New("boom")}}
		m := paneModel(t, f, flowsOf("p1", "p2")...)
		next, held := m.Update(settledMsg{gen: m.settleGen})
		m = next.(Model)
		assert.Assert(t, held != nil)
		m, _ = pressKey(m, down)
		m, cmd := pressKey(m, enter)
		m = deliverThen(t, m, cmd, 1)

		m = deliverThen(t, m, held, 1) // p1's failure lands after it was dropped
		assert.Check(t, cmp.DeepEqual(f.openedIDs(), []string{"p2", "p1"}))
		assert.Check(t, m.focus == focusPane)
		assert.Check(t, cmp.Equal(m.planScreen().ID(), "p2"))
		assert.Check(t, cmp.Equal(m.note, ""))
	})

	// An open asked for with Enter that fails is noted, and the list keeps the
	// keyboard.
	t.Run("asked for with enter", func(t *testing.T) {
		f := &fakePlans{plan: "# Plan", err: errors.New("boom")}
		m := paneModel(t, f, flowsOf("p1")...)
		m, cmd := pressKey(m, enter)
		m = deliverThen(t, m, cmd, 1)
		assert.Check(t, m.focus == focusList)
		assert.Check(t, cmp.Equal(m.note, "could not open the plan: boom"))
		assert.Check(t, !m.HasPlan())
	})
}

// A flow that leaves the list takes its open workspace, and the keyboard,
// with it; the pane follows the new selection.
func TestAFlowLeavingTheListClosesItsWorkspace(t *testing.T) {
	var mu sync.Mutex
	shown := flowsOf("p1", "p2")
	list := func(context.Context) ([]circleci.Flow, error) {
		mu.Lock()
		defer mu.Unlock()
		return append([]circleci.Flow(nil), shown...), nil
	}
	f := &fakePlans{plan: "# Plan"}
	m := sized(New().WithConnection(chunkd.Connection{}).
		WithPlans(t.Context(), "/src/chunk-cli", f.planner).
		WithFlowList(flowlist.New(t.Context(), list)))
	m = listed(t, m)
	m = settle(t, m)
	m, cmd := pressKey(m, enter)
	m = deliver(t, m, cmd)
	assert.Assert(t, m.focus == focusPane)

	mu.Lock()
	shown = flowsOf("p2")
	mu.Unlock()
	m = listed(t, m)
	assert.Check(t, m.focus == focusList && m.planScreen() == nil)
	assert.Check(t, cmp.DeepEqual(f.closedIDs(), []string{"p1"}))
	assert.Check(t, cmp.Len(m.planWorkspaces, 0))
	assert.Check(t, cmp.Equal(m.planKey, "p2"))
	assert.Check(t, cmp.Contains(screen(m), "Opening the plan…"))
}

// A new plan's workspace is kept when the keyboard goes back to the list,
// with n returning to it.
func TestNewPlanWorkspaceIsKeptWhenLeft(t *testing.T) {
	f := &fakePlans{}
	m := paneModel(t, f, flowsOf("p1")...)
	m = openPlanScreen(t, m)
	assert.Assert(t, m.focus == focusPane)
	m = typeInto(m, "add a flag")
	m, back := pressKey(m, esc)
	m = deliver(t, m, back)
	assert.Assert(t, m.focus == focusList)
	assert.Check(t, cmp.Equal(m.planKey, "p1"), "the pane follows the list again")

	m = openPlanScreen(t, m)
	assert.Check(t, m.focus == focusPane)
	assert.Check(t, m.planScreen().Unsent() != "", "the request typed before was lost")
	assert.Check(t, cmp.DeepEqual(f.openedIDs(), []string{""}), "a second new plan was started")
}

// A new plan's workspace moves to its plan's ID once factory gives it one,
// and keeps working there.
func TestANewPlanMovesToItsID(t *testing.T) {
	f := &fakePlans{newID: "plan-9", reply: "ok"}
	m := openPlanScreen(t, planModel(t, f))
	assert.Assert(t, m.planWorkspaces[""] != nil)
	ws := m.planWorkspaces[""]
	m = typeInto(m, "add a flag")
	m, cmd := pressKey(m, enter)
	m = deliverThen(t, m, cmd, 1) // the plan is created, then the message sent

	assert.Check(t, m.planWorkspaces[""] == nil)
	assert.Check(t, m.planWorkspaces["plan-9"] == ws, "the workspace did not move to its plan's ID")
	assert.Check(t, cmp.Equal(m.planKey, "plan-9"))
	assert.Check(t, m.focus == focusPane)

	// What it started before the move still reaches it.
	m = deliver(t, m, nextOf(m))
	m = deliver(t, m, pollOf(m))
	assert.Check(t, m.planWorkspaces["plan-9"] == ws)
	assert.Check(t, m.planWorkspaces[""] == nil)
}

// A relative the plan screen opens is selected in the list, and its
// workspace has the keyboard.
func TestARelativeOpenedFromThePlanIsSelected(t *testing.T) {
	f := &fakePlans{plan: "# Plan"}
	m := paneModel(t, f, flowsOf("p1", "p2")...)
	m, cmd := pressKey(m, enter)
	m = deliverThen(t, m, cmd, 1)
	parent := m.planWorkspaces[m.planKey]

	next, cmd := m.Update(workspaceMsg{ws: parent, msg: plan.OpenMsg{ID: "p2", Title: "Flow p2"}})
	m = deliverThen(t, next.(Model), cmd, 0)
	id, _ := m.flows.Selected()
	assert.Check(t, cmp.Equal(id, "p2"))
	assert.Check(t, m.focus == focusPane)
	assert.Check(t, cmp.Equal(m.planScreen().ID(), "p2"))
	assert.Check(t, cmp.Equal(m.planKey, "p2"))
}

// A press in the pane gives it the keyboard, and one in the list takes it
// back; a press on a pane still opening acts as enter.
func TestClickingGivesTheKeyboard(t *testing.T) {
	f := &fakePlans{plan: "# Plan"}
	m := paneModel(t, f, flowsOf("p1")...)

	// Still opening: the press is enter.
	next, cmd := m.Update(tea.MouseClickMsg{X: 60, Y: 10, Button: tea.MouseLeft})
	m = next.(Model)
	assert.Assert(t, cmd != nil && m.planWorkspaces["p1"] != nil && m.planWorkspaces["p1"].active, "a press on the pane did not ask for the workspace")
	m = deliverThen(t, m, cmd, 0)
	assert.Assert(t, m.focus == focusPane)

	m = click(t, m, 5, 5)
	assert.Check(t, m.focus == focusList, "a press in the list left the pane the keyboard")
	m = click(t, m, 60, 10)
	assert.Check(t, m.focus == focusPane, "a press in the pane did not give it the keyboard")
	assert.Check(t, cmp.Len(f.openedIDs(), 1))
	assert.Check(t, cmp.Equal(m.planScreen().ID(), "p1"))

	// The wheel scrolls whichever is under it, and changes neither's focus.
	wheel := func(m Model, x int) Model {
		next, _ := m.Update(tea.MouseWheelMsg{X: x, Y: 5, Button: tea.MouseWheelDown})
		return next.(Model)
	}
	inPane := theme.ListWidth + 10
	m = wheel(m, 3)
	assert.Check(t, m.focus == focusPane, "the wheel over the list took the keyboard")
	m = click(t, m, 3, 5)
	m = wheel(m, inPane)
	assert.Check(t, m.focus == focusList, "the wheel gave the pane the keyboard")

	// A press on a pane that shows an error retries the open, as enter does.
	t.Run("an error pane", func(t *testing.T) {
		f := &fakePlans{plan: "# Plan", err: errors.New("boom")}
		m := settle(t, paneModel(t, f, flowsOf("p1")...))
		assert.Assert(t, cmp.Contains(screen(m), "Could not open the plan"))

		f.setErr(nil)
		next, cmd := m.Update(tea.MouseClickMsg{X: inPane, Y: 5, Button: tea.MouseLeft})
		m = next.(Model)
		assert.Assert(t, cmd != nil)
		m = deliverThen(t, m, cmd, 0)
		assert.Check(t, cmp.DeepEqual(f.openedIDs(), []string{"p1", "p1"}))
		assert.Check(t, m.focus == focusPane)
		assert.Check(t, cmp.Equal(m.planScreen().ID(), "p1"))
	})
}

// The pane is as large as the list leaves it, which its note shrinks.
func TestPaneSizeFollowsTheFooter(t *testing.T) {
	f := &fakePlans{plan: "# Plan"}
	m := settle(t, paneModel(t, f, flowsOf("p1")...).WithPlanResume(&fakeResumer{}))
	w, h := m.flows.DetailSize()
	assert.Check(t, cmp.Equal(m.sentSize, [2]int{w, h}))

	next, _ := m.Update(flowlist.ResumeMsg{ID: "p1", Title: "Flow p1"})
	m = next.(Model)
	assert.Assert(t, m.note != "")
	w2, h2 := m.flows.DetailSize()
	assert.Check(t, cmp.Equal(w2, w))
	assert.Check(t, cmp.Equal(h2, h-1), "the note did not take a row from the pane")
	assert.Check(t, cmp.Equal(m.sentSize, [2]int{w2, h2}), "the workspace was not given the new size")
}
