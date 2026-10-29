package plan

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

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

// drive feeds msg to m, then runs each command it returns and feeds back
// what they produce, until the screen settles. Ticks and blinks are skipped:
// they never settle.
func drive(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		var cmd tea.Cmd
		m, cmd = m.Update(queue[0])
		queue = append(queue[1:], run(cmd)...)
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

func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	// Timers (the cursor's blink, the spinner, the poll) are dropped rather
	// than waited for; everything else the screen does answers at once.
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var got tea.Msg
	select {
	case got = <-done:
	case <-time.After(100 * time.Millisecond):
		return nil
	}
	switch msg := got.(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, run(c)...)
		}
		return out
	case eventMsg, sentMsg, approveStepMsg, approvedMsg, designApprovedMsg, conflictsSentMsg, startedMsg, refreshedMsg, familiedMsg, commentSavedMsg, queueSavedMsg, createdMsg, editSavedMsg:
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
	for _, msg := range run(m.Poll()) {
		m = drive(t, m, msg)
	}
	return m
}

func TestFirstMessageRendersPlanAndReply(t *testing.T) {
	store := newFakeStore()
	store.TextV = "# Add a flag\n\n## 1. Outcome\n\nThe flag exists."
	agent := turnReturning("Wrote a first plan.")
	m := New(context.Background(), agent, nil, store, "add a --dry-run flag")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	if got := agent.Sent(); len(got) != 1 || got[0] != "add a --dry-run flag" {
		t.Fatalf("agent got %q", got)
	}
	view := ansi.Strip(m.Content())
	for _, want := range []string{"Add a flag", "The flag exists.", "Wrote a first plan.", "add a --dry-run flag"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
}

func TestFirstSendCreatesPlan(t *testing.T) {
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
	store := newFakeStore()
	store.Created, store.TextV = true, "# Existing plan\n\nKeep this outcome."
	store.CreateErr = errors.New("an existing plan must not be recreated")
	agent := turnReturning("Keeping the existing plan.")
	m := New(t.Context(), agent, nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 32})
	m = refreshed(t, m)
	if len(agent.Sent()) != 0 || !strings.Contains(ansi.Strip(m.Content()), "Keep this outcome.") {
		t.Fatal("opening should read the plan without sending an agent message")
	}
	m = typeText(t, m, "explain the outcome")
	m = drive(t, m, press("enter"))
	if got := agent.Sent(); len(got) != 1 || got[0] != "explain the outcome" {
		t.Fatalf("message should continue the existing plan, got %v", got)
	}
	if !strings.Contains(ansi.Strip(m.Content()), "Keeping the existing plan.") {
		t.Fatal("the agent's reply is missing")
	}
}

func TestApprovingApprovesThePlanInFactory(t *testing.T) {
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
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
	store := newFakeStore()
	store.TextV = "# Plan"
	store.StateV = "Planning - Agent"
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

// A plan in a state chunk does not know, as factory may add before chunk is
// updated, or one chunk offers no action for, such as Integrating - Human
// with no conflict, is shown in that state, is not said to be in review, and
// is not sent.
func TestAPlanInAnUnknownStateIsShownAsItIs(t *testing.T) {
	for _, state := range []string{"Blocked", "Building - Agent", "Integrating - Bot", "Integrating - Human", "Paused\x1b[31m"} {
		t.Run(state, func(t *testing.T) {
			store := newFakeStore()
			store.TextV = "# Plan"
			store.StateV = state
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
			if phase, _ := planstate.Parse(shown); slices.Contains(steps, phase) {
				row1 = "Planning › Building › Integrating › Done"
			}
			assert.Check(t, cmp.Contains(lines[headerRows], row1))
			assert.Check(t, !strings.Contains(view, "Plan · "), "the pane's title names the state:\n%s", view)

			m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			assert.Check(t, !m.confirming, "ctrl+s asked to send a plan in %q", state)
			assert.Check(t, strings.Contains(ansi.Strip(m.status), "while it is "+shown), m.status)
			drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
			assert.Check(t, !sent, "a plan in %q was sent", state)
		})
	}
}

// A plan waits in ReadyState after early-plan too, for its design to be
// reviewed: with no units yet there is nothing to build, so ctrl+s approves
// the design instead, for factory to write its units. The plan's build is not
// started.
func TestCtrlSApprovesTheDesignOfAPlanWithNoUnits(t *testing.T) {
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n"
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
	if len(approved) != 1 || approved[0] != "Planning - Human" {
		t.Fatalf("the design was not approved from Planning - Human once: %v", approved)
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
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n\n1. FNDSF-21 api: add it\n2. FNDSF-22 ui: show it\n"
	store.setEvents("address-comments", "approve-for-building", "abandon")
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
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.setEvents("address-comments", "approve-for-units", "abandon")
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
	assert.Check(t, cmp.DeepEqual(store.DesignApproved, []string{"Planning - Human"}))
}

// TestWaitingForFactoryEndsWhenTheStateMoves checks that the wait a design
// approval starts lasts until a refresh finds the plan in another state,
// however factory names it.
func TestWaitingForFactoryEndsWhenTheStateMoves(t *testing.T) {
	tests := []struct {
		name string
		to   string
	}{
		{
			name: "agent",
			to:   "Planning - Agent",
		},
		{
			name: "a state chunk does not know",
			to:   "Blocked",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			to := tc.to
			store := newFakeStore()
			store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n"
			m := New(context.Background(), turnReturning("ok"), func(func(string)) (string, error) { return "build-1", nil }, store, "do it")
			m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
			assert.Assert(t, m.moving != nil)

			// A refresh in the same state keeps the wait.
			m = drive(t, m, pollMsg{})
			assert.Check(t, m.moving != nil, "the wait ended with the plan still in Planning - Human")
			assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "waiting for factory"))

			store.mu.Lock()
			store.StateV = to
			store.mu.Unlock()
			m = drive(t, m, pollMsg{})
			assert.Check(t, m.moving == nil, "the wait outlived the plan moving to %q", to)
			assert.Check(t, !strings.Contains(ansi.Strip(m.actionBar()), "waiting for factory"), m.actionBar())
			assert.Check(t, !strings.Contains(m.actionBar(), fgSeq(m.purple())), "the bar's border stayed purple after the wait")

			if to != "Planning - Agent" {
				return
			}
			store.mu.Lock()
			store.StateV = "Planning - Human"
			store.TextV += "\n### Unit 1: add the flag\n\nParse it in main.go.\n"
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
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n"
	m := New(context.Background(), turnReturning("ok"), nil, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Assert(t, m.confirming)

	store.mu.Lock()
	store.StateV = "Planning - Agent"
	store.mu.Unlock()
	m = drive(t, m, pollMsg{})
	assert.Check(t, m.confirming)
	assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "Approve the design"))
}

// A design with comments still to send or resolve is not approved: those
// would have factory address them, not write the units.
func TestCtrlSWaitsForCommentsBeforeApprovingTheDesign(t *testing.T) {
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

func TestStageOf(t *testing.T) {
	both := []string{"approve-for-units", "approve-for-building"}
	cases := []struct {
		name   string
		events []string
		units  int
		want   Stage
	}{
		{
			name: "no events, no units",
			want: StageDesign,
		},
		{
			name:  "no events, units",
			units: 2,
			want:  StagePlan,
		},
		{
			name:   "both, no units",
			events: both,
			want:   StageDesign,
		},
		{
			name:   "both, units",
			events: both,
			units:  2,
			want:   StagePlan,
		},
		{
			name:   "only units, no units",
			events: []string{"approve-for-units"},
			want:   StageDesign,
		},
		{
			name:   "only units, units",
			events: []string{"address-comments", "approve-for-units"},
			units:  2,
			want:   StageDesign,
		},
		{
			name:   "only building, no units",
			events: []string{"address-comments", "approve-for-building"},
			want:   StagePlan,
		},
		{
			name:   "only building, units",
			events: []string{"approve-for-building"},
			units:  2,
			want:   StagePlan,
		},
		{
			name:   "unrelated, no units",
			events: []string{"address-comments", "abandon"},
			want:   StageQuestions,
		},
		{
			name:   "unrelated, units",
			events: []string{"address-comments", "abandon"},
			units:  2,
			want:   StageQuestions,
		},
		{
			name:   "empty, no units",
			events: []string{},
			want:   StageQuestions,
		},
		{
			name:   "empty, units",
			events: []string{},
			units:  2,
			want:   StageQuestions,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.Equal(StageOf(tc.events, tc.units), tc.want))
		})
	}
}

