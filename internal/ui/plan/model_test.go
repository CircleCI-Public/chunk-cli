package plan

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// fgSeq is the escape sequence that sets c as the foreground, as bgSeq does
// for the background.
func fgSeq(c color.Color) string {
	s := lipgloss.NewStyle().Foreground(c).Render("x")
	before, _, _ := strings.Cut(s, "x")
	return before
}

// quiet stops m's timers, which never settle: its cursors no longer blink,
// and its poll and build timers never fire.
func quiet(m Model) Model {
	for _, in := range []*textarea.Model{&m.input, &m.commentInput} {
		s := in.Styles()
		s.Cursor.Blink = false
		in.SetStyles(s)
	}
	m.tick = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	return m
}

// drive feeds msg to m, then runs each command it returns and feeds back
// what they produce, until the screen settles. It quiets m first, so the
// screen settles without waiting on its timers.
func drive(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	m = quiet(m)
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		var cmd tea.Cmd
		m, cmd = m.Update(queue[0])
		queue = append(queue[1:], run(t, cmd)...)
		if len(queue) == 0 {
			// What a fake agent says is handed over once the screen has
			// settled, as the agent would answer after the send.
			if a, ok := m.agent.(*fakeAgent); ok {
				for _, ev := range a.take() {
					queue = append(queue, eventMsg{agent: a, Event: ev})
				}
			}
		}
	}
	return m
}

// cmdWait bounds how long run waits on one command. Nothing the screen
// does in a test should come near it once quiet has stopped the timers.
const cmdWait = 10 * time.Second

func run(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var got tea.Msg
	select {
	case got = <-done:
	case <-time.After(cmdWait):
		name := runtime.FuncForPC(reflect.ValueOf(cmd).Pointer()).Name()
		t.Errorf("%s did not answer in %s: is a timer still running?", name, cmdWait)
		return nil
	}
	switch msg := got.(type) {
	case tea.BatchMsg:
		// A batch's commands run at once, as bubbletea runs them, so its
		// timers cost one wait between them rather than one each. What they
		// produce comes back in the batch's order.
		outs := make([][]tea.Msg, len(msg))
		var wg sync.WaitGroup
		for i, c := range msg {
			wg.Go(func() { outs[i] = run(t, c) })
		}
		wg.Wait()
		return slices.Concat(outs...)
	case eventMsg, sentMsg, resumedMsg, approveStepMsg, approvedMsg, designApprovedMsg, startedMsg, refreshedMsg, familiedMsg, commentSavedMsg, queueSavedMsg, createdMsg, editSavedMsg, buildReadMsg, buildResumedMsg, buildStoppedMsg, takeOverDoneMsg:
		return []tea.Msg{msg}
	}
	return nil
}

// turnReturning is an agent that answers every message with reply. The
// plan's text comes from the store, not the agent, so there is nothing else
// to return.
func turnReturning(reply string) *fakeAgent {
	a := newFakeAgent()
	a.reply = func(string) []Event { return []Event{{Reply: &Reply{Text: reply}}} }
	return a
}

// refreshed runs a poll, as the screen's own timer would, and drives its
// result through m, deterministically rather than waiting on the timer.
func refreshed(t *testing.T, m Model) Model {
	t.Helper()
	m = quiet(m)
	for _, msg := range run(t, m.Poll()) {
		m = drive(t, m, msg)
	}
	return m
}

// The chat and comment cursors blink on a screen from New; only the tests'
// quiet stops them.
func TestScreenCursorsBlink(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	assert.Check(t, m.input.Styles().Cursor.Blink)
	assert.Check(t, m.commentInput.Styles().Cursor.Blink)
	blink := m.commentInput.Focus()
	assert.Check(t, blink != nil, "a focused comment editor should blink")
	q := quiet(m)
	blink = q.commentInput.Focus()
	assert.Check(t, blink == nil, "quiet should stop the blink")
}

func TestFirstMessageRendersPlanAndReply(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Add a flag\n\n## 1. Outcome\n\nThe flag exists."
	agent := turnReturning("Wrote a first plan.")
	m := New(context.Background(), agent, nil, store, "add a --dry-run flag")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	if got := agent.Sent(); len(got) != 1 || got[0] != "add a --dry-run flag" {
		t.Fatalf("agent got %q", got)
	}
	view := ansi.Strip(m.Content())
	for _, want := range []string{"The flag exists.", "Wrote a first plan.", "add a --dry-run flag", "Activity"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
	assert.Check(t, !strings.Contains(view, chatPlaceholder), "the request input is still shown:\n%s", view)

	fresh := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	fresh = drive(t, fresh, tea.WindowSizeMsg{Width: 120, Height: 40})
	assert.Check(t, cmp.Contains(ansi.Strip(fresh.Content()), "Activity"))
	assert.Check(t, cmp.Contains(ansi.Strip(fresh.Content()), chatPlaceholder))
}

// Enter on an empty or blank request makes no plan and starts no session.
func TestEmptyRequestCreatesNothing(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	agent := newFakeAgent()
	m := New(context.Background(), agent, nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, press("enter"))
	m = typeText(t, m, "   ")
	drive(t, m, press("enter"))
	assert.Check(t, cmp.Equal(store.Created, false))
	assert.Check(t, cmp.Len(agent.Sent(), 0))
}

func TestFirstSendCreatesPlan(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	var createdBeforeTurn bool
	agent := newFakeAgent()
	agent.reply = func(string) []Event {
		createdBeforeTurn = store.Created
		return nil
	}
	m := New(context.Background(), agent, nil, store, "add a --dry-run flag\nmore detail")
	drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	if !store.Created {
		t.Fatal("Create was not called")
	}
	if store.Message != "add a --dry-run flag\nmore detail" {
		t.Errorf("Create got %q, want the developer's whole message", store.Message)
	}
	if !createdBeforeTurn {
		t.Error("the turn started before the plan was created")
	}
}

func TestExistingPlanOpensWithoutStartingOrRecreatingIt(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.Created, store.TextV = true, "# Existing plan\n\n## 1. Outcome\n\nKeep this outcome."
	store.FlowV.Next = []Step{{Actor: "human", Event: "address-comments"}}
	store.CreateErr = errors.New("an existing plan must not be recreated")
	agent := turnReturning("Keeping the existing plan.")
	m := New(t.Context(), agent, nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 32})
	m = refreshed(t, m)
	if len(agent.Sent()) != 0 || !strings.Contains(ansi.Strip(m.Content()), "Keep this outcome.") {
		t.Fatal("opening should read the plan without sending an agent message")
	}
	assert.Check(t, cmp.Equal(m.focus, panePlan), "an existing plan opens on the plan pane")
	m, _ = m.focusOn(paneChat)
	m = typeText(t, m, "explain the outcome")
	m = drive(t, m, press("enter"))
	assert.Check(t, cmp.Len(agent.Sent(), 0))
	assert.Check(t, cmp.Equal(agent.Resumes(), 0), "a plan waiting on the developer was resumed")
	assert.Check(t, cmp.Equal(m.input.Value(), ""))
}

func TestApprovingApprovesThePlanInFactory(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.UnitsV = []Unit{{Name: "Unit 1: Add the flag"}}
	store.StateV = "refine-units"
	store.setEvents("address-comments", "approve-for-building", "abandon")
	store.setRequired("approve-for-building")
	agent := turnReturning("ok")
	sent := false
	approve := func(func(string)) (string, error) {
		sent = true
		return "build-1", nil
	}
	m := New(context.Background(), agent, approve, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !m.confirming {
		t.Fatal("ctrl+s should ask to confirm")
	}
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if !sent || m.BuildID != "build-1" {
		t.Fatalf("approved %v, build %q", sent, m.BuildID)
	}
}

// A plan still with the agent, its review among what it waits on, is not
// sent: the factory would build what the review has not passed.
func TestAPlanNotReadyIsNotSent(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists."
	store.StateV = "refine-units"
	store.FlowV = Flow{Claimed: true, WaitingOn: "session", Next: []Step{
		{Actor: "session", Task: "review-plan", Dispatched: true},
		{Actor: "human", Event: "approve-for-building", Required: true},
	}}
	sent := false
	approve := func(func(string)) (string, error) {
		sent = true
		return "build-1", nil
	}
	m := New(context.Background(), turnReturning("ok"), approve, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	if !strings.Contains(view, "factory is reviewing the plan") || strings.Contains(view, "Next: you approve the plan · ctrl+s") {
		t.Errorf("header does not say the plan is in review:\n%s", view)
	}
	if !strings.Contains(view, "wait for the review") {
		t.Errorf("the action bar offers to approve a plan in review:\n%s", view)
	}
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.confirming {
		t.Fatal("ctrl+s asked to send a plan in review")
	}
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if sent {
		t.Fatal("a plan in review was sent")
	}
}

// ReadyToApprove lets a plan be approved only while factory waits on the
// developer and requires an approval; one it lists but does not require is
// not offered.
func TestReadyToApprove(t *testing.T) {
	t.Parallel()
	human := func(event string, required bool) Step {
		return Step{Actor: "human", Event: event, Required: required}
	}
	for _, tc := range []struct {
		name string
		flow Flow
		want bool
	}{
		{
			name: "human, building required",
			flow: Flow{State: "refine-units", WaitingOn: "human", Next: []Step{
				human("address-comments", false), human("approve-for-building", true), human("abandon", false),
			}},
			want: true,
		},
		{
			name: "human, answer-open-questions required",
			flow: Flow{State: "refine-proposal", WaitingOn: "human", Next: []Step{
				human("answer-open-questions", true), human("approve-for-units", false), human("abandon", false),
			}},
		},
		{
			name: "session, building required",
			flow: Flow{State: "refine-units", WaitingOn: "session", Next: []Step{
				{Actor: "session", Task: "review-plan", Dispatched: true}, human("approve-for-building", true),
			}},
		},
		{
			name: "human, units not required, nothing required",
			flow: Flow{State: "refine-proposal", WaitingOn: "human", Next: []Step{
				human("address-comments", false), human("approve-for-units", false), human("abandon", false),
			}},
		},
		{
			name: "human, building not required, nothing required",
			flow: Flow{State: "refine-proposal", WaitingOn: "human", Next: []Step{human("approve-for-building", false)}},
		},
		{
			name: "human, no approval",
			flow: Flow{State: "build", WaitingOn: "human", Next: []Step{human("address-comments", false)}},
		},
		{
			name: "ended",
			flow: Flow{State: "ended", Ended: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Check(t, cmp.Equal(ReadyToApprove(tc.flow), tc.want))
		})
	}
}

// A plan in a new-spelled planning state is approved as the old one is,
// and one whose review runs is not.
func TestANewNamedPlanIsApprovedFromNext(t *testing.T) {
	t.Parallel()
	t.Run("waits on you", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
		store.StateV = "refine-units"
		store.setEvents("address-comments", "approve-for-building", "abandon")
		store.setRequired("approve-for-building")
		sent := false
		approve := func(func(string)) (string, error) {
			sent = true
			return "build-1", nil
		}
		m := New(context.Background(), turnReturning("ok"), approve, store, "do it")
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Next: you approve the plan · ctrl+s"))
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assert.Assert(t, m.confirming, "ctrl+s did not ask to approve: %q", m.status)
		drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		assert.Check(t, sent, "the plan was not sent")
	})
	t.Run("request-revision listed", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
		store.StateV = "refine-proposal"
		store.FlowV = Flow{WaitingOn: "human", Next: []Step{
			{Actor: "human", Event: "address-comments"},
			{Actor: "human", Event: "approve-for-building", Required: true},
			{Actor: "session", Event: "request-revision"},
			{Actor: "human", Event: "abandon"},
		}}
		store.setEvents("address-comments", "approve-for-building", "abandon")
		sent := false
		approve := func(func(string)) (string, error) {
			sent = true
			return "build-1", nil
		}
		m := New(context.Background(), turnReturning("ok"), approve, store, "do it")
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Next: you approve the plan · ctrl+s"))
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assert.Assert(t, m.confirming, "ctrl+s did not ask to approve: %q", m.status)
		drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		assert.Check(t, sent, "the plan was not sent")
	})
	t.Run("review running", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists."
		store.StateV = "refine-units"
		store.FlowV = Flow{Claimed: true, WaitingOn: "session", Next: []Step{
			{Actor: "session", Task: "review-plan", Dispatched: true},
			{Actor: "human", Event: "approve-for-building", Required: true},
		}}
		m := New(context.Background(), turnReturning("ok"), nil, store, "do it")
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "wait for the review"))
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assert.Check(t, !m.confirming, "ctrl+s asked to approve a plan in review")
	})
}

// A plan in a state chunk does not know, as factory may add before chunk is
// updated, or one chunk offers no action for, such as integrate with no
// conflict, is shown in that state, is not said to be in review, and
// is not sent.
func TestAPlanInAnUnknownStateIsShownAsItIs(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"Blocked", "Reviewing - Bot", "integrate", "Paused\x1b[31m", "build", "ended"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists."
			store.StateV = state
			store.setEvents("abandon")
			sent := false
			approve := func(func(string)) (string, error) {
				sent = true
				return "build-1", nil
			}
			m := New(context.Background(), turnReturning("ok"), approve, store, "do it")
			m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
			raw := m.Content()
			assert.Check(t, !strings.Contains(raw, "\x1b[31m"), "the state's escapes were drawn")
			view := ansi.Strip(raw)
			shown := strings.TrimSuffix(state, "\x1b[31m")
			assert.Check(t, !strings.Contains(view, "factory is reviewing the plan"), "a plan in %q is said to be in review:\n%s", state, view)
			assert.Check(t, !strings.Contains(view, "Next: you approve the plan · ctrl+s"), view)
			assert.Check(t, !strings.Contains(view, "PR conflicts"), view)
			assert.Check(t, strings.Contains(view, "the plan is "+shown), "the action bar does not name the state:\n%s", view)
			lines := strings.Split(view, "\n")
			row1 := shown
			if slices.Contains(steps, planstate.Step(shown)) {
				row1 = "Planning › Building › Integrating › Done"
			}
			assert.Check(t, cmp.Contains(lines[headerRows], row1))
			assert.Check(t, !strings.Contains(view, "Plan · "), "the pane's title names the state:\n%s", view)

			m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			assert.Check(t, !m.confirming, "ctrl+s asked to send a plan in %q", state)
			assert.Check(t, strings.Contains(ansi.Strip(m.status), "cannot be approved now: it is "+shown), m.status)
			drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
			assert.Check(t, !sent, "a plan in %q was sent", state)
		})
	}
}

// A plan waits ReadyToApprove after early-plan too, for its design to be
// reviewed: when factory requires approve-for-units, ctrl+s approves the
// design, for factory to write its units, even with approve-for-building
// listed too. The plan's build is not started.
func TestCtrlSApprovesTheDesignWhenFactoryRequiresIt(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n"
	store.setEvents("address-comments", "approve-for-units", "approve-for-building", "abandon")
	store.setRequired("approve-for-units")
	sent := false
	approve := func(func(string)) (string, error) {
		sent = true
		return "build-1", nil
	}
	m := New(context.Background(), turnReturning("ok"), approve, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	if !strings.Contains(view, "Next: you approve the design · ctrl+s") || strings.Contains(view, "Next: you approve the plan · ctrl+s") {
		t.Errorf("header does not say only the design is ready:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+s Approve design") || strings.Contains(view, "ctrl+s Approve plan") {
		t.Errorf("the action bar does not offer to approve the design:\n%s", view)
	}

	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !m.confirming || !strings.Contains(ansi.Strip(m.Content()), "Approve the design") {
		t.Fatalf("ctrl+s did not ask to approve the design:\n%s", ansi.Strip(m.Content()))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if sent {
		t.Fatal("approving the design started the plan's build")
	}
	store.mu.Lock()
	approved, handedOff := slices.Clone(store.DesignApproved), slices.Clone(store.HandedOff)
	store.mu.Unlock()
	if len(approved) != 1 || approved[0] != "refine-proposal" {
		t.Fatalf("the design was not approved from refine-proposal once: %v", approved)
	}
	if len(handedOff) != 0 {
		t.Errorf("approving the design handed factory comments: %v", handedOff)
	}
	assert.Assert(t, m.moving != nil, "no wait for factory after approving the design")
	assert.Check(t, cmp.Equal(m.moving.kind, movingDesign))
	assert.Check(t, cmp.Equal(m.status, ""), "a status was left up: %q", m.status)
	bar := ansi.Strip(m.actionBar())
	assert.Check(t, cmp.Contains(bar, "Design approved · waiting for factory to start the units…"))
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Check(t, !m.confirming, "ctrl+s asked a question while factory has yet to move the plan")
	assert.Check(t, cmp.Equal(m.status, ""), "ctrl+s set a status while waiting: %q", m.status)
	assert.Check(t, cmp.Equal(ansi.Strip(m.actionBar()), bar), "ctrl+s changed the bar while waiting")
}

// A plan whose units are a list rather than headings, as a parent plan's
// sub-plans are, is one factory offers to build: ctrl+s approves the plan,
// not its design.
func TestCtrlSApprovesThePlanWhenFactoryOffersOnlyBuilding(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n\n1. FNDSF-21 api: add it\n2. FNDSF-22 ui: show it\n"
	store.setEvents("address-comments", "approve-for-building", "abandon")
	store.setRequired("approve-for-building")
	sent := false
	approve := func(func(string)) (string, error) {
		sent = true
		return "build-1", nil
	}
	m := New(context.Background(), turnReturning("ok"), approve, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Next: you approve the plan · ctrl+s"))
	assert.Check(t, !strings.Contains(view, "Next: you approve the design · ctrl+s"), view)
	assert.Check(t, cmp.Contains(view, "ctrl+s Approve plan"))

	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Assert(t, m.confirming)
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Approve this plan and build it?"))
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	assert.Check(t, sent, "approving did not start the plan's build")
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Check(t, cmp.Len(store.DesignApproved, 0))
}

// A plan factory still offers only design approval for is a design, even
// with unit headings written by hand.
func TestCtrlSApprovesTheDesignWhenFactoryOffersOnlyUnits(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.UnitsV = []Unit{{Name: "Unit 1: Add the flag"}}
	store.setEvents("address-comments", "approve-for-units", "abandon")
	store.setRequired("approve-for-units")
	sent := false
	approve := func(func(string)) (string, error) {
		sent = true
		return "build-1", nil
	}
	m := New(context.Background(), turnReturning("ok"), approve, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Next: you approve the design · ctrl+s"))
	assert.Check(t, !strings.Contains(view, "Next: you approve the plan · ctrl+s"), view)
	assert.Check(t, cmp.Contains(view, "ctrl+s Approve design"))

	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Assert(t, m.confirming)
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Approve the design"))
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	assert.Check(t, !sent, "approving the design started the plan's build")
	assert.Assert(t, m.moving != nil, "no wait for factory after approving the design")
	assert.Check(t, cmp.Equal(m.moving.kind, movingDesign))
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Check(t, cmp.DeepEqual(store.DesignApproved, []string{"refine-proposal"}))
}

// TestWaitingForFactoryEndsOnceFactoryActs checks that the wait a design
// approval or a send of comments starts lasts until a refresh finds factory
// has acted: the plan moved, a task for it is out to any session, or factory
// waits on the developer again after waiting on someone else.
func TestWaitingForFactoryEndsOnceFactoryActs(t *testing.T) {
	t.Parallel()
	// poll is what one refresh reads, and whether the screen still waits
	// for factory after it. state "" is "refine-proposal". resolved is
	// whether the planner has resolved the plan's comments by then, as an
	// address-comments task does.
	type poll struct {
		state, waitingOn, task string
		resolved, waiting      bool
	}
	tests := []struct {
		name string
		// send is what starts the wait: "design" approves the design,
		// "comments" sends one comment.
		send  string
		polls []poll
		// approves is whether ctrl+s asks to approve once the wait is over.
		approves bool
	}{
		{
			name: "agent",
			send: "design",
			polls: []poll{
				{waiting: true},
				{state: "refine-units"},
			},
		},
		{
			name: "a state chunk does not know",
			send: "design",
			polls: []poll{
				{waiting: true},
				{state: "Blocked"},
			},
		},
		{
			name: "comments: the task goes to another session",
			send: "comments",
			polls: []poll{
				{waitingOn: "session", waiting: true},
				{waitingOn: "session", task: "address-comments"},
			},
		},
		{
			name: "comments: the task goes out and back between two polls",
			send: "comments",
			polls: []poll{
				{waitingOn: "session", waiting: true},
				{waitingOn: "human", resolved: true},
			},
			approves: true,
		},
		{
			name: "comments: factory has not applied the event yet",
			send: "comments",
			polls: []poll{
				{waitingOn: "human", waiting: true},
				{waitingOn: "human", waiting: true},
			},
		},
		{
			name: "comments: the task is due and no session takes it",
			send: "comments",
			polls: []poll{
				{waitingOn: "session", waiting: true},
				{waitingOn: "session", waiting: true},
			},
		},
		{
			name: "comments: a conductor that does not serve waiting_on",
			send: "comments",
			polls: []poll{
				{waiting: true},
				{task: "address-comments"},
			},
		},
		{
			name: "comments: a task of another category was already out",
			send: "comments",
			polls: []poll{
				{waitingOn: "session", task: "early-plan"},
			},
		},
		{
			name: "design: write-units goes to another session",
			send: "design",
			polls: []poll{
				{waitingOn: "session", waiting: true},
				{waitingOn: "session", task: "write-units"},
			},
		},
		{
			name: "design: write-units goes out and back between two polls",
			send: "design",
			polls: []poll{
				{waitingOn: "session", waiting: true},
				{waitingOn: "human"},
			},
			approves: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.setWaitingOn("human") // factory waits on the developer when they send
			var m Model
			switch tc.send {
			case "design":
				store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n"
				m = New(context.Background(), turnReturning("ok"), func(func(string)) (string, error) { return "build-1", nil }, store, "do it")
				m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
				m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
				m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
			case "comments":
				store.TextV = twoParts
				m = sendOneComment(t, store)
				m = drive(t, m, press("s"))
			}
			assert.Assert(t, m.moving != nil, "the send started no wait")

			for i, p := range tc.polls {
				state := p.state
				if state == "" {
					state = "refine-proposal"
				}
				store.mu.Lock()
				store.StateV, store.TaskV, store.FlowV.WaitingOn = state, p.task, p.waitingOn
				if p.resolved {
					for j := range store.CommentsV {
						store.CommentsV[j].Resolved = true
					}
				}
				store.mu.Unlock()
				m = drive(t, m, pollMsg{})
				assert.Check(t, cmp.Equal(m.moving != nil, p.waiting), "after poll %d", i+1)
			}
			if m.moving != nil {
				assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "waiting for factory"))
				return
			}
			assert.Check(t, !strings.Contains(ansi.Strip(m.actionBar()), "waiting for factory"), m.actionBar())
			assert.Check(t, !strings.Contains(m.actionBar(), fgSeq(m.purple())), "the bar's border stayed purple after the wait")
			if tc.approves {
				m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
				assert.Check(t, m.confirming, "ctrl+s did not ask to approve once the wait was over: %q", ansi.Strip(m.status))
			}

			if tc.name != "agent" {
				return
			}
			store.mu.Lock()
			store.StateV, store.FlowV.WaitingOn = "refine-units", string(ActorHuman)
			store.TextV += "\n### Unit 1: add the flag\n\nParse it in main.go.\n"
			store.UnitsV = []Unit{{Name: "Unit 1: add the flag"}}
			store.mu.Unlock()
			m = drive(t, m, pollMsg{})
			assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "ctrl+s Approve plan"))
		})
	}
}

// TestQuestionInTheStatusBarOutlivesAChangeOfState checks that a question
// waiting on the developer's y stays up when a refresh finds the plan in
// another state.
func TestQuestionInTheStatusBarOutlivesAChangeOfState(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n"
	m := New(context.Background(), turnReturning("ok"), nil, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Assert(t, m.confirming)

	store.mu.Lock()
	store.StateV = "refine-units"
	store.mu.Unlock()
	m = drive(t, m, pollMsg{})
	assert.Check(t, m.confirming)
	assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "Approve the design"))
}

// A design with comments still to send or resolve is not approved: those
// would have factory address them, not write the units.
func TestCtrlSWaitsForCommentsBeforeApprovingTheDesign(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		model func(t *testing.T) (Model, *fakeStore)
	}{
		{
			name:  "no units",
			model: planModel,
		},
		{
			name: "units, factory offers only design approval",
			model: func(t *testing.T) (Model, *fakeStore) {
				t.Helper()
				store := newFakeStore()
				store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
				store.setEvents("address-comments", "approve-for-units")
				m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
				m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
				return m, store
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, store := tc.model(t)
			m = drive(t, m, press("tab"))
			m = drive(t, m, press("c"))
			m = typeText(t, m, "too vague")
			m = drive(t, m, press("enter"))

			m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			assert.Check(t, !m.confirming, "ctrl+s asked to approve a design with a comment queued")
			assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Send or resolve your comments first"))
			store.mu.Lock()
			defer store.mu.Unlock()
			assert.Check(t, cmp.Len(store.DesignApproved, 0))
		})
	}
}