// A plan whose flow offers neither approval waits for its open questions to
// be answered: ctrl+s approves nothing and says so.
func TestCtrlSApprovesNothingWhileQuestionsAreOpen(t *testing.T) {
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 3. Open Questions\n\n* Which store?\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.setEvents("address-comments", "abandon")
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

func TestAFailedConflictResolutionSaysSo(t *testing.T) {
	m, store := conflictingModel(t)
	store.AddressErr = errors.New("factory is down")
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	assert.Check(t, !m.resolvingConflicts, "still sending after the send failed")
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Could not send resolve-conflicts: factory is down"))
	assert.Check(t, m.moving == nil, "a failed event started the wait")
}

// TestWaitingBarYieldsToStatuses checks that a prompt or an error during the
// wait for factory shows in the action bar over the waiting line, which
// would otherwise hide it until the refresh that ends the wait clears it.
func TestWaitingBarYieldsToStatuses(t *testing.T) {
	m, store := planModel(t)
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("s"))
	assert.Assert(t, m.moving != nil, "no wait for factory after the hand-off")

	// The quit prompt shows over the waiting line, on the purple border.
	m = drive(t, m, press("tab"))
	m = typeText(t, m, "draft")
	m = drive(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	bar := ansi.Strip(m.actionBar())
	assert.Check(t, cmp.Contains(bar, "will be lost"))
	assert.Check(t, !strings.Contains(bar, "waiting for factory"), bar)
	assert.Check(t, strings.Contains(m.actionBar(), fgSeq(m.purple())), "the bar's border is not purple under the quit prompt")
	m = drive(t, m, press("x"))
	assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "waiting for factory"))

	// An error shows over it too.
	m = drive(t, m, commentSavedMsg{err: errors.New("edit failed")})
	assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "edit failed"))

	// The refresh that ends the wait clears the error with it.
	store.mu.Lock()
	store.StateV = "Planning - Agent"
	store.mu.Unlock()
	m = drive(t, m, pollMsg{})
	assert.Check(t, m.moving == nil, "the wait outlived the plan moving")
	bar = ansi.Strip(m.actionBar())
	assert.Check(t, !strings.Contains(bar, "edit failed"), bar)
	assert.Check(t, cmp.Contains(bar, "ctrl+s Approve plan"))
}

func TestWaitingTitleNamesTheMove(t *testing.T) {
	tests := []struct {
		name string
		from string
		want string
	}{
		{
			name: "planning",
			from: "Planning - Human",
			want: "Plan · Planning - Human → Agent",
		},
		{
			name: "building",
			from: "Building - Human",
			want: "Plan · Building - Human → Agent",
		},
		{
			name: "unknown state",
			from: "Blocked",
			want: "Plan · Blocked → …",
		},
		{
			name: "escapes and a newline",
			from: "\x1b[31mPlanning\n - Human",
			want: "Plan · Planning - Human → Agent",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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
			assert.Check(t, cmp.Contains(top, "Chat"))
			assert.Check(t, cmp.Contains(top, tc.want))
			assert.Check(t, !regexp.MustCompile(`\d+%`).MatchString(top), top)
		})
	}
}

func TestWaitingBarSaysHowLongOnceItIsSlow(t *testing.T) {
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

const twoParts = "## 1. Outcome\n\nThe flag exists.\n\n## 2. Spec\n\n* [AGENT] parse it in main.go"

// planModel is a screen whose plan is already text, with turn replying ok.
func planModel(t *testing.T) (Model, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	store.TextV = twoParts
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	return m, store
}

// conflictingModel is the screen on a plan in Integrating - Human whose PR
// factory found conflicting, so its flow takes resolve-conflicts.
func conflictingModel(t *testing.T) (Model, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	store.Created = true
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.StateV = "Integrating - Human"
	store.setEvents("address-comments", "resolve-conflicts", "abandon-integrate", "abandon")
	m := New(context.Background(), turnReturning("ok"), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)
	return m, store
}

// A plan whose PR factory found conflicting says so, and ctrl+s, after a y,
// sends resolve-conflicts and waits for factory to move the plan.
func TestCtrlSResolvesConflicts(t *testing.T) {
	m, store := conflictingModel(t)
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "PR conflicts"))
	assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "ctrl+s Resolve conflicts"))
	assert.Check(t, !strings.Contains(view, "Approve plan"), view)

	// A no leaves nothing pending for a later y to send.
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Assert(t, m.confirming)
	m = drive(t, m, press("n"))
	assert.Check(t, !m.confirming)
	assert.Check(t, !m.pendingConflicts)
	assert.Check(t, m.moving == nil)
	store.mu.Lock()
	assert.Check(t, cmp.Len(store.ConflictsSent, 0))
	store.mu.Unlock()

	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Assert(t, m.confirming)
	assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "rebase its PR onto its base?"))
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	store.mu.Lock()
	sent := slices.Clone(store.ConflictsSent)
	store.mu.Unlock()
	assert.Check(t, cmp.DeepEqual(sent, []string{"Integrating - Human"}))
	assert.Assert(t, m.moving != nil, "no wait for factory after resolve-conflicts")
	assert.Check(t, cmp.Equal(m.moving.kind, movingConflicts))
	assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "Conflicts sent · waiting for factory to hand the rebase to the agent…"))

	store.mu.Lock()
	store.StateV = "Integrating - Agent"
	store.EventsV = []string{}
	store.mu.Unlock()
	m = drive(t, m, pollMsg{})
	assert.Check(t, m.moving == nil, "the wait outlived the move to Integrating - Agent")
	assert.Check(t, !strings.Contains(ansi.Strip(m.Content()), "PR conflicts"))
}

// Comments queued on a conflicting plan go first, since sending them hands it
// to the agent too; comments already saved leave ctrl+s resolving conflicts.
func TestConflictsWithComments(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		m, store := conflictingModel(t)
		m = drive(t, m, press("tab"))
		// An Integrating plan opens on the Flow tab; comments are the Plan's.
		m = drive(t, m, press("right"))
		m = drive(t, m, press("c"))
		m = typeText(t, m, "too vague")
		m = drive(t, m, press("enter"))
		bar := ansi.Strip(m.actionBar())
		assert.Check(t, cmp.Contains(bar, "Save 1 comment"))
		assert.Check(t, !strings.Contains(bar, "Resolve conflicts"), bar)

		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assert.Check(t, !m.confirming)
		assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Send your comments first"))
		store.mu.Lock()
		defer store.mu.Unlock()
		assert.Check(t, cmp.Len(store.ConflictsSent, 0))
	})
	t.Run("open", func(t *testing.T) {
		m, store := conflictingModel(t)
		store.mu.Lock()
		store.CommentsV = []Comment{{ID: "c1", Quote: "Add the flag", Body: "why?"}}
		store.mu.Unlock()
		m = refreshed(t, m)
		assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "ctrl+s Resolve conflicts"))
	})
}

func TestSSavesCommentsFromThePlan(t *testing.T) {
	store := newFakeStore()
	store.TextV = twoParts
	agent := turnReturning("ok")
	m := New(context.Background(), agent, nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	agent.reset()

	// A message started in the chat, then a comment in the plan.
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
	if len(store.HandedOff) != 1 || store.HandedOff[0] != "Planning - Human" {
		t.Errorf("handed off: %+v", store.HandedOff)
	}
	if got := agent.Sent(); len(got) != 0 {
		t.Errorf("the chat's draft, out of sight, was sent: %q", got)
	}
	if m.focus != panePlan || len(queuedComments(m.comments)) != 0 || m.input.Value() != "draft" {
		t.Errorf("focus %v, comments %+v, draft %q", m.focus, m.comments, m.input.Value())
	}
	assert.Assert(t, m.moving != nil, "no wait for factory after the hand-off")
	assert.Check(t, cmp.Equal(m.moving.kind, movingComments))
	assert.Check(t, cmp.Equal(m.moving.from, "Planning - Human"))

	bar := ansi.Strip(m.actionBar())
	assert.Check(t, cmp.Contains(bar, "Comments sent · waiting for factory to hand them to chunk…"))
	assert.Check(t, !strings.Contains(bar, "Approve"), "the bar offers to approve while waiting: %q", bar)
	assert.Check(t, strings.Contains(m.actionBar(), fgSeq(m.purple())), "the bar's border is not purple while waiting")
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Waiting for factory"))
	assert.Check(t, !strings.Contains(view, "Next: you approve the design · ctrl+s") && !strings.Contains(view, "Next: you approve the plan · ctrl+s"), view)
	assert.Check(t, cmp.Contains(view, "Plan · Planning - Human → Agent"))
}

func TestSSavesCommentsWhileChunkWorks(t *testing.T) {
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
	m.renderPlan()
	m.comments = []comment{{block: 3, quote: "* [AGENT] parse it in main.go", text: "x"}}

	m.plan = "## 0. New section\n\n" + twoParts
	m.renderPlan()
	if got := m.comments[0].block; got != 4 {
		t.Errorf("comment is on block %d, want 4", got)
	}

	m.plan = "## 1. Outcome\n\nRewritten."
	m.renderPlan()
	if got := m.comments[0].block; got != -1 {
		t.Errorf("comment on a rewritten block is on %d, want -1", got)
	}
}

func TestCursorDoesNotHideTheCommentBar(t *testing.T) {
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
	headingLine := find(lines, "SPEC")
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

		// A conflicting plan, its prompt, and the wait once it is sent.
		store = newFakeStore()
		store.Created = true
		store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
		store.StateV = "Integrating - Human"
		store.setEvents("address-comments", "resolve-conflicts", "abandon-integrate", "abandon")
		m = New(context.Background(), turnReturning("ok"), nil, store, "")
		m = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 24})
		m = refreshed(t, m)
		assertFits(t, m, width, 24)
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assertFits(t, m, width, 24)
		m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		assertFits(t, m, width, 24)
		bar = ansi.Strip(m.actionBar())
		if width < narrowBelow {
			assert.Check(t, cmp.Contains(bar, "Waiting for factory…"), "width %d", width)
		} else {
			assert.Check(t, cmp.Contains(bar, "Conflicts sent"), "width %d", width)
		}
	}
}