// A plan whose flow requires answer-open-questions waits for its open
// questions to be answered, though it lists the design's approval: ctrl+s
// approves nothing and says so.
func TestCtrlSApprovesNothingWhileQuestionsAreOpen(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 3. Open Questions\n\n* Which store?\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.setEvents("address-comments", "answer-open-questions", "approve-for-units", "abandon")
	store.setRequired("answer-open-questions")
	sent := false
	approve := func(func(string)) (string, error) {
		sent = true
		return "build-1", nil
	}
	m := New(context.Background(), turnReturning("ok"), approve, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Next: you answer 3. Open Questions in comments"))
	assert.Check(t, !strings.Contains(view, "Next: you approve the plan · ctrl+s"), view)
	assert.Check(t, !strings.Contains(view, "Next: you approve the design · ctrl+s"), view)
	assert.Check(t, !strings.Contains(view, "Approve plan"), view)
	assert.Check(t, !strings.Contains(view, "Approve design"), view)

	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Check(t, !m.confirming, "ctrl+s asked to approve a plan with questions open")
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Answer 3. Open Questions in comments first"))
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	assert.Check(t, !m.confirming)
	assert.Check(t, !sent, "ctrl+s started the plan's build with questions open")
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Check(t, cmp.Len(store.DesignApproved, 0))
}

func TestAFailedDesignApprovalSaysSo(t *testing.T) {
	t.Parallel()
	m, store := planModel(t)
	store.AddressErr = errors.New("factory is down")
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.approvingDesign {
		t.Error("still approving after the approval failed")
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "Could not approve the design: factory is down") {
		t.Errorf("the failure is not shown:\n%s", view)
	}
	assert.Check(t, m.moving == nil, "a failed event started the wait")
	assert.Check(t, !strings.Contains(ansi.Strip(m.actionBar()), "waiting for factory"))
}

// TestWaitingBarYieldsToStatuses checks that a prompt or an error during the
// wait for factory shows in the action bar over the waiting line, which
// would otherwise hide it until the refresh that ends the wait clears it.
func TestWaitingBarYieldsToStatuses(t *testing.T) {
	t.Parallel()
	m, store := planModel(t)
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("s"))
	assert.Assert(t, m.moving != nil, "no wait for factory after the hand-off")

	// The quit prompt shows over the waiting line, on the purple border.
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "more")
	m = drive(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	bar := ansi.Strip(m.actionBar())
	assert.Check(t, cmp.Contains(bar, "will be lost"))
	assert.Check(t, !strings.Contains(bar, "waiting for factory"), bar)
	assert.Check(t, strings.Contains(m.actionBar(), fgSeq(m.purple())), "the bar's border is not purple under the quit prompt")
	m = drive(t, m, press("x"))
	m = drive(t, m, press("esc"))
	assert.Assert(t, !m.commenting)
	assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "waiting for factory"))

	// An error shows over it too.
	m = drive(t, m, commentSavedMsg{err: errors.New("edit failed")})
	assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "edit failed"))

	// The refresh that ends the wait clears the error with it.
	store.mu.Lock()
	store.StateV = "refine-units"
	store.mu.Unlock()
	m = drive(t, m, pollMsg{})
	assert.Check(t, m.moving == nil, "the wait outlived the plan moving")
	bar = ansi.Strip(m.actionBar())
	assert.Check(t, !strings.Contains(bar, "edit failed"), bar)
	assert.Check(t, cmp.Contains(bar, "ctrl+s Approve plan"))
}

func TestWaitingTitleNamesTheMove(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		from string
		want string
	}{
		{
			name: "planning",
			from: "refine-proposal",
			want: "Plan · refine-proposal → …",
		},
		{
			name: "building",
			from: "build",
			want: "Plan · build → …",
		},
		{
			name: "unknown state",
			from: "Blocked",
			want: "Plan · Blocked → …",
		},
		{
			name: "escapes and a newline",
			from: "\x1b[31mPlanning\n - Human",
			want: "Plan · Planning - Human → …",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.TextV = twoParts
			store.StateV = tc.from
			m := New(context.Background(), turnReturning("ok"), nil, store, "")
			m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
			m = refreshed(t, m)
			m.moving = &transition{kind: movingComments, from: tc.from, since: time.Now()}

			raw := m.Content()
			assert.Check(t, cmp.Contains(ansi.Strip(raw), tc.want))
			assert.Check(t, !strings.Contains(raw, "\x1b[31m"), "the state's escapes were drawn")
			lines := strings.Split(ansi.Strip(raw), "\n")
			assert.Assert(t, len(lines) > 1)
			assert.Check(t, cmp.Contains(lines[headerRows+stripRows], tc.want))
		})
	}
}

func TestFocusedPlanPaneTitleHasNoPercentage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		keys   []string
		moving bool
		want   string
	}{
		{
			name: "plan tab",
			keys: []string{"tab", "down", "down"},
			want: "Flow  Plan",
		},
		{
			name: "flow tab",
			keys: []string{"tab", "left", "down", "down"},
			want: "Flow  Plan",
		},
		{
			name:   "moving a comment",
			keys:   []string{"tab", "down"},
			moving: true,
			want:   "Flow  Plan · ",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _ := planModel(t)
			m = refreshed(t, m)
			for _, k := range tc.keys {
				m = drive(t, m, press(k))
			}
			if tc.moving {
				m.moving = &transition{kind: movingComments, from: m.state, since: time.Now()}
			}
			assert.Assert(t, cmp.Equal(m.focus, panePlan))

			top := strings.Split(ansi.Strip(m.Content()), "\n")[headerRows+stripRows]
			assert.Check(t, cmp.Contains(top, "Activity"))
			assert.Check(t, cmp.Contains(top, tc.want))
			assert.Check(t, !regexp.MustCompile(`\d+%`).MatchString(top), top)
		})
	}
}

func TestWaitingBarSaysHowLongOnceItIsSlow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ago  time.Duration
		// want is the elapsed time the bar shows, or "" for none.
		want string
	}{
		{
			name: "not yet slow",
			ago:  5 * time.Second,
			want: "",
		},
		{
			name: "slow",
			ago:  20 * time.Second,
			want: "20s",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _ := planModel(t)
			m.moving = &transition{kind: movingComments, from: m.state, since: time.Now().Add(-tc.ago)}
			bar := ansi.Strip(m.actionBar())
			if tc.want == "" {
				assert.Check(t, !regexp.MustCompile(`\d+s\b`).MatchString(bar), bar)
				return
			}
			assert.Check(t, cmp.Contains(bar, tc.want))
		})
	}
}

// press is a key press of k, named as tea names it.
func press(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "shift+down":
		return tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}
	case "shift+up":
		return tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m = drive(t, m, press(string(r)))
	}
	return m
}

const twoParts = "## 1. Outcome\n\nThe flag exists.\n\n## 2. Design\n\n* [AGENT] parse it in main.go"

// planModel is a screen whose plan is already text, with turn replying ok.
func planModel(t *testing.T) (Model, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	store.TextV = twoParts
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	return m, store
}

func TestSSavesCommentsFromThePlan(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	agent := turnReturning("ok")
	m := New(context.Background(), agent, nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	agent.reset()

	// Keys in the Activity pane, then a comment in the plan.
	m = typeText(t, m, "draft")
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))
	if bar := ansi.Strip(m.actionBar()); !strings.Contains(bar, "s Save 1 comment") {
		t.Errorf("the action bar does not offer s:\n%s", bar)
	}

	m = drive(t, m, press("s"))
	if len(store.Added) != 1 || store.Added[0][1] != "too vague" {
		t.Fatalf("comments saved: %+v", store.Added)
	}
	if len(store.HandedOff) != 1 || store.HandedOff[0] != "refine-proposal" {
		t.Errorf("handed off: %+v", store.HandedOff)
	}
	if got := agent.Sent(); len(got) != 0 {
		t.Errorf("the keys in the Activity pane were sent: %q", got)
	}
	if m.focus != panePlan || len(queuedComments(m.comments)) != 0 || m.input.Value() != "" {
		t.Errorf("focus %v, comments %+v, draft %q", m.focus, m.comments, m.input.Value())
	}
	assert.Assert(t, m.moving != nil, "no wait for factory after the hand-off")
	assert.Check(t, cmp.Equal(m.moving.kind, movingComments))
	assert.Check(t, cmp.Equal(m.moving.from, "refine-proposal"))

	bar := ansi.Strip(m.actionBar())
	assert.Check(t, cmp.Contains(bar, "Comments sent · waiting for factory to hand them to chunk…"))
	assert.Check(t, !strings.Contains(bar, "Approve"), "the bar offers to approve while waiting: %q", bar)
	assert.Check(t, strings.Contains(m.actionBar(), fgSeq(m.purple())), "the bar's border is not purple while waiting")
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Waiting for factory"))
	assert.Check(t, !strings.Contains(view, "Next: you approve the design · ctrl+s") && !strings.Contains(view, "Next: you approve the plan · ctrl+s"), view)
	assert.Check(t, cmp.Contains(view, "Plan · refine-proposal → …"))
}

// sendOneComment is a plan screen on store, refreshed, with a comment "too
// vague" queued on the plan's first part, and the plan pane focused.
func sendOneComment(t *testing.T, store *fakeStore) Model {
	t.Helper()
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	// Set rather than typed: each key press leaves a blink timer that run
	// waits out.
	m.commentInput.SetValue("too vague")
	return drive(t, m, press("enter"))
}

func TestSFollowsWhatFactoryShows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		waitingOn      string
		events         []string
		offerOnComment bool
		takeOnComment  bool
		takeOnHandOff  bool
		addressErr     error
		flowErr        error
		// want
		offered     bool
		saved       int
		handedOff   int
		waiting     bool // the screen waits for factory, as after a hand-off
		status      string
		retryIsOwed bool
	}{
		{
			name:           "first comment, factory offers address-comments after the save",
			waitingOn:      "human",
			events:         []string{"approve-for-units", "abandon"},
			offerOnComment: true,
			offered:        true,
			saved:          1,
			handedOff:      1,
			waiting:        true,
		},
		{
			name:          "first comment, factory takes the comments itself",
			waitingOn:     "human",
			events:        []string{"approve-for-units", "abandon"},
			takeOnComment: true,
			offered:       true,
			saved:         1,
			waiting:       true,
		},
		{
			name:      "waiting on a session with address-comments still offered",
			waitingOn: "session",
			events:    []string{"address-comments", "abandon"},
			offered:   true,
			saved:     1,
			handedOff: 1,
			waiting:   true,
		},
		{
			name:      "factory works on the plan",
			waitingOn: "session",
			events:    []string{"abandon"},
		},
		{
			name:      "factory neither takes nor asks for the comments",
			waitingOn: "human",
			events:    []string{"abandon"},
			offered:   true,
			saved:     1,
			status:    "Comments saved.",
		},
		{
			name:          "the daemon dispatches between the read and the send",
			waitingOn:     "human",
			events:        []string{"address-comments", "abandon"},
			takeOnHandOff: true,
			offered:       true,
			saved:         1,
			waiting:       true,
		},
		{
			name:        "the hand-off is refused and factory has not taken the comments",
			waitingOn:   "human",
			events:      []string{"address-comments", "abandon"},
			addressErr:  errors.New("refused"),
			offered:     true,
			saved:       1,
			status:      "hand the comments to factory",
			retryIsOwed: true,
		},
		{
			name:        "the flow read after the save fails",
			waitingOn:   "human",
			events:      []string{"approve-for-units"},
			flowErr:     errors.New("boom"),
			offered:     true,
			saved:       1,
			status:      "read the plan's flow: boom",
			retryIsOwed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.TextV = twoParts
			store.OfferOnComment, store.TakeOnComment, store.TakeOnHandOff = tc.offerOnComment, tc.takeOnComment, tc.takeOnHandOff
			store.AddressErr = tc.addressErr // fails AddressComments, leaving next as it is
			store.setEvents(tc.events...)
			store.setWaitingOn(tc.waitingOn)
			m := sendOneComment(t, store)

			bar := ansi.Strip(m.actionBar())
			assert.Check(t, cmp.Equal(strings.Contains(bar, "s Save 1 comment"), tc.offered), bar)
			if !tc.offered {
				assert.Check(t, cmp.Contains(bar, "1 comment held until factory is waiting on you"))
			}
			if tc.flowErr != nil {
				store.mu.Lock()
				store.FlowErr = tc.flowErr
				store.mu.Unlock()
			}
			m = drive(t, m, press("s"))

			assert.Check(t, cmp.Len(store.Added, tc.saved))
			assert.Check(t, cmp.Len(store.HandedOff, tc.handedOff))
			assert.Check(t, cmp.Equal(m.moving != nil, tc.waiting))
			assert.Check(t, cmp.Equal(m.pendingHandOff, tc.retryIsOwed))
			if tc.status != "" {
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), tc.status))
			}
		})
	}
}

func TestASendRetriesTheHandOffOnceTheFlowReads(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.OfferOnComment = true
	store.setEvents("approve-for-units")
	store.setWaitingOn("human")
	m := sendOneComment(t, store)
	store.mu.Lock()
	store.FlowErr = errors.New("boom")
	store.mu.Unlock()
	m = drive(t, m, press("s"))
	assert.Assert(t, m.pendingHandOff)

	store.mu.Lock()
	store.FlowErr = nil
	store.mu.Unlock()
	m = refreshed(t, m)
	m = drive(t, m, press("s"))
	assert.Check(t, cmp.Len(store.Added, 1), "the comment was saved twice")
	assert.Check(t, cmp.Len(store.HandedOff, 1))
	assert.Check(t, !m.pendingHandOff)
}

func TestSSavesCommentsWhileChunkWorks(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))

	// Comments go to factory, not to the agent, so a busy agent does not
	// hold them up.
	m.busy = true
	drive(t, m, press("s"))
	if len(store.Added) != 1 {
		t.Errorf("comments saved while chunk works: %+v", store.Added)
	}
}

func TestCommentEditorOpensUnderItsBlock(t *testing.T) {
	t.Parallel()
	m, _ := planModel(t)
	m = drive(t, m, press("tab"))
	for range 3 {
		m = drive(t, m, press("down"))
	}
	m = drive(t, m, press("c"))
	m = typeText(t, m, "abc")

	lines := strings.Split(ansi.Strip(m.planView.View()), "\n")
	bulletLine, abcLine := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "parse it in main.go") {
			bulletLine = i
		}
		if strings.Contains(l, "abc") {
			abcLine = i
		}
	}
	// The editor's card has its header line between the bullet and the text.
	if bulletLine < 0 || abcLine != bulletLine+2 || !strings.Contains(lines[bulletLine+1], "your comment") {
		t.Fatalf("comment editor at line %d does not come right after the bullet at %d:\n%s", abcLine, bulletLine, strings.Join(lines, "\n"))
	}
	footer := ansi.Strip(m.planFooter())
	if strings.Contains(footer, "abc") || strings.Contains(footer, "c to comment") {
		t.Errorf("the plan footer should not hold the editor: %q", footer)
	}
}

func TestCommentTakesNewLines(t *testing.T) {
	t.Parallel()
	m, _ := planModel(t)
	m = drive(t, m, press("tab"))
	for range 3 {
		m = drive(t, m, press("down"))
	}
	m = drive(t, m, press("c"))
	m = typeText(t, m, "a")
	m = drive(t, m, tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	m = typeText(t, m, "b")
	m = drive(t, m, press("enter"))

	if len(m.comments) != 1 || m.comments[0].text != "a\nb" {
		t.Fatalf("comments: %+v", m.comments)
	}
	view := ansi.Strip(m.planView.View())
	lines := strings.Split(view, "\n")
	aLine := -1
	for i, l := range lines {
		if strings.HasSuffix(strings.TrimRight(l, " "), "┃a") {
			aLine = i
		}
	}
	if aLine < 0 || aLine+1 >= len(lines) || !strings.HasSuffix(strings.TrimRight(lines[aLine+1], " "), "b") {
		t.Fatalf("a and b are not on separate consecutive lines:\n%s", view)
	}
}

func TestCommentEditorGrowsWithWrappedText(t *testing.T) {
	t.Parallel()
	m, _ := planModel(t)
	m = drive(t, m, press("tab"))
	for range 3 {
		m = drive(t, m, press("down"))
	}
	m = drive(t, m, press("c"))
	if h := m.commentInput.Height(); h != 1 {
		t.Errorf("an empty comment editor is %d rows tall, want 1", h)
	}

	// Text that wraps past one row grows the editor, and text that wraps
	// past six stops at six and scrolls inside itself.
	width := m.commentInput.Width()
	m.commentInput.SetValue(strings.Repeat("x", width+1))
	if h := m.commentInput.Height(); h != 2 {
		t.Errorf("a comment editor on two wrapped rows is %d rows tall, want 2", h)
	}
	m.commentInput.SetValue(strings.Repeat("x", (maxCommentRows+3)*width))
	if h := m.commentInput.Height(); h != maxCommentRows {
		t.Errorf("a comment editor past %d wrapped rows is %d rows tall, want %d", maxCommentRows, h, maxCommentRows)
	}
}

func TestCommentTakesMoreLinesThanTheEditorShows(t *testing.T) {
	t.Parallel()
	m, _ := planModel(t)
	m = drive(t, m, press("tab"))
	for range 3 {
		m = drive(t, m, press("down"))
	}
	m = drive(t, m, press("c"))

	// The editor shows six rows at most; the comment itself goes on past
	// them, as a message in the chat does.
	var want []string
	for i := range maxCommentRows + 3 {
		if i > 0 {
			m = drive(t, m, tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
		}
		m = typeText(t, m, fmt.Sprintf("line%d", i))
		want = append(want, fmt.Sprintf("line%d", i))
	}
	if h := m.commentInput.Height(); h != maxCommentRows {
		t.Errorf("comment editor is %d rows tall, want %d", h, maxCommentRows)
	}

	m = drive(t, m, press("enter"))
	if len(m.comments) != 1 || m.comments[0].text != strings.Join(want, "\n") {
		t.Fatalf("comments: %+v, want one of %d lines", m.comments, len(want))
	}
}

func TestEscCancelsTheCommentInPlace(t *testing.T) {
	t.Parallel()
	m, _ := planModel(t)
	m = drive(t, m, press("tab"))
	for range 3 {
		m = drive(t, m, press("down"))
	}
	m = drive(t, m, press("c"))
	m = typeText(t, m, "abc")
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if len(m.comments) != 0 {
		t.Fatalf("comments: %+v", m.comments)
	}
	if view := ansi.Strip(m.Content()); strings.Contains(view, "abc") {
		t.Errorf("the cancelled comment is still shown:\n%s", view)
	}
}

func TestCommentFollowsItsBlockWhenThePlanChanges(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 3, quote: "* [AGENT] parse it in main.go", text: "x"}}

	setPlan(&m, strings.Replace(twoParts, "## 1. Outcome\n\n", "## 1. Outcome\n\nAdded.\n\n", 1))
	m.renderPlan()
	if got := m.comments[0].block; got != 4 {
		t.Errorf("comment is on block %d, want 4", got)
	}

	setPlan(&m, "## 1. Outcome\n\nRewritten.")
	m.renderPlan()
	if got := m.comments[0].block; got != -1 {
		t.Errorf("comment on a rewritten block is on %d, want -1", got)
	}
}

func TestCursorDoesNotHideTheCommentBar(t *testing.T) {
	t.Parallel()
	m, _ := planModel(t)
	m = drive(t, m, press("tab"))
	for range 3 {
		m = drive(t, m, press("down"))
	}
	m = drive(t, m, press("c"))
	m = typeText(t, m, "use cobra flags")
	m = drive(t, m, press("enter"))

	find := func(lines []string, want string) int {
		for i, l := range lines {
			if strings.Contains(l, want) {
				return i
			}
		}
		return -1
	}
	lines := strings.Split(ansi.Strip(m.planView.View()), "\n")
	bulletLine := find(lines, "parse it in main.go")
	commentLine := find(lines, "use cobra flags")
	if bulletLine < 0 || commentLine < 0 {
		t.Fatalf("could not find the bullet or its comment:\n%s", strings.Join(lines, "\n"))
	}
	for _, i := range []int{bulletLine, commentLine} {
		if !strings.HasPrefix(lines[i], "▌┃") {
			t.Errorf("line %d does not carry both bars: %q", i, lines[i])
		}
	}

	m = drive(t, m, press("up"))
	lines = strings.Split(ansi.Strip(m.planView.View()), "\n")
	for _, i := range []int{bulletLine, commentLine} {
		if !strings.HasPrefix(lines[i], " ┃") {
			t.Errorf("line %d should have lost the cursor bar: %q", i, lines[i])
		}
	}
	headingLine := find(lines, "DESIGN")
	if headingLine < 0 || !strings.HasPrefix(lines[headingLine], "▌ ") {
		t.Errorf("the heading line should carry the cursor bar alone: %q", lines[headingLine])
	}
}

// assertFits fails when any line of the view is wider than width, or the
// view taller than height: either breaks the screen in a real terminal.
func assertFits(t *testing.T, m Model, width, height int) {
	t.Helper()
	lines := strings.Split(m.Content(), "\n")
	if len(lines) > height {
		t.Errorf("view is %d lines, screen is %d", len(lines), height)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("line %d is %d wide, screen is %d: %q", i, w, width, ansi.Strip(l))
		}
	}
}

func TestScreenFitsTheTerminal(t *testing.T) {
	t.Parallel()
	for _, width := range []int{60, 80, 110} {
		store := newFakeStore()
		store.TextV = twoParts
		m := New(context.Background(), turnReturning("ok"), nil, store, "")
		m = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 24})
		assertFits(t, m, width, 24)

		m = drive(t, m, press("x"))
		m = drive(t, m, press("enter"))
		m = drive(t, m, press("tab"))
		// A narrow screen's tab goes to the Flow tab first.
		m = drive(t, m, press("right"))

		m = drive(t, m, press("c"))
		m.commentInput.SetValue(strings.Repeat("a long word ", 20) + "\nand a second line")
		m.moveCursor(m.cursor)
		assertFits(t, m, width, 24)
		m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

		m = drive(t, m, press("c"))
		m = typeText(t, m, "too vague")
		m = drive(t, m, press("enter"))
		assertFits(t, m, width, 24)

		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assertFits(t, m, width, 24)

		// Waiting for factory once the comment is handed off.
		m = drive(t, m, press("n"))
		m = drive(t, m, press("tab"))
		m = drive(t, m, press("enter"))
		assert.Check(t, m.moving != nil, "no wait for factory after the hand-off at width %d", width)
		assertFits(t, m, width, 24)
		bar := ansi.Strip(m.actionBar())
		if width < narrowBelow {
			assert.Check(t, cmp.Contains(bar, "Waiting for factory…"), "width %d", width)
		} else {
			assert.Check(t, cmp.Contains(bar, "Comments sent · waiting for factory to hand them to chunk…"), "width %d", width)
		}
	}
}

// The pane with the keyboard is drawn in chunk's brand accent, as in chunk
// watch, and follows the terminal's background: yellow on a dark one, navy
// on a light one.
func TestFocusedPaneUsesTheBrandAccent(t *testing.T) {
	t.Parallel()
	for _, dark := range []bool{true, false} {
		m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "").WithTerminal(dark, false)
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
		st := theme.New(dark)
		top := strings.Split(m.Content(), "\n")[headerRows+stripRows]
		focused := lipgloss.NewStyle().Foreground(st.Brand.GetForeground()).Render("╭─ ")
		quiet := lipgloss.NewStyle().Foreground(st.VDim.GetForeground()).Render("╭─ ")
		if !strings.HasPrefix(top, focused) {
			t.Errorf("dark=%v: the focused chat pane's edge is not the accent: %q", dark, top)
		}
		if !strings.Contains(top, quiet) {
			t.Errorf("dark=%v: the plan pane without the keyboard is not quiet: %q", dark, top)
		}
	}
}

// The message box takes one step less contrast than the pane's own accent
// border: dim while the chat has the keyboard, quiet once the plan pane
// does, and never the pane's own accent colour either way.
func TestMessageBoxIsBelowThePaneAccent(t *testing.T) {
	t.Parallel()
	// check asserts the message box's title row starts right after a chat
	// pane gutter drawn in gutterColor, with its own edge in edgeColor, and
	// never in the brand accent.
	check := func(t *testing.T, m Model, gutterColor, edgeColor color.Color, label string) {
		t.Helper()
		var line string
		for _, l := range strings.Split(m.Content(), "\n") {
			if strings.Contains(l, "Message") {
				line = l
				break
			}
		}
		if line == "" {
			t.Fatalf("%s: no line with the message box's title: %q", label, m.Content())
		}
		gutter := lipgloss.NewStyle().Foreground(gutterColor).Render("│") + " "
		rest, ok := strings.CutPrefix(line, gutter)
		if !ok {
			t.Fatalf("%s: the message box does not start right after the pane's gutter: %q", label, line)
		}
		wantEdge := lipgloss.NewStyle().Foreground(edgeColor).Render("╭─ ")
		if !strings.HasPrefix(rest, wantEdge) {
			t.Errorf("%s: the message box's edge is not as expected: %q", label, line)
		}
		// Only the box's own top edge, not the pane's right border further
		// along the same (horizontally joined) line, which may be the accent.
		box := rest
		if end := strings.Index(rest, "╮"); end >= 0 {
			box = rest[:end+len("╮")]
		}
		if strings.Contains(box, fgSeq(m.brand())) {
			t.Errorf("%s: the message box took the pane's own accent colour: %q", label, line)
		}
	}

	for _, dark := range []bool{true, false} {
		m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "").WithTerminal(dark, false)
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
		// The chat has the keyboard: its own gutter is the accent, and the
		// message box one step down, dim.
		check(t, m, m.brand(), m.dim(), fmt.Sprintf("dark=%v chat focused", dark))

		// Tab moves the keyboard to the plan pane: the chat's gutter goes
		// quiet, and the message box follows it to quiet too.
		m = drive(t, m, press("tab"))
		check(t, m, m.quiet(), m.quiet(), fmt.Sprintf("dark=%v plan focused", dark))
	}
}