// The pane with the keyboard is drawn in chunk's brand accent, as in chunk
// watch, and follows the terminal's background: yellow on a dark one, navy
// on a light one.
func TestFocusedPaneUsesTheBrandAccent(t *testing.T) {
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

// The developer's message bubbles are content, not focus: they get a faint
// border, never the pane's accent.
func TestUserBubbleIsQuiet(t *testing.T) {
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
// with its chat closed: the planning session has stopped.
func TestApprovingInAKeptScreenFollowsTheBuild(t *testing.T) {
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	approvals := 0
	approve := func(func(string)) (string, error) {
		approvals++
		return "build-1", nil
	}
	agent := newFakeAgent()
	m := New(context.Background(), agent, approve, store, "").WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = refreshed(t, m)
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

	for _, k := range []string{"m", "o", "r", "e"} {
		m = drive(t, m, press(k))
	}
	m = drive(t, m, press("enter"))
	assert.Check(t, cmp.Len(agent.Sent(), 0), "the closed chat sent to the agent")
	assert.Check(t, cmp.Equal(m.input.Value(), "more"))
	assert.Check(t, cmp.Contains(m.input.Placeholder, "chat is closed"))

	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Check(t, !m.confirming, "ctrl+s asks to approve a plan already building")
	m = drive(t, m, press("y"))
	assert.Check(t, cmp.Equal(approvals, 1), "the plan was approved again")
}

// While the plan is approved and its build started, the screen says which
// step it is on, and the header's spinner turns.
func TestApprovingShowsEachStep(t *testing.T) {
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
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
	m, _ := planModel(t)
	m = m.WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "esc back") {
		t.Errorf("the footer does not say esc goes back:\n%s", view)
	}

	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got, ok := closedBy(cmd); !ok || got.BuildID != "" {
		t.Fatalf("esc closed with %+v, %v", got, ok)
	}
	if len(m.comments) != 1 {
		t.Errorf("closing dropped the comment: %+v", m.comments)
	}
}

func TestBackEscLeavesWhileChunkWorks(t *testing.T) {
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
	agent := turnReturning(strings.Repeat("A long reply.\n\n", 30))
	m := New(context.Background(), agent, nil, newFakeStore(), "first")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "pgup/pgdn scroll chat") {
		t.Errorf("chat scrolling is not in the footer:\n%s", view)
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
	store := newFakeStore()
	store.TextV = "# Plan"
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = "## Spec\n\n* Tests\n* Tests"
	m.renderPlan()
	m.comments = []comment{
		{block: 1, quote: "* Tests", text: "first"},
		{block: 2, quote: "* Tests", text: "second"},
	}

	m.plan = "## New\n\n## Spec\n\n* Tests\n* Tests"
	m.renderPlan()
	a, b := m.comments[0].block, m.comments[1].block
	if a == b || min(a, b) != 2 || max(a, b) != 3 {
		t.Errorf("comments are on blocks %d and %d, want one each on 2 and 3", a, b)
	}
}

func TestCommentOnARewrittenPartIsStillShown(t *testing.T) {
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
	m.renderPlan()
	m.comments = []comment{{block: 3, quote: "* [AGENT] parse it in main.go", text: "x"}}

	m.plan = "## 1. Outcome\n\nRewritten."
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
	m.renderPlan()
	m.comments = []comment{{block: 3, quote: "* [AGENT] parse it in main.go", text: "x"}}
	m.plan = "## 1. Outcome\n\nRewritten."
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
	m.renderPlan()
	m.comments = []comment{{block: 2, quote: "## 2. Spec", text: "a"}}
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

	store.TextV = "## 0. New\n\nAdded.\n\n" + twoParts
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
			store := newFakeStore()
			m := New(context.Background(), turnReturning("ok"), nil, store, "do it")
			m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			assert.Assert(t, !m.busy, "still busy after the reply")

			// Factory hands out the task, then claude starts on it.
			store.setTask(tc.task)
			m.moving = &transition{kind: movingComments, from: "Planning - Human", since: time.Now()}
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

func TestProblemClearsTheTask(t *testing.T) {
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

func TestMessageWhileChunkWorksGoesToItAtOnce(t *testing.T) {
	agent := newFakeAgent()
	m := New(context.Background(), agent, nil, newFakeStore(), "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !m.busy {
		t.Fatal("not busy after the first send")
	}

	m = typeText(t, m, "and keep it small")
	m = drive(t, m, press("enter"))
	if got := agent.Sent(); len(got) != 2 || got[1] != "and keep it small" {
		t.Fatalf("agent got %q", got)
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "sent while chunk was working") {
		t.Errorf("the message does not say it went to a busy chunk:\n%s", view)
	}
}

func TestFailedSendShowsInTheChat(t *testing.T) {
	agent := newFakeAgent()
	agent.err = errors.New("no f plugin")
	m := New(context.Background(), agent, nil, newFakeStore(), "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.busy {
		t.Error("still busy after the send failed")
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "no f plugin") {
		t.Errorf("the failure is not in the chat:\n%s", view)
	}
}

func TestProblemEndsTheTurn(t *testing.T) {
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

func TestPlanInReviewSaysOnlyCommentsChangeIt(t *testing.T) {
	store := newFakeStore()
	store.TextV = twoParts
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	if store.StateV != "Planning - Human" {
		t.Fatalf("test assumes the fake store starts in Planning - Human, got %q", store.StateV)
	}
	if m.input.Placeholder != reviewPlaceholder {
		t.Errorf("placeholder in review is %q", m.input.Placeholder)
	}

	store.StateV = "Planning - Agent"
	m = refreshed(t, m)
	if m.input.Placeholder != chatPlaceholder {
		t.Errorf("placeholder while chunk plans is %q", m.input.Placeholder)
	}
}

func TestPlanIsSentOnceWhileTheFactoryAnswers(t *testing.T) {
	m := New(context.Background(), turnReturning("ok"), nil, newFakeStore(), "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.approving = true
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.confirming {
		t.Error("ctrl+s asked to send the plan again while it is being sent")
	}
}

func TestBubbleFitsAShortMessageAndWrapsALongOne(t *testing.T) {
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
	store := newFakeStore()
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

func TestPlanShowsUnitsAsSteps(t *testing.T) {
	store := newFakeStore()
	store.TextV = planWithUnits
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

// TestPlanStepsAreFactorysUnits checks that the plan pane's steps are the
// units factory counts: not a worked example's fenced heading, nor a unit's
// own subsection, and a unit done with a SHA is drawn done.
func TestPlanStepsAreFactorysUnits(t *testing.T) {
	store := newFakeStore()
	store.TextV = "# Retry on 429\n\n## 1. Outcome\n\nCreate retries.\n\n" +
		"## 4. Worked example\n\n```md\n### Unit 9: example\n```\n\n" +
		"## 5. Units\n\n### Unit 1: Add RetryPolicy (done, abc1234)\n\nCode.\n\n#### Building\n\nCommitted.\n"
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	assert.Check(t, strings.Contains(view, "✓ 1  Add RetryPolicy"), "no done step:\n%s", view)
	for _, unwanted := range []string{"○ 2", "✓ 2", "○ 1  example", "abc1234"} {
		assert.Check(t, !strings.Contains(view, unwanted), "view has %q:\n%s", unwanted, view)
	}
}

func TestNarrowScreenTakesTurnsWithTabs(t *testing.T) {
	store := newFakeStore()
	store.TextV = planWithUnits
	m := New(context.Background(), turnReturning("Wrote it."), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 70, Height: 30})
	view := ansi.Strip(m.Content())
	if !regexp.MustCompile(`Chat +Flow +Plan · 2 units`).MatchString(view) {
		t.Errorf("no tab strip:\n%s", view)
	}
	if strings.Contains(view, "Retry on 429") {
		t.Errorf("the plan shows beside the chat on a narrow screen:\n%s", view)
	}
	assertFits(t, m, 70, 30)

	// tab goes round chat, Flow and Plan.
	m = drive(t, m, press("tab"))
	view = ansi.Strip(m.Content())
	if !strings.Contains(view, "PROGRESS") || strings.Contains(view, "Wrote it.") || strings.Contains(view, "Retry on 429") {
		t.Errorf("tab did not bring the Flow tab up in place of the chat:\n%s", view)
	}
	assertFits(t, m, 70, 30)

	m = drive(t, m, press("tab"))
	view = ansi.Strip(m.Content())
	if !strings.Contains(view, "Retry on 429") || strings.Contains(view, "PROGRESS") {
		t.Errorf("a second tab did not bring the plan up:\n%s", view)
	}
	assertFits(t, m, 70, 30)

	m = drive(t, m, press("tab"))
	view = ansi.Strip(m.Content())
	if !strings.Contains(view, "Wrote it.") || strings.Contains(view, "Retry on 429") {
		t.Errorf("a third tab did not bring the chat back:\n%s", view)
	}
	assert.Check(t, cmp.Equal(m.focus, paneChat))
	assertFits(t, m, 70, 30)
}

func TestFirstPlanShowsASkeletonWhileClaudeWrites(t *testing.T) {
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 110, Height: 30})
	m.plan = twoParts
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

	want := "The flag exists.\n\n## 2. Spec"
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "").WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
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
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
	m.renderPlan()
	m.comments = []comment{{block: 1, span: 2, quote: m.quoteOf(1, 2), text: "x"}}

	m.plan = "## 0. New section\n\n" + twoParts
	m.renderPlan()
	if c := m.comments[0]; c.block != 2 || c.span != 2 {
		t.Errorf("comment is on %d+%d, want 2+2", c.block, c.span)
	}

	// Rewriting either block leaves the comment on a rewritten part.
	m.plan = strings.Replace(twoParts, "## 2. Spec", "## 2. Design", 1)
	m.renderPlan()
	if got := m.comments[0].block; got != -1 {
		t.Errorf("comment on a rewritten block is on %d, want -1", got)
	}
}

// --- Store-backed polling and comments ---

func TestPaneRendersStoreText(t *testing.T) {
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nDone."
	store.StateV = "Planning - Agent"
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
	if strings.Contains(view, "Plan · Planning - Agent") {
		t.Errorf("title still shows the state:\n%s", view)
	}
}

func TestSavedCommentAnchorsByQuote(t *testing.T) {
	store := newFakeStore()
	store.TextV = twoParts
	store.AddComment(context.Background(), "## 2. Spec", "needs more detail")
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
	store := newFakeStore()
	store.TextV = "## Spec\n\n* Tests\n* Tests"
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
	store := newFakeStore()
	store.TextV = twoParts
	m := New(context.Background(), newFakeAgent(), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)
	before := m.plan

	m.moving = &transition{kind: movingComments, from: "Planning - Human", since: time.Now()}
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
	m = typeText(t, m, "please address these")
	m = drive(t, m, press("enter"))

	if len(store.Added) != 2 || store.Added[0] != [2]string{"The flag exists.", "first"} ||
		store.Added[1] != [2]string{"* [AGENT] parse it in main.go", "second"} {
		t.Fatalf("added: %+v", store.Added)
	}
	if len(store.HandedOff) != 1 || store.HandedOff[0] != "Planning - Human" {
		t.Fatalf("handed off: %+v", store.HandedOff)
	}
	if got := agent.Sent(); len(got) != 2 || got[1] != "please address these" {
		t.Fatalf("agent got %q, want only the typed message after the first", got)
	}
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
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "Press enter to save your comments") {
		t.Errorf("placeholder does not say comments are saved:\n%s", view)
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
// state leaves the queued comments queued, unsaved and not handed off, and
// sends the message with them on alone.
func TestCommentsHeldOutsideHumanStates(t *testing.T) {
	store := newFakeStore()
	store.TextV = twoParts
	store.StateV = "Planning - Agent"
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
	assert.Check(t, cmp.Equal(m.input.Placeholder, heldPlaceholder))
	m = typeText(t, m, "keep going")
	m = drive(t, m, press("enter"))

	assert.Check(t, cmp.Len(store.Added, 0))
	assert.Check(t, cmp.Len(store.HandedOff, 0))
	assert.Check(t, cmp.Len(queuedComments(m.comments), 1))
	assert.Check(t, cmp.DeepEqual(agent.Sent(), []string{"keep going"}))
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Comments not sent: factory is working on the plan"))
}

// TestHeldCommentsSendOnceThePlanWaitsOnTheDeveloper checks that comments
// held while factory worked are saved and handed off by the first send once
// the plan is in a Human state.
func TestHeldCommentsSendOnceThePlanWaitsOnTheDeveloper(t *testing.T) {
	store := newFakeStore()
	store.TextV = twoParts
	store.StateV = "Planning - Agent"
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
	store.StateV = "Planning - Human"
	store.mu.Unlock()
	m = drive(t, m, pollMsg{})
	assert.Assert(t, m.keys().SendComments.Enabled(), "s does not send comments from a Human state")
	drive(t, m, press("s"))

	assert.Check(t, cmp.Len(store.Added, 1))
	assert.Check(t, cmp.DeepEqual(store.HandedOff, []string{"Planning - Human"}))
}

// TestCommentsHeldWhileFactoryPicksUpTheHandOff checks that comments queued
// after a hand-off, before factory has moved the plan, are held rather than
// sent with a second address-comments event, which factory would store
// again.
func TestCommentsHeldWhileFactoryPicksUpTheHandOff(t *testing.T) {
	m, store := planModel(t)
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "first")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("s"))
	assert.Assert(t, cmp.DeepEqual(store.HandedOff, []string{"Planning - Human"}))

	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "second")
	m = drive(t, m, press("enter"))
	assert.Check(t, !m.keys().SendComments.Enabled(), "s offered while factory has yet to move the plan")
	m = drive(t, m, press("s"))
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))

	assert.Check(t, cmp.Len(store.Added, 1))
	assert.Check(t, cmp.DeepEqual(store.HandedOff, []string{"Planning - Human"}))
	assert.Check(t, cmp.Len(queuedComments(m.comments), 1))
	assert.Check(t, cmp.Equal(m.status, ""), "the held note covers the wait: %q", m.status)
}