// The message box, like the panes, sits on the terminal's own background: no
// ground behind it and no band on the cursor's line.
func TestMessageBoxSitsOnTheTerminalsBackground(t *testing.T) {
	t.Parallel()
	for _, dark := range []bool{true, false} {
		m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "").WithTerminal(dark, false)
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
		lines := strings.Split(m.Content(), "\n")
		start := -1
		for i, l := range lines {
			if strings.Contains(l, "Message") {
				start = i
				break
			}
		}
		if start < 0 {
			t.Fatalf("dark=%v: no line with the message box's title: %q", dark, m.Content())
		}
		for i := start; i < start+inputBoxRows && i < len(lines); i++ {
			if strings.Contains(lines[i], "\x1b[48;") {
				t.Errorf("dark=%v: line %d of the message box paints a background: %q", dark, i, lines[i])
			}
		}
	}
}

// The request box is six rows tall, and the chat viewport above it gives up
// the rows.
func TestRequestBoxIsSixRowsTall(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "").WithTerminal(true, false)
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if inputRows != 6 {
		t.Fatalf("inputRows = %d, want 6", inputRows)
	}
	lines := strings.Split(m.Content(), "\n")
	start, end := -1, -1
	for i, l := range lines {
		if start < 0 && strings.Contains(l, "Message") {
			start = i
		}
		if start >= 0 && strings.Contains(l, "╰") {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatalf("no message box in %q", m.Content())
	}
	if got := end - start + 1; got != inputBoxRows {
		t.Errorf("box is %d lines from title to closing border, want inputBoxRows = %d", got, inputBoxRows)
	}

	m.input.SetValue("zz1\nzz2\nzz3\nzz4\nzz5\nzz6\nzz7")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	shown := 0
	for _, l := range strings.Split(m.Content(), "\n") {
		if strings.Contains(l, "zz") {
			shown++
		}
	}
	if shown != 6 {
		t.Errorf("%d lines of the seven-line message show, want 6", shown)
	}

	short := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "").WithTerminal(true, false)
	short = drive(t, short, tea.WindowSizeMsg{Width: 120, Height: 10})
	if h := short.chatView.Height(); h < 1 {
		t.Errorf("viewport height at 10 rows = %d, want at least 1", h)
	}
}

// The developer's message bubbles are content, not focus: they get a faint
// border, never the pane's accent.
func TestUserBubbleIsQuiet(t *testing.T) {
	t.Parallel()
	for _, dark := range []bool{true, false} {
		m := New(context.Background(), turnReturning("ok"), nil, newFakeStore(), "").WithTerminal(dark, false)
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
		m = typeText(t, m, "hello there")
		m = drive(t, m, press("enter"))

		var top string
		for _, l := range strings.Split(m.Content(), "\n") {
			if strings.Contains(l, fgSeq(m.faint())+"╭") {
				top = l
				break
			}
		}
		if top == "" {
			t.Fatalf("dark=%v: no bubble border drawn with faint: %q", dark, m.Content())
		}
		if strings.Contains(top, fgSeq(m.brand())+"╭") {
			t.Errorf("dark=%v: the user bubble took the pane's accent colour: %q", dark, top)
		}
	}
}

// The panes and the action bar sit on the terminal's own background, as
// chunk watch's do: only what is set apart (the block the cursor is on,
// comments, code) has a ground of its own.
func TestPanesSitOnTheTerminalsBackground(t *testing.T) {
	t.Parallel()
	for _, dark := range []bool{true, false} {
		m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "").WithTerminal(dark, false)
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
		lines := strings.Split(m.Content(), "\n")
		for _, i := range []int{2, len(lines) - 2} {
			if strings.Contains(lines[i], "\x1b[48;") {
				t.Errorf("dark=%v: line %d paints a background: %q", dark, i, lines[i])
			}
		}
	}
}

func TestChatStaysWhereTheDeveloperScrolled(t *testing.T) {
	t.Parallel()
	agent := turnReturning(strings.Repeat("A long reply.\n\n", 30))
	m := New(context.Background(), agent, nil, newFakeStore(), "first")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if !m.chatView.AtBottom() {
		t.Fatal("the chat should follow a new reply")
	}

	m.chatView.ScrollUp(5)
	m.busy = true
	m, _ = m.Update(m.spin.Tick())
	if m.chatView.AtBottom() {
		t.Error("a spinner tick scrolled the chat back to the bottom")
	}
}

func TestQuitAsksAgainWhenCommentsAreUnsent(t *testing.T) {
	t.Parallel()
	m, _ := planModel(t)
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))

	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	var cmd tea.Cmd
	m, cmd = m.Update(ctrlC)
	if cmd != nil {
		t.Fatal("ctrl+c with an unsent comment quit at once")
	}
	if !strings.Contains(ansi.Strip(m.Content()), "1 unsent comment") {
		t.Errorf("no warning about the unsent comment:\n%s", ansi.Strip(m.Content()))
	}
	if _, cmd := m.Update(ctrlC); cmd == nil {
		t.Error("a second ctrl+c did not quit")
	}

	// Any other key in between starts over.
	next, _ := m.Update(press("j"))
	if _, cmd := next.Update(ctrlC); cmd != nil {
		t.Error("ctrl+c after another key quit without asking")
	}
}

func TestQuitWarnsOfQueuedOnly(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.AddComment(context.Background(), "The flag exists.", "saved")
	m := New(context.Background(), turnReturning("ok"), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)
	if len(m.comments) != 1 || m.comments[0].id == "" {
		t.Fatalf("comments after refresh: %+v", m.comments)
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if got, ok := closedBy(cmd); !ok {
		t.Fatalf("quitting with only a saved comment should close at once, got %+v %v", got, ok)
	}
}

func TestNewlineHintMatchesTheTerminal(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 30})
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "ctrl+j new line") {
		t.Errorf("a terminal that sends shift+enter as enter should be told ctrl+j:\n%s", view)
	}
	m = drive(t, m, tea.KeyboardEnhancementsMsg{Flags: ansi.KittyDisambiguateEscapeCodes})
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "shift+enter new line") {
		t.Errorf("a terminal that tells shift+enter apart should be told it:\n%s", view)
	}

	m2, _ := planModel(t)
	m2 = drive(t, m2, press("tab"))
	m2 = drive(t, m2, press("c"))
	if view := ansi.Strip(m2.Content()); !strings.Contains(view, "ctrl+j new line") {
		t.Errorf("the comment editor's footer should hint ctrl+j on a terminal that can't tell shift+enter apart:\n%s", view)
	}
}

func TestActionBarKeepsApprovingThePlanInReach(t *testing.T) {
	t.Parallel()
	m, _ := planModel(t)
	for range 2 {
		if view := ansi.Strip(m.Content()); !strings.Contains(view, "ctrl+s Approve") {
			t.Errorf("the action bar dropped ctrl+s:\n%s", view)
		}
		m = drive(t, m, press("tab"))
	}
}

// closedBy is the ClosedMsg cmd produces, if it produces one.
func closedBy(cmd tea.Cmd) (ClosedMsg, bool) {
	if cmd == nil {
		return ClosedMsg{}, false
	}
	msg, ok := cmd().(ClosedMsg)
	return msg, ok
}

func TestApprovingThePlanClosesTheScreenWithItsBuild(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	_, cmd := m.Update(approvedMsg{buildID: "build-1"})
	if got, ok := closedBy(cmd); !ok || got.BuildID != "build-1" {
		t.Fatalf("approving the plan closed with %+v, %v", got, ok)
	}
}

// messagesOf runs cmd and every command a batch of it holds, and returns
// what they answer, without the filtering run does.
func messagesOf(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		out = append(out, messagesOf(c)...)
	}
	return out
}

// A host that keeps the screen keeps it after the approval, on the Flow tab,
// with nothing sent from its Activity pane to the agent: the planning session
// has stopped. A comment queued before the approve stays queued, and is saved
// once factory has acted on the approve and takes comments again.
func TestApprovingInAKeptScreenFollowsTheBuild(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.Created = true
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.UnitsV = []Unit{{Name: "Unit 1: Add the flag"}}
	approvals := 0
	approve := func(func(string)) (string, error) {
		approvals++
		return "build-1", nil
	}
	agent := newFakeAgent()
	m := New(context.Background(), agent, approve, store, "").WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = refreshed(t, m)
	m, _ = m.focusOn(panePlan)
	assert.Assert(t, m.focus == panePlan && m.tab == tabPlan, "focus %v, tab %v", m.focus, m.tab)
	m = drive(t, m, press("c"))
	m = typeText(t, m, "before")
	m = drive(t, m, press("enter"))
	assert.Assert(t, cmp.Len(queuedComments(m.comments), 1))
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m, cmd := m.Update(press("y"))
	batch, ok := cmd().(tea.BatchMsg)
	assert.Assert(t, ok && len(batch) == 2, "y did not start the approval")
	m, cmd = m.Update(batch[0]())
	assert.Check(t, cmp.Equal(approvals, 1))

	msgs := messagesOf(cmd)
	assert.Check(t, slices.Contains(msgs, tea.Msg(BuildingMsg{BuildID: "build-1"})), "the screen said %#v", msgs)
	for _, msg := range msgs {
		_, isClosed := msg.(ClosedMsg)
		assert.Check(t, !isClosed, "a kept screen closed on approval")
	}
	assert.Check(t, cmp.Equal(m.tab, tabFlow))
	assert.Check(t, cmp.Equal(m.BuildID, "build-1"))
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Approved · building as run build-1"))

	m, _ = m.focusOn(paneChat)
	for _, k := range []string{"m", "o", "r", "e"} {
		m = drive(t, m, press(k))
	}
	m = drive(t, m, press("enter"))
	assert.Check(t, cmp.Len(agent.Sent(), 0), "the Activity pane sent to the agent")
	assert.Check(t, cmp.Equal(m.input.Value(), ""))

	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Check(t, !m.confirming, "ctrl+s asks to approve a plan already building")
	m = drive(t, m, press("y"))
	assert.Check(t, cmp.Equal(approvals, 1), "the plan was approved again")

	// The comment queued before the approve is kept, and held while factory
	// has yet to act on the approve, though the flow read still waits on you.
	assert.Assert(t, cmp.Len(queuedComments(m.comments), 1))
	m = refreshed(t, m) // the fake store's flow has not moved
	assert.Check(t, m.moving != nil && m.moving.kind == movingApprove)
	m, _ = m.focusOn(panePlan)
	m = drive(t, m, press("right")) // to the Plan tab
	assert.Assert(t, m.focus == panePlan && m.tab == tabPlan, "focus %v, tab %v", m.focus, m.tab)
	assert.Check(t, !m.keys().SendComments.Enabled(), "s offered before factory acted on the approve")
	// The approve's status shows over the waiting line, as any status does.
	assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "Approved · building as run build-1"))
	assert.Check(t, cmp.Contains(ansi.Strip(m.waitingLine(200)), "Plan approved · waiting for factory to start the build"))
	m = drive(t, m, press("s"))
	m, _ = m.focusOn(paneChat)
	m = drive(t, m, press("enter")) // the Activity pane's send, held too
	assert.Check(t, cmp.Len(store.Added, 0))
	assert.Check(t, cmp.Len(store.HandedOff, 0))
	assert.Check(t, cmp.Len(queuedComments(m.comments), 1))
	m, _ = m.focusOn(panePlan)

	// Factory moves the plan on; it waits on you again, so comments go out.
	store.mu.Lock()
	store.StateV = "build"
	store.mu.Unlock()
	m = refreshed(t, m)
	assert.Check(t, m.moving == nil)
	assert.Assert(t, m.takesComments())
	if m.tab != tabPlan {
		m = drive(t, m, press("right")) // back to the Plan tab
	}
	assert.Assert(t, m.focus == panePlan && m.tab == tabPlan, "focus %v, tab %v", m.focus, m.tab)
	assert.Check(t, m.keys().SendComments.Enabled(), "s not offered after the approve")
	m = drive(t, m, press("s"))
	assert.Check(t, cmp.DeepEqual(store.Added, [][2]string{{"## 5. Units", "before"}}))
	assert.Check(t, cmp.Len(queuedComments(m.comments), 0))
}

// TestApprovingWithQueuedCommentsSaysWhatHappensToThem checks the approve
// prompt with a comment queued: a screen that closes on approval warns that
// it is lost, and one that stays open keeps it and asks as usual.
func TestApprovingWithQueuedCommentsSaysWhatHappensToThem(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		back bool
	}{
		{
			name: "closes on approval",
		},
		{
			name: "stays open",
			back: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
			store.UnitsV = []Unit{{Name: "Unit 1: Add the flag"}}
			store.setEvents("address-comments", "approve-for-building")
			store.setRequired("approve-for-building")
			approve := func(func(string)) (string, error) { return "build-1", nil }
			m := New(context.Background(), turnReturning("ok"), approve, store, "plan it")
			if tc.back {
				m = m.WithBack()
			}
			m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
			m = drive(t, m, press("tab"))
			m = drive(t, m, press("c"))
			m = typeText(t, m, "too vague")
			m = drive(t, m, press("enter"))
			assert.Assert(t, cmp.Len(queuedComments(m.comments), 1))

			m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			assert.Check(t, m.confirming)
			status := ansi.Strip(m.status)
			if !tc.back {
				assert.Check(t, cmp.Contains(status, "Your 1 unsent comment will be lost when this screen closes. Approve this plan and build it?"))
				return
			}
			assert.Check(t, cmp.Contains(status, "Approve this plan and build it?"))
			assert.Check(t, !strings.Contains(status, "lost"), "status %q", status)
		})
	}
}

// While the plan is approved and its build started, the screen says which
// step it is on, and the header's spinner turns.
func TestApprovingShowsEachStep(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.UnitsV = []Unit{{Name: "Unit 1: Add the flag"}}
	release := make(chan struct{})
	approve := func(step func(string)) (string, error) {
		step("Approving the plan in factory…")
		step("Starting the build on the chunk daemon…")
		<-release
		return "build-1", nil
	}
	m := New(context.Background(), turnReturning("ok"), approve, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	m, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("approving did not start the approval and the spinner: %#v", cmd())
	}
	wait, tick := batch[0], batch[1]
	for _, want := range []string{"Approving the plan in factory…", "Starting the build on the chunk daemon…"} {
		step, ok := wait().(approveStepMsg)
		if !ok {
			t.Fatalf("no step %q", want)
		}
		m, wait = m.Update(step)
		if view := ansi.Strip(m.Content()); !strings.Contains(view, want) {
			t.Errorf("the step %q is not shown:\n%s", want, view)
		}
	}

	// The spinner keeps turning while the approval works, with nothing busy.
	_, next := m.Update(tick())
	if next == nil {
		t.Error("the spinner stopped while approving")
	}

	close(release)
	m, cmd = m.Update(wait())
	if got, ok := closedBy(cmd); !ok || got.BuildID != "build-1" {
		t.Fatalf("the approval closed with %+v, %v", got, ok)
	}
	if _, next = m.Update(tick()); next != nil {
		t.Error("the spinner turns on once the approval is done")
	}
}

func TestAFailedApprovalKeepsTheScreen(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.approving = true
	m, cmd := m.Update(approvedMsg{err: errors.New("factory is down")})
	if _, ok := closedBy(cmd); ok {
		t.Fatal("a failed approval closed the screen")
	}
	if m.approving || m.BuildID != "" {
		t.Errorf("approving %v, build %q after a failed approval", m.approving, m.BuildID)
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "Could not approve the plan: factory is down") {
		t.Errorf("the failure is not shown:\n%s", view)
	}
}

func TestBackClosesAtOnceAndKeepsWhatIsUnsent(t *testing.T) {
	t.Parallel()
	m, _ := planModel(t)
	m = m.WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))
	h := m.keys().Quit.Help()
	assert.Check(t, cmp.Equal(h.Key+" "+h.Desc, "esc back"))

	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got, ok := closedBy(cmd); !ok || got.BuildID != "" {
		t.Fatalf("esc closed with %+v, %v", got, ok)
	}
	if len(m.comments) != 1 {
		t.Errorf("closing dropped the comment: %+v", m.comments)
	}
}

func TestBackEscLeavesWhileChunkWorks(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "").WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	m.busy = true

	// The session is the host's and goes on working, so leaving stops
	// nothing.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, ok := closedBy(cmd); !ok {
		t.Fatal("esc while chunk works did not go back")
	}
}

// longPlan is a plan too long for the pane, with no comments.
func longPlan() string {
	var b strings.Builder
	b.WriteString("## 1. Outcome\n\n")
	for i := range 60 {
		fmt.Fprintf(&b, "Paragraph %d of the plan.\n\n", i)
	}
	return b.String()
}

func longPlanModel(t *testing.T) Model {
	t.Helper()
	store := newFakeStore()
	store.TextV = longPlan()
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	return drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
}

func TestDOnABlockWithoutACommentDoesNotScroll(t *testing.T) {
	t.Parallel()
	m := longPlanModel(t)
	m = drive(t, m, press("tab"))
	for _, k := range []string{"d", "u", "f", "b", " "} {
		m = drive(t, m, press(k))
		if m.planView.YOffset() != 0 || m.cursor != 0 {
			t.Fatalf("%q moved the plan: offset %d, cursor %d", k, m.planView.YOffset(), m.cursor)
		}
	}
}

func TestPagingMovesTheCursorWithTheView(t *testing.T) {
	t.Parallel()
	m := longPlanModel(t)
	m = drive(t, m, press("tab"))

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	off := m.planView.YOffset()
	if off == 0 || m.blockTop[m.cursor] < off || m.blockTop[m.cursor] >= off+m.planView.Height() {
		t.Fatalf("pgdown: offset %d, cursor %d at line %d", off, m.cursor, m.blockTop[m.cursor])
	}
	half := drive(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if half.planView.YOffset() >= off || half.cursor >= m.cursor {
		t.Errorf("ctrl+u: offset %d, cursor %d", half.planView.YOffset(), half.cursor)
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.planView.YOffset() != 0 || m.cursor != 0 {
		t.Errorf("pgup back: offset %d, cursor %d", m.planView.YOffset(), m.cursor)
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.cursor != 0 {
		t.Errorf("pgup at the top left the cursor on %d", m.cursor)
	}
}

func TestPageKeysScrollTheChat(t *testing.T) {
	t.Parallel()
	agent := turnReturning(strings.Repeat("A long reply.\n\n", 30))
	m := New(context.Background(), agent, nil, newFakeStore(), "first")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "pgup/pgdn scroll activity") {
		t.Errorf("activity scrolling is not in the footer:\n%s", view)
	}
	bottom := m.chatView.YOffset()
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.chatView.YOffset() >= bottom {
		t.Fatalf("pgup left the chat at %d", m.chatView.YOffset())
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if !m.chatView.AtBottom() {
		t.Error("pgdown did not scroll back down")
	}
}

func TestEnterDoesNotConfirmSendingThePlan(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists."
	sent := false
	approve := func(func(string)) (string, error) {
		sent = true
		return "build-1", nil
	}
	m := New(context.Background(), turnReturning("ok"), approve, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = drive(t, m, press("enter"))
	if sent || m.confirming {
		t.Fatalf("enter answered the question: sent %v, still asking %v", sent, m.confirming)
	}
}

func TestTwoCommentsOnIdenticalBlocksStayOnSeparateBlocks(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, "## 2. Design\n\n* Tests\n* Tests")
	m.renderPlan()
	m.comments = []comment{
		{block: 1, quote: "* Tests", text: "first"},
		{block: 2, quote: "* Tests", text: "second"},
	}

	setPlan(&m, "## 1. Outcome\n\n## 2. Design\n\n* Tests\n* Tests")
	m.renderPlan()
	a, b := m.comments[0].block, m.comments[1].block
	if a == b || min(a, b) != 2 || max(a, b) != 3 {
		t.Errorf("comments are on blocks %d and %d, want one each on 2 and 3", a, b)
	}
}

func TestCommentOnARewrittenPartIsStillShown(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 3, quote: "* [AGENT] parse it in main.go", text: "x"}}

	setPlan(&m, "## 1. Outcome\n\nRewritten.")
	m.renderPlan()
	if got := m.comments[0].block; got != -1 {
		t.Fatalf("comment on a rewritten block is on %d, want -1", got)
	}

	view := ansi.Strip(m.planView.View())
	for _, want := range []string{"On parts chunk has since rewritten", `on "* [AGENT] parse it in main.go"`, "x"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
}

func TestCommentOnARewrittenPartCanBeEditedAndDeleted(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 3, quote: "* [AGENT] parse it in main.go", text: "x"}}
	setPlan(&m, "## 1. Outcome\n\nRewritten.")
	m.renderPlan()

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("G"))
	if m.cursor != len(m.blocks) {
		t.Fatalf("cursor is %d, want %d", m.cursor, len(m.blocks))
	}

	m = drive(t, m, press("c"))
	m = typeText(t, m, "y")

	// The editor is drawn in the section itself, under the part the comment
	// was on, with what has been typed into it so far.
	lines := strings.Split(ansi.Strip(m.planView.View()), "\n")
	onLine, editorLine := -1, -1
	for i, l := range lines {
		if strings.Contains(l, `on "* [AGENT] parse it in main.go"`) {
			onLine = i
		}
		if strings.Contains(l, "xy") {
			editorLine = i
		}
	}
	if onLine < 0 || editorLine != onLine+2 || !strings.Contains(lines[onLine+1], "your comment") {
		t.Fatalf("the editor at line %d does not come right after the part at %d:\n%s", editorLine, onLine, strings.Join(lines, "\n"))
	}

	m = drive(t, m, press("enter"))
	if len(m.comments) != 1 || m.comments[0].text != "xy" || m.comments[0].quote != "* [AGENT] parse it in main.go" || m.comments[0].block != -1 {
		t.Fatalf("comment after editing: %+v", m.comments)
	}

	m = drive(t, m, press("d"))
	if len(m.comments) != 0 {
		t.Fatalf("comments after delete: %+v", m.comments)
	}
	if view := ansi.Strip(m.planView.View()); strings.Contains(view, "On parts chunk has since rewritten") {
		t.Errorf("the rewritten-parts section should be gone:\n%s", view)
	}
	if m.cursor != len(m.blocks)-1 {
		t.Errorf("cursor is %d, want %d", m.cursor, len(m.blocks)-1)
	}
}

func TestNextReachesTheNearestCommentThenRewrittenParts(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{
		{block: 3, quote: "* [AGENT] parse it in main.go", text: "a"},
		{block: 1, quote: "The flag exists.", text: "b"},
		{block: -1, quote: "something gone", text: "c"},
		{block: -1, quote: "something else gone", text: "d"},
	}
	m.moveCursor(0)
	m = drive(t, m, press("tab"))

	// The commented blocks in turn, nearest first, then each comment on a
	// part Claude has since rewritten.
	want := []int{1, 3, len(m.blocks), len(m.blocks) + 1}
	for i, at := range want {
		m = drive(t, m, press("n"))
		if m.cursor != at {
			t.Fatalf("cursor after n %d: %d, want %d", i+1, m.cursor, at)
		}
	}
}

func TestNextReachesARewrittenPartsCommentFromAnywhereAbove(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: -1, quote: "something gone", text: "c"}}
	m.moveCursor(0)
	m = drive(t, m, press("tab"))

	// With nothing commented below it, n skips the blocks between the
	// cursor and the comments on parts Claude has rewritten.
	m = drive(t, m, press("n"))
	if m.cursor != len(m.blocks) {
		t.Fatalf("cursor after n: %d, want %d", m.cursor, len(m.blocks))
	}
}

func TestNextStaysPutWithNoCommentBelow(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 2, quote: "## 2. Design", text: "a"}}
	m = drive(t, m, press("tab"))

	// From above the only comment n reaches it and is offered; on it and
	// below it there is nowhere to go, so n neither moves nor is offered.
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "n next comment") {
		t.Errorf("the footer should offer n with a comment below:\n%s", view)
	}
	m = drive(t, m, press("n"))
	if m.cursor != 2 {
		t.Fatalf("cursor after n: %d, want 2", m.cursor)
	}
	for _, at := range []int{2, len(m.blocks) - 1} {
		m.moveCursor(at)
		m = drive(t, m, press("n"))
		if m.cursor != at {
			t.Errorf("cursor after n at stop %d: %d, want %d", at, m.cursor, at)
		}
		if view := ansi.Strip(m.Content()); strings.Contains(view, "n next comment") {
			t.Errorf("the footer offers n with nothing commented below stop %d:\n%s", at, view)
		}
	}
}

func TestCursorStaysOnARewrittenPartsCommentAcrossAPoll(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)
	m.comments = []comment{{block: -1, quote: "gone", text: "x"}}
	m = drive(t, m, press("tab"))
	m.moveCursor(len(m.blocks))
	if m.commentAt(m.cursor) != 0 {
		t.Fatalf("cursor is not on the orphan: %d", m.cursor)
	}
	m = drive(t, m, press("c"))

	store.TextV = twoParts + "\n\n## 4. Worked example\n\nAdded."
	m = refreshed(t, m)

	ci := m.commentAt(m.cursor)
	if ci < 0 || m.comments[ci].text != "x" {
		t.Fatalf("cursor is not on the orphan after the poll: cursor=%d comments=%+v", m.cursor, m.comments)
	}

	m = typeText(t, m, "z")
	m = drive(t, m, press("enter"))
	if len(m.comments) != 1 || m.comments[0].text != "xz" {
		t.Fatalf("comments after saving: %+v", m.comments)
	}
}

func TestCommentUnderItsBlockHasNoExcerpt(t *testing.T) {
	t.Parallel()
	m, _ := planModel(t)
	m = drive(t, m, press("tab"))
	for range 3 {
		m = drive(t, m, press("down"))
	}
	m = drive(t, m, press("c"))
	m = typeText(t, m, "use cobra flags")
	m = drive(t, m, press("enter"))

	if view := ansi.Strip(m.planView.View()); strings.Contains(view, `on "* [AGENT]`) {
		t.Errorf("a comment under its own block should not have an excerpt:\n%s", view)
	}
}

func TestTaskLabel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		category string
		want     string
	}{
		{
			category: "early-plan",
			want:     "Early plan",
		},
		{
			category: "write-units",
			want:     "Write units",
		},
		{
			category: "address-comments",
			want:     "Address comments",
		},
		{
			category: "build-unit",
			want:     "Build unit",
		},
		{
			category: "rebase",
			want:     "Rebase",
		},
		{
			category: "",
			want:     "",
		},
	} {
		got := taskLabel(tc.category)
		assert.Check(t, cmp.Equal(got, tc.want), "category %q", tc.category)
	}
}

func TestActivityWithNothingSentIsAFactoryTask(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		task      string // the category of the task the flow has out; "" for none
		wantLine  string // the activity block's first line
		wantMark  string // the reply's header
		notInView string
	}{
		{
			name:      "a named task",
			task:      "early-plan",
			wantLine:  "Early plan",
			wantMark:  "chunk · Early plan",
			notInView: "factory task",
		},
		{
			name:     "an unnamed task",
			wantLine: "working on a factory task",
			wantMark: "chunk · factory task",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			m := New(context.Background(), turnReturning("ok"), nil, store, "do it")
			m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			assert.Assert(t, !m.busy, "still busy after the reply")

			// Factory hands out the task, then claude starts on it.
			store.setTask(tc.task)
			m.moving = &transition{kind: movingComments, from: "refine-proposal", since: time.Now()}
			m = drive(t, m, eventFor(m, Event{Activity: "reading main.go"}))
			assert.Assert(t, m.busy && m.byTask, "busy %v, byTask %v; want a factory task's turn", m.busy, m.byTask)
			assert.Check(t, m.moving == nil, "a factory task started and the screen still waits for factory")
			view := ansi.Strip(m.Content())
			assert.Check(t, cmp.Contains(view, tc.wantLine))
			if tc.notInView != "" {
				assert.Check(t, !strings.Contains(view, tc.notInView), "view:\n%s", view)
			}

			m = drive(t, m, eventFor(m, Event{Reply: &Reply{Text: "Wrote the units.", ByTask: true}}))
			assert.Check(t, !m.busy, "still busy after the task's reply")
			view = ansi.Strip(m.Content())
			assert.Check(t, cmp.Contains(view, tc.wantMark))
			assert.Check(t, cmp.Contains(view, "Wrote the units."))
			assert.Check(t, cmp.Equal(m.task, ""), "the task outlived its reply")
		})
	}
}

// belowStrip is the screen under the header and the status strip, which
// shows the flow's task whatever the chat labels its turn.
func belowStrip(m Model) string {
	lines := strings.Split(ansi.Strip(m.Content()), "\n")
	return strings.Join(lines[headerRows+stripRows:], "\n")
}

// The flow names the task a turn is on only until the agent submits it; a
// later refresh that sees the next task, or the daemon's review, does not
// relabel the turn.
func TestTaskKeepsItsFirstCategory(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	m := New(context.Background(), turnReturning("ok"), nil, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	store.setTask("early-plan")
	m = drive(t, m, eventFor(m, Event{Activity: "reading main.go"}))

	store.setTask("write-units")
	m = drive(t, m, pollMsg{})
	// The strip names the flow's task now; the chat keeps the turn's.
	view := belowStrip(m)
	assert.Check(t, cmp.Contains(view, "Early plan"))
	assert.Check(t, !strings.Contains(view, "Write units"), "view:\n%s", view)

	m = drive(t, m, eventFor(m, Event{Reply: &Reply{Text: "Wrote sections 1-4.", ByTask: true}}))
	view = ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "chunk · Early plan"))
}

// A refresh while the developer's message is in hand labels nothing, and the
// label does not outlive that turn.
func TestTaskDuringDevelopersTurnIsNotShown(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	a := newFakeAgent() // never replies, so the developer's turn stays in hand
	m := New(context.Background(), a, nil, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	assert.Assert(t, m.busy && !m.byTask, "busy %v, byTask %v; want the developer's turn", m.busy, m.byTask)

	store.setTask("early-plan")
	m = drive(t, m, pollMsg{})
	m = drive(t, m, eventFor(m, Event{Activity: "reading main.go"}))
	view := belowStrip(m)
	assert.Check(t, !strings.Contains(view, "Early plan"), "view:\n%s", view)

	m = drive(t, m, eventFor(m, Event{Reply: &Reply{Text: "Done."}}))
	store.setTask("")
	m = drive(t, m, eventFor(m, Event{Activity: "reading go.mod"}))
	assert.Check(t, m.byTask)
	view = ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "working on a factory task"))
}

// A screen opened while the daemon's planner is mid-turn is told so with one
// "Working…" activity, which shows the planner busy on a factory task.
func TestAWorkingEventWhileIdleShowsTheAgentBusy(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), turnReturning("ok"), nil, newFakeStore(), "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	assert.Assert(t, !m.busy, "still busy after the reply")

	m = drive(t, m, eventMsg{agent: m.agent, Event: Event{Activity: "Working…"}})
	assert.Check(t, m.busy && m.byTask, "busy %v, byTask %v; want a factory task's turn", m.busy, m.byTask)
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Working…"))
}

func TestProblemClearsTheTask(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	m := New(context.Background(), turnReturning("ok"), nil, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	store.setTask("early-plan")
	m = drive(t, m, eventFor(m, Event{Activity: "reading main.go"}))
	m = drive(t, m, eventFor(m, Event{Problem: errors.New("claude exited")}))
	assert.Check(t, cmp.Equal(m.task, ""))

	store.setTask("")
	m = drive(t, m, eventFor(m, Event{Activity: "reading go.mod"}))
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "working on a factory task"))
}

func TestEventsFromAnotherAgentAreIgnored(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), turnReturning("ok"), nil, newFakeStore(), "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	before := len(m.messages)

	// A host that kept an earlier screen's wait running can deliver what that
	// screen's agent says to this one.
	other := newFakeAgent()
	m = drive(t, m, eventMsg{agent: other, Event: Event{Reply: &Reply{Text: "an old session"}}})
	m = drive(t, m, sentMsg{agent: other, err: errors.New("an old failure")})
	if len(m.messages) != before || m.busy {
		t.Errorf("another agent's events reached the screen: %+v", m.messages[before:])
	}
}

// Once the plan exists the left pane is its activity: what is typed there
// goes nowhere, so nothing reaches a working planner.
func TestTypingAfterThePlanExistsSendsNothing(t *testing.T) {
	t.Parallel()
	agent := newFakeAgent()
	m := New(context.Background(), agent, nil, newFakeStore(), "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	assert.Assert(t, m.busy, "not busy after the request")
	assert.Check(t, cmp.Equal(m.focus, paneChat))

	m = typeText(t, m, "and keep it small")
	m = drive(t, m, press("enter"))
	assert.Check(t, cmp.Equal(m.input.Value(), ""))
	assert.Check(t, cmp.DeepEqual(agent.Sent(), []string{"plan it"}))
}

func TestFailedSendShowsInTheChat(t *testing.T) {
	t.Parallel()
	agent := newFakeAgent()
	agent.err = errors.New("no f plugin")
	m := New(context.Background(), agent, nil, newFakeStore(), "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.busy {
		t.Error("still busy after the send failed")
	}
	assert.Check(t, cmp.Contains(chatText(m), "Could not start the planning session: no f plugin"))
}

// chatText is the Activity pane's text, unwrapped.
func chatText(m Model) string {
	return strings.Join(strings.Fields(ansi.Strip(m.chatView.GetContent())), " ")
}

func TestResumeOwed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		flow Flow
		want bool
	}{
		{
			name: "refine-units waits on a session",
			flow: Flow{State: "refine-units", WaitingOn: "session"},
			want: true,
		},
		{
			name: "refine-proposal waits on a session",
			flow: Flow{State: "refine-proposal", WaitingOn: "session"},
			want: true,
		},
		{
			name: "ready waits on a session",
			flow: Flow{State: "ready", WaitingOn: "session"},
			want: true,
		},
		{
			name: "new plan in init",
			flow: Flow{State: "init", WaitingOn: "session"},
			want: true,
		},
		{
			name: "stand-by",
			flow: Flow{State: "stand-by", WaitingOn: "session"},
		},
		{
			name: "ended",
			flow: Flow{State: "refine-units", WaitingOn: "session", Ended: true},
		},
		{
			name: "claimed",
			flow: Flow{State: "refine-units", WaitingOn: "session", Claimed: true},
		},
		{
			name: "building",
			flow: Flow{State: "build", WaitingOn: "session"},
		},
		{
			name: "waiting on the developer",
			flow: Flow{State: "refine-proposal", WaitingOn: "human"},
		},
		{
			name: "deferred",
			flow: Flow{State: "init", WaitingOn: "human"},
		},
		{
			name: "waiting on the daemon",
			flow: Flow{State: "refine-units", WaitingOn: "daemon"},
		},
		{
			name: "an old name",
			flow: Flow{State: "Planning - Agent", WaitingOn: "session"},
		},
		{
			name: "no flow",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Check(t, cmp.Equal(resumeOwed(tc.flow), tc.want))
		})
	}
}

// sessionStep is a flow step waiting on a planning session.
var sessionStep = []Step{{Actor: "session", Task: "write-units", Dispatched: true}}

// resumeModel is a screen on an existing plan whose flow is in state, with
// next as its next steps.
func resumeModel(t *testing.T, agent *fakeAgent, state string, next []Step) (Model, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	store.Created, store.TextV, store.StateV = true, twoParts, state
	store.FlowV.Next = next
	store.FlowV.WaitingOn = waitingOn(next)
	m := New(context.Background(), agent, nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	return m, store
}

// setFlow moves store's flow to state, with next as its next steps.
func setFlow(store *fakeStore, state string, next []Step, claimed bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.StateV, store.FlowV.Next, store.FlowV.Claimed = state, next, claimed
	store.FlowV.WaitingOn = waitingOn(next)
}

// waitingOn is who factory waits on for a flow with next: a session when
// next has a session's entry, else the developer, as the fake store starts.
func waitingOn(next []Step) string {
	if slices.ContainsFunc(next, func(s Step) bool { return s.Actor == ActorSession }) {
		return string(ActorSession)
	}
	return string(ActorHuman)
}

func TestRefreshResumesAPlannerThatIsNotThere(t *testing.T) {
	t.Parallel()
	agent := newFakeAgent()
	m, store := resumeModel(t, agent, "refine-units", sessionStep)
	m = refreshed(t, m)
	assert.Check(t, cmp.Equal(agent.Resumes(), 1))
	m = refreshed(t, m)
	assert.Check(t, cmp.Equal(agent.Resumes(), 1), "resumed twice in one state")

	setFlow(store, "refine-units", sessionStep, true)
	m = refreshed(t, m)
	setFlow(store, "refine-units", sessionStep, false)
	refreshed(t, m)
	assert.Check(t, cmp.Equal(agent.Resumes(), 2), "not resumed after a session held the claim and lost it")
	assert.Check(t, cmp.Len(agent.Sent(), 0))
}

func TestRefreshDoesNotResumeAnUnprioritizedPlanASessionLetGo(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"init"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			t.Run("claimed then released", func(t *testing.T) {
				t.Parallel()
				agent := newFakeAgent()
				m, store := resumeModel(t, agent, state, sessionStep)
				m = refreshed(t, m)
				assert.Check(t, cmp.Equal(agent.Resumes(), 1))

				setFlow(store, state, sessionStep, true)
				m = refreshed(t, m)
				setFlow(store, state, sessionStep, false)
				for range 3 {
					m = refreshed(t, m)
				}
				assert.Check(t, cmp.Equal(agent.Resumes(), 1), "resumed after a session let the plan go")

				setFlow(store, "refine-units", sessionStep, false)
				m = refreshed(t, m)
				assert.Check(t, cmp.Equal(agent.Resumes(), 2), "not resumed once the state moved")

				setFlow(store, state, sessionStep, false)
				m = refreshed(t, m)
				refreshed(t, m)
				assert.Check(t, cmp.Equal(agent.Resumes(), 3), "not resumed exactly once on coming back")
			})
			t.Run("a claim the screen never saw", func(t *testing.T) {
				t.Parallel()
				agent := newFakeAgent()
				m, _ := resumeModel(t, agent, state, sessionStep)
				for range 3 {
					m = refreshed(t, m)
				}
				assert.Check(t, cmp.Equal(agent.Resumes(), 1), "resumed twice in one state")
			})
			t.Run("R still resumes", func(t *testing.T) {
				t.Parallel()
				agent := newFakeAgent()
				m, store, r, _ := resumeScreen(t, agent, state, sessionStep, nil)
				assert.Assert(t, cmp.Equal(agent.Resumes(), 1))
				setFlow(store, state, sessionStep, true)
				m = refreshed(t, m)
				setFlow(store, state, sessionStep, false)
				m = refreshed(t, m)
				assert.Check(t, cmp.Equal(agent.Resumes(), 1))

				m, cmd := m.Update(press("R"))
				for _, msg := range run(t, cmd) {
					m = drive(t, m, msg)
				}
				assert.Check(t, cmp.Equal(agent.Resumes(), 2), "R did not resume")
				assert.Check(t, cmp.Len(r.Calls(), 0))
			})
		})
	}
}

func TestAFailedResumeIsShownOnceAndRetriedOnANewState(t *testing.T) {
	t.Parallel()
	agent := newFakeAgent()
	agent.resumeErr = errors.New("no Claude Code")
	m, store := resumeModel(t, agent, "refine-units", sessionStep)
	for range 3 {
		m = refreshed(t, m)
	}
	assert.Check(t, cmp.Equal(agent.Resumes(), 1))
	failures := 0
	for _, msg := range m.messages {
		if strings.Contains(msg.text, "Could not resume the planning session") {
			failures++
		}
	}
	assert.Check(t, cmp.Equal(failures, 1))
	assert.Check(t, cmp.Contains(chatText(m), "Could not resume the planning session: no Claude Code"))

	setFlow(store, "refine-proposal", []Step{{Actor: "human", Event: "address-comments"}}, false)
	m = refreshed(t, m)
	assert.Check(t, cmp.Equal(agent.Resumes(), 1), "resumed while factory waits on the developer")

	setFlow(store, "refine-units", sessionStep, false)
	refreshed(t, m)
	assert.Check(t, cmp.Equal(agent.Resumes(), 2), "not retried once the state changed")
}

func TestAPlanBeingBuiltIsNeverResumed(t *testing.T) {
	t.Parallel()
	agent := newFakeAgent()
	m, _ := resumeModel(t, agent, "build", sessionStep)
	for range 3 {
		m = refreshed(t, m)
	}
	assert.Check(t, cmp.Equal(agent.Resumes(), 0))

	m, _ = m.focusOn(paneChat)
	m = typeText(t, m, "keep going")
	drive(t, m, press("enter"))
	assert.Check(t, cmp.Len(agent.Sent(), 0))
	assert.Check(t, cmp.Equal(agent.Resumes(), 0))
}

func TestNoProjectNoResume(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		project string
		want    int
	}{
		{
			name: "no project",
		},
		{
			name:    "a project",
			project: "chunk-cli",
			want:    1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			agent := newFakeAgent()
			m, _ := resumeModel(t, agent, "refine-units", sessionStep)
			m = m.WithInbox("plan", tc.project)
			refreshed(t, m)
			assert.Check(t, cmp.Equal(agent.Resumes(), tc.want))
		})
	}
}

// A request whose session failed to start is kept, and sent again in place
// of a resume, so the planner never claims a new plan without it.
func TestAFailedRequestIsSentAgainNotResumed(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	agent := newFakeAgent()
	agent.err = errors.New("no Claude Code")
	m := New(context.Background(), agent, nil, store, "add a flag")
	m = quiet(m)
	m, cmd := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	var request tea.Cmd
	for _, msg := range run(t, cmd) {
		if created, ok := msg.(createdMsg); ok {
			m, request = m.Update(created)
		}
	}
	assert.Assert(t, m.created && request != nil, "the plan was not created")

	// Factory has the new plan, with a task for its planner; the request is
	// still in flight, so a refresh starts nothing more.
	setFlow(store, "init", []Step{{Actor: "session", Task: "record-context"}}, false)
	m = refreshed(t, m)
	assert.Check(t, cmp.Len(agent.Sent(), 0))
	assert.Check(t, cmp.Equal(agent.Resumes(), 0))

	for _, msg := range run(t, request) {
		m = drive(t, m, msg)
	}
	assert.Check(t, cmp.Contains(chatText(m), "Could not start the planning session: no Claude Code"))

	agent.mu.Lock()
	agent.err = nil
	agent.mu.Unlock()
	m = refreshed(t, m)
	assert.Check(t, cmp.DeepEqual(agent.Sent(), []string{"add a flag", "add a flag"}))
	assert.Check(t, cmp.Equal(agent.Resumes(), 0))

	refreshed(t, m)
	assert.Check(t, cmp.Len(agent.Sent(), 2), "the request was sent again once it started")
	assert.Check(t, cmp.Equal(agent.Resumes(), 0))
}

func TestProblemEndsTheTurn(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, eventFor(m, Event{Problem: errors.New("claude exited (code 1)")}))
	if m.busy {
		t.Error("still busy after the session's problem")
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "claude exited") {
		t.Errorf("the problem is not in the chat:\n%s", view)
	}
}

func TestPlanIsSentOnceWhileTheFactoryAnswers(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), turnReturning("ok"), nil, newFakeStore(), "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.approving = true
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.confirming {
		t.Error("ctrl+s asked to send the plan again while it is being sent")
	}
}

func TestBubbleFitsAShortMessageAndWrapsALongOne(t *testing.T) {
	t.Parallel()
	m := &Model{}
	const w = 60
	short := m.userBubble("hi", w)
	top, _, _ := strings.Cut(ansi.Strip(short), "\n")
	if got := ansi.StringWidth(strings.TrimSpace(top)); got != len("hi")+bubbleChrome {
		t.Errorf("bubble for %q is %d wide, want %d:\n%s", "hi", got, len("hi")+bubbleChrome, ansi.Strip(short))
	}

	long := m.userBubble(strings.Repeat("word ", 30), w)
	lines := strings.Split(ansi.Strip(long), "\n")
	if len(lines) < 4 {
		t.Errorf("long message did not wrap:\n%s", ansi.Strip(long))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got > w {
			t.Errorf("line %d is %d wide, chat is %d: %q", i, got, w, l)
		}
		if !strings.HasPrefix(l, " ") {
			t.Errorf("line %d leaves no room on the left: %q", i, l)
		}
	}
}

func TestStripShowsFlowStatus(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.setWaitingOn("human")
	store.setEvents("address-comments", "approve-for-units", "approve-for-building", "abandon")
	store.setRequired("approve-for-units")
	m := New(context.Background(), turnReturning("Wrote a plan."), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "No plan yet") {
		t.Errorf("header does not say there is no plan:\n%s", view)
	}

	store.TextV = "# Add a flag\n\n## 1. Outcome\n\nThe flag exists."
	m, _ = m.send("add a flag")
	m = drive(t, m, eventFor(m, Event{Reply: &Reply{Text: "Wrote a plan."}}))
	view := ansi.Strip(m.Content())
	if !strings.Contains(view, "Next: you approve the design · ctrl+s") {
		t.Errorf("strip does not say the design is ready:\n%s", view)
	}
	lines := strings.Split(view, "\n")
	assert.Check(t, cmp.Contains(lines[1], "Planning › Building › Integrating › Done"))
	assert.Check(t, cmp.Contains(lines[2], "Next: you approve the design · ctrl+s"))

	store.TextV += "\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.UnitsV = []Unit{{Name: "Unit 1: Add the flag"}}
	store.StateV = "refine-units"
	store.setEvents("address-comments", "approve-for-building", "abandon")
	store.setRequired("approve-for-building")
	m = drive(t, m, eventFor(m, Event{Reply: &Reply{Text: "Wrote the units."}}))
	view = ansi.Strip(m.Content())
	if !strings.Contains(view, "Next: you approve the plan · ctrl+s") {
		t.Errorf("strip does not say the plan is ready:\n%s", view)
	}
	lines = strings.Split(view, "\n")
	assert.Check(t, cmp.Contains(lines[1], "Planning › Building › Integrating › Done"))
	assert.Check(t, cmp.Contains(lines[2], "Next: you approve the plan · ctrl+s"))

	m.comments = []comment{{block: 0, quote: "# Add a flag", text: "name it --dry"}}
	m.renderPlan()
	view = ansi.Strip(m.Content())
	for _, want := range []string{"1 comment queued", "your comment · queued", "name it --dry"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		m = drive(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		assertFits(t, m, size[0], size[1])
	}
}

const planWithUnits = `# Retry on 429

## 1. Outcome

Create retries.

## 3. Open Questions

- Which timeout caps the wait?

## 5. Units

### Unit 1: Add RetryPolicy (done)

Code.

### Unit 2: Wire it in

More code.`

// planWithUnitsRead are planWithUnits' units, as factory reads them.
var planWithUnitsRead = []Unit{{Name: "Unit 1: Add RetryPolicy", Done: true}, {Name: "Unit 2: Wire it in"}}

func TestPlanShowsUnitsAsSteps(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = planWithUnits
	store.UnitsV = []Unit{{Name: "Unit 1: Add RetryPolicy", Done: true}, {Name: "Unit 2: Wire it in"}}
	store.OpenQuestionsV = []Question{{Text: "Which timeout caps the wait?"}}
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it").WithBranch("hanabel/retry-429")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	for _, want := range []string{"hanabel/retry-429", "OUTCOME ─", "OPEN QUESTIONS", "✓ 1  Add RetryPolicy", "○ 2  Wire it in", "2 units", "1 open question", "ctrl+s Approve plan"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
	assert.Check(t, !strings.Contains(view, "(done)"), "a done unit's heading keeps its suffix:\n%s", view)
}

func TestPlanWithNoSkeletonSectionIsNotYetAPlan(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "## Notes\n\nMine.\n"
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = refreshed(t, m)
	view := ansi.Strip(m.planView.View())
	assert.Check(t, cmp.Contains(view, "No plan yet"))
	assert.Check(t, !strings.Contains(view, "Mine."), "a section outside the skeleton is drawn:\n%s", view)
}

func TestOpenQuestionsProseOutsideItsListIsNotDrawn(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "## 1. Outcome\n\nOne.\n\n## 3. Open Questions\n\nNone.\n"
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = refreshed(t, m)
	view := ansi.Strip(m.planView.View())
	assert.Check(t, cmp.Contains(view, "OPEN QUESTIONS"), "the heading is drawn:\n%s", view)
	assert.Check(t, !strings.Contains(view, "None."), "prose outside the list is drawn:\n%s", view)
}

func TestIdenticalBlocksOfDifferentUnitsEachHaveTheirOwnAt(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, "## 5. Units\n\n### Unit 1: parser (done)\n\n- Test it.\n\n### Unit 2: api\n\n- Test it.\n")
	m.renderPlan()
	var ats []int
	for _, b := range m.blocks {
		if b.source == "- Test it." {
			ats = append(ats, b.at)
		}
	}
	assert.Assert(t, cmp.Len(ats, 2))
	assert.Check(t, ats[0] >= 0 && ats[1] > ats[0], "the two blocks' ats are %v", ats)
}

func TestMalformedUnitHeadingHasNoAt(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, "## 5. Units\n\n### unit 1 - x\n\nBody.\n")
	m.renderPlan()
	at := -2
	for _, b := range m.blocks {
		if b.source == "### Unit 1: unit 1 - x" {
			at = b.at
		}
	}
	assert.Check(t, cmp.Equal(at, -1), "the made-up heading is in the plan text")
	m = drive(t, m, press("tab"))
	for range len(m.blocks) + 1 {
		m = drive(t, m, press("down"))
	}
}