// TestCommentsFollowTheFlowsEventsNotItsState checks that whether a send
// hands comments to factory is up to the events its flow applies now, not
// the name of the plan's state.
func TestCommentsFollowTheFlowsEventsNotItsState(t *testing.T) {
	store := newFakeStore()
	store.TextV = twoParts
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
	store.StateV = "Planning - Agent"
	store.mu.Unlock()
	store.setEvents("address-comments")
	m = drive(t, m, pollMsg{})
	assert.Assert(t, m.keys().SendComments.Enabled(), "s does not send comments the flow takes")
	drive(t, m, press("s"))

	assert.Check(t, cmp.Len(store.Added, 1))
	assert.Check(t, cmp.DeepEqual(store.HandedOff, []string{"Planning - Agent"}))
}

// TestHandOffRetriedOnNextSendWithNothingQueued checks that a hand-off event
// failing after every comment was saved is retried on the next send, even
// with nothing queued, rather than leaving the plan stuck in its Human state,
// and that the retry sends the event the flow lists then.
func TestHandOffRetriedOnNextSendWithNothingQueued(t *testing.T) {
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
			handedOff: []string{"Planning - Human"},
		},
		{
			name:     "to answer-open-questions",
			first:    []string{"address-comments"},
			retry:    []string{"answer-open-questions"},
			answered: []string{"Planning - Human"},
		},
		{
			name:      "to address-comments",
			first:     []string{"answer-open-questions"},
			retry:     []string{"address-comments"},
			handedOff: []string{"Planning - Human"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			m = typeText(t, m, "go")
			m = drive(t, m, press("enter"))

			if len(store.Added) != 1 {
				t.Fatalf("comment not saved: %+v", store.Added)
			}
			assert.Check(t, cmp.Len(store.HandedOff, 0))
			assert.Check(t, cmp.Len(store.Answered, 0))
			if len(agent.Sent()) != 0 {
				t.Fatalf("a turn started despite the hand-off failing")
			}
			if strings.TrimSpace(m.input.Value()) != "go" {
				t.Errorf("message not kept in the input: %q", m.input.Value())
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
			if len(agent.Sent()) != 1 {
				t.Fatalf("turn calls = %d, want 1", len(agent.Sent()))
			}
			assert.Check(t, m.moving != nil)
		})
	}
}