// TestPlanStepsComeFromTheRoute checks that the plan pane's steps are the
// units the route answers, matched to the text's headings: a done unit's SHA
// suffix is not drawn, and a heading in an earlier section with a unit's name
// is not its step.
func TestPlanStepsComeFromTheRoute(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Retry on 429\n\n## 1. Outcome\n\nCreate retries.\n\n" +
		"## 4. Worked example\n\n### Unit 1: Add RetryPolicy\n\nAn example.\n\n" +
		"## 5. Units\n\n### Unit 1: Add RetryPolicy (done, abc1234)\n\nCode.\n\n#### Building\n\nCommitted.\n"
	store.UnitsV = []Unit{{Name: "Unit 1: Add RetryPolicy", Done: true}}
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	assert.Check(t, strings.Contains(view, "✓ 1  Add RetryPolicy"), "no done step:\n%s", view)
	assert.Check(t, cmp.Equal(strings.Count(view, "✓ 1  Add RetryPolicy"), 1), "the step is drawn more than once:\n%s", view)
	for _, unwanted := range []string{"○ 2", "✓ 2", "abc1234"} {
		assert.Check(t, !strings.Contains(view, unwanted), "view has %q:\n%s", unwanted, view)
	}
}

// TestPlanWaitsForItsUnits checks that a plan's text is not drawn until its
// units are read with it: a failed units read fails the refresh, and the next
// good refresh draws the text with its steps.
func TestPlanWaitsForItsUnits(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = planWithUnits
	store.UnitsErr = errors.New("503 service unavailable")
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "could not refresh the plan: 503 service unavailable"), "view:\n%s", view)
	for _, unwanted := range []string{"Retry on 429", "✓ 1", "○ 2", "2 units"} {
		assert.Check(t, !strings.Contains(view, unwanted), "view has %q:\n%s", unwanted, view)
	}

	store.mu.Lock()
	store.UnitsErr, store.UnitsV = nil, planWithUnitsRead
	store.mu.Unlock()
	m = refreshed(t, m)
	view = ansi.Strip(m.Content())
	for _, want := range []string{"✓ 1  Add RetryPolicy", "○ 2  Wire it in", "2 units"} {
		assert.Check(t, cmp.Contains(view, want), "view:\n%s", view)
	}
	assert.Check(t, !strings.Contains(view, "could not refresh the plan"), "view:\n%s", view)
}

// TestPlanDrawsUnitsThatChangeWithTheText checks that the plan pane is drawn
// again when only the units change, as a unit marked done before its text is.
func TestPlanDrawsUnitsThatChangeWithTheText(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = planWithUnits
	store.UnitsV = []Unit{{Name: "Unit 1: Add RetryPolicy"}, {Name: "Unit 2: Wire it in"}}
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "○ 1  Add RetryPolicy"), "view:\n%s", view)

	store.mu.Lock()
	store.UnitsV = planWithUnitsRead
	store.mu.Unlock()
	m = refreshed(t, m)
	view = ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "✓ 1  Add RetryPolicy"), "view:\n%s", view)
}

func TestNarrowScreenTakesTurnsWithTabs(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV, store.UnitsV = planWithUnits, planWithUnitsRead
	m := New(context.Background(), turnReturning("Wrote it."), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 70, Height: 30})
	view := ansi.Strip(m.Content())
	if !regexp.MustCompile(`Activity +Flow +Plan · 2 units`).MatchString(view) {
		t.Errorf("no tab strip:\n%s", view)
	}
	if strings.Contains(view, "Create retries.") {
		t.Errorf("the plan shows beside the chat on a narrow screen:\n%s", view)
	}
	assertFits(t, m, 70, 30)

	// tab goes round chat, Flow and Plan.
	m = drive(t, m, press("tab"))
	view = ansi.Strip(m.Content())
	if !strings.Contains(view, "PROGRESS") || strings.Contains(view, "Wrote it.") || strings.Contains(view, "Create retries.") {
		t.Errorf("tab did not bring the Flow tab up in place of the chat:\n%s", view)
	}
	assertFits(t, m, 70, 30)

	m = drive(t, m, press("tab"))
	view = ansi.Strip(m.Content())
	if !strings.Contains(view, "Create retries.") || strings.Contains(view, "PROGRESS") {
		t.Errorf("a second tab did not bring the plan up:\n%s", view)
	}
	assertFits(t, m, 70, 30)

	m = drive(t, m, press("tab"))
	view = ansi.Strip(m.Content())
	if !strings.Contains(view, "Wrote it.") || strings.Contains(view, "Create retries.") {
		t.Errorf("a third tab did not bring the chat back:\n%s", view)
	}
	assert.Check(t, cmp.Equal(m.focus, paneChat))
	assertFits(t, m, 70, 30)
}

func TestFirstPlanShowsASkeletonWhileClaudeWrites(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = m.send("plan it")
	m = drive(t, m, eventFor(m, Event{Activity: "Read create.go"}))
	view := ansi.Strip(m.Content())
	for _, want := range []string{"chunk is drafting", "Read create.go", "███", "chunk is working"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
}

// While a comment is written enter saves it, so the bar does not offer enter
// as the key that sends comments to the factory.
func TestActionBarOffersNoButtonWhileACommentIsWritten(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 110, Height: 30})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 0, quote: "## 1. Outcome", text: "x"}}
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))

	view := ansi.Strip(m.Content())
	if !strings.Contains(view, "enter save comment") {
		t.Fatalf("the bar does not say enter saves:\n%s", view)
	}
	for _, not := range []string{"Save 1 comment", "Approve plan"} {
		if strings.Contains(view, not) {
			t.Errorf("the bar offers %q while a comment is written:\n%s", not, view)
		}
	}
}

func TestShiftSelectsSeveralBlocksForOneComment(t *testing.T) {
	t.Parallel()
	m, _ := planModel(t)

	// From the second heading, up over the paragraph above it.
	m = drive(t, m, press("tab"))
	for range 2 {
		m = drive(t, m, press("down"))
	}
	m = drive(t, m, press("shift+up"))
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "2 parts selected") {
		t.Errorf("the footer does not say what is selected:\n%s", view)
	}
	m = drive(t, m, press("c"))
	m = typeText(t, m, "merge these")
	m = drive(t, m, press("enter"))

	want := "The flag exists.\n\n## 2. Design"
	if len(m.comments) != 1 || m.comments[0].block != 1 || m.comments[0].span != 2 || m.comments[0].quote != want {
		t.Fatalf("comments: %+v", m.comments)
	}
	if m.selecting() {
		t.Error("the selection outlived its comment")
	}
	// The cursor on either block is on the comment.
	m = drive(t, m, press("up"))
	if m.commentAt(m.cursor) != 0 {
		t.Errorf("block %d is not on the comment", m.cursor)
	}
}

func TestCommentOnSeveralBlocksHangsUnderTheLast(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 1, span: 3, quote: m.quoteOf(1, 3), text: "use cobra flags"}}
	m.drawPlan()

	lines := strings.Split(ansi.Strip(m.planView.View()), "\n")
	var from, card int
	for i, l := range lines {
		switch {
		case strings.Contains(l, "The flag exists."):
			from = i
		case strings.Contains(l, "use cobra flags"):
			card = i
		}
	}
	if card <= from {
		t.Fatalf("the comment is not under its blocks:\n%s", strings.Join(lines, "\n"))
	}
	for i := from; i <= card; i++ {
		if !strings.HasPrefix(lines[i], " ┃") {
			t.Errorf("line %d does not carry the comment bar: %q", i, lines[i])
		}
	}
	if strings.Count(strings.Join(lines, "\n"), "use cobra flags") != 1 {
		t.Error("the comment is shown more than once")
	}
}

func TestSelectionTakesInTheCommentsItTouches(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{
		{block: 3, quote: "* [AGENT] parse it in main.go", text: "first"},
		{block: 1, quote: "The flag exists.", text: "second"},
	}
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("shift+down"))
	m = drive(t, m, press("c"))
	// The selection was 1 to 2; it grows to take in the whole comment on 1,
	// and the editor holds that comment's text to add to.
	if got := m.commentInput.Value(); got != "second" {
		t.Errorf("editor holds %q", got)
	}
	m = typeText(t, m, " too")
	m = drive(t, m, press("enter"))
	if len(m.comments) != 2 {
		t.Fatalf("comments: %+v", m.comments)
	}
	if c := m.comments[1]; c.block != 1 || c.span != 2 || c.text != "second too" {
		t.Errorf("the comment taken in is %+v", c)
	}

	// A selection over both comments makes them one, in the order they
	// were written.
	m = drive(t, m, press("shift+down"))
	m = drive(t, m, press("c"))
	if got := m.commentInput.Value(); got != "first\n\nsecond too" {
		t.Errorf("editor holds %q", got)
	}
	m = drive(t, m, press("enter"))
	if len(m.comments) != 1 || m.comments[0].block != 1 || m.comments[0].span != 3 {
		t.Errorf("comments: %+v", m.comments)
	}
}

func TestEmptyCommentOnASelectionDeletesWhatItTookIn(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 1, quote: "The flag exists.", text: "x"}}
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("shift+down"))
	m = drive(t, m, press("c"))
	m.commentInput.Reset()
	m = drive(t, m, press("enter"))
	if len(m.comments) != 0 {
		t.Errorf("comments: %+v", m.comments)
	}
}

func TestEscClearsTheSelectionBeforeItLeaves(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "").WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("shift+down"))
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "esc clear selection") {
		t.Errorf("the footer does not offer esc:\n%s", view)
	}
	var cmd tea.Cmd
	m, cmd = m.Update(press("esc"))
	if cmd != nil {
		if _, ok := cmd().(ClosedMsg); ok {
			t.Fatal("esc left the screen with a selection to clear")
		}
	}
	if m.selecting() {
		t.Error("esc left the selection")
	}
}

func TestMovingWithoutShiftDropsTheSelection(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("shift+down"))
	m = drive(t, m, press("shift+down"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "x")
	m = drive(t, m, press("enter"))
	if len(m.comments) != 1 || m.comments[0].block != 3 || m.comments[0].span != 1 {
		t.Errorf("comments: %+v", m.comments)
	}
}

func TestCommentOnSeveralBlocksFollowsThemWhenThePlanChanges(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 1, span: 2, quote: m.quoteOf(1, 2), text: "x"}}

	setPlan(&m, strings.Replace(twoParts, "## 1. Outcome\n\n", "## 1. Outcome\n\nAdded.\n\n", 1))
	m.renderPlan()
	if c := m.comments[0]; c.block != 2 || c.span != 2 {
		t.Errorf("comment is on %d+%d, want 2+2", c.block, c.span)
	}

	// Rewriting either block leaves the comment on a rewritten part.
	setPlan(&m, strings.Replace(twoParts, "## 2. Design", "## 4. Worked example", 1))
	m.renderPlan()
	if got := m.comments[0].block; got != -1 {
		t.Errorf("comment on a rewritten block is on %d, want -1", got)
	}
}

// --- Store-backed polling and comments ---

func TestPaneRendersStoreText(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nDone."
	store.StateV = "refine-units"
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = refreshed(t, m)

	view := ansi.Strip(m.Content())
	if !strings.Contains(view, "Done.") {
		t.Errorf("plan text not shown:\n%s", view)
	}
	if lines := strings.Split(view, "\n"); !strings.Contains(lines[headerRows], "Planning › Building") {
		t.Errorf("strip does not show the step:\n%s", view)
	}
	if strings.Contains(view, "Plan · refine-units") {
		t.Errorf("title still shows the state:\n%s", view)
	}
}

func TestSavedCommentAnchorsByQuote(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.AddComment(context.Background(), "## 2. Design", "needs more detail")
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	view := ansi.Strip(m.planView.View())
	if !strings.Contains(view, "needs more detail") || strings.Contains(view, "On parts chunk has since rewritten") {
		t.Fatalf("comment is not drawn under its block:\n%s", view)
	}

	store.TextV = "## 1. Outcome\n\nRewritten."
	m = refreshed(t, m)
	view = ansi.Strip(m.planView.View())
	if !strings.Contains(view, "On parts chunk has since rewritten") || !strings.Contains(view, "needs more detail") {
		t.Errorf("comment did not become an orphan:\n%s", view)
	}
}

// TestSavedCommentHeadSaysOpen checks that a comment already in factory
// heads its card "your comment · open", not "queued" like one still
// waiting to be sent.
func TestSavedCommentHeadSaysOpen(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.AddComment(context.Background(), "The flag exists.", "needs more detail")
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	view := ansi.Strip(m.planView.View())
	if !strings.Contains(view, "your comment · open") {
		t.Fatalf("saved comment's head does not say it is open:\n%s", view)
	}
	if strings.Contains(view, "your comment · queued") {
		t.Errorf("saved comment is headed as queued:\n%s", view)
	}
}

// TestPlanFooterCountsQueuedAndOpen checks that the footer counts queued
// and already-saved comments separately.
func TestPlanFooterCountsQueuedAndOpen(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.AddComment(context.Background(), "The flag exists.", "saved one")
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "queued one")
	m = drive(t, m, press("enter"))

	footer := ansi.Strip(m.planFooter())
	if !strings.Contains(footer, "1 queued · 1 open") {
		t.Errorf("footer is %q, want counts for both queued and open comments", footer)
	}
}

func TestResolvedCommentsListedAsResolved(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	c, _ := store.AddComment(context.Background(), "The flag exists.", "looks good")
	store.ResolveComment(context.Background(), c.ID)
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	if len(m.comments) != 0 || len(m.resolved) != 1 {
		t.Fatalf("comments=%+v resolved=%+v", m.comments, m.resolved)
	}
	view := ansi.Strip(m.planView.View())
	for _, want := range []string{"Resolved", "resolved", "looks good"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

// TestResolvedStopDisablesCommentKeys checks that c, r and d do nothing on
// a resolved comment's stop: the cursor can reach it, but none of them act
// on it.
func TestResolvedStopDisablesCommentKeys(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	c, _ := store.AddComment(context.Background(), "The flag exists.", "looks good")
	store.ResolveComment(context.Background(), c.ID)
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("G"))
	if !m.onResolved(m.cursor) {
		t.Fatalf("cursor did not reach the resolved stop: %d", m.cursor)
	}

	m = drive(t, m, press("c"))
	if m.commenting {
		t.Error("c opened an editor on a resolved comment")
	}
	m = drive(t, m, press("r"))
	if len(store.Resolved) != 1 {
		t.Errorf("r resolved something on a resolved comment: %+v", store.Resolved)
	}
	m = drive(t, m, press("d"))
	if len(store.Deleted) != 0 || m.confirming {
		t.Errorf("d deleted or asked to delete a resolved comment: deleted=%+v confirming=%v", store.Deleted, m.confirming)
	}
}

func TestPollKeepsCommentPlace(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "## 2. Design\n\n* Tests\n* Tests"
	store.AddComment(context.Background(), "* Tests", "second one")
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)
	if len(m.comments) != 1 || m.comments[0].block < 0 {
		t.Fatalf("comment not anchored: %+v", m.comments)
	}
	block := m.comments[0].block

	m = refreshed(t, m)
	if m.comments[0].block != block {
		t.Errorf("comment moved from block %d to %d across a poll", block, m.comments[0].block)
	}
}

func TestRefreshErrorKeepsPane(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)
	before := m.plan

	m.moving = &transition{kind: movingComments, from: "refine-proposal", since: time.Now()}
	store.FlowErr = errors.New("network blip")
	m = refreshed(t, m)
	if m.plan != before {
		t.Errorf("plan changed on a failed poll")
	}
	assert.Check(t, m.moving != nil, "a failed poll ended the wait")
	if !strings.Contains(m.refreshErr, "network blip") {
		t.Errorf("refreshErr: %q", m.refreshErr)
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "could not refresh the plan") {
		t.Errorf("no quiet note in the footer:\n%s", view)
	}

	store.FlowErr = nil
	m = refreshed(t, m)
	if m.refreshErr != "" {
		t.Errorf("refreshErr not cleared after a successful poll: %q", m.refreshErr)
	}
}

func TestQueuedCommentsSavedOnSend(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	agent := turnReturning("ok")
	m := New(context.Background(), agent, nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "first")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "second")
	m = drive(t, m, press("enter"))
	if len(m.comments) != 2 {
		t.Fatalf("comments before send: %+v", m.comments)
	}

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))

	if len(store.Added) != 2 || store.Added[0] != [2]string{"The flag exists.", "first"} ||
		store.Added[1] != [2]string{"* [AGENT] parse it in main.go", "second"} {
		t.Fatalf("added: %+v", store.Added)
	}
	if len(store.HandedOff) != 1 || store.HandedOff[0] != "refine-proposal" {
		t.Fatalf("handed off: %+v", store.HandedOff)
	}
	assert.Check(t, cmp.DeepEqual(agent.Sent(), []string{"plan it"}))
	if len(queuedComments(m.comments)) != 0 {
		t.Errorf("comments still queued: %+v", m.comments)
	}
	// Saved with new IDs, the comments stay on their blocks while the plan's
	// text is unchanged.
	for _, c := range m.comments {
		if c.id == "" || c.block < 0 {
			t.Errorf("saved comment not anchored after send: %+v", c)
		}
	}
	if view := ansi.Strip(m.planView.View()); strings.Contains(view, "On parts chunk has since rewritten") {
		t.Errorf("saved comments fell into the orphan list after send:\n%s", view)
	}
}

func TestSendWithOnlyCommentsStartsNoTurn(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	agent := turnReturning("ok")
	m := New(context.Background(), agent, nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	agent.reset()

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "first")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("tab"))
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "queued · press enter or s to send") {
		t.Errorf("the Activity pane does not say how comments are sent:\n%s", view)
	}
	m = drive(t, m, press("enter"))

	if len(agent.Sent()) != 0 {
		t.Error("a turn started with nothing typed")
	}
	if len(store.Added) != 1 {
		t.Fatalf("comments saved: %+v", store.Added)
	}
	if len(queuedComments(m.comments)) != 0 {
		t.Errorf("comments still queued: %+v", m.comments)
	}
}

// TestCommentsHeldOutsideHumanStates checks that a send outside a Human
// state leaves the queued comments queued, unsaved and not handed off.
func TestCommentsHeldOutsideHumanStates(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.StateV, store.FlowV.WaitingOn = "refine-units", string(ActorSession)
	agent := turnReturning("ok")
	m := New(context.Background(), agent, nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	agent.reset()

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "x")
	m = drive(t, m, press("enter"))
	assert.Check(t, !m.keys().SendComments.Enabled(), "s sends comments outside a Human state")
	bar := ansi.Strip(m.actionBar())
	assert.Check(t, !strings.Contains(bar, "Save 1 comment"), "the bar offers to save held comments: %q", bar)
	assert.Check(t, cmp.Contains(bar, "1 comment held until factory is waiting on you"))
	main, _ := m.barRight()
	assert.Check(t, cmp.Equal(main, ""), "the bar has a button for held comments")
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))

	assert.Check(t, cmp.Len(store.Added, 0))
	assert.Check(t, cmp.Len(store.HandedOff, 0))
	assert.Check(t, cmp.Len(queuedComments(m.comments), 1))
	assert.Check(t, cmp.Len(agent.Sent(), 0))
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Comments not sent: factory is working on the plan"))
}

// TestHeldCommentsSendOnceThePlanWaitsOnTheDeveloper checks that comments
// held while factory worked are saved and handed off by the first send once
// the plan is in a Human state.
func TestHeldCommentsSendOnceThePlanWaitsOnTheDeveloper(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.StateV, store.FlowV.WaitingOn = "refine-units", string(ActorSession)
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "x")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("s"))
	assert.Assert(t, cmp.Len(store.Added, 0), "s saved comments outside a Human state")

	store.mu.Lock()
	store.StateV, store.FlowV.WaitingOn = "refine-proposal", string(ActorHuman)
	store.mu.Unlock()
	m = drive(t, m, pollMsg{})
	assert.Assert(t, m.keys().SendComments.Enabled(), "s does not send comments from a Human state")
	drive(t, m, press("s"))

	assert.Check(t, cmp.Len(store.Added, 1))
	assert.Check(t, cmp.DeepEqual(store.HandedOff, []string{"refine-proposal"}))
}

// TestCommentsHeldWhileFactoryPicksUpTheHandOff checks that comments queued
// after a hand-off, before factory has moved the plan, are held rather than
// sent with a second address-comments event, which factory would store
// again.
func TestCommentsHeldWhileFactoryPicksUpTheHandOff(t *testing.T) {
	t.Parallel()
	m, store := planModel(t)
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "first")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("s"))
	assert.Assert(t, cmp.DeepEqual(store.HandedOff, []string{"refine-proposal"}))

	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "second")
	m = drive(t, m, press("enter"))
	assert.Check(t, !m.keys().SendComments.Enabled(), "s offered while factory has yet to move the plan")
	m = drive(t, m, press("s"))
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))

	assert.Check(t, cmp.Len(store.Added, 1))
	assert.Check(t, cmp.DeepEqual(store.HandedOff, []string{"refine-proposal"}))
	assert.Check(t, cmp.Len(queuedComments(m.comments), 1))
	assert.Check(t, cmp.Equal(m.status, ""), "the held note covers the wait: %q", m.status)

	// Factory hands the comments to a session: the wait is over, but factory
	// takes no answer while a task is out, so s stays off.
	store.setTask("address-comments")
	store.setWaitingOn("session")
	store.setEvents("abandon")
	m = drive(t, m, pollMsg{})
	m = drive(t, m, press("tab")) // back to the plan pane, where s works
	assert.Check(t, m.moving == nil, "the wait outlived the task going out")
	assert.Check(t, !m.keys().SendComments.Enabled(), "s offered while a task is out")

	// The task is submitted: factory waits on the developer, and the held
	// comment goes out.
	store.setTask("")
	store.setWaitingOn("human")
	store.setEvents("address-comments", "approve-for-units", "abandon")
	m = drive(t, m, pollMsg{})
	assert.Check(t, m.keys().SendComments.Enabled(), "s not offered once factory waits on the developer")
	m = drive(t, m, press("s"))
	assert.Check(t, cmp.Len(store.Added, 2))
	assert.Check(t, cmp.DeepEqual(store.HandedOff, []string{"refine-proposal", "refine-proposal"}))
}

// TestCommentsFollowTheFlowsEventsNotItsState checks that whether a send
// hands comments to factory is up to the events its flow applies now, not
// the name of the plan's state.
func TestCommentsFollowTheFlowsEventsNotItsState(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.FlowV.WaitingOn = string(ActorSession)
	store.setEvents("approve-for-building")
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "x")
	m = drive(t, m, press("enter"))
	assert.Check(t, !m.keys().SendComments.Enabled(), "s sends comments the flow does not take")
	m = drive(t, m, press("s"))
	assert.Assert(t, cmp.Len(store.Added, 0), "comments saved with address-comments not applicable")

	store.mu.Lock()
	store.StateV = "refine-units"
	store.mu.Unlock()
	store.setEvents("address-comments")
	m = drive(t, m, pollMsg{})
	assert.Assert(t, m.keys().SendComments.Enabled(), "s does not send comments the flow takes")
	drive(t, m, press("s"))

	assert.Check(t, cmp.Len(store.Added, 1))
	assert.Check(t, cmp.DeepEqual(store.HandedOff, []string{"refine-units"}))
}

// TestHandOffRetriedOnNextSendWithNothingQueued checks that a hand-off event
// failing after every comment was saved is retried on the next send, even
// with nothing queued, rather than leaving the plan stuck in its Human state,
// and that the retry sends the event the flow lists then.
func TestHandOffRetriedOnNextSendWithNothingQueued(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		first     []string
		retry     []string
		handedOff []string
		answered  []string
	}{
		{
			name:      "address-comments",
			first:     []string{"address-comments"},
			retry:     []string{"address-comments"},
			handedOff: []string{"refine-proposal"},
		},
		{
			name:     "to answer-open-questions",
			first:    []string{"address-comments"},
			retry:    []string{"answer-open-questions"},
			answered: []string{"refine-proposal"},
		},
		{
			name:      "to address-comments",
			first:     []string{"answer-open-questions"},
			retry:     []string{"address-comments"},
			handedOff: []string{"refine-proposal"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.TextV = twoParts
			store.setEvents(tc.first...)
			store.AddressErr = errors.New("transient")
			agent := turnReturning("ok")
			m := New(context.Background(), agent, nil, store, "plan it")
			m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
			agent.reset()

			m = drive(t, m, press("tab"))
			m = drive(t, m, press("down"))
			m = drive(t, m, press("c"))
			m = typeText(t, m, "first")
			m = drive(t, m, press("enter"))
			m = drive(t, m, press("tab"))
			m = drive(t, m, press("enter"))

			if len(store.Added) != 1 {
				t.Fatalf("comment not saved: %+v", store.Added)
			}
			assert.Check(t, cmp.Len(store.HandedOff, 0))
			assert.Check(t, cmp.Len(store.Answered, 0))
			if len(agent.Sent()) != 0 {
				t.Fatalf("a turn started despite the hand-off failing")
			}
			if len(queuedComments(m.comments)) != 0 {
				t.Fatalf("the saved comment should not stay queued: %+v", m.comments)
			}
			assert.Check(t, m.moving == nil, "a failed event started the wait")

			store.AddressErr = nil
			store.setEvents(tc.retry...)
			// The retry reads m.events: poll so it sees tc.retry.
			m = drive(t, m, pollMsg{})
			m = drive(t, m, press("enter"))

			assert.Check(t, cmp.DeepEqual(store.HandedOff, tc.handedOff))
			assert.Check(t, cmp.DeepEqual(store.Answered, tc.answered))
			if len(store.Added) != 1 {
				t.Fatalf("the already-saved comment was saved again: %+v", store.Added)
			}
			assert.Check(t, cmp.Len(agent.Sent(), 0), "the retried hand-off sent to the agent")
			assert.Check(t, m.moving != nil)
		})
	}
}

func TestSaveFailureKeepsRestQueued(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.FailAddAfter = 1
	agent := turnReturning("ok")
	m := New(context.Background(), agent, nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	agent.reset()

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "first")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "second")
	m = drive(t, m, press("enter"))
	if len(m.comments) != 2 {
		t.Fatalf("comments before send: %+v", m.comments)
	}

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))

	if len(store.Added) != 1 {
		t.Fatalf("saved: %+v", store.Added)
	}
	queued := queuedComments(m.comments)
	if len(queued) != 1 || queued[0].text != "second" {
		t.Fatalf("queued comments after failure: %+v (all comments: %+v)", queued, m.comments)
	}
	if len(m.comments) != 2 {
		t.Fatalf("the saved comment should still show as open: %+v", m.comments)
	}
	if len(store.HandedOff) != 0 {
		t.Errorf("handed off despite the failure: %+v", store.HandedOff)
	}
	if len(agent.Sent()) != 0 {
		t.Errorf("a turn started despite the failure")
	}
}

func TestEditingSavedCommentPutsBody(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	c, _ := store.AddComment(context.Background(), "The flag exists.", "orig")
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)
	if len(m.comments) != 1 || m.comments[0].id == "" {
		t.Fatalf("comments after refresh: %+v", m.comments)
	}

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	if m.editingID == "" {
		t.Fatalf("editingID not set for a saved comment")
	}
	m.commentInput.SetValue("")
	m = typeText(t, m, "better text")
	m = drive(t, m, press("enter"))

	if store.Edited[c.ID] != "better text" {
		t.Errorf("edited: %+v", store.Edited)
	}
	if m.commenting {
		t.Error("the editor is still open after saving")
	}
}

// TestClearingSavedCommentResolvesIt checks editCmd's documented contract:
// clearing a saved comment's editor to empty and saving resolves it rather
// than editing its body to empty text.
func TestClearingSavedCommentResolvesIt(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	c, _ := store.AddComment(context.Background(), "The flag exists.", "orig")
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m.commentInput.SetValue("")
	m = drive(t, m, press("enter"))

	if len(store.Resolved) != 1 || store.Resolved[0] != c.ID {
		t.Fatalf("resolved: %+v, want [%s]", store.Resolved, c.ID)
	}
	if _, edited := store.Edited[c.ID]; edited {
		t.Errorf("EditComment was called instead of ResolveComment: %+v", store.Edited)
	}
}

// TestEditFailureKeepsEditorOpen checks that editing a saved comment shows
// "Saving…" while the request is in flight, and, on failure, stays open
// with the typed text and no committed edit.
func TestEditFailureKeepsEditorOpen(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	c, _ := store.AddComment(context.Background(), "The flag exists.", "orig")
	store.EditErr = errors.New("boom")
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m.commentInput.SetValue("")
	m = typeText(t, m, "better text")

	m, cmd := m.Update(press("enter"))
	if !m.savingEdit || !m.commenting {
		t.Fatalf("editor did not enter the saving state: savingEdit=%v commenting=%v", m.savingEdit, m.commenting)
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "Saving…") {
		t.Errorf("view does not show Saving…:\n%s", view)
	}
	for _, msg := range run(t, cmd) {
		m = drive(t, m, msg)
	}

	if !m.commenting {
		t.Error("the editor closed despite the save failing")
	}
	if got := m.commentInput.Value(); got != "better text" {
		t.Errorf("editor text is %q, want the typed text kept", got)
	}
	if store.Edited[c.ID] == "better text" {
		t.Error("the edit should not have gone through")
	}
	if !strings.Contains(ansi.Strip(m.status), "boom") {
		t.Errorf("status does not show the error: %q", m.status)
	}
}

func TestSelectionOverSavedCommentDeletesItAfterPost(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	c, _ := store.AddComment(context.Background(), "The flag exists.", "orig")
	m := New(context.Background(), turnReturning("ok"), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("shift+down"))
	m = drive(t, m, press("c"))
	if got := m.commentInput.Value(); got != "orig" {
		t.Fatalf("editor holds %q, want the saved comment's text", got)
	}
	m = typeText(t, m, " plus the spec")
	m = drive(t, m, press("enter"))

	if len(m.comments) != 1 || m.comments[0].id != "" {
		t.Fatalf("the saved comment is not hidden at once: %+v", m.comments)
	}
	if len(store.Deleted) != 0 {
		t.Fatalf("the saved comment was deleted before sending: %+v", store.Deleted)
	}

	m = drive(t, m, press("s"))

	if len(store.Deleted) != 1 || store.Deleted[0] != c.ID {
		t.Fatalf("deleted: %+v, want [%s]", store.Deleted, c.ID)
	}
}

// TestMergeAccumulatesReplacesAcrossQueuedComments checks that merging a
// queued comment, which already carries a saved comment's id in replaces,
// into a further selection keeps that id, so a chain of merges still
// deletes every saved comment it ever absorbed once it is sent.
func TestMergeAccumulatesReplacesAcrossQueuedComments(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	saved, _ := store.AddComment(context.Background(), "## 1. Outcome", "orig")
	m := New(context.Background(), turnReturning("ok"), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	// Merge the saved comment (block 0) with block 1 into a queued one.
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("shift+down"))
	m = drive(t, m, press("c"))
	m.commentInput.SetValue("")
	m = typeText(t, m, "merge1")
	m = drive(t, m, press("enter"))

	if len(m.comments) != 1 || m.comments[0].id != "" || !slices.Contains(m.comments[0].replaces, saved.ID) {
		t.Fatalf("after first merge: %+v", m.comments)
	}

	// Merge that queued comment with block 2 into a second one.
	m = drive(t, m, press("shift+down"))
	m = drive(t, m, press("c"))
	m.commentInput.SetValue("")
	m = typeText(t, m, "merge2")
	m = drive(t, m, press("enter"))

	if len(m.comments) != 1 || m.comments[0].id != "" {
		t.Fatalf("after second merge: %+v", m.comments)
	}
	if !slices.Contains(m.comments[0].replaces, saved.ID) {
		t.Fatalf("second merge lost the original saved comment's id: %+v", m.comments[0].replaces)
	}

	// Sending must delete the originally saved comment, not just the one
	// absorbed directly into this queued comment.
	m = drive(t, m, press("s"))

	if len(store.Deleted) != 1 || store.Deleted[0] != saved.ID {
		t.Fatalf("deleted: %+v, want [%s]", store.Deleted, saved.ID)
	}
}

func TestResolveKeyResolves(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	c, _ := store.AddComment(context.Background(), "The flag exists.", "x")
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	drive(t, m, press("r"))

	if len(store.Resolved) != 1 || store.Resolved[0] != c.ID {
		t.Fatalf("resolved: %+v", store.Resolved)
	}
}

func TestDeleteSavedCommentAsksFirst(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	c, _ := store.AddComment(context.Background(), "The flag exists.", "x")
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("d"))
	if !m.confirming || len(store.Deleted) != 0 {
		t.Fatalf("delete happened without asking: confirming=%v deleted=%+v", m.confirming, store.Deleted)
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "Delete this comment in factory?") {
		t.Errorf("no confirmation shown:\n%s", view)
	}
	m = drive(t, m, press("y"))
	if len(store.Deleted) != 1 || store.Deleted[0] != c.ID {
		t.Fatalf("deleted: %+v", store.Deleted)
	}
}

// TestSendHandsOffWithTheEventTheFlowLists checks that sending comments hands
// them to factory from each Human state past Planning, with the event the
// flow lists: answer-open-questions for a plan with open questions, over
// address-comments when both are listed, and address-comments otherwise.
func TestSendHandsOffWithTheEventTheFlowLists(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"build", "integrate"} {
		for _, tc := range []struct {
			name      string
			events    []string
			handedOff []string
			answered  []string
		}{
			{
				name:      "address-comments",
				events:    []string{"address-comments", "abandon"},
				handedOff: []string{state},
			},
			{
				name:     "answer-open-questions",
				events:   []string{"answer-open-questions", "abandon"},
				answered: []string{state},
			},
			{
				name:     "both",
				events:   []string{"answer-open-questions", "address-comments", "abandon"},
				answered: []string{state},
			},
		} {
			t.Run(state+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				store := newFakeStore()
				store.TextV = twoParts
				store.StateV = state
				store.setEvents(tc.events...)
				m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
				m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})

				m = drive(t, m, press("tab"))
				// A plan past Planning opens on the Flow tab.
				m = drive(t, m, press("right"))
				m = drive(t, m, press("down"))
				m = drive(t, m, press("c"))
				m = typeText(t, m, "first")
				m = drive(t, m, press("enter"))
				m = drive(t, m, press("tab"))
				assert.Check(t, m.keys().Send.Enabled(), "enter in the Activity pane does not send the comments")
				drive(t, m, press("enter"))

				assert.Check(t, cmp.Len(store.Added, 1))
				assert.Check(t, cmp.DeepEqual(store.HandedOff, tc.handedOff))
				assert.Check(t, cmp.DeepEqual(store.Answered, tc.answered))
			})
		}
	}
}

// TestSecondSendWhileSavingCommentsDoesNothing checks that a second enter in
// the chat while queued comments are still being saved does not start a
// second save.
func TestSecondSendWhileSavingCommentsDoesNothing(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "first")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("tab"))

	m.savingComments = true
	before := len(store.Added)
	next, cmd := m.Update(press("enter"))
	if cmd != nil {
		t.Error("a second enter while saving comments started another save")
	}
	if len(store.Added) != before {
		t.Errorf("comments saved again while already saving: %+v", store.Added)
	}
	if len(queuedComments(next.comments)) != 1 {
		t.Errorf("the queued comment was dropped by the second enter: %+v", next.comments)
	}
}

func TestFailedCreateShowsInTheChatAndIsTriedAgain(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.CreateErr = errors.New("factory said no")
	store.TitleV = "add-a-flag"
	agent := newFakeAgent()
	m := New(context.Background(), agent, nil, store, "add a --dry-run flag").WithInbox("New plan", "chunk")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	if got := agent.Sent(); len(got) != 0 {
		t.Fatalf("sent before the plan existed: %q", got)
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "Could not create the plan: factory said no") {
		t.Errorf("the failure is not in the chat:\n%s", view)
	}
	if m.busy {
		t.Error("still busy after the create failed")
	}
	assert.Check(t, cmp.Contains(ansi.Strip(m.header()), "New plan"))

	store.mu.Lock()
	store.CreateErr = nil
	store.mu.Unlock()
	m = typeText(t, m, "add a flag")
	m = drive(t, m, press("enter"))
	if got := agent.Sent(); len(got) != 1 || got[0] != "add a flag" {
		t.Errorf("agent got %q", got)
	}
	assert.Check(t, cmp.Contains(ansi.Strip(m.header()), "chunk plan / add-a-flag"))
}

// Once factory has created the plan, the title the host gave the screen is
// replaced with the plan's own; a screen with no title keeps the branch.
func TestTheHeaderNamesThePlanOnceCreated(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		host      func(Model) Model
		width     int
		title     string
		createErr error
		want      string
		notWant   string
	}{
		{
			name:    "the plan's title replaces New plan",
			host:    func(m Model) Model { return m.WithInbox("New plan", "chunk") },
			width:   120,
			title:   "replace-header-plan",
			want:    "chunk plan / replace-header-plan",
			notWant: "New plan",
		},
		{
			name:  "a long title is cut down",
			host:  func(m Model) Model { return m.WithInbox("New plan", "chunk") },
			width: 60, // narrow enough that the header cuts the title down
			title: "a-very-long-plan-title-indeed",
			want:  "chunk plan / a-very-long-p…",
		},
		{
			name:      "a failed create keeps New plan",
			host:      func(m Model) Model { return m.WithInbox("New plan", "chunk") },
			width:     120,
			title:     "replace-header-plan",
			createErr: errors.New("factory said no"),
			want:      "chunk plan / New plan",
			notWant:   "replace-header-plan",
		},
		{
			name:  "no title keeps New plan",
			host:  func(m Model) Model { return m.WithInbox("New plan", "chunk") },
			width: 120,
			want:  "chunk plan / New plan",
		},
		{
			name:    "the standalone screen keeps its branch",
			host:    func(m Model) Model { return m.WithBranch("main") },
			width:   120,
			title:   "replace-header-plan",
			want:    "chunk plan / main",
			notWant: "replace-header-plan",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.TitleV, store.CreateErr = tc.title, tc.createErr
			m := tc.host(New(context.Background(), newFakeAgent(), nil, store, ""))
			m = drive(t, m, tea.WindowSizeMsg{Width: tc.width, Height: 40})
			m = typeText(t, m, "replace the header")
			m, create := m.Update(press("enter"))
			for _, msg := range run(t, create) {
				m = drive(t, m, msg)
			}

			header := ansi.Strip(m.header())
			assert.Check(t, cmp.Contains(header, tc.want))
			if tc.notWant != "" {
				assert.Check(t, !strings.Contains(header, tc.notWant), "header: %q", header)
			}
		})
	}
}

// The plan is created off the screen's loop; a second enter meanwhile does
// not race the create.
func TestMessageWhileThePlanIsCreatedWaitsInTheInput(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	agent := newFakeAgent()
	m := New(context.Background(), agent, nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = typeText(t, m, "first")
	m, create := m.Update(press("enter"))
	if !m.creating {
		t.Fatal("the plan is not being created")
	}

	m, cmd := m.Update(press("enter"))
	assert.Check(t, cmd == nil, "a second enter while the plan is created did something")

	for _, msg := range run(t, create) {
		m = drive(t, m, msg)
	}
	if got := agent.Sent(); len(got) != 1 || got[0] != "first" {
		t.Errorf("agent got %q", got)
	}
	if !m.created || m.creating {
		t.Errorf("created %v, creating %v", m.created, m.creating)
	}
}

// Saving drops the queued comments it saved, so they cannot change until it
// is done; one deleted or rewritten meanwhile would be lost.
func TestQueuedCommentsStayAsTheyAreWhileSaved(t *testing.T) {
	t.Parallel()
	m, store := planModel(t)
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))

	m, save := m.Update(press("s"))
	if !m.savingComments {
		t.Fatal("the comments are not being saved")
	}
	m, _ = m.Update(press("c"))
	if m.commenting {
		t.Error("the editor opened while the comments were saved")
	}
	m, _ = m.Update(press("d"))
	if n := len(queuedComments(m.comments)); n != 1 {
		t.Errorf("%d queued while saving, want 1", n)
	}

	for _, msg := range run(t, save) {
		m = drive(t, m, msg)
	}
	if len(store.Added) != 1 || len(queuedComments(m.comments)) != 0 {
		t.Errorf("saved %+v, still queued %+v", store.Added, queuedComments(m.comments))
	}
}

func idlePlan(t *testing.T) Model {
	t.Helper()
	m, _ := planModel(t)
	return m
}

// With no host to go back to, esc closes the screen when it has nothing
// else to do, asking first when that would drop something unsent.
func TestEscQuitsAScreenWithNoHost(t *testing.T) {
	t.Parallel()
	m := idlePlan(t)
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "esc quit") {
		t.Errorf("the footer does not say esc quits:\n%s", view)
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd == nil {
		t.Fatal("esc with nothing unsent did not quit")
	}

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("esc with an unsent comment quit at once")
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "1 unsent comment will be lost. Press esc again") {
		t.Errorf("no warning naming esc about the unsent comment:\n%s", view)
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd == nil {
		t.Error("a second esc did not quit")
	}
}

// q leaves from the plan and the Activity pane, where nothing is being
// typed, and is only a letter in a new plan's request.
func TestQQuitsFromThePlanOnly(t *testing.T) {
	t.Parallel()
	// The request's text box is shared by copies of the model, so each part
	// starts from a screen of its own.
	request := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	request = drive(t, request, tea.WindowSizeMsg{Width: 140, Height: 40})
	request, cmd := request.Update(press("q"))
	if _, ok := closedBy(cmd); ok {
		t.Error("q in the request quit")
	}
	if got := request.input.Value(); got != "q" {
		t.Errorf("q in the request typed %q, want q", got)
	}

	activity := idlePlan(t)
	assert.Assert(t, cmp.Equal(activity.focus, paneChat))
	activity, cmd = activity.Update(press("q"))
	if _, ok := closedBy(cmd); !ok {
		t.Error("q in the Activity pane did not quit")
	}
	assert.Check(t, cmp.Equal(activity.input.Value(), ""))

	plan := idlePlan(t)
	plan = drive(t, plan, press("tab"))
	_, cmd = plan.Update(press("q"))
	if _, ok := closedBy(cmd); !ok {
		t.Error("q in the plan did not quit")
	}
}

func TestNMovesToThePreviousComment(t *testing.T) {
	t.Parallel()
	m := idlePlan(t)
	m = drive(t, m, press("tab"))
	comment := func(m Model, text string) Model {
		m = drive(t, m, press("c"))
		m = typeText(t, m, text)
		return drive(t, m, press("enter"))
	}
	m = comment(m, "first")
	m = drive(t, m, press("j"))
	m = drive(t, m, press("j"))
	m = comment(m, "second")
	if m.cursor == 0 {
		t.Fatal("the cursor did not move down")
	}
	last := m.cursor

	m = drive(t, m, press("N"))
	if m.cursor >= last {
		t.Errorf("N stayed at %d, want a comment above %d", m.cursor, last)
	}
	above := m.cursor
	m = drive(t, m, press("N"))
	if m.cursor != above {
		t.Errorf("N with no comment above moved to %d from %d", m.cursor, above)
	}
	m = drive(t, m, press("n"))
	if m.cursor != last {
		t.Errorf("n went to %d, want back at %d", m.cursor, last)
	}
}

// flowModel is a screen of 140×40 on store, refreshed once, so the first
// refresh has picked the right pane's tab.
func flowModel(t *testing.T, store *fakeStore) Model {
	t.Helper()
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	return refreshed(t, m)
}

// threeUnitsRead are a plan's three units, the first done, as factory reads them.
var threeUnitsRead = []Unit{{Name: "Unit 1: lexer", Done: true}, {Name: "Unit 2: parser"}, {Name: "Unit 3: printer"}}

func TestRightPaneOpensOnTheTabForItsPhase(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		state string
		want  rightTab
	}{
		{
			name:  "planning",
			state: "refine-proposal",
		},
		{
			name:  "refine-units",
			state: "refine-units",
		},
		{
			name:  "ready",
			state: "ready",
		},
		{
			name:  "unprioritized",
			state: "init",
		},
		{
			name: "no state",
		},
		{
			name:  "stand-by",
			state: "stand-by",
			want:  tabFlow,
		},
		{
			name:  "building",
			state: "build",
			want:  tabFlow,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.StateV, store.TextV = tc.state, twoParts
			m := flowModel(t, store)
			assert.Check(t, cmp.Equal(m.tab, tc.want))

			// A later move leaves the tab where it is.
			store.mu.Lock()
			store.StateV = "integrate"
			if tc.want == tabFlow {
				store.StateV = "refine-proposal"
			}
			store.mu.Unlock()
			m = refreshed(t, m)
			assert.Check(t, cmp.Equal(m.tab, tc.want), "a state change switched the tab")
		})
	}
}

func TestTabKeysSwitchTheRightPane(t *testing.T) {
	t.Parallel()
	t.Run("in the request", func(t *testing.T) {
		t.Parallel()
		m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
		m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
		m = refreshed(t, m)
		m = typeText(t, m, "fp")
		m = drive(t, m, press("left"))
		m = drive(t, m, press("left"))
		m = drive(t, m, press("right"))
		m = typeText(t, m, "x")
		assert.Check(t, cmp.Equal(m.input.Value(), "fxp"), "← and → in the request move the cursor")
		assert.Check(t, cmp.Equal(m.tab, tabPlan))
	})

	t.Run("in the Activity pane", func(t *testing.T) {
		t.Parallel()
		m, _ := planModel(t)
		m = refreshed(t, m)
		assert.Assert(t, cmp.Equal(m.focus, paneChat))
		for _, k := range []string{"f", "p", "left", "right"} {
			m = drive(t, m, press(k))
		}
		assert.Check(t, cmp.Equal(m.tab, tabPlan))
		assert.Check(t, cmp.Equal(m.input.Value(), ""))
	})

	m, store := planModel(t)
	m = drive(t, m, press("tab"))
	// A queued comment on the plan, for the Flow tab's keys to leave alone.
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))
	assert.Assert(t, cmp.Len(queuedComments(m.comments), 1))

	m = drive(t, m, press("f"))
	assert.Check(t, cmp.Equal(m.tab, tabPlan), "f still switches to Flow")
	m = drive(t, m, press("left"))
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "PROGRESS"))
	assert.Check(t, !strings.Contains(view, "The flag exists."), view)

	// The plan's keys belong to the Plan tab.
	for _, k := range []string{"s", "e", "d", "r", "n", "c", "enter"} {
		m = drive(t, m, press(k))
	}
	assert.Check(t, !m.commenting, "a key on the Flow tab opened the comment editor")
	assert.Check(t, !m.editing && !m.confirming)
	assert.Check(t, cmp.Len(queuedComments(m.comments), 1))
	store.mu.Lock()
	assert.Check(t, cmp.Len(store.Added, 0))
	assert.Check(t, cmp.Len(store.Resolved, 0))
	store.mu.Unlock()

	m = drive(t, m, press("p"))
	assert.Check(t, cmp.Equal(m.tab, tabFlow), "p still switches to Plan")
	m = drive(t, m, press("right"))
	view = ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "The flag exists."))
	assert.Check(t, !strings.Contains(view, "PROGRESS"), view)

	h := m.keys().FlowTab.Help()
	assert.Check(t, cmp.Equal(h.Key, "←/→"))
	assert.Check(t, cmp.Equal(h.Desc, "flow/plan"))

	m = drive(t, m, press("right"))
	assert.Check(t, cmp.Equal(m.tab, tabPlan), "→ on the Plan tab moved")
	m = drive(t, m, press("left"))
	assert.Check(t, cmp.Equal(m.tab, tabFlow))
	m = drive(t, m, press("left"))
	assert.Check(t, cmp.Equal(m.tab, tabFlow), "← on the Flow tab moved")
	m = drive(t, m, press("right"))
	assert.Check(t, cmp.Equal(m.tab, tabPlan))

	// While a comment is written the arrows move its cursor. The block under
	// the cursor has the queued comment, so move to the next one first.
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	assert.Assert(t, m.commenting)
	assert.Assert(t, cmp.Equal(m.commentInput.Value(), ""))
	m = typeText(t, m, "ab")
	m = drive(t, m, press("left"))
	m = typeText(t, m, "x")
	assert.Check(t, cmp.Equal(m.commentInput.Value(), "axb"))
	m = drive(t, m, press("right"))
	assert.Check(t, cmp.Equal(m.tab, tabPlan), "an arrow in the comment editor switched tabs")
}