// TestPlainMessageInHumanStateDoesNotHandOff checks that sending an ordinary
// chat message while the plan sits in a Human state, with no comment ever
// queued or saved, sends factory no address-comments event.
func TestPlainMessageInHumanStateDoesNotHandOff(t *testing.T) {
	store := newFakeStore()
	store.TextV = twoParts
	agent := turnReturning("ok")
	m := New(context.Background(), agent, nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	agent.reset()

	if store.StateV != "Planning - Human" {
		t.Fatalf("test assumes the fake store starts in Planning - Human, got %q", store.StateV)
	}
	m = typeText(t, m, "also handle edge case X")
	drive(t, m, press("enter"))

	if len(store.HandedOff) != 0 {
		t.Fatalf("handed off on a plain message with no comments ever saved: %+v", store.HandedOff)
	}
	if len(store.Added) != 0 {
		t.Fatalf("comments saved on a plain message: %+v", store.Added)
	}
	if len(agent.Sent()) != 1 {
		t.Fatalf("turn calls = %d, want 1", len(agent.Sent()))
	}
}

func TestSaveFailureKeepsRestQueued(t *testing.T) {
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
	m = typeText(t, m, "go")
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
	if strings.TrimSpace(m.input.Value()) != "go" {
		t.Errorf("message not kept in the input: %q", m.input.Value())
	}
}

func TestEditingSavedCommentPutsBody(t *testing.T) {
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
	for _, msg := range run(cmd) {
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

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))

	if len(store.Deleted) != 1 || store.Deleted[0] != c.ID {
		t.Fatalf("deleted: %+v, want [%s]", store.Deleted, c.ID)
	}
}

// TestMergeAccumulatesReplacesAcrossQueuedComments checks that merging a
// queued comment, which already carries a saved comment's id in replaces,
// into a further selection keeps that id, so a chain of merges still
// deletes every saved comment it ever absorbed once it is sent.
func TestMergeAccumulatesReplacesAcrossQueuedComments(t *testing.T) {
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
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))

	if len(store.Deleted) != 1 || store.Deleted[0] != saved.ID {
		t.Fatalf("deleted: %+v, want [%s]", store.Deleted, saved.ID)
	}
}