func TestFlowTabShowsProgress(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		units   []Unit
		unit    string
		family  Family
		flow    Flow
		want    []string
		without []string
	}{
		{
			name:  "units",
			units: threeUnitsRead,
			unit:  "Unit 2: parser",
			want:  []string{"✓ 1  lexer", "▸ 2  parser", "○ 3  printer"},
		},
		{
			name:    "a task unit that matches none",
			units:   threeUnitsRead,
			unit:    "Unit 9: nothing",
			want:    []string{"✓ 1  lexer", "○ 2  parser", "○ 3  printer"},
			without: []string{"▸"},
		},
		{
			name: "no units",
			want: []string{"No units"},
		},
		{
			name:  "units factory answers as an empty list",
			units: []Unit{},
			want:  []string{"No units"},
		},
		{
			name:  "open sub-plans",
			units: threeUnitsRead,
			family: Family{Children: []Relative{
				{ID: "c1", Title: "Lex it", State: "build"},
				{ID: "c2", Title: "Print it", State: "refine-proposal"},
			}},
			want:    []string{"Progress · 2 open sub-plans", "▸ Lex it  build", "○ Print it  refine-proposal"},
			without: []string{"lexer"},
		},
		{
			name:   "a family without sub-plans",
			units:  threeUnitsRead,
			family: Family{Parent: &Relative{ID: "p", Title: "Everything", State: "build"}},
			want:   []string{"✓ 1  lexer", "○ 2  parser"},
		},
		{
			name:  "summary and counts",
			units: threeUnitsRead,
			unit:  "Unit 2: parser",
			flow:  Flow{Progress: &Progress{Summary: "Building unit 2 of 3", Findings: 3, Fixups: 1, Deviations: ptr(2), BindingDeviations: ptr(1)}},
			want:  []string{"Building unit 2 of 3 · 3 review findings · 2 deviations (1 binding) · 1 fixup left"},
		},
		{
			name:  "counts without a summary",
			units: threeUnitsRead,
			flow:  Flow{Progress: &Progress{Findings: 1}},
			want:  []string{"1 review finding"},
		},
		{
			name:    "nothing to say: no line",
			units:   threeUnitsRead,
			flow:    Flow{Progress: &Progress{Deviations: ptr(0)}},
			without: []string{"·", "deviation"},
		},
		{
			name:  "waiting on with why",
			units: threeUnitsRead,
			flow:  Flow{WaitingOn: "session", WaitingOnWhy: "the session builds Unit 2: parser"},
			want:  []string{"Waiting on session: the session builds Unit 2: parser"},
		},
		{
			name:    "waiting on without why",
			units:   threeUnitsRead,
			flow:    Flow{WaitingOn: "human"},
			want:    []string{"Waiting on human"},
			without: []string{"human:"},
		},
		{
			name:    "no waiting_on: no line",
			units:   threeUnitsRead,
			without: []string{"Waiting on"},
		},
		{
			name:  "current_unit wins over the task's unit",
			units: threeUnitsRead,
			unit:  "Unit 2: parser",
			flow:  Flow{Progress: &Progress{CurrentUnit: "Unit 3: printer"}},
			want:  []string{"○ 2  parser", "▸ 3  printer"},
		},
		{
			name:    "a current_unit that matches none: no ▸, no fallback",
			units:   threeUnitsRead,
			unit:    "Unit 2: parser",
			flow:    Flow{Progress: &Progress{CurrentUnit: "Unit 9: nothing"}},
			want:    []string{"○ 2  parser"},
			without: []string{"▸"},
		},
		{
			name:  "a unit with no name is ▸ by its number alone",
			units: []Unit{{Name: "Unit 1:"}, {Name: "Unit 2: b"}},
			flow:  Flow{Progress: &Progress{CurrentUnit: "Unit 1:"}},
			want:  []string{"▸ 1", "○ 2  b"},
		},
		{
			name:    "a heading not in Unit N: name form never gets ▸",
			units:   []Unit{{Name: "unit 1 - x"}},
			flow:    Flow{Progress: &Progress{CurrentUnit: "unit 1 - x"}},
			want:    []string{"○ 1  unit 1 - x"},
			without: []string{"▸"},
		},
		{
			name:   "sub-plans keep the summary line",
			units:  threeUnitsRead,
			family: Family{Children: []Relative{{ID: "c1", Title: "Lex it", State: "build"}}},
			flow:   Flow{Progress: &Progress{Summary: "Waiting on 1 sub-plan"}},
			want:   []string{"Waiting on 1 sub-plan", "Progress · 1 open sub-plan", "▸ Lex it"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.StateV, store.UnitsV = "build", tc.units
			store.FlowV.Progress = tc.flow.Progress
			store.FlowV.WaitingOn, store.FlowV.WaitingOnWhy = tc.flow.WaitingOn, tc.flow.WaitingOnWhy
			store.FlowV.Unit, store.FamilyV = tc.unit, tc.family
			m := flowModel(t, store)
			assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "PROGRESS"))
			// The tab alone, wide enough that no line is cut: the rest of the
			// screen has its own "·".
			view := ansi.Strip(m.flowContent(200))
			for _, w := range tc.want {
				assert.Check(t, cmp.Contains(view, w))
			}
			for _, w := range tc.without {
				assert.Check(t, !strings.Contains(view, w), "%q shows:\n%s", w, view)
			}
			assertFits(t, m, 140, 40)
		})
	}
}

// TestFlowTabPRIsAHyperlink checks that the pull request's URL is sent as a
// terminal hyperlink when it is http(s), and as plain text when it is not.
func TestFlowTabPRIsAHyperlink(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, url string
		link      bool
	}{
		{"https", "https://github.com/o/r/pull/7", true},
		{"not http", "javascript:alert(1)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.StateV, store.UnitsV = "Integrating - Agent", threeUnitsRead
			store.FlowV.PR = tc.url
			raw := flowModel(t, store).flowContent(200)
			if tc.link {
				assert.Check(t, cmp.Contains(raw, ansi.SetHyperlink(tc.url)))
				return
			}
			noHyperlink(t, raw)
		})
	}
	t.Run("control character", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.StateV, store.UnitsV = "Integrating - Agent", threeUnitsRead
		store.FlowV.PR = "https://github.com/o/r\x1b]0;x\x07"
		raw := flowModel(t, store).flowContent(200)
		assert.Check(t, !strings.Contains(raw, "\x1b]0;"), "no injected escape")
	})
}

func TestFlowTabShowsThePR(t *testing.T) {
	t.Parallel()
	const url = "https://github.com/o/r/pull/7"
	store := newFakeStore()
	store.StateV, store.UnitsV = "integrate", threeUnitsRead
	store.FlowV.PR = url
	view := ansi.Strip(flowModel(t, store).Content())
	assert.Check(t, cmp.Contains(view, "PULL REQUEST"))
	assert.Check(t, cmp.Contains(view, url))
	assert.Check(t, !strings.Contains(view, "• "), "deviations listed with none:\n%s", view)

	store.FlowV.Deviations = []string{"lint", "tests"}
	view = ansi.Strip(flowModel(t, store).Content())
	assert.Check(t, cmp.Contains(view, "• lint"))
	assert.Check(t, cmp.Contains(view, "• tests"))

	store.FlowV = Flow{}
	view = ansi.Strip(flowModel(t, store).Content())
	assert.Check(t, !strings.Contains(view, "PULL REQUEST"), view)
}

// enterOn presses enter on m and returns what the command it gives back
// says, or nil for none.
func enterOn(m Model) (Model, tea.Msg) {
	m, cmd := m.Update(press("enter"))
	if cmd == nil {
		return m, nil
	}
	return m, cmd()
}

func TestRelatedOpensARelative(t *testing.T) {
	t.Parallel()
	family := Family{
		Parent:   &Relative{ID: "p1", Title: "Parent plan", State: "build"},
		Children: []Relative{{ID: "c1", Title: "Child plan", State: "refine-units"}},
		Blockers: []Relative{{ID: "b1", Title: "Blocker plan", State: "integrate", Active: true}},
	}
	inbox := func(t *testing.T, fam Family) (Model, *fakeStore) {
		t.Helper()
		store := newFakeStore()
		store.Created = true
		store.StateV, store.UnitsV, store.FamilyV = "build", threeUnitsRead, fam
		m := New(context.Background(), newFakeAgent(), nil, store, "").WithInbox("Plan", "/src/p")
		m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
		// An existing plan opens on the right pane.
		return refreshed(t, m), store
	}

	t.Run("enter opens the row under the cursor", func(t *testing.T) {
		t.Parallel()
		m, _ := inbox(t, family)
		view := ansi.Strip(m.Content())
		for _, w := range []string{"RELATED", "Part of", "Parent plan", "Sub-plan", "Child plan", "Blocked by", "Blocker plan", "integrate"} {
			assert.Check(t, cmp.Contains(view, w))
		}
		assert.Check(t, cmp.Contains(view, "▶ Part of"))
		m = drive(t, m, press("down"))
		m = drive(t, m, press("down"))
		assert.Check(t, cmp.Equal(m.related, 2))
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "▶ Blocked by"))
		_, msg := enterOn(m)
		assert.Check(t, cmp.DeepEqual(msg, OpenMsg{ID: "b1", Title: "Blocker plan"}))
	})

	t.Run("a poll with fewer rows keeps the cursor on one", func(t *testing.T) {
		t.Parallel()
		m, store := inbox(t, family)
		m = drive(t, m, press("down"))
		m = drive(t, m, press("down"))
		store.mu.Lock()
		store.FamilyV = Family{Parent: family.Parent}
		store.mu.Unlock()
		m = refreshed(t, m)
		assert.Check(t, cmp.Equal(m.related, 0))
		_, msg := enterOn(m)
		assert.Check(t, cmp.DeepEqual(msg, OpenMsg{ID: "p1", Title: "Parent plan"}))

		store.mu.Lock()
		store.FamilyV = Family{}
		store.mu.Unlock()
		m = refreshed(t, m)
		_, msg = enterOn(m)
		assert.Check(t, msg == nil, "enter with no rows gave %#v", msg)
	})

	t.Run("no rows", func(t *testing.T) {
		t.Parallel()
		m, _ := inbox(t, Family{})
		assert.Check(t, !strings.Contains(ansi.Strip(m.Content()), "RELATED"))
		_, msg := enterOn(m)
		assert.Check(t, msg == nil, "enter with no rows gave %#v", msg)
	})

	t.Run("without an inbox", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.StateV, store.UnitsV, store.FamilyV = "build", threeUnitsRead, family
		m := drive(t, flowModel(t, store), press("tab"))
		view := ansi.Strip(m.Content())
		assert.Check(t, cmp.Contains(view, "Parent plan"))
		assert.Check(t, !strings.Contains(view, "▶"), "a cursor without an inbox:\n%s", view)
		_, msg := enterOn(m)
		assert.Check(t, msg == nil, "enter without an inbox gave %#v", msg)
	})
}

func TestFamilyErrorIsQuiet(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.FamilyErr = errors.New("boom")
	m := flowModel(t, store)
	assert.Check(t, cmp.Equal(m.refreshErr, ""))
	row2 := func(m Model) string { return ansi.Strip(strings.Split(m.Content(), "\n")[headerRows+1]) }
	assert.Check(t, cmp.Contains(row2(m), "could not read related plans: boom"))

	store.mu.Lock()
	store.TextV = "## 1. Outcome\n\nA new outcome."
	store.mu.Unlock()
	m = refreshed(t, m)
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "A new outcome."))
	assert.Check(t, cmp.Equal(m.refreshErr, ""))

	store.mu.Lock()
	store.FamilyErr = nil
	store.mu.Unlock()
	m = refreshed(t, m)
	assert.Check(t, !strings.Contains(row2(m), "could not read related plans"), row2(m))
}

// TestUnitsErrorFailsTheRefresh checks that a failed units read fails the
// plan's refresh, as a failed text read does: the screen keeps its last text
// and units, and the next good refresh clears the note.
func TestUnitsErrorFailsTheRefresh(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.StateV, store.TextV, store.UnitsV = "build", twoParts, []Unit{{Name: "Unit 1: lexer"}}
	m := flowModel(t, store)
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "1  lexer"))
	assert.Check(t, cmp.Equal(m.refreshErr, ""))

	store.mu.Lock()
	store.UnitsErr = errors.New("boom")
	store.TextV = "## 1. Outcome\n\nA new outcome."
	store.mu.Unlock()
	m = refreshed(t, m)
	assert.Check(t, cmp.Equal(m.refreshErr, "could not refresh the plan: boom"))
	assert.Check(t, cmp.Equal(m.plan, twoParts), "a failed units read keeps the last text")
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "1  lexer"))

	store.mu.Lock()
	store.UnitsErr = nil
	store.mu.Unlock()
	m = refreshed(t, m)
	assert.Check(t, cmp.Equal(m.refreshErr, ""))
	assert.Check(t, cmp.Contains(m.plan, "A new outcome."))
}

// TestCtrlSStartsAnUnprioritizedPlan checks that ctrl+s, after a y, sends
// prioritize for an init plan whose flow requires it of the developer, and
// refuses one whose flow does not offer it.
func TestCtrlSStartsAnUnprioritizedPlan(t *testing.T) {
	t.Parallel()
	unprioritized := func(t *testing.T, events ...string) (Model, *fakeStore) {
		t.Helper()
		store := newFakeStore()
		store.Created = true
		store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
		store.StateV = "init"
		store.setWaitingOn("human")
		store.setEvents(events...)
		store.setRequired("prioritize")
		m := New(context.Background(), turnReturning("ok"), nil, store, "")
		m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
		return refreshed(t, m), store
	}
	started := func(store *fakeStore) []string {
		store.mu.Lock()
		defer store.mu.Unlock()
		return slices.Clone(store.Started)
	}

	t.Run("y starts work", func(t *testing.T) {
		t.Parallel()
		m, store := unprioritized(t, "prioritize", "abandon")
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assert.Assert(t, m.confirming)
		assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "Start work on the plan?"))
		m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		assert.Check(t, cmp.DeepEqual(started(store), []string{"init"}))
		assert.Check(t, !m.startingWork)
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Starting work…"))
	})

	t.Run("n sends nothing", func(t *testing.T) {
		t.Parallel()
		m, store := unprioritized(t, "prioritize", "abandon")
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		m = drive(t, m, press("n"))
		assert.Check(t, !m.confirming)
		assert.Check(t, !m.pendingStart)
		m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		assert.Check(t, cmp.Len(started(store), 0))
	})

	t.Run("not offered", func(t *testing.T) {
		t.Parallel()
		m, store := unprioritized(t, "abandon")
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assert.Check(t, !m.confirming)
		assert.Check(t, cmp.Len(started(store), 0))
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "cannot be approved now: it is init"))
	})

	t.Run("a failed send says so", func(t *testing.T) {
		t.Parallel()
		m, store := unprioritized(t, "prioritize", "abandon")
		store.AddressErr = errors.New("factory is down")
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		assert.Check(t, !m.startingWork)
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Could not start work: factory is down"))
	})
}

// endScreen is a 140×40 screen on store's plan, created and in state, whose
// flow applies events now, refreshed once and with the plan pane focused.
func endScreen(t *testing.T, store *fakeStore, state string, events ...string) Model {
	t.Helper()
	store.Created = true
	store.TextV = twoParts
	store.StateV = state
	store.setEvents(events...)
	m := New(context.Background(), turnReturning("ok"), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	// An existing plan opens on the right pane.
	return refreshed(t, m)
}

// pressAll drives each of keys through m in turn.
func pressAll(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		m = drive(t, m, press(k))
	}
	return m
}

// TestAbandonThePlan checks that x, while factory offers end or abandon, asks
// for a reason and ends the plan with the one picked, and that any other key
// keeps the plan.
func TestAbandonThePlan(t *testing.T) {
	t.Parallel()
	ended := func(store *fakeStore) []string {
		store.mu.Lock()
		defer store.mu.Unlock()
		return slices.Clone(store.Ended)
	}
	live := []string{"address-comments", "abandon"}
	liveEnd := []string{"address-comments", "end"}
	for _, tc := range []struct {
		name   string
		state  string
		events []string
		ended  bool
		err    error
		check  func(t *testing.T, m Model, store *fakeStore)
	}{
		{
			name:   "not offered without end or abandon",
			state:  "refine-proposal",
			events: []string{"address-comments"},
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, !strings.Contains(ansi.Strip(m.actionBar()), "abandon plan"))
				m = pressAll(t, m, "x")
				assert.Check(t, !m.askingReason)
				assert.Check(t, cmp.Len(ended(store), 0))
			},
		},
		{
			name:   "c abandons as canceled",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "x abandon plan"))
				m = pressAll(t, m, "x")
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandon plan as: c canceled · w won't do · d duplicate · esc keep it"))
				m = pressAll(t, m, "c")
				assert.Check(t, cmp.DeepEqual(ended(store), []string{"canceled"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandoning: canceled"))
			},
		},
		{
			name:   "c ends as canceled when the flow offers end",
			state:  "build",
			events: liveEnd,
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "x abandon plan"))
				m = pressAll(t, m, "x")
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandon plan as: c canceled · w won't do · d duplicate · esc keep it"))
				m = pressAll(t, m, "c")
				assert.Check(t, cmp.DeepEqual(ended(store), []string{"canceled"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandoning: canceled"))
			},
		},
		{
			name:   "w abandons as won't do",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "w")
				assert.Check(t, cmp.DeepEqual(ended(store), []string{"wont-do"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandoning: won't do"))
			},
		},
		{
			name:   "d abandons as duplicate",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "d")
				assert.Check(t, cmp.DeepEqual(ended(store), []string{"duplicate"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandoning: duplicate"))
			},
		},
		{
			name:   "esc keeps the plan",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x")
				m, cmd := m.Update(press("esc"))
				_, gone := closedBy(cmd)
				assert.Check(t, !gone, "esc at the reason prompt left the screen")
				assert.Check(t, !m.askingReason)
				assert.Check(t, cmp.Equal(m.status, ""))
				assert.Check(t, cmp.Len(ended(store), 0))
			},
		},
		{
			name:   "q keeps the plan",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x")
				m, cmd := m.Update(press("q"))
				_, gone := closedBy(cmd)
				assert.Check(t, !gone, "q at the reason prompt left the screen")
				assert.Check(t, !m.askingReason)
				assert.Check(t, cmp.Equal(m.status, ""))
				assert.Check(t, cmp.Len(ended(store), 0))
			},
		},
		{
			name:   "a failed abandon says so",
			state:  "refine-proposal",
			events: live,
			err:    errors.New("factory is down"),
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x")
				m, cmd := m.Update(press("w"))
				assert.Assert(t, cmd != nil)
				m, _ = m.Update(cmd())
				assert.Check(t, !m.abandoning)
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Could not abandon the plan: factory is down"))
			},
		},
		{
			name:   "a refusal of the end says so",
			state:  "build",
			events: liveEnd,
			err:    errors.New("factory offers no event that ends the plan"),
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x")
				m, cmd := m.Update(press("d"))
				assert.Assert(t, cmd != nil)
				m, _ = m.Update(cmd())
				assert.Check(t, !m.abandoning)
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Could not abandon the plan: factory offers no event that ends the plan"))
			},
		},
		{
			name:  "not offered once the flow has ended",
			state: "ended",
			ended: true,
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, !strings.Contains(ansi.Strip(m.actionBar()), "abandon plan"))
				m = pressAll(t, m, "x")
				assert.Check(t, !m.askingReason)
			},
		},
		{
			name:   "a refresh keeps the prompt",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x")
				store.mu.Lock()
				store.StateV = "refine-units"
				store.mu.Unlock()
				m = refreshed(t, m)
				assert.Check(t, m.askingReason)
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandon plan as:"))
				m = pressAll(t, m, "w")
				assert.Check(t, cmp.DeepEqual(ended(store), []string{"wont-do"}))
			},
		},
		{
			name:   "x while abandoning asks nothing",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "w")
				assert.Assert(t, m.abandoning)
				m = pressAll(t, m, "x")
				assert.Check(t, !m.askingReason)
				assert.Check(t, cmp.Len(ended(store), 1))
			},
		},
		{
			name:   "an abandon sent refreshes the plan",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "w")
				m, cmd := m.Update(abandonedMsg{})
				assert.Check(t, !m.abandoning)
				assert.Check(t, !strings.Contains(ansi.Strip(m.status), "Abandoning"))
				assert.Check(t, cmd != nil, "no refresh after the abandon")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.FlowV.Ended = tc.ended
			store.EndErr = tc.err
			m := endScreen(t, store, tc.state, tc.events...)
			tc.check(t, m, store)
		})
	}
}

// TestDeleteThePlan checks that X, while no session claims the plan, deletes
// it after a y and closes the screen, and that a refusal keeps it.
func TestDeleteThePlan(t *testing.T) {
	t.Parallel()
	deleted := func(store *fakeStore) bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.PlanDeleted
	}
	// deletes presses X and y, and delivers what the delete answers.
	deletes := func(t *testing.T, m Model) (Model, tea.Cmd) {
		t.Helper()
		m = pressAll(t, m, "X")
		assert.Assert(t, m.confirming)
		m, cmd := m.Update(press("y"))
		assert.Assert(t, cmd != nil)
		return m.Update(cmd())
	}
	for _, tc := range []struct {
		name    string
		state   string
		claimed bool
		host    func(Model) Model
		err     error
		check   func(t *testing.T, m Model, store *fakeStore)
	}{
		{
			name:    "not offered while claimed",
			state:   "refine-proposal",
			claimed: true,
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, !strings.Contains(ansi.Strip(m.actionBar()), "delete plan"))
				m = pressAll(t, m, "X")
				assert.Check(t, !m.confirming)
				assert.Check(t, !m.pendingDeletePlan)
			},
		},
		{
			name: "offered on a plan with no flow",
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "X delete plan"))
			},
		},
		{
			name:  "n keeps the plan",
			state: "ended",
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "X")
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Delete this plan, its comments and its flow?"))
				m = pressAll(t, m, "n")
				assert.Check(t, !m.pendingDeletePlan)
				assert.Check(t, !deleted(store))
			},
		},
		{
			name:  "a refresh keeps the question",
			state: "ended",
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "X")
				store.mu.Lock()
				store.StateV, store.FlowV.Outcome = "ended", "canceled"
				store.mu.Unlock()
				m = refreshed(t, m)
				assert.Check(t, m.pendingDeletePlan)
				_ = pressAll(t, m, "y")
				assert.Check(t, deleted(store))
			},
		},
		{
			name:  "X while deleting asks nothing",
			state: "ended",
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "X", "y")
				assert.Assert(t, m.deleting)
				m = pressAll(t, m, "X")
				assert.Check(t, !m.confirming)
			},
		},
		{
			name:  "y deletes and closes a screen with no host",
			state: "ended",
			check: func(t *testing.T, m Model, store *fakeStore) {
				m, cmd := deletes(t, m)
				assert.Check(t, deleted(store))
				assert.Check(t, !m.deleting)
				_, gone := closedBy(cmd)
				assert.Check(t, gone, "the screen stayed after the delete")
			},
		},
		{
			name:  "y deletes and goes back",
			state: "ended",
			host:  Model.WithBack,
			check: func(t *testing.T, m Model, store *fakeStore) {
				_, cmd := deletes(t, m)
				assert.Check(t, deleted(store))
				_, gone := closedBy(cmd)
				assert.Check(t, gone, "the screen stayed after the delete")
			},
		},
		{
			name:  "y deletes and goes back to plans",
			state: "ended",
			host:  func(m Model) Model { return m.WithInbox("A plan", "chunk") },
			check: func(t *testing.T, m Model, store *fakeStore) {
				_, cmd := deletes(t, m)
				assert.Check(t, deleted(store))
				_, gone := closedBy(cmd)
				assert.Check(t, gone, "the screen stayed after the delete")
			},
		},
		{
			name:  "a conflict says so and stays",
			state: "ended",
			err: &circleci.StatusError{
				Op:            "delete plan",
				StatusCode:    409,
				ServerMessage: "plan has sub-plans",
			},
			check: func(t *testing.T, m Model, store *fakeStore) {
				m, cmd := deletes(t, m)
				assert.Check(t, !deleted(store))
				_, gone := closedBy(cmd)
				assert.Check(t, !gone, "the screen closed on a refused delete")
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Could not delete the plan: "))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "plan has sub-plans"))
			},
		},
		{
			name:  "a refusal says so and stays",
			state: "ended",
			err:   errors.New("plan has sub-plans"),
			check: func(t *testing.T, m Model, store *fakeStore) {
				m, cmd := deletes(t, m)
				_, gone := closedBy(cmd)
				assert.Check(t, !gone, "the screen closed on a refused delete")
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Could not delete the plan: plan has sub-plans"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.FlowV.Claimed = tc.claimed
			store.DeletePlanErr = tc.err
			m := endScreen(t, store, tc.state)
			if tc.host != nil {
				m = tc.host(m)
			}
			tc.check(t, m, store)
		})
	}
}

// fakeResumer is a Resumer that records each call, as "root plan kind".
type fakeResumer struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (r *fakeResumer) Resume(_ context.Context, root, planID string, kind planstate.ResumeKind, holder string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := fmt.Sprintf("%s %s %d", root, planID, kind)
	if holder != "" {
		call += " " + holder
	}
	r.calls = append(r.calls, call)
	return r.err
}

// Calls is every call so far.
func (r *fakeResumer) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// fail has every later call fail with err.
func (r *fakeResumer) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// resumeScreen is resumeModel's screen, on a host that resumes through a fake
// Resumer and reads builds from a fake Build, refreshed once after setup, when
// not nil, has changed the store.
func resumeScreen(t *testing.T, agent *fakeAgent, state string, next []Step, setup func(*fakeStore)) (Model, *fakeStore, *fakeResumer, *fakeBuild) {
	t.Helper()
	m, store := resumeModel(t, agent, state, next)
	if setup != nil {
		store.mu.Lock()
		setup(store)
		store.mu.Unlock()
	}
	r, b := &fakeResumer{}, &fakeBuild{}
	m = m.WithResume(r).WithInbox("Alpha", "/src/app").WithBuild(b)
	return refreshed(t, m), store, r, b
}

// offersResume is whether the footer's hints, given all the room they want,
// offer R.
func offersResume(m Model) bool {
	return strings.Contains(ansi.Strip(theme.Help(theme.New(m.dark), 1000).View(m.keys())), "R resume")
}

// flowLines is the Flow tab's text, line by line, without styles.
func flowLines(m Model) []string {
	return strings.Split(ansi.Strip(m.flowView.GetContent()), "\n")
}

// TestFlowTabSaysWhetherItResumes checks the Flow tab's first line, which
// says what R does or why it does nothing, and that the footer offers R only
// when it does something.
func TestFlowTabSaysWhetherItResumes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		state   string
		next    []Step
		setup   func(*fakeStore)
		line    string
		offered bool
	}{
		{
			name:    "a build nobody holds",
			state:   "build",
			next:    sessionStep,
			line:    "No session holds this plan · R starts a build session",
			offered: true,
		},
		{
			name:  "claimed",
			state: "build",
			next:  sessionStep,
			setup: func(s *fakeStore) {
				s.FlowV.Claimed = true
				s.FlowV.Holder = "bb5a623b-8dd3-4ae8-9c91-48b4eaaf6d62"
			},
			line: "Claimed by session bb5a623b · factory daemon release saved-plan frees it",
		},
		{
			name:  "waiting on the developer",
			state: "integrate",
			line:  "Waiting on you",
		},
		{
			name:  "blocked",
			state: "build",
			next:  sessionStep,
			setup: func(s *fakeStore) {
				s.FamilyV.Blockers = []Relative{{ID: "p-1", Title: "Add the flag", State: "build", Active: true}}
			},
			line: "Waiting on Add the flag",
		},
		{
			name:  "no flow",
			state: "",
		},
		{
			name:  "blockers not read",
			state: "build",
			next:  sessionStep,
			setup: func(s *fakeStore) {
				s.FamilyErr = errors.New("factory is down")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _, _, _ := resumeScreen(t, newFakeAgent(), tc.state, tc.next, tc.setup)
			lines := flowLines(m)
			if tc.line == "" {
				assert.Check(t, strings.HasPrefix(lines[0], "PROGRESS"), "the Flow tab starts %q", lines[0])
			} else {
				assert.Assert(t, len(lines) > 2)
				assert.Check(t, cmp.Equal(lines[0], tc.line))
				assert.Check(t, cmp.Equal(lines[1], ""))
				assert.Check(t, strings.HasPrefix(lines[2], "PROGRESS"), "the Flow tab goes on %q", lines[2])
			}
			assert.Check(t, cmp.Equal(offersResume(m), tc.offered))
		})
	}
}

// TestRBuildResumeStartsABuildSession checks that R on a build nobody holds
// starts a build session in the host's project, then follows it at once,
// before factory's state moves.
func TestRBuildResumeStartsABuildSession(t *testing.T) {
	t.Parallel()
	m, _ := resumeModel(t, newFakeAgent(), "build", sessionStep)
	r, b := &fakeResumer{}, &fakeBuild{}
	// The build is read from after the first refresh, so the only read is
	// the one the resume starts.
	m = refreshed(t, m.WithResume(r).WithInbox("Alpha", "/src/app")).WithBuild(b)
	assert.Assert(t, offersResume(m))

	m, cmd := m.Update(press("R"))
	assert.Check(t, m.resumingBuild)
	assert.Check(t, cmp.Contains(ansi.Strip(m.status), "starting a build session…"))
	assert.Check(t, !offersResume(m), "R offered while the build session starts")
	msgs := run(t, cmd)
	assert.Check(t, cmp.DeepEqual(r.Calls(), []string{"/src/app saved-plan 2"}))
	assert.Assert(t, cmp.Len(msgs, 1))

	m, cmd = m.Update(msgs[0])
	assert.Check(t, m.built)
	assert.Check(t, !m.resumingBuild)
	assert.Check(t, cmp.Equal(m.status, ""))
	assert.Check(t, cmp.Len(b.Asked(), 0))
	run(t, cmd)
	askedFor(t, b, 0)
	assert.Check(t, cmp.Equal(m.state, "build"))
}

// TestCommentsAfterABuildResumeFollowFactory checks that once R has started a
// build session, a queued comment is offered for saving exactly when factory
// takes comments, as on a screen opened fresh.
func TestCommentsAfterABuildResumeFollowFactory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		after func(*fakeStore) // the flow factory serves after the resume
		takes bool
	}{
		{
			name: "factory asks for comments",
			after: func(s *fakeStore) {
				s.FlowV.Next, s.FlowV.WaitingOn = nil, string(ActorHuman)
				s.EventsV, s.eventsSet = []string{"address-comments"}, true
			},
			takes: true,
		},
		{
			name: "factory works on the build",
			after: func(s *fakeStore) {
				s.FlowV.Next = []Step{{Actor: "session", Task: "build-unit", Dispatched: true}}
				s.FlowV.WaitingOn = string(ActorSession)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, store, _, _ := resumeScreen(t, newFakeAgent(), "build", sessionStep, nil)
			m = drive(t, m, press("R"))
			assert.Assert(t, m.built)
			store.mu.Lock()
			tc.after(store)
			store.mu.Unlock()
			m = refreshed(t, m)
			m = pressAll(t, m, "right", "c")
			m = typeText(t, m, "x")
			m = drive(t, m, press("enter"))
			assert.Check(t, cmp.Equal(m.keys().SendComments.Enabled(), tc.takes))
			bar := ansi.Strip(m.actionBar())
			if tc.takes {
				assert.Check(t, cmp.Contains(bar, "s Save 1 comment"))
				m, _ = m.focusOn(paneChat)
				assert.Check(t, m.keys().Send.Enabled(), "enter not offered after the resume")
				m, _ = m.focusOn(panePlan)
				m = drive(t, m, press("s"))
				assert.Check(t, cmp.Len(store.Added, 1))
				return
			}
			assert.Check(t, cmp.Contains(bar, "1 comment held until factory is waiting on you"))
			m = drive(t, m, press("s"))
			assert.Check(t, cmp.Len(store.Added, 0))
			// enter in the Activity pane says why, and sends nothing either.
			m, _ = m.focusOn(paneChat)
			m = drive(t, m, press("enter"))
			assert.Check(t, cmp.Len(store.Added, 0))
			assert.Check(t, cmp.Len(store.HandedOff, 0))
			assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Comments not sent"))
		})
	}
}

// TestRBuildResumeFailureShowsInTheStatusBar checks that the daemon's refusal
// is shown as it is, and R is offered again.
func TestRBuildResumeFailureShowsInTheStatusBar(t *testing.T) {
	t.Parallel()
	m, _, r, _ := resumeScreen(t, newFakeAgent(), "build", sessionStep, nil)
	r.fail(errors.New("plan p is claimed by session s"))
	m = drive(t, m, press("R"))
	assert.Check(t, cmp.Len(r.Calls(), 1))
	assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Could not start a build session: plan p is claimed by session s"))
	assert.Check(t, !m.built)
	assert.Check(t, !m.resumingBuild)
	assert.Check(t, offersResume(m), "R not offered again after a failure")
}

// TestRPlannerResumeGoesThroughTheAgent checks that R on a planning plan
// nobody holds has the screen's own planner claim it again, not the Resumer.
func TestRPlannerResumeGoesThroughTheAgent(t *testing.T) {
	t.Parallel()
	agent := newFakeAgent()
	m, _, r, _ := resumeScreen(t, agent, "refine-units", sessionStep, nil)
	// The refresh resumed the planner on its own, once.
	assert.Assert(t, cmp.Equal(agent.Resumes(), 1))
	assert.Assert(t, !m.resuming)
	assert.Check(t, cmp.Equal(flowLines(m)[0], "No session holds this plan · R resumes its planner"))
	assert.Assert(t, offersResume(m))

	m, cmd := m.Update(press("R"))
	assert.Check(t, m.resuming)
	for _, msg := range run(t, cmd) {
		m = drive(t, m, msg)
	}
	assert.Check(t, cmp.Equal(agent.Resumes(), 2))
	assert.Check(t, !m.resuming)
	assert.Check(t, cmp.Len(r.Calls(), 0))
}

// claimedPlanning is a resumeScreen of a planning plan the session s-1 holds.
func claimedPlanning(t *testing.T) (Model, *fakeResumer) {
	t.Helper()
	m, _, r, _ := resumeScreen(t, newFakeAgent(), "refine-units", sessionStep, func(s *fakeStore) {
		s.FlowV.Claimed = true
		s.FlowV.Holder = "s-1"
	})
	return m, r
}

// TestRTakeOverAsksThenReleases checks that R on a planning plan another
// session holds asks first, that only y sends the confirmed session to the
// resumer, and that a refusal shows in the status bar.
func TestRTakeOverAsksThenReleases(t *testing.T) {
	takeOver := fmt.Sprintf("/src/app saved-plan %d s-1", planstate.ResumeTakeOver)
	t.Run("the line and the question", func(t *testing.T) {
		m, r := claimedPlanning(t)
		assert.Check(t, cmp.Equal(flowLines(m)[0], "Claimed by session s-1 · R releases it and resumes the planner"))
		assert.Assert(t, offersResume(m))
		m, cmd := m.Update(press("R"))
		assert.Check(t, cmd == nil)
		assert.Check(t, m.confirming)
		assert.Check(t, cmp.Contains(ansi.Strip(m.status), planstate.TakeOverQuestion("s-1")))
		assert.Check(t, cmp.Len(r.Calls(), 0))
	})
	t.Run("y releases", func(t *testing.T) {
		m, r := claimedPlanning(t)
		m = drive(t, m, press("R"))
		m, cmd := m.Update(press("y"))
		assert.Check(t, m.takingOver)
		assert.Check(t, !offersResume(m), "R offered while the take-over is in flight")
		m, none := m.Update(press("R"))
		assert.Check(t, none == nil)
		for _, msg := range run(t, cmd) {
			m = drive(t, m, msg)
		}
		assert.Check(t, cmp.DeepEqual(r.Calls(), []string{takeOver}))
		assert.Check(t, !m.takingOver)
		assert.Check(t, cmp.Equal(m.status, ""))
	})
	t.Run("another key cancels", func(t *testing.T) {
		m, r := claimedPlanning(t)
		m = drive(t, m, press("R"))
		m, cmd := m.Update(press("x"))
		assert.Check(t, cmd == nil)
		assert.Check(t, !m.confirming)
		assert.Check(t, cmp.Equal(m.pendingTakeOver, ""))
		assert.Check(t, cmp.Len(r.Calls(), 0))
	})
	t.Run("a refusal shows in the status bar", func(t *testing.T) {
		m, r := claimedPlanning(t)
		r.fail(errors.New("the claim moved to session s-2; press R again"))
		m = drive(t, m, press("R"))
		m = drive(t, m, press("y"))
		assert.Check(t, cmp.Len(r.Calls(), 1))
		assert.Check(t, cmp.Contains(ansi.Strip(m.status), "the claim moved to session s-2; press R again"))
		assert.Check(t, !m.takingOver)
	})
	t.Run("no resumer", func(t *testing.T) {
		m, store := resumeModel(t, newFakeAgent(), "refine-units", sessionStep)
		store.mu.Lock()
		store.FlowV.Claimed = true
		store.FlowV.Holder = "s-1"
		store.mu.Unlock()
		m = refreshed(t, m)
		assert.Check(t, cmp.Equal(flowLines(m)[0], "Claimed by session s-1 · factory daemon release saved-plan frees it"))
		assert.Check(t, !offersResume(m))
	})
}

// TestRIsIgnoredWhileBusy checks that R resumes nothing while the developer
// is writing, being asked something, or before the plan exists, and that a
// build resume under way is not started twice.
func TestRIsIgnoredWhileBusy(t *testing.T) {
	t.Parallel()
	t.Run("writing a comment", func(t *testing.T) {
		t.Parallel()
		m, _, r, _ := resumeScreen(t, newFakeAgent(), "build", sessionStep, nil)
		m = pressAll(t, m, "right", "c")
		assert.Assert(t, m.commenting)
		assert.Check(t, !offersResume(m))
		m = pressAll(t, m, "R")
		assert.Check(t, cmp.Len(r.Calls(), 0))
		assert.Check(t, !m.resumingBuild)
		assert.Check(t, cmp.Equal(m.commentInput.Value(), "R"))
	})
	t.Run("asked why the plan is abandoned", func(t *testing.T) {
		t.Parallel()
		m, _, r, _ := resumeScreen(t, newFakeAgent(), "build", sessionStep, func(s *fakeStore) {
			s.EventsV, s.eventsSet = []string{"abandon"}, true
		})
		m = pressAll(t, m, "x")
		assert.Assert(t, m.askingReason)
		assert.Check(t, !offersResume(m))
		m = pressAll(t, m, "R")
		assert.Check(t, cmp.Len(r.Calls(), 0))
		assert.Check(t, !m.resumingBuild)
	})
	t.Run("asked for a y", func(t *testing.T) {
		t.Parallel()
		m, store, r, _ := resumeScreen(t, newFakeAgent(), "build", sessionStep, nil)
		m = pressAll(t, m, "X")
		assert.Assert(t, m.confirming)
		assert.Check(t, !offersResume(m))
		m = pressAll(t, m, "R")
		assert.Check(t, cmp.Len(r.Calls(), 0))
		assert.Check(t, !m.resumingBuild)
		store.mu.Lock()
		defer store.mu.Unlock()
		assert.Check(t, !store.PlanDeleted)
	})
	t.Run("plan not created", func(t *testing.T) {
		t.Parallel()
		agent := newFakeAgent()
		r := &fakeResumer{}
		m := New(context.Background(), agent, nil, newFakeStore(), "").
			WithResume(r).WithInbox("Alpha", "/src/app").WithBuild(&fakeBuild{})
		m = refreshed(t, drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40}))
		assert.Check(t, !offersResume(m))
		m = pressAll(t, m, "R")
		assert.Check(t, cmp.Len(r.Calls(), 0))
		assert.Check(t, cmp.Equal(agent.Resumes(), 0))
		assert.Check(t, !m.resumingBuild)
	})
	t.Run("build resume under way", func(t *testing.T) {
		t.Parallel()
		m, _, r, _ := resumeScreen(t, newFakeAgent(), "build", sessionStep, nil)
		m, first := m.Update(press("R"))
		m, second := m.Update(press("R"))
		run(t, first)
		run(t, second)
		assert.Check(t, cmp.Len(r.Calls(), 1))
		assert.Check(t, m.resumingBuild)
	})
}

// offersStop is whether the footer's hints, given all the room they want,
// offer S.
func offersStop(m Model) bool {
	return strings.Contains(ansi.Strip(theme.Help(theme.New(m.dark), 1000).View(m.keys())), "S stop build")
}

// stopScreen is a flow screen in build whose build session sess-7 has been
// read running.
func stopScreen(t *testing.T) (Model, *fakeStore, *fakeBuild) {
	t.Helper()
	m, store, _, b := resumeScreen(t, newFakeAgent(), "build", nil, nil)
	b.answer(BuildActivity{Found: true, ID: "sess-7", Running: true, Total: 1, Lines: []BuildLine{{Text: "Bash go test"}}})
	m = buildTick(t, m)
	assert.Assert(t, m.buildRunning())
	return m, store, b
}

// TestSStopsTheBuildSession checks that S, while the plan's build session
// runs, asks for a y, then stops the session it was pressed on, and that the
// Building section then says the session was stopped.
func TestSStopsTheBuildSession(t *testing.T) {
	t.Parallel()
	m, store, b := stopScreen(t)
	assert.Check(t, offersStop(m))

	m = drive(t, m, press("S"))
	assert.Check(t, m.confirming)
	assert.Check(t, strings.Contains(ansi.Strip(m.status), "Stop this build session?"), "status %q", ansi.Strip(m.status))
	assert.Check(t, cmp.Equal(m.pendingStop, "sess-7"))

	m, cmd := m.Update(press("y"))
	assert.Check(t, cmp.Equal(ansi.Strip(m.status), "Stopping the build session…"))
	assert.Check(t, !offersStop(m))
	assert.Check(t, m.stoppingBuild)
	// The y stopped the build; it did not fall through to an approve.
	assert.Check(t, !m.approving)
	msgs := run(t, cmd)
	assert.Check(t, cmp.DeepEqual(b.Stopped(), []string{"sess-7"}))
	for _, msg := range msgs {
		m = drive(t, m, msg)
	}
	assert.Check(t, !m.stoppingBuild)
	assert.Check(t, cmp.Equal(m.status, ""))
	assert.Check(t, !m.approving)
	store.mu.Lock()
	assert.Check(t, cmp.Len(store.DesignApproved, 0))
	store.mu.Unlock()

	b.answerFrom(BuildActivity{Found: true, ID: "sess-7", Cancelled: true, Total: 1})
	m = buildTick(t, m)
	content := ansi.Strip(m.Content())
	assert.Check(t, strings.Contains(content, "Build session stopped"), "content %q", content)
	assert.Check(t, !strings.Contains(content, "Build session ended"), "content %q", content)
	assert.Check(t, !offersStop(m))

	t.Run("any other key keeps it", func(t *testing.T) {
		t.Parallel()
		m, _, b := stopScreen(t)
		m = pressAll(t, m, "S", "n")
		assert.Check(t, !m.confirming)
		assert.Check(t, cmp.Equal(m.pendingStop, ""))
		assert.Check(t, cmp.Len(b.Stopped(), 0))
		assert.Check(t, offersStop(m))
	})

	t.Run("a click on no keeps it", func(t *testing.T) {
		t.Parallel()
		m, _, b := stopScreen(t)
		m = drive(t, m, press("S"))
		text := ansi.Strip(m.status)
		at := strings.Index(text, confirmNo)
		assert.Assert(t, at >= 0, "the question has no no button: %q", text)
		x := 2 + ansi.StringWidth(text[:at])
		m = drive(t, m, click(x+1, m.height-footerRows+1))
		assert.Check(t, !m.confirming)
		assert.Check(t, cmp.Equal(m.pendingStop, ""))
		assert.Check(t, cmp.Len(b.Stopped(), 0))
		m = drive(t, m, press("y"))
		assert.Check(t, cmp.Len(b.Stopped(), 0))
		assert.Check(t, !m.stoppingBuild)
		assert.Check(t, !m.approving)
	})

	t.Run("a failed stop says so", func(t *testing.T) {
		t.Parallel()
		m, _, b := stopScreen(t)
		b.mu.Lock()
		b.stopErr = errors.New("the chunk daemon is not reachable")
		b.mu.Unlock()
		m = pressAll(t, m, "S", "y")
		assert.Check(t, strings.Contains(ansi.Strip(m.status), "Could not stop the build session: the chunk daemon is not reachable"),
			"status %q", ansi.Strip(m.status))
		assert.Check(t, !m.stoppingBuild)
		assert.Check(t, offersStop(m))
	})

	t.Run("a newer session before y", func(t *testing.T) {
		t.Parallel()
		m, _, b := stopScreen(t)
		m = drive(t, m, press("S"))
		b.answerFrom(BuildActivity{Found: true, ID: "sess-8", Running: true})
		m = buildTick(t, m)
		assert.Check(t, cmp.Equal(m.latestBuildID(), "sess-8"))
		m = drive(t, m, press("y"))
		assert.Check(t, cmp.DeepEqual(b.Stopped(), []string{"sess-7"}))
		assert.Check(t, cmp.Equal(m.latestBuildID(), "sess-8"))
	})

	t.Run("not offered", func(t *testing.T) {
		t.Parallel()
		t.Run("while commenting", func(t *testing.T) {
			t.Parallel()
			m, _, _ := stopScreen(t)
			m = pressAll(t, m, "right", "c")
			assert.Assert(t, m.commenting)
			assert.Check(t, !offersStop(m))
		})
		t.Run("while confirming", func(t *testing.T) {
			t.Parallel()
			m, _, _ := stopScreen(t)
			m = pressAll(t, m, "X")
			assert.Assert(t, m.confirming)
			assert.Check(t, !offersStop(m))
		})
		t.Run("when the build is not running", func(t *testing.T) {
			t.Parallel()
			m, _, _, b := resumeScreen(t, newFakeAgent(), "build", nil, nil)
			b.answer(BuildActivity{Found: true, ID: "sess-7", Total: 1})
			m = buildTick(t, m)
			assert.Check(t, !offersStop(m))
			m = drive(t, m, press("S"))
			assert.Check(t, !m.confirming)
			assert.Check(t, cmp.Len(b.Stopped(), 0))
		})
		t.Run("with no build", func(t *testing.T) {
			t.Parallel()
			m, _ := resumeModel(t, newFakeAgent(), "build", nil)
			m = refreshed(t, m)
			assert.Check(t, !offersStop(m))
			m = drive(t, m, press("S"))
			assert.Check(t, !m.confirming)
		})
	})
}

// TestNoResumerOffersNoBuildResume checks that a screen whose host has no
// Resumer says where a build can be resumed from, and R does nothing.
func TestNoResumerOffersNoBuildResume(t *testing.T) {
	t.Parallel()
	agent := newFakeAgent()
	m, _ := resumeModel(t, agent, "build", sessionStep)
	m = refreshed(t, m.WithInbox("Alpha", "/src/app").WithBuild(&fakeBuild{}))
	assert.Check(t, cmp.Equal(flowLines(m)[0], "Resume this build from chunk watch"))
	assert.Check(t, !offersResume(m))
	m = pressAll(t, m, "R")
	assert.Check(t, !m.resumingBuild)
	assert.Check(t, !m.built)
	assert.Check(t, cmp.Equal(m.status, ""))
	assert.Check(t, cmp.Equal(agent.Resumes(), 0))
}

func ptr[T any](v T) *T { return &v }

func linesWith(raw string, words ...string) []string {
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		for _, w := range words {
			if strings.Contains(ansi.Strip(l), w) {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

func noHyperlink(t *testing.T, raw string) {
	t.Helper()
	assert.Check(t, !strings.Contains(raw, "\x1b]8;;"), "no hyperlink is drawn")
	assert.Check(t, !strings.Contains(raw, "\x1b]0;"), "no injected escape")
}

func TestPlanLinks(t *testing.T) {
	t.Parallel()
	const planUUID = "164a2947-74a7-44d9-9708-c8ea7284dcba"
	const planURL = "https://app.circleci.com/factory/github/circleci/flows/" + planUUID

	cases := []struct {
		name  string
		text  string
		width int
		check func(t *testing.T, raw string)
	}{
		{
			name:  "link",
			text:  "## 1. Outcome\n\n" + "Builds after [plan-id-links](" + planURL + ").",
			width: 140,
			check: func(t *testing.T, raw string) {
				assert.Check(t, cmp.Contains(raw, ansi.SetHyperlink(planURL)))
				assert.Check(t, cmp.Contains(ansi.Strip(raw), "Builds after plan-id-links."))
				assert.Check(t, !strings.Contains(ansi.Strip(raw), planURL), "the URL is not drawn")
			},
		},
		{
			name:  "other",
			text:  "## 1. Outcome\n\n" + "See [docs](https://example.com/x).",
			width: 140,
			check: func(t *testing.T, raw string) {
				assert.Check(t, cmp.Contains(raw, ansi.SetHyperlink("https://example.com/x")))
			},
		},
		{
			name:  "wrapped",
			text:  "## 1. Outcome\n\n" + "[a very long plan name that wraps over lines because it is longer than the plan pane is wide](" + planURL + ")",
			width: 100,
			check: func(t *testing.T, raw string) {
				ls := linesWith(raw, "very long", "because it", "pane is wide")
				assert.Check(t, len(ls) >= 2, "the link text wraps over more than one line")
				for _, l := range ls {
					assert.Check(t, cmp.Contains(l, ansi.SetHyperlink(planURL)))
					assert.Check(t, cmp.Contains(l, ansi.ResetHyperlink()))
				}
			},
		},
		{
			name:  "uuid-text",
			text:  "## 1. Outcome\n\n" + "[" + planUUID + "](" + planURL + ") " + planUUID,
			width: 140,
			check: func(t *testing.T, raw string) {
				assert.Check(t, cmp.Equal(strings.Count(ansi.Strip(raw), planUUID), 2))
				assert.Check(t, cmp.Equal(strings.Count(raw, ansi.SetHyperlink(planURL)), 1))
			},
		},
		{
			name:  "escape",
			text:  "## 1. Outcome\n\n" + "[x](https://a.test/\x1b]0;pwned\x07)",
			width: 140,
			check: noHyperlink,
		},
		{
			name:  "scheme",
			text:  "## 1. Outcome\n\n" + "[x](file:///etc/passwd)",
			width: 140,
			check: noHyperlink,
		},
		{
			name:  "fence",
			text:  "## 1. Outcome\n\n" + "```\n[x](" + planURL + ")\n```",
			width: 140,
			check: noHyperlink,
		},
		{
			name:  "heading",
			text:  "## 5. Units\n\n### Unit 1: [x](" + planURL + ")",
			width: 140,
			check: noHyperlink,
		},
		{
			name:  "bare",
			text:  "## 1. Outcome\n\n" + "see " + planUUID,
			width: 140,
			check: noHyperlink,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m, _ := planned(t, c.width, 40, c.text, nil)
			c.check(t, m.Content())
		})
	}
}

// TestSavedCommentOnABlockGoldmarkSplitsDifferentlyIsStillShown checks a
// comment saved when the screen split the plan itself. Its quote is a
// splitBlocks block that items does not hold: a rule straight after a table's
// lines is a setext underline to goldmark, so the lines are a heading outside
// the skeleton and are not drawn.
func TestSavedCommentOnABlockGoldmarkSplitsDifferentlyIsStillShown(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "## 1. Outcome\n\n| a | b |\n|---|---|\n| 1 | 2 |\n---\n"
	store.AddComment(context.Background(), "---", "needs more detail")
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	view := ansi.Strip(m.planView.View())
	assert.Check(t, cmp.Contains(view, "needs more detail"), "view:\n%s", view)
	assert.Check(t, cmp.Contains(view, "On parts chunk has since rewritten"), "view:\n%s", view)
}