func TestResolveKeyResolves(t *testing.T) {
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
	for _, state := range []string{"Building - Human", "Integrating - Human"} {
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
				assert.Check(t, cmp.Equal(m.input.Placeholder, commentsPlaceholder))
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
			store := newFakeStore()
			store.TitleV, store.CreateErr = tc.title, tc.createErr
			m := tc.host(New(context.Background(), newFakeAgent(), nil, store, ""))
			m = drive(t, m, tea.WindowSizeMsg{Width: tc.width, Height: 40})
			m = typeText(t, m, "replace the header")
			m, create := m.Update(press("enter"))
			for _, msg := range run(create) {
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

// The plan is created off the screen's loop; a message sent meanwhile waits
// in the input rather than racing the create.
func TestMessageWhileThePlanIsCreatedWaitsInTheInput(t *testing.T) {
	store := newFakeStore()
	agent := newFakeAgent()
	m := New(context.Background(), agent, nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = typeText(t, m, "first")
	m, create := m.Update(press("enter"))
	if !m.creating {
		t.Fatal("the plan is not being created")
	}

	m = typeText(t, m, "second")
	m, _ = m.Update(press("enter"))
	if m.input.Value() != "second" {
		t.Errorf("input is %q, want the waiting message", m.input.Value())
	}

	for _, msg := range run(create) {
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

	for _, msg := range run(save) {
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

// q leaves from the plan, where nothing is being typed, and is only a letter
// in the chat.
func TestQQuitsFromThePlanOnly(t *testing.T) {
	// The chat's text box is shared by copies of the model, so each half
	// starts from a screen of its own.
	chat := idlePlan(t)
	chat, cmd := chat.Update(press("q"))
	if _, ok := closedBy(cmd); ok {
		t.Error("q in the chat quit")
	}
	if got := chat.input.Value(); got != "q" {
		t.Errorf("q in the chat typed %q, want q", got)
	}

	plan := idlePlan(t)
	plan = drive(t, plan, press("tab"))
	_, cmd = plan.Update(press("q"))
	if _, ok := closedBy(cmd); !ok {
		t.Error("q in the plan did not quit")
	}
}

func TestNMovesToThePreviousComment(t *testing.T) {
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

// threeUnits is a plan whose first unit is done.
const threeUnits = "# Parse it\n\n## 5. Units\n\n### Unit 1: lexer (done)\n\n### Unit 2: parser\n\n### Unit 3: printer\n"

func TestRightPaneOpensOnTheTabForItsPhase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		want  rightTab
	}{
		{
			name:  "planning",
			state: "Planning - Human",
		},
		{
			name:  "unprioritized",
			state: "Unprioritized",
		},
		{
			name: "no state",
		},
		{
			name:  "building",
			state: "Building - Agent",
			want:  tabFlow,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.StateV, store.TextV = tc.state, twoParts
			m := flowModel(t, store)
			assert.Check(t, cmp.Equal(m.tab, tc.want))

			// A later move leaves the tab where it is.
			store.mu.Lock()
			store.StateV = "Integrating - Agent"
			if tc.want == tabFlow {
				store.StateV = "Planning - Human"
			}
			store.mu.Unlock()
			m = refreshed(t, m)
			assert.Check(t, cmp.Equal(m.tab, tc.want), "a state change switched the tab")
		})
	}
}

func TestTabKeysSwitchTheRightPane(t *testing.T) {
	m, store := planModel(t)
	m = typeText(t, m, "fp")
	m = drive(t, m, press("left"))
	m = drive(t, m, press("left"))
	m = drive(t, m, press("right"))
	m = typeText(t, m, "x")
	assert.Check(t, cmp.Equal(m.input.Value(), "fxp"), "← and → in the chat move the cursor")
	assert.Check(t, cmp.Equal(m.tab, tabPlan))
	m.input.Reset()

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
	for _, tc := range []struct {
		name    string
		text    string
		unit    string
		family  Family
		want    []string
		without []string
	}{
		{
			name: "units",
			text: threeUnits,
			unit: "Unit 2: parser",
			want: []string{"✓ 1  lexer", "▸ 2  parser", "○ 3  printer"},
		},
		{
			name:    "a task unit that matches none",
			text:    threeUnits,
			unit:    "Unit 9: nothing",
			want:    []string{"✓ 1  lexer", "○ 2  parser", "○ 3  printer"},
			without: []string{"▸"},
		},
		{
			name: "no units",
			text: twoParts,
			want: []string{"No units"},
		},
		{
			name: "open sub-plans",
			text: threeUnits,
			family: Family{Children: []Relative{
				{ID: "c1", Title: "Lex it", State: "Building - Agent"},
				{ID: "c2", Title: "Print it", State: "Planning - Human"},
			}},
			want:    []string{"Progress · 2 open sub-plans", "▸ Lex it  Building - Agent", "○ Print it  Planning - Human"},
			without: []string{"lexer"},
		},
		{
			name:   "a family without sub-plans",
			text:   threeUnits,
			family: Family{Parent: &Relative{ID: "p", Title: "Everything", State: "Building - Agent"}},
			want:   []string{"✓ 1  lexer", "○ 2  parser"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.StateV, store.TextV = "Building - Agent", tc.text
			store.FlowV.Unit, store.FamilyV = tc.unit, tc.family
			m := flowModel(t, store)
			view := ansi.Strip(m.Content())
			assert.Check(t, cmp.Contains(view, "PROGRESS"))
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

func TestFlowTabShowsThePR(t *testing.T) {
	const url = "https://github.com/o/r/pull/7"
	store := newFakeStore()
	store.StateV, store.TextV = "Integrating - Agent", threeUnits
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
	family := Family{
		Parent:   &Relative{ID: "p1", Title: "Parent plan", State: "Building - Agent"},
		Children: []Relative{{ID: "c1", Title: "Child plan", State: "Planning - Agent"}},
		Blockers: []Relative{{ID: "b1", Title: "Blocker plan", State: "Integrating - Human"}},
	}
	inbox := func(t *testing.T, fam Family) (Model, *fakeStore) {
		t.Helper()
		store := newFakeStore()
		store.Created = true
		store.StateV, store.TextV, store.FamilyV = "Building - Agent", threeUnits, fam
		m := New(context.Background(), newFakeAgent(), nil, store, "").WithInbox("Plan", "/src/p")
		m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
		m = refreshed(t, m)
		return drive(t, m, press("tab")), store
	}

	t.Run("enter opens the row under the cursor", func(t *testing.T) {
		m, _ := inbox(t, family)
		view := ansi.Strip(m.Content())
		for _, w := range []string{"RELATED", "Part of", "Parent plan", "Sub-plan", "Child plan", "Blocked by", "Blocker plan", "Integrating - Human"} {
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
		m, _ := inbox(t, Family{})
		assert.Check(t, !strings.Contains(ansi.Strip(m.Content()), "RELATED"))
		_, msg := enterOn(m)
		assert.Check(t, msg == nil, "enter with no rows gave %#v", msg)
	})

	t.Run("without an inbox", func(t *testing.T) {
		store := newFakeStore()
		store.StateV, store.TextV, store.FamilyV = "Building - Agent", threeUnits, family
		m := drive(t, flowModel(t, store), press("tab"))
		view := ansi.Strip(m.Content())
		assert.Check(t, cmp.Contains(view, "Parent plan"))
		assert.Check(t, !strings.Contains(view, "▶"), "a cursor without an inbox:\n%s", view)
		_, msg := enterOn(m)
		assert.Check(t, msg == nil, "enter without an inbox gave %#v", msg)
	})
}

func TestFamilyErrorIsQuiet(t *testing.T) {
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

// TestCtrlSStartsAnUnprioritizedPlan checks that ctrl+s, after a y, sends
// prioritize for an Unprioritized plan whose flow offers it, and refuses one
// whose flow does not.
func TestCtrlSStartsAnUnprioritizedPlan(t *testing.T) {
	unprioritized := func(t *testing.T, events ...string) (Model, *fakeStore) {
		t.Helper()
		store := newFakeStore()
		store.Created = true
		store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
		store.StateV = "Unprioritized"
		store.setEvents(events...)
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
		m, store := unprioritized(t, "prioritize", "abandon")
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assert.Assert(t, m.confirming)
		assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "Start work on the plan?"))
		m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		assert.Check(t, cmp.DeepEqual(started(store), []string{"Unprioritized"}))
		assert.Check(t, !m.startingWork)
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Starting work…"))
	})

	t.Run("n sends nothing", func(t *testing.T) {
		m, store := unprioritized(t, "prioritize", "abandon")
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		m = drive(t, m, press("n"))
		assert.Check(t, !m.confirming)
		assert.Check(t, !m.pendingStart)
		m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		assert.Check(t, cmp.Len(started(store), 0))
	})

	t.Run("not offered", func(t *testing.T) {
		m, store := unprioritized(t, "abandon")
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assert.Check(t, !m.confirming)
		assert.Check(t, cmp.Len(started(store), 0))
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "cannot be approved while it is"))
	})

	t.Run("a failed send says so", func(t *testing.T) {
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
	m = refreshed(t, m)
	return drive(t, m, press("tab"))
}

// pressAll drives each of keys through m in turn.
func pressAll(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		m = drive(t, m, press(k))
	}
	return m
}

// TestAbandonThePlan checks that x, while factory offers abandon, asks for a
// reason and sends abandon with the one picked, and that any other key keeps
// the plan.
func TestAbandonThePlan(t *testing.T) {
	abandoned := func(store *fakeStore) []string {
		store.mu.Lock()
		defer store.mu.Unlock()
		return slices.Clone(store.Abandoned)
	}
	live := []string{"address-comments", "abandon"}
	for _, tc := range []struct {
		name   string
		state  string
		events []string
		ended  bool
		err    error
		check  func(t *testing.T, m Model, store *fakeStore)
	}{
		{
			name:   "not offered without abandon",
			state:  "Planning - Human",
			events: []string{"address-comments"},
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, !strings.Contains(ansi.Strip(m.actionBar()), "abandon plan"))
				m = pressAll(t, m, "x")
				assert.Check(t, !m.askingReason)
				assert.Check(t, cmp.Len(abandoned(store), 0))
			},
		},
		{
			name:   "c abandons as canceled",
			state:  "Planning - Human",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "x abandon plan"))
				m = pressAll(t, m, "x")
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandon plan as: c canceled · w won't do · d duplicate · esc keep it"))
				m = pressAll(t, m, "c")
				assert.Check(t, cmp.DeepEqual(abandoned(store), []string{"canceled"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandoning: canceled"))
			},
		},
		{
			name:   "w abandons as won't do",
			state:  "Planning - Human",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "w")
				assert.Check(t, cmp.DeepEqual(abandoned(store), []string{"wont-do"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandoning: won't do"))
			},
		},
		{
			name:   "d abandons as duplicate",
			state:  "Planning - Human",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "d")
				assert.Check(t, cmp.DeepEqual(abandoned(store), []string{"duplicate"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandoning: duplicate"))
			},
		},
		{
			name:   "esc keeps the plan",
			state:  "Planning - Human",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x")
				m, cmd := m.Update(press("esc"))
				_, gone := closedBy(cmd)
				assert.Check(t, !gone, "esc at the reason prompt left the screen")
				assert.Check(t, !m.askingReason)
				assert.Check(t, cmp.Equal(m.status, ""))
				assert.Check(t, cmp.Len(abandoned(store), 0))
			},
		},
		{
			name:   "q keeps the plan",
			state:  "Planning - Human",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x")
				m, cmd := m.Update(press("q"))
				_, gone := closedBy(cmd)
				assert.Check(t, !gone, "q at the reason prompt left the screen")
				assert.Check(t, !m.askingReason)
				assert.Check(t, cmp.Equal(m.status, ""))
				assert.Check(t, cmp.Len(abandoned(store), 0))
			},
		},
		{
			name:   "a failed abandon says so",
			state:  "Planning - Human",
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
			name:  "not offered once the flow has ended",
			state: "Done",
			ended: true,
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, !strings.Contains(ansi.Strip(m.actionBar()), "abandon plan"))
				m = pressAll(t, m, "x")
				assert.Check(t, !m.askingReason)
			},
		},
		{
			name:   "a refresh keeps the prompt",
			state:  "Planning - Human",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x")
				store.mu.Lock()
				store.StateV = "Planning - Agent"
				store.mu.Unlock()
				m = refreshed(t, m)
				assert.Check(t, m.askingReason)
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Abandon plan as:"))
				m = pressAll(t, m, "w")
				assert.Check(t, cmp.DeepEqual(abandoned(store), []string{"wont-do"}))
			},
		},
		{
			name:   "x while abandoning asks nothing",
			state:  "Planning - Human",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "w")
				assert.Assert(t, m.abandoning)
				m = pressAll(t, m, "x")
				assert.Check(t, !m.askingReason)
				assert.Check(t, cmp.Len(abandoned(store), 1))
			},
		},
		{
			name:   "an abandon sent refreshes the plan",
			state:  "Planning - Human",
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
			store := newFakeStore()
			store.FlowV.Ended = tc.ended
			store.AbandonErr = tc.err
			m := endScreen(t, store, tc.state, tc.events...)
			tc.check(t, m, store)
		})
	}
}

// TestDeleteThePlan checks that X, while no session claims the plan, deletes
// it after a y and closes the screen, and that a refusal keeps it.
func TestDeleteThePlan(t *testing.T) {
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
			state:   "Planning - Human",
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
			state: "Done",
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
			state: "Done",
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "X")
				store.mu.Lock()
				store.StateV = "Canceled"
				store.mu.Unlock()
				m = refreshed(t, m)
				assert.Check(t, m.pendingDeletePlan)
				_ = pressAll(t, m, "y")
				assert.Check(t, deleted(store))
			},
		},
		{
			name:  "X while deleting asks nothing",
			state: "Done",
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "X", "y")
				assert.Assert(t, m.deleting)
				m = pressAll(t, m, "X")
				assert.Check(t, !m.confirming)
			},
		},
		{
			name:  "y deletes and closes a screen with no host",
			state: "Done",
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
			state: "Done",
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
			state: "Done",
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
			state: "Done",
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
			state: "Done",
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
