package plan

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"net/http"
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
	ts := m.titleInput.Styles()
	ts.Cursor.Blink = false
	m.titleInput.SetStyles(ts)
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
	case approvedMsg, claimedMsg, releasedMsg, designApprovedMsg, startedMsg, refreshedMsg, familiedMsg, commentSavedMsg, queueSavedMsg, createdMsg, editSavedMsg:
		return []tea.Msg{msg}
	}
	return nil
}

// startPlan has the screen on a new plan send it with title: the title is set,
// enter moves to the request, and enter sends. It settles once the plan is
// created, claimed and read.
func startPlan(t *testing.T, m Model, title string) Model {
	t.Helper()
	m = quiet(m)
	m.titleInput.SetValue(title)
	m = drive(t, m, press("enter"))
	return drive(t, m, press("enter"))
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

// The request and comment cursors blink on a screen from New; only the tests'
// quiet stops them.
func TestScreenCursorsBlink(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeStore(), "")
	assert.Check(t, m.input.Styles().Cursor.Blink)
	assert.Check(t, m.commentInput.Styles().Cursor.Blink)
	blink := m.commentInput.Focus()
	assert.Check(t, blink != nil, "a focused comment editor should blink")
	q := quiet(m)
	blink = q.commentInput.Focus()
	assert.Check(t, blink == nil, "quiet should stop the blink")
}

// A new plan asks for a title and a request, and once they are sent the
// Overview shows the plan and the request in place of the inputs.
func TestNewPlanSendsItsTitleAndRequestThenShowsThePlan(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Add a flag\n\n## 1. Outcome\n\nThe flag exists."
	store.ClaimHost = "scotts-mbp"
	m := New(context.Background(), store, "add a --dry-run flag")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Title"))
	assert.Check(t, cmp.Contains(view, titlePlaceholder))
	assert.Check(t, cmp.Contains(view, "add a --dry-run flag"))
	assert.Check(t, m.titleInput.Focused(), "the title has the keyboard first")

	m = startPlan(t, m, "Add a flag")
	assert.Assert(t, cmp.Len(store.Created, 1))
	assert.Check(t, cmp.Equal(store.Created[0].Title, "Add a flag"))
	assert.Check(t, cmp.Equal(store.Created[0].Request, "add a --dry-run flag"))
	view = ansi.Strip(m.Content())
	for _, want := range []string{"The flag exists.", "add a --dry-run flag", "Overview"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
	assert.Check(t, !strings.Contains(view, requestPlaceholder), "the request input is still shown:\n%s", view)
	assert.Check(t, !strings.Contains(view, titlePlaceholder), "the title input is still shown:\n%s", view)

	fresh := New(context.Background(), newFakeStore(), "")
	fresh = drive(t, fresh, tea.WindowSizeMsg{Width: 120, Height: 40})
	assert.Check(t, cmp.Contains(ansi.Strip(fresh.Content()), "Request"))
	assert.Check(t, cmp.Contains(ansi.Strip(fresh.Content()), titlePlaceholder))
	assert.Check(t, cmp.Contains(ansi.Strip(fresh.Content()), requestPlaceholder))
}

// Tab or enter in the title moves to the request, shift+tab goes back, and
// enter in the request sends.
func TestTitleAndRequestKeepTheKeyboardInTurnOfFields(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = typeText(t, m, "Add")
	assert.Check(t, cmp.Equal(m.titleInput.Value(), "Add"))
	m = drive(t, m, press("tab"))
	assert.Check(t, !m.titleInput.Focused() && m.input.Focused(), "tab did not move to the request")
	m = typeText(t, m, "ask")
	assert.Check(t, cmp.Equal(m.input.Value(), "ask"))
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	assert.Check(t, m.titleInput.Focused() && !m.input.Focused(), "shift+tab did not go back to the title")
	m = drive(t, m, press("enter"))
	assert.Check(t, !m.titleInput.Focused() && m.input.Focused(), "enter did not move to the request")
	assert.Check(t, cmp.Len(store.Created, 0), "enter in the title sent the plan")
	m = drive(t, m, press("enter"))
	assert.Assert(t, cmp.Len(store.Created, 1))
	assert.Check(t, cmp.Equal(store.Created[0].Title, "Add"))
	assert.Check(t, cmp.Equal(store.Created[0].Request, "ask"))
}

// An empty or blank title or request sends nothing, and the screen says which
// is missing.
func TestCreateNeedsTitleAndRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		title   string
		request string
		want    string
	}{
		{
			name:    "no title",
			title:   "",
			request: "ask",
			want:    "Add a title.",
		},
		{
			name:    "no request",
			title:   "Title",
			request: "",
			want:    "Add a request.",
		},
		{
			name:    "neither",
			title:   "",
			request: "",
			want:    "Add a title and a request.",
		},
		{
			name:    "blank title",
			title:   "   ",
			request: "ask",
			want:    "Add a title.",
		},
		{
			name:    "blank request",
			title:   "Title",
			request: " \t ",
			want:    "Add a request.",
		},
		{
			name:    "blank both",
			title:   "  ",
			request: "  ",
			want:    "Add a title and a request.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			m := New(context.Background(), store, "")
			m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			m.titleInput.SetValue(tc.title)
			m.input.SetValue(tc.request)
			m = drive(t, m, press("enter"))
			m = drive(t, m, press("enter"))
			assert.Check(t, cmp.Len(store.Created, 0))
			assert.Check(t, cmp.Equal(store.Claims, 0))
			assert.Check(t, cmp.Equal(m.status, tc.want))
			assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), tc.want))
			assert.Check(t, !m.created && !m.creating)
		})
	}

	t.Run("request argument", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		m := New(context.Background(), store, "  add a flag  ")
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		assert.Check(t, cmp.Equal(m.input.Value(), "add a flag"))
		assert.Check(t, m.titleInput.Focused(), "the title has the keyboard")
		m = drive(t, m, press("enter"))
		m = drive(t, m, press("enter"))
		assert.Check(t, cmp.Len(store.Created, 0))
		assert.Check(t, cmp.Equal(m.status, "Add a title."))
	})
}

// A failed create is shown, keeps what was typed, and claims nothing.
func TestCreateFails(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.CreateErr = errors.New("factory is down")
	m := New(context.Background(), store, "ask")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Title")
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Could not create the plan: factory is down"))
	assert.Check(t, cmp.Equal(m.titleInput.Value(), "Title"))
	assert.Check(t, cmp.Equal(m.input.Value(), "ask"))
	assert.Check(t, cmp.Equal(store.Claims, 0), "a plan that was not created was claimed")
	assert.Check(t, !m.created && !m.creating)

	// Sending again, once factory is back, creates it.
	store.CreateErr = nil
	m = drive(t, m, press("enter"))
	assert.Check(t, cmp.Len(store.Created, 1))
	assert.Check(t, m.created)
}

// Creating the plan has the default worker claim it, and the flow that is read
// next names the worker.
func TestNewPlanClaimsAfterCreate(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ClaimHost = "scotts-mbp"
	store.TextV = "# Retry\n\n## 1. Outcome\n\nRetry uploads."
	m := New(context.Background(), store, "Retry uploads")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Retry flaky uploads")
	assert.Assert(t, cmp.Len(store.Created, 1))
	assert.Check(t, cmp.Equal(store.Created[0].Title, "Retry flaky uploads"))
	assert.Check(t, cmp.Equal(store.Created[0].Request, "Retry uploads"))
	assert.Check(t, cmp.Equal(store.Claims, 1))
	assert.Check(t, cmp.Equal(m.workerName, "scotts-mbp"))
	assert.Assert(t, m.flow.Worker != nil, "the flow poll that follows the claim did not name the worker")
	assert.Check(t, cmp.Equal(m.flow.Worker.Hostname, "scotts-mbp"))
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "scotts-mbp · architect running"))
}

// A claim that fails after the plan is made leaves the plan created and
// unclaimed, says why, and R claims it again.
func TestClaimAfterCreateFails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		err     error
		want    string
		notWant string
	}{
		{
			name:    "no worker",
			err:     ErrNoWorker,
			want:    "No live worker can claim this plan · start factory daemon, then press R",
			notWant: "",
		},
		{
			name:    "factory fails",
			err:     errors.New("claim the plan: 503 Service Unavailable"),
			want:    "claim the plan: 503 Service Unavailable",
			notWant: "No live worker",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.ClaimErr = tc.err
			store.TextV = twoParts
			// A new plan's flow starts in init, waiting on a session.
			store.StateV, store.FlowV.WaitingOn, store.FlowV.Next = "init", "session", sessionStep
			m := New(context.Background(), store, "ask")
			m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
			m = startPlan(t, m, "Title")
			assert.Check(t, m.created, "the plan is not kept")
			assert.Check(t, cmp.Len(store.Created, 1))
			assert.Check(t, cmp.Equal(store.Claims, 1))
			view := ansi.Strip(m.Content())
			assert.Check(t, cmp.Contains(view, tc.want))
			if tc.notWant != "" {
				assert.Check(t, !strings.Contains(view, tc.notWant), view)
			}
			assert.Check(t, m.flow.Worker == nil)
			assert.Check(t, rKeyOffered(m), "R is not offered to claim it again")

			store.ClaimErr, store.ClaimHost = nil, "scotts-mbp"
			m = drive(t, m, press("R"))
			assert.Check(t, cmp.Equal(store.Claims, 2), "a second R did not call Claim again")
			assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "scotts-mbp"))
		})
	}
}

// rKeyOffered is whether the footer offers R.
func rKeyOffered(m Model) bool {
	return strings.Contains(ansi.Strip(theme.Help(theme.New(m.dark), 1000).View(m.keys())), "R claim")
}

func TestExistingPlanOpensWithoutCreatingOrClaimingIt(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.Existing, store.TextV = true, "# Existing plan\n\n## 1. Outcome\n\nKeep this outcome."
	store.FlowV.Next = []Step{{Actor: "human", Event: "address-comments"}}
	store.CreateErr = errors.New("an existing plan must not be recreated")
	m := New(t.Context(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 32})
	m = refreshed(t, m)
	if !strings.Contains(ansi.Strip(m.Content()), "Keep this outcome.") {
		t.Fatal("opening should read the plan")
	}
	assert.Check(t, cmp.Len(store.Created, 0))
	assert.Check(t, cmp.Equal(store.Claims, 0), "opening a plan claimed it")
	assert.Check(t, cmp.Equal(m.focus, panePlan), "an existing plan opens on the plan pane")
	m, _ = m.focusOn(paneOverview)
	m = typeText(t, m, "explain the outcome")
	m = drive(t, m, press("enter"))
	assert.Check(t, cmp.Len(store.Created, 0))
	assert.Check(t, cmp.Equal(m.input.Value(), ""))
}

func TestApprovingApprovesThePlanInFactory(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.UnitsV = []Unit{{Name: "Unit 1: Add the flag"}}
	store.StateV = "refine-units"
	store.setEvents("address-comments", "approve-for-building", "end")
	store.setRequired("approve-for-building")
	m := New(context.Background(), store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !m.confirming {
		t.Fatal("ctrl+s should ask to confirm")
	}
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if store.Approved != 1 || !m.Approved {
		t.Fatalf("approved %d times, screen approved %v", store.Approved, m.Approved)
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
	m := New(context.Background(), store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
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
	if store.Approved > 0 {
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
				human("address-comments", false), human("approve-for-building", true), human("end", false),
			}},
			want: true,
		},
		{
			name: "human, answer-open-questions required",
			flow: Flow{State: "refine-proposal", WaitingOn: "human", Next: []Step{
				human("answer-open-questions", true), human("approve-for-units", false), human("end", false),
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
				human("address-comments", false), human("approve-for-units", false), human("end", false),
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
		store.setEvents("address-comments", "approve-for-building", "end")
		store.setRequired("approve-for-building")
		m := New(context.Background(), store, "do it")
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		m = startPlan(t, m, "Plan")
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Next: you approve the plan · ctrl+s"))
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assert.Assert(t, m.confirming, "ctrl+s did not ask to approve: %q", m.status)
		drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		assert.Check(t, store.Approved > 0, "the plan was not sent")
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
			{Actor: "human", Event: "end"},
		}}
		store.setEvents("address-comments", "approve-for-building", "end")
		m := New(context.Background(), store, "do it")
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		m = startPlan(t, m, "Plan")
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Next: you approve the plan · ctrl+s"))
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assert.Assert(t, m.confirming, "ctrl+s did not ask to approve: %q", m.status)
		drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		assert.Check(t, store.Approved > 0, "the plan was not sent")
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
		m := New(context.Background(), store, "do it")
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		m = startPlan(t, m, "Plan")
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
			store.setEvents("end")
			m := New(context.Background(), store, "do it")
			m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
			m = startPlan(t, m, "Plan")
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
			if stage := planstate.Stage(shown); stage != "" {
				row1 = stage
			}
			assert.Check(t, cmp.Contains(lines[headerRows], row1))
			assert.Check(t, !strings.Contains(view, "Plan · "), "the pane's title names the state:\n%s", view)

			m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			assert.Check(t, !m.confirming, "ctrl+s asked to send a plan in %q", state)
			assert.Check(t, strings.Contains(ansi.Strip(m.status), "cannot be approved now: it is "+shown), m.status)
			drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
			assert.Check(t, store.Approved == 0, "a plan in %q was sent", state)
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
	store.setEvents("address-comments", "approve-for-units", "approve-for-building", "end")
	store.setRequired("approve-for-units")
	m := New(context.Background(), store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
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
	if store.Approved > 0 {
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
	store.setEvents("address-comments", "approve-for-building", "end")
	store.setRequired("approve-for-building")
	m := New(context.Background(), store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Next: you approve the plan · ctrl+s"))
	assert.Check(t, !strings.Contains(view, "Next: you approve the design · ctrl+s"), view)
	assert.Check(t, cmp.Contains(view, "ctrl+s Approve plan"))

	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Assert(t, m.confirming)
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Approve this plan and build it?"))
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	assert.Check(t, store.Approved > 0, "approving did not start the plan's build")
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
	store.setEvents("address-comments", "approve-for-units", "end")
	store.setRequired("approve-for-units")
	m := New(context.Background(), store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Next: you approve the design · ctrl+s"))
	assert.Check(t, !strings.Contains(view, "Next: you approve the plan · ctrl+s"), view)
	assert.Check(t, cmp.Contains(view, "ctrl+s Approve design"))

	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Assert(t, m.confirming)
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Approve the design"))
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	assert.Check(t, store.Approved == 0, "approving the design started the plan's build")
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
				m = New(context.Background(), store, "do it")
				m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
				m = startPlan(t, m, "Plan")
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
	m := New(context.Background(), store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
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
				m := New(context.Background(), store, "plan it")
				m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
				m = startPlan(t, m, "Plan")
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
	store.setEvents("address-comments", "answer-open-questions", "approve-for-units", "end")
	store.setRequired("answer-open-questions")
	m := New(context.Background(), store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
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
	assert.Check(t, store.Approved == 0, "ctrl+s started the plan's build with questions open")
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
			m := New(context.Background(), store, "")
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
			name: "plan",
			keys: []string{"tab", "down", "down"},
			want: "Plan",
		},
		{
			name:   "moving a comment",
			keys:   []string{"tab", "down"},
			moving: true,
			want:   "Plan · ",
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
			assert.Check(t, cmp.Contains(top, "Overview"))
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
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startPlan(t, m, "Plan")
	return m, store
}

func TestSSavesCommentsFromThePlan(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startPlan(t, m, "Plan")

	// Keys in the Overview, then a comment in the plan.
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
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startPlan(t, m, "Plan")
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
			events:         []string{"approve-for-units", "end"},
			offerOnComment: true,
			offered:        true,
			saved:          1,
			handedOff:      1,
			waiting:        true,
		},
		{
			name:          "first comment, factory takes the comments itself",
			waitingOn:     "human",
			events:        []string{"approve-for-units", "end"},
			takeOnComment: true,
			offered:       true,
			saved:         1,
			waiting:       true,
		},
		{
			name:      "waiting on a session with address-comments still offered",
			waitingOn: "session",
			events:    []string{"address-comments", "end"},
			offered:   true,
			saved:     1,
			handedOff: 1,
			waiting:   true,
		},
		{
			name:      "factory works on the plan",
			waitingOn: "session",
			events:    []string{"end"},
		},
		{
			name:      "factory neither takes nor asks for the comments",
			waitingOn: "human",
			events:    []string{"end"},
			offered:   true,
			saved:     1,
			status:    "Comments saved.",
		},
		{
			name:          "the daemon dispatches between the read and the send",
			waitingOn:     "human",
			events:        []string{"address-comments", "end"},
			takeOnHandOff: true,
			offered:       true,
			saved:         1,
			waiting:       true,
		},
		{
			name:        "the hand-off is refused and factory has not taken the comments",
			waitingOn:   "human",
			events:      []string{"address-comments", "end"},
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

func TestSSavesCommentsWhileAWorkerWorks(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.ClaimHost = "scotts-mbp"
	store.TextV = twoParts
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startPlan(t, m, "Plan")
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))

	// Comments go to factory, so a worker holding the plan does not hold them
	// up.
	assert.Assert(t, m.flow.Worker != nil, "no worker holds the plan")
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
	// them, as a long request does.
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
	m := New(context.Background(), newFakeStore(), "")
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
		m := New(context.Background(), store, "")
		m = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 24})
		assertFits(t, m, width, 24)

		m.input.SetValue("x")
		m = startPlan(t, m, "Plan")
		// A narrow screen's tab goes to the plan.
		m = drive(t, m, press("tab"))

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
		m = drive(t, m, press("s"))
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
		m := New(context.Background(), newFakeStore(), "").WithTerminal(dark, false)
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
		st := theme.New(dark)
		top := strings.Split(m.Content(), "\n")[headerRows+stripRows]
		focused := lipgloss.NewStyle().Foreground(st.Brand.GetForeground()).Render("╭─ ")
		quiet := lipgloss.NewStyle().Foreground(st.VDim.GetForeground()).Render("╭─ ")
		if !strings.HasPrefix(top, focused) {
			t.Errorf("dark=%v: the focused new plan pane's edge is not the accent: %q", dark, top)
		}
		if !strings.Contains(top, quiet) {
			t.Errorf("dark=%v: the plan pane without the keyboard is not quiet: %q", dark, top)
		}
	}
}

// The input boxes take one step less contrast than the pane's own accent
// border: dim for the one with the keyboard while the new plan's pane has it,
// quiet for the other and for both once the plan pane does, and never the
// pane's own accent colour either way.
func TestInputBoxesAreBelowThePaneAccent(t *testing.T) {
	t.Parallel()
	// check asserts the title row of the box named name starts right after the
	// pane's gutter drawn in gutterColor, with its own edge in edgeColor, and
	// never in the brand accent.
	check := func(t *testing.T, m Model, name string, gutterColor, edgeColor color.Color, label string) {
		t.Helper()
		var line string
		for _, l := range strings.Split(m.Content(), "\n") {
			if strings.Contains(l, "─ "+name) || strings.Contains(l, name+"\x1b[m") && strings.Contains(l, "╭─") {
				line = l
				break
			}
		}
		if line == "" {
			t.Fatalf("%s: no line with the %s box's title: %q", label, name, m.Content())
		}
		gutter := lipgloss.NewStyle().Foreground(gutterColor).Render("│") + " "
		rest, ok := strings.CutPrefix(line, gutter)
		if !ok {
			t.Fatalf("%s: the %s box does not start right after the pane's gutter: %q", label, name, line)
		}
		wantEdge := lipgloss.NewStyle().Foreground(edgeColor).Render("╭─ ")
		if !strings.HasPrefix(rest, wantEdge) {
			t.Errorf("%s: the %s box's edge is not as expected: %q", label, name, line)
		}
		// Only the box's own top edge, not the pane's right border further
		// along the same (horizontally joined) line, which may be the accent.
		box := rest
		if end := strings.Index(rest, "╮"); end >= 0 {
			box = rest[:end+len("╮")]
		}
		if strings.Contains(box, fgSeq(m.brand())) {
			t.Errorf("%s: the %s box took the pane's own accent colour: %q", label, name, line)
		}
	}

	for _, dark := range []bool{true, false} {
		m := New(context.Background(), newFakeStore(), "").WithTerminal(dark, false)
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
		// The title has the keyboard: the pane's own gutter is the accent, the
		// title's box one step down, dim, and the request's quiet.
		check(t, m, "Title", m.brand(), m.dim(), fmt.Sprintf("dark=%v title focused", dark))
		check(t, m, "Request", m.brand(), m.quiet(), fmt.Sprintf("dark=%v title focused", dark))

		m = drive(t, m, press("tab"))
		check(t, m, "Title", m.brand(), m.quiet(), fmt.Sprintf("dark=%v request focused", dark))
		check(t, m, "Request", m.brand(), m.dim(), fmt.Sprintf("dark=%v request focused", dark))

		// Tab moves the keyboard to the plan pane: the pane's gutter goes
		// quiet, and both boxes follow it to quiet too.
		m = drive(t, m, press("tab"))
		check(t, m, "Title", m.quiet(), m.quiet(), fmt.Sprintf("dark=%v plan focused", dark))
		check(t, m, "Request", m.quiet(), m.quiet(), fmt.Sprintf("dark=%v plan focused", dark))
	}
}

// The input boxes, like the panes, sit on the terminal's own background: no
// ground behind them and no band on the cursor's line.
func TestInputBoxesSitOnTheTerminalsBackground(t *testing.T) {
	t.Parallel()
	for _, dark := range []bool{true, false} {
		m := New(context.Background(), newFakeStore(), "").WithTerminal(dark, false)
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
		lines := strings.Split(m.Content(), "\n")
		start := -1
		for i, l := range lines {
			if strings.Contains(l, "Title") {
				start = i
				break
			}
		}
		if start < 0 {
			t.Fatalf("dark=%v: no line with the title box's title: %q", dark, m.Content())
		}
		for i := start; i < start+titleBoxRows+inputBoxRows && i < len(lines); i++ {
			if strings.Contains(lines[i], "\x1b[48;") {
				t.Errorf("dark=%v: line %d of the input boxes paints a background: %q", dark, i, lines[i])
			}
		}
	}
}

// The request box is six rows tall, the title box one, and the pane's log
// above them gives up the rows.
func TestRequestBoxIsSixRowsTall(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeStore(), "").WithTerminal(true, false)
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if inputRows != 6 {
		t.Fatalf("inputRows = %d, want 6", inputRows)
	}
	lines := strings.Split(m.Content(), "\n")
	start, end := -1, -1
	for i, l := range lines {
		if start < 0 && strings.Contains(ansi.Strip(l), "─ Request") {
			start = i
		}
		if start >= 0 && strings.Contains(l, "╰") {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatalf("no request box in %q", m.Content())
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

	short := New(context.Background(), newFakeStore(), "").WithTerminal(true, false)
	short = drive(t, short, tea.WindowSizeMsg{Width: 120, Height: 10})
	if h := short.overviewView.Height(); h < 1 {
		t.Errorf("viewport height at 10 rows = %d, want at least 1", h)
	}
}

// The panes and the action bar sit on the terminal's own background, as
// chunk ui's do: only what is set apart (the block the cursor is on,
// comments, code) has a ground of its own.
func TestPanesSitOnTheTerminalsBackground(t *testing.T) {
	t.Parallel()
	for _, dark := range []bool{true, false} {
		m := New(context.Background(), newFakeStore(), "").WithTerminal(dark, false)
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
		lines := strings.Split(m.Content(), "\n")
		for _, i := range []int{2, len(lines) - 2} {
			if strings.Contains(lines[i], "\x1b[48;") {
				t.Errorf("dark=%v: line %d paints a background: %q", dark, i, lines[i])
			}
		}
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
	m := New(context.Background(), store, "")
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
	m := New(context.Background(), newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 30})
	m = drive(t, m, press("tab")) // to the request, which has a new line to type
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

// approvableStore is a store whose plan waits on the developer for
// approve-for-building, with live worker w holding it when w is not nil.
func approvableStore(w *Worker) *fakeStore {
	store := newFakeStore()
	store.Existing = true
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.UnitsV = []Unit{{Name: "Unit 1: Add the flag"}}
	store.StateV = "refine-units"
	store.setEvents("address-comments", "approve-for-building", "end")
	store.setRequired("approve-for-building")
	store.FlowV.Worker = w
	return store
}

// approved has m approve the plan with ctrl+s and y, and returns what the
// approval sends: the screen, and the command that answers it.
func approved(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Assert(t, m.confirming, "ctrl+s did not ask to approve: %q", m.status)
	m, cmd := m.Update(press("y"))
	batch, ok := cmd().(tea.BatchMsg)
	assert.Assert(t, ok && len(batch) == 2, "y did not start the approval")
	return m.Update(batch[0]())
}

// Approving closes a screen that is not kept, saying which worker builds the
// plan: the live one that holds it, and none otherwise.
func TestApproveClosesWithWorker(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		worker *Worker
		want   string
	}{
		{
			name:   "a live worker",
			worker: &Worker{ID: "w1", Hostname: "scotts-mbp", Live: true},
			want:   "scotts-mbp",
		},
		{
			name:   "no worker",
			worker: nil,
			want:   "",
		},
		{
			name:   "a worker that is not live",
			worker: &Worker{ID: "w1", Hostname: "scotts-mbp"},
			want:   "",
		},
		{
			name:   "a worker that can't be read",
			worker: &Worker{ID: "w1", Unread: "no such worker"},
			want:   "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := approvableStore(tc.worker)
			m := New(context.Background(), store, "")
			m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			m = refreshed(t, m)
			m, cmd := approved(t, m)
			got, ok := closedBy(cmd)
			assert.Check(t, ok, "approving did not close the screen")
			assert.Check(t, cmp.Equal(got, ClosedMsg{Approved: true, Worker: tc.want}))
			assert.Check(t, cmp.Equal(store.Approved, 1))
			assert.Check(t, m.Approved)
			assert.Check(t, cmp.Equal(m.Worker, tc.want))
		})
	}

	t.Run("opened from chunk ui", func(t *testing.T) {
		t.Parallel()
		store := approvableStore(&Worker{ID: "w1", Hostname: "scotts-mbp", Live: true})
		m := New(context.Background(), store, "").WithInbox("Retry uploads")
		m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		m = refreshed(t, m)
		_, cmd := approved(t, m)
		msgs := messagesOf(cmd)
		assert.Check(t, slices.Contains(msgs, tea.Msg(BuildingMsg{Worker: "scotts-mbp"})), "the screen said %#v", msgs)
		for _, msg := range msgs {
			_, isClosed := msg.(ClosedMsg)
			assert.Check(t, !isClosed, "a kept screen closed on approval")
		}
	})
}

// A refused or failed approval leaves the screen open, with the error shown
// and no approved line.
func TestApproveSendFails(t *testing.T) {
	t.Parallel()
	store := approvableStore(&Worker{ID: "w1", Hostname: "scotts-mbp", Live: true})
	store.ApproveErr = errors.New("factory is down")
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = refreshed(t, m)
	m, cmd := approved(t, m)
	_, isClosed := closedBy(cmd)
	assert.Check(t, !isClosed, "a failed approval closed the screen")
	assert.Check(t, !m.Approved && m.Worker == "" && !m.approving)
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Could not approve the plan: factory is down"))
	assert.Check(t, !strings.Contains(view, "Approved"), "an approved line is shown:\n%s", view)
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

// A host that keeps the screen keeps it after the approval, on the Overview. A
// comment queued before the approve stays queued, and is saved once factory
// has acted on the approve and takes comments again.
func TestApprovingInAKeptScreenFollowsTheBuild(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.Existing = true
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.UnitsV = []Unit{{Name: "Unit 1: Add the flag"}}
	store.FlowV.Worker = &Worker{ID: "w1", Hostname: "scotts-mbp", Live: true}
	m := New(context.Background(), store, "").WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = refreshed(t, m)
	m, _ = m.focusOn(panePlan)
	assert.Assert(t, m.focus == panePlan, "focus %v", m.focus)
	m = drive(t, m, press("c"))
	m = typeText(t, m, "before")
	m = drive(t, m, press("enter"))
	assert.Assert(t, cmp.Len(queuedComments(m.comments), 1))
	m, cmd := approved(t, m)
	assert.Check(t, cmp.Equal(store.Approved, 1))

	msgs := messagesOf(cmd)
	assert.Check(t, slices.Contains(msgs, tea.Msg(BuildingMsg{Worker: "scotts-mbp"})), "the screen said %#v", msgs)
	for _, msg := range msgs {
		_, isClosed := msg.(ClosedMsg)
		assert.Check(t, !isClosed, "a kept screen closed on approval")
	}
	assert.Check(t, cmp.Equal(m.focus, paneOverview), "the keyboard did not go to the Overview")
	assert.Check(t, m.Approved)
	assert.Check(t, cmp.Equal(m.Worker, "scotts-mbp"))
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "Approved · scotts-mbp is building it"))

	m, _ = m.focusOn(paneOverview)
	for _, k := range []string{"m", "o", "r", "e"} {
		m = drive(t, m, press(k))
	}
	m = drive(t, m, press("enter"))
	assert.Check(t, cmp.Equal(m.input.Value(), ""))

	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Check(t, !m.confirming, "ctrl+s asks to approve a plan already building")
	m = drive(t, m, press("y"))
	assert.Check(t, cmp.Equal(store.Approved, 1), "the plan was approved again")

	// The comment queued before the approve is kept, and held while factory
	// has yet to act on the approve, though the flow read still waits on you.
	assert.Assert(t, cmp.Len(queuedComments(m.comments), 1))
	m = refreshed(t, m) // the fake store's flow has not moved
	assert.Check(t, m.moving != nil && m.moving.kind == movingApprove)
	m, _ = m.focusOn(panePlan)
	assert.Check(t, !m.keys().SendComments.Enabled(), "s offered before factory acted on the approve")
	// The approve's status shows over the waiting line, as any status does.
	assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "Approved · scotts-mbp is building it"))
	assert.Check(t, cmp.Contains(ansi.Strip(m.waitingLine(200)), "Plan approved · waiting for factory to start the build"))
	m = drive(t, m, press("s"))
	m, _ = m.focusOn(paneOverview)
	assert.Check(t, !m.keys().SendComments.Enabled(), "s offered in the Overview before factory acted on the approve")
	m = drive(t, m, press("s")) // the Overview's send, held too
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
	assert.Assert(t, m.focus == panePlan, "focus %v", m.focus)
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
			m := New(context.Background(), store, "plan it")
			if tc.back {
				m = m.WithBack()
			}
			m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
			m = startPlan(t, m, "Plan")
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

func TestApprovingShowsItWhileFactoryAnswers(t *testing.T) {
	t.Parallel()
	store := approvableStore(nil)
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = refreshed(t, m)
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	m, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("approving did not start the approval and the spinner: %#v", cmd())
	}
	assert.Check(t, m.approving)
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Approving the plan…"))

	// The spinner keeps turning while the approval works.
	_, next := m.Update(batch[1]())
	if next == nil {
		t.Error("the spinner stopped while approving")
	}

	m, cmd = m.Update(batch[0]())
	if got, ok := closedBy(cmd); !ok || !got.Approved {
		t.Fatalf("the approval closed with %+v, %v", got, ok)
	}
	if _, next = m.Update(batch[1]()); next != nil {
		t.Error("the spinner turns on once the approval is done")
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
	if got, ok := closedBy(cmd); !ok || got.Approved {
		t.Fatalf("esc closed with %+v, %v", got, ok)
	}
	if len(m.comments) != 1 {
		t.Errorf("closing dropped the comment: %+v", m.comments)
	}
}

func TestBackEscLeavesWhileAWorkerWorks(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.Existing = true
	store.FlowV.Worker = &Worker{ID: "w1", Hostname: "scotts-mbp", Live: true}
	m := New(context.Background(), store, "").WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	m = refreshed(t, m)

	// The worker is the host's to stop, and goes on working, so leaving stops
	// nothing.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, ok := closedBy(cmd); !ok {
		t.Fatal("esc while a worker works did not go back")
	}
	assert.Check(t, cmp.Equal(store.Released, 0))
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
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	return startPlan(t, m, "Plan")
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

// Once the plan exists the page keys scroll the Overview, and the footer says
// so; it keeps where it is scrolled to.
func TestPageKeysScrollTheOverview(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.CreateErr = errors.New(strings.Repeat("A long failure.\n", 30))
	m := New(context.Background(), store, "first")
	// Wide enough for the footer to keep paging beside R.
	m = drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 30})
	m = startPlan(t, m, "Plan")
	store.mu.Lock()
	store.CreateErr = nil
	store.mu.Unlock()
	m = drive(t, m, press("enter"))
	assert.Assert(t, m.created)
	m = drive(t, m, press("enter")) // opens the log, which is taller than the pane
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "pgup/pgdn page") {
		t.Errorf("paging is not in the footer:\n%s", view)
	}
	assert.Assert(t, !m.overviewView.AtBottom(), "the log should be taller than the pane")
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	assert.Check(t, m.overviewView.YOffset() > 0, "pgdn did not scroll")
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	assert.Check(t, cmp.Equal(m.overviewView.YOffset(), 0), "pgup did not scroll back")
}

func TestEnterDoesNotConfirmSendingThePlan(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists."
	m := New(context.Background(), store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = drive(t, m, press("enter"))
	if store.Approved > 0 || m.confirming {
		t.Fatalf("enter answered the question: approved %d, still asking %v", store.Approved, m.confirming)
	}
}

func TestTwoCommentsOnIdenticalBlocksStayOnSeparateBlocks(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeStore(), "")
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
	m := New(context.Background(), newFakeStore(), "")
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
	m := New(context.Background(), newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 3, quote: "* [AGENT] parse it in main.go", text: "x"}}
	setPlan(&m, "## 1. Outcome\n\nRewritten.")
	m.renderPlan()

	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), newFakeStore(), "")
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
	m, _ = m.focusOn(panePlan)

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
	m := New(context.Background(), newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: -1, quote: "something gone", text: "c"}}
	m.moveCursor(0)
	m, _ = m.focusOn(panePlan)

	// With nothing commented below it, n skips the blocks between the
	// cursor and the comments on parts Claude has rewritten.
	m = drive(t, m, press("n"))
	if m.cursor != len(m.blocks) {
		t.Fatalf("cursor after n: %d, want %d", m.cursor, len(m.blocks))
	}
}

func TestNextStaysPutWithNoCommentBelow(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 2, quote: "## 2. Design", text: "a"}}
	m, _ = m.focusOn(panePlan)

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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)
	m.comments = []comment{{block: -1, quote: "gone", text: "x"}}
	m, _ = m.focusOn(panePlan)
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

// Once the plan exists the left pane is its activity: what is typed there
// goes nowhere.
func TestTypingAfterThePlanExistsSendsNothing(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
	assert.Assert(t, m.created)
	assert.Check(t, cmp.Equal(m.focus, paneOverview))

	m = typeText(t, m, "and keep it small")
	m = drive(t, m, press("enter"))
	assert.Check(t, cmp.Equal(m.input.Value(), ""))
	assert.Check(t, cmp.Equal(m.titleInput.Value(), ""))
	assert.Check(t, cmp.Len(store.Created, 1))
}

// sessionStep is a flow step waiting on a planning session.
var sessionStep = []Step{{Actor: "session", Task: "write-units", Dispatched: true}}

func resumeModel(t *testing.T, state string, next []Step) (Model, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	store.Existing, store.TextV, store.StateV = true, twoParts, state
	store.FlowV.Next = next
	store.FlowV.WaitingOn = waitingOn(next)
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	return m, store
}

// waitingOn is who factory waits on for a flow with next: a session when
// next has a session's entry, else the developer, as the fake store starts.
func waitingOn(next []Step) string {
	if slices.ContainsFunc(next, func(s Step) bool { return s.Actor == ActorSession }) {
		return string(ActorSession)
	}
	return string(ActorHuman)
}

func TestPlanIsSentOnceWhileTheFactoryAnswers(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeStore(), "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
	m.approving = true
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.confirming {
		t.Error("ctrl+s asked to send the plan again while it is being sent")
	}
}

func TestStripShowsFlowStatus(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.setWaitingOn("human")
	store.setEvents("address-comments", "approve-for-units", "approve-for-building", "end")
	store.setRequired("approve-for-units")
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "No plan yet") {
		t.Errorf("header does not say there is no plan:\n%s", view)
	}

	store.TextV = "# Add a flag\n\n## 1. Outcome\n\nThe flag exists."
	m.input.SetValue("add a flag")
	m = startPlan(t, m, "Add a flag")
	view := ansi.Strip(m.Content())
	if !strings.Contains(view, "Next: you approve the design · ctrl+s") {
		t.Errorf("strip does not say the design is ready:\n%s", view)
	}
	lines := strings.Split(view, "\n")
	assert.Check(t, cmp.Contains(lines[1], "Planning · Next: you approve the design · ctrl+s"))
	assert.Check(t, !strings.Contains(view, "Planning › Building"), view)

	store.TextV += "\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	store.UnitsV = []Unit{{Name: "Unit 1: Add the flag"}}
	store.StateV = "refine-units"
	store.setEvents("address-comments", "approve-for-building", "end")
	store.setRequired("approve-for-building")
	m = refreshed(t, m)
	view = ansi.Strip(m.Content())
	if !strings.Contains(view, "Next: you approve the plan · ctrl+s") {
		t.Errorf("strip does not say the plan is ready:\n%s", view)
	}
	lines = strings.Split(view, "\n")
	assert.Check(t, cmp.Contains(lines[1], "Planning · Next: you approve the plan · ctrl+s"))
	assert.Check(t, !strings.Contains(view, "Planning › Building"), view)

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
	m := New(context.Background(), store, "plan it").WithBranch("hanabel/retry-429")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
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
	m := New(context.Background(), store, "")
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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = refreshed(t, m)
	view := ansi.Strip(m.planView.View())
	assert.Check(t, cmp.Contains(view, "OPEN QUESTIONS"), "the heading is drawn:\n%s", view)
	assert.Check(t, !strings.Contains(view, "None."), "prose outside the list is drawn:\n%s", view)
}

func TestIdenticalBlocksOfDifferentUnitsEachHaveTheirOwnAt(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeStore(), "")
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
	m := New(context.Background(), newFakeStore(), "")
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
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
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
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
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
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Plan")
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "○ 1  Add RetryPolicy"), "view:\n%s", view)

	store.mu.Lock()
	store.UnitsV = planWithUnitsRead
	store.mu.Unlock()
	m = refreshed(t, m)
	view = ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "✓ 1  Add RetryPolicy"), "view:\n%s", view)
}

func TestNarrowScreenShowsOnePaneAtATime(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV, store.UnitsV = planWithUnits, planWithUnitsRead
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 70, Height: 30})
	m = refreshed(t, m)
	view := ansi.Strip(m.Content())
	if !regexp.MustCompile(`New plan +Plan · 2 units`).MatchString(view) {
		t.Errorf("no tab strip with the new plan:\n%s", view)
	}
	assertFits(t, m, 70, 30)

	m.input.SetValue("plan it")
	m = startPlan(t, m, "Plan")
	view = ansi.Strip(m.Content())
	if !regexp.MustCompile(`Overview +Plan · 2 units`).MatchString(view) {
		t.Errorf("no tab strip with the Overview:\n%s", view)
	}
	if strings.Contains(view, "Which timeout caps the wait?") {
		t.Errorf("the plan shows beside the Overview on a narrow screen:\n%s", view)
	}
	assertFits(t, m, 70, 30)

	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "ACTIVITY"))

	// tab goes between the Overview and the plan, and no further.
	m = drive(t, m, press("tab"))
	view = ansi.Strip(m.Content())
	if !strings.Contains(view, "Which timeout caps the wait?") || strings.Contains(view, "ACTIVITY") {
		t.Errorf("tab did not bring the plan up in place of the Overview:\n%s", view)
	}
	assert.Check(t, cmp.Equal(m.focus, panePlan))
	assertFits(t, m, 70, 30)

	m = drive(t, m, press("tab"))
	view = ansi.Strip(m.Content())
	if !strings.Contains(view, "ACTIVITY") || strings.Contains(view, "Which timeout caps the wait?") {
		t.Errorf("a second tab did not bring the Overview back:\n%s", view)
	}
	assert.Check(t, cmp.Equal(m.focus, paneOverview))
	assertFits(t, m, 70, 30)
}

func TestNewPlanShowsASkeletonWhileItIsCreated(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = m.send("Plan", "plan it")
	assert.Assert(t, m.creating)
	view := ansi.Strip(m.Content())
	for _, want := range []string{"Creating", "███"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
}

// While a comment is written enter saves it, so the bar does not offer enter
// as the key that sends comments to the factory.
func TestActionBarOffersNoButtonWhileACommentIsWritten(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 110, Height: 30})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 0, quote: "## 1. Outcome", text: "x"}}
	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), newFakeStore(), "")
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
	m := New(context.Background(), newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{
		{block: 3, quote: "* [AGENT] parse it in main.go", text: "first"},
		{block: 1, quote: "The flag exists.", text: "second"},
	}
	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m.comments = []comment{{block: 1, quote: "The flag exists.", text: "x"}}
	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), newFakeStore(), "").WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), newFakeStore(), "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	setPlan(&m, twoParts)
	m.renderPlan()
	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), newFakeStore(), "")
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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = refreshed(t, m)

	view := ansi.Strip(m.Content())
	if !strings.Contains(view, "Done.") {
		t.Errorf("plan text not shown:\n%s", view)
	}
	if lines := strings.Split(view, "\n"); !strings.Contains(lines[headerRows], "Planning") {
		t.Errorf("strip does not show the stage:\n%s", view)
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
	m := New(context.Background(), store, "")
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
	m := New(context.Background(), store, "")
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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), store, "")
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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), store, "")
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
	m := New(context.Background(), store, "")
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
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startPlan(t, m, "Plan")

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
	m = drive(t, m, press("s"))

	if len(store.Added) != 2 || store.Added[0] != [2]string{"The flag exists.", "first"} ||
		store.Added[1] != [2]string{"* [AGENT] parse it in main.go", "second"} {
		t.Fatalf("added: %+v", store.Added)
	}
	if len(store.HandedOff) != 1 || store.HandedOff[0] != "refine-proposal" {
		t.Fatalf("handed off: %+v", store.HandedOff)
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
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startPlan(t, m, "Plan")

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "first")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("tab"))
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "1 queued · s to send") {
		t.Errorf("the Overview does not say how comments are sent:\n%s", view)
	}
	m = drive(t, m, press("s"))

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
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startPlan(t, m, "Plan")

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
	assert.Check(t, !m.keys().SendComments.Enabled(), "s sends comments from the Overview outside a Human state")
	m = drive(t, m, press("s"))

	assert.Check(t, cmp.Len(store.Added, 0))
	assert.Check(t, cmp.Len(store.HandedOff, 0))
	assert.Check(t, cmp.Len(queuedComments(m.comments), 1))
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "1 queued · s to send"), "the comments are not shown waiting")
}

// TestHeldCommentsSendOnceThePlanWaitsOnTheDeveloper checks that comments
// held while factory worked are saved and handed off by the first send once
// the plan is in a Human state.
func TestHeldCommentsSendOnceThePlanWaitsOnTheDeveloper(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.StateV, store.FlowV.WaitingOn = "refine-units", string(ActorSession)
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startPlan(t, m, "Plan")

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
	store.setEvents("end")
	m = drive(t, m, pollMsg{})
	m = drive(t, m, press("tab")) // back to the plan pane, where s works
	assert.Check(t, m.moving == nil, "the wait outlived the task going out")
	assert.Check(t, !m.keys().SendComments.Enabled(), "s offered while a task is out")

	// The task is submitted: factory waits on the developer, and the held
	// comment goes out.
	store.setTask("")
	store.setWaitingOn("human")
	store.setEvents("address-comments", "approve-for-units", "end")
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
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startPlan(t, m, "Plan")

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
			m := New(context.Background(), store, "plan it")
			m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
			m = startPlan(t, m, "Plan")

			m = drive(t, m, press("tab"))
			m = drive(t, m, press("down"))
			m = drive(t, m, press("c"))
			m = typeText(t, m, "first")
			m = drive(t, m, press("enter"))
			m = drive(t, m, press("tab"))
			m = drive(t, m, press("s"))

			if len(store.Added) != 1 {
				t.Fatalf("comment not saved: %+v", store.Added)
			}
			assert.Check(t, cmp.Len(store.HandedOff, 0))
			assert.Check(t, cmp.Len(store.Answered, 0))
			if len(queuedComments(m.comments)) != 0 {
				t.Fatalf("the saved comment should not stay queued: %+v", m.comments)
			}
			assert.Check(t, m.moving == nil, "a failed event started the wait")

			store.AddressErr = nil
			store.setEvents(tc.retry...)
			// The retry reads m.events: poll so it sees tc.retry.
			m = drive(t, m, pollMsg{})
			m = drive(t, m, press("s"))

			assert.Check(t, cmp.DeepEqual(store.HandedOff, tc.handedOff))
			assert.Check(t, cmp.DeepEqual(store.Answered, tc.answered))
			if len(store.Added) != 1 {
				t.Fatalf("the already-saved comment was saved again: %+v", store.Added)
			}
			assert.Check(t, m.moving != nil)
		})
	}
}

func TestSaveFailureKeepsRestQueued(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.FailAddAfter = 1
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startPlan(t, m, "Plan")

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
	m = drive(t, m, press("s"))

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
}

func TestEditingSavedCommentPutsBody(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	c, _ := store.AddComment(context.Background(), "The flag exists.", "orig")
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)
	if len(m.comments) != 1 || m.comments[0].id == "" {
		t.Fatalf("comments after refresh: %+v", m.comments)
	}

	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	// Merge the saved comment (block 0) with block 1 into a queued one.
	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m, _ = m.focusOn(panePlan)
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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	m, _ = m.focusOn(panePlan)
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
				events:    []string{"address-comments", "end"},
				handedOff: []string{state},
			},
			{
				name:     "answer-open-questions",
				events:   []string{"answer-open-questions", "end"},
				answered: []string{state},
			},
			{
				name:     "both",
				events:   []string{"answer-open-questions", "address-comments", "end"},
				answered: []string{state},
			},
		} {
			t.Run(state+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				store := newFakeStore()
				store.TextV = twoParts
				store.StateV = state
				store.setEvents(tc.events...)
				m := New(context.Background(), store, "plan it")
				m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
				m = startPlan(t, m, "Plan")

				m = drive(t, m, press("tab"))
				m = drive(t, m, press("down"))
				m = drive(t, m, press("c"))
				m = typeText(t, m, "first")
				m = drive(t, m, press("enter"))
				m = drive(t, m, press("tab"))
				assert.Check(t, m.keys().SendComments.Enabled(), "s in the Overview does not send the comments")
				drive(t, m, press("s"))

				assert.Check(t, cmp.Len(store.Added, 1))
				assert.Check(t, cmp.DeepEqual(store.HandedOff, tc.handedOff))
				assert.Check(t, cmp.DeepEqual(store.Answered, tc.answered))
			})
		}
	}
}

// TestSecondSendWhileSavingCommentsDoesNothing checks that a second enter in
// the Overview while queued comments are still being saved does not start a
// second save.
func TestSecondSendWhileSavingCommentsDoesNothing(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	m := New(context.Background(), store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startPlan(t, m, "Plan")

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

func TestFailedCreateShowsInThePaneAndIsTriedAgain(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.CreateErr = errors.New("factory said no")
	m := New(context.Background(), store, "add a --dry-run flag").WithInbox("New plan")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = startPlan(t, m, "Add a flag")

	if view := ansi.Strip(m.Content()); !strings.Contains(view, "Could not create the plan: factory said no") {
		t.Errorf("the failure is not in the pane:\n%s", view)
	}
	if m.creating {
		t.Error("still creating after the create failed")
	}
	assert.Check(t, cmp.Contains(ansi.Strip(m.header()), "New plan"))

	store.mu.Lock()
	store.CreateErr = nil
	store.mu.Unlock()
	m = drive(t, m, press("enter"))
	assert.Check(t, cmp.Contains(ansi.Strip(m.header()), "chunk plan / Add a flag"))
}

// Once factory has created the plan, the title the host gave the screen is
// replaced with the title the developer typed; a screen with no title keeps the
// branch, and a plan that was not made keeps the host's title.
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
			host:    func(m Model) Model { return m.WithInbox("New plan") },
			width:   120,
			title:   "replace-header-plan",
			want:    "chunk plan / replace-header-plan",
			notWant: "New plan",
		},
		{
			name:  "a long title is cut down",
			host:  func(m Model) Model { return m.WithInbox("New plan") },
			width: 60, // narrow enough that the header cuts the title down
			title: "a-very-long-plan-title-indeed",
			want:  "chunk plan / a-very-long-p…",
		},
		{
			name:      "a failed create keeps New plan",
			host:      func(m Model) Model { return m.WithInbox("New plan") },
			width:     120,
			title:     "replace-header-plan",
			createErr: errors.New("factory said no"),
			want:      "chunk plan / New plan",
			notWant:   "replace-header-plan",
		},
		{
			name:  "no title keeps New plan",
			host:  func(m Model) Model { return m.WithInbox("New plan") },
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
			store.CreateErr = tc.createErr
			m := tc.host(New(context.Background(), store, "replace the header"))
			m = drive(t, m, tea.WindowSizeMsg{Width: tc.width, Height: 40})
			m = startPlan(t, m, tc.title)

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
func TestEnterWhileThePlanIsCreatedWaitsInTheInput(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	m := New(context.Background(), store, "first")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.titleInput.SetValue("Title")
	m.focusField(false)
	m, create := m.Update(press("enter"))
	if !m.creating {
		t.Fatal("the plan is not being created")
	}

	m, cmd := m.Update(press("enter"))
	assert.Check(t, cmd == nil, "a second enter while the plan is created did something")

	for _, msg := range run(t, create) {
		m = drive(t, m, msg)
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

// q leaves from the plan and the Overview, where nothing is being
// typed, and is only a letter in a new plan's request.
func TestQQuitsFromThePlanOnly(t *testing.T) {
	t.Parallel()
	// The request's text box is shared by copies of the model, so each part
	// starts from a screen of its own.
	request := New(context.Background(), newFakeStore(), "")
	request = drive(t, request, tea.WindowSizeMsg{Width: 140, Height: 40})
	request, cmd := request.Update(press("q"))
	if _, ok := closedBy(cmd); ok {
		t.Error("q in the title quit")
	}
	if got := request.titleInput.Value(); got != "q" {
		t.Errorf("q in the title typed %q, want q", got)
	}
	request = drive(t, request, press("tab"))
	request, cmd = request.Update(press("q"))
	if _, ok := closedBy(cmd); ok {
		t.Error("q in the request quit")
	}
	if got := request.input.Value(); got != "q" {
		t.Errorf("q in the request typed %q, want q", got)
	}

	activity := idlePlan(t)
	assert.Assert(t, cmp.Equal(activity.focus, paneOverview))
	activity, cmd = activity.Update(press("q"))
	if _, ok := closedBy(cmd); !ok {
		t.Error("q in the Overview did not quit")
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

// flowModel is a screen of 140×40 on store, a plan that exists, refreshed once,
// so the first refresh has put the keyboard where the plan's phase wants it.
func flowModel(t *testing.T, store *fakeStore) Model {
	t.Helper()
	store.Existing = true
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	return refreshed(t, m)
}

// threeUnitsRead are a plan's three units, the first done, as factory reads them.
var threeUnitsRead = []Unit{{Name: "Unit 1: lexer", Done: true}, {Name: "Unit 2: parser"}, {Name: "Unit 3: printer"}}

func TestScreenOpensOnThePaneForItsPhase(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		state string
		want  pane
	}{
		{
			name:  "planning",
			state: "refine-proposal",
			want:  panePlan,
		},
		{
			name:  "refine-units",
			state: "refine-units",
			want:  panePlan,
		},
		{
			name:  "ready",
			state: "ready",
			want:  panePlan,
		},
		{
			name:  "unprioritized",
			state: "init",
			want:  panePlan,
		},
		{
			name: "no state",
			want: panePlan,
		},
		{
			name:  "stand-by",
			state: "stand-by",
			want:  paneOverview,
		},
		{
			name:  "building",
			state: "build",
			want:  paneOverview,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.StateV, store.TextV = tc.state, twoParts
			m := flowModel(t, store)
			assert.Check(t, cmp.Equal(m.focus, tc.want))

			// A later move leaves the focus where it is.
			store.mu.Lock()
			store.StateV = "integrate"
			if tc.want == paneOverview {
				store.StateV = "refine-proposal"
			}
			store.mu.Unlock()
			m = refreshed(t, m)
			assert.Check(t, cmp.Equal(m.focus, tc.want), "a state change moved the keyboard")
		})
	}
}

func TestOverviewKeys(t *testing.T) {
	t.Parallel()
	t.Run("in the request", func(t *testing.T) {
		t.Parallel()
		m := New(context.Background(), newFakeStore(), "")
		m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
		m = refreshed(t, m)
		m = drive(t, m, press("tab")) // to the request
		m = typeText(t, m, "fp")
		m = drive(t, m, press("left"))
		m = drive(t, m, press("left"))
		m = drive(t, m, press("right"))
		m = typeText(t, m, "x")
		assert.Check(t, cmp.Equal(m.input.Value(), "fxp"), "← and → in the request move the cursor")
		assert.Check(t, cmp.Equal(m.focus, paneOverview))
	})

	// A queued comment on the plan, for the Overview's keys to leave alone.
	m, store := planModel(t)
	m = refreshed(t, m)
	assert.Assert(t, cmp.Equal(m.focus, paneOverview), "a new plan keeps the keyboard where the request was typed")
	m = drive(t, m, press("tab"))
	assert.Assert(t, cmp.Equal(m.focus, panePlan))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))
	assert.Assert(t, cmp.Len(queuedComments(m.comments), 1))

	m = drive(t, m, press("tab")) // to the Overview
	assert.Check(t, cmp.Equal(m.focus, paneOverview))
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "ACTIVITY"))
	assert.Check(t, cmp.Contains(view, "1 queued · s to send"))
	m = drive(t, m, press("enter")) // expands the current stage; sends nothing
	store.mu.Lock()
	assert.Check(t, cmp.Len(store.Added, 0))
	store.mu.Unlock()

	// The plan's keys belong to the plan.
	for _, k := range []string{"e", "d", "r", "n", "c"} {
		m = drive(t, m, press(k))
	}
	assert.Check(t, !m.commenting, "a key in the Overview opened the comment editor")
	assert.Check(t, !m.editing && !m.confirming)
	assert.Check(t, cmp.Len(queuedComments(m.comments), 1))
	store.mu.Lock()
	assert.Check(t, cmp.Len(store.Added, 0))
	assert.Check(t, cmp.Len(store.Resolved, 0))
	store.mu.Unlock()
	// The arrows do not switch panes: ← and → do nothing here.
	m = drive(t, m, press("left"))
	m = drive(t, m, press("right"))
	assert.Check(t, cmp.Equal(m.focus, paneOverview))

	// s sends the queued comments from the Overview, as it does in the plan.
	m = drive(t, m, press("s"))
	store.mu.Lock()
	assert.Check(t, cmp.Len(store.Added, 1))
	store.mu.Unlock()
	assert.Check(t, cmp.Len(queuedComments(m.comments), 0))

	m = drive(t, m, press("tab")) // back to the plan
	assert.Check(t, cmp.Equal(m.focus, panePlan))
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "The flag exists."))

	// While a comment is written the arrows move its cursor. The block under
	// the cursor has the comment, so move to the next one first.
	m = drive(t, m, press("down"))
	m = drive(t, m, press("c"))
	assert.Assert(t, m.commenting)
	assert.Assert(t, cmp.Equal(m.commentInput.Value(), ""))
	m = typeText(t, m, "ab")
	m = drive(t, m, press("left"))
	m = typeText(t, m, "x")
	assert.Check(t, cmp.Equal(m.commentInput.Value(), "axb"))
	m = drive(t, m, press("right"))
	assert.Check(t, cmp.Equal(m.focus, panePlan), "an arrow in the comment editor moved the keyboard")
}

// overviewOf is m's Overview as overviewContent draws it, w cells wide.
func overviewOf(m Model, w int) string {
	content, _ := m.overviewContent(w)
	return content
}

func TestOverviewShowsProgress(t *testing.T) {
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
			want:    []string{"SUB-FLOWS", "▸ Lex it  build", "○ Print it  refine-proposal"},
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
			without: []string{"deviation", "findings ·"},
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
			want:   []string{"Waiting on 1 sub-plan", "SUB-FLOWS", "▸ Lex it"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.StateV, store.UnitsV, store.TextV = "build", tc.units, twoParts
			store.FlowV.Progress = tc.flow.Progress
			store.FlowV.WaitingOn, store.FlowV.WaitingOnWhy = tc.flow.WaitingOn, tc.flow.WaitingOnWhy
			store.FlowV.Unit, store.FamilyV = tc.unit, tc.family
			m := flowModel(t, store)
			assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "ACTIVITY"))
			// The Overview alone, wide enough that no line is cut: the rest of
			// the screen has its own "·".
			view := ansi.Strip(overviewOf(m, 200))
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

// TestOverviewPRIsAHyperlink checks that the pull request's URL is sent as a
// terminal hyperlink when it is http(s), and as plain text when it is not.
func TestOverviewPRIsAHyperlink(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		url  string
		link bool
	}{
		{
			name: "https",
			url:  "https://github.com/o/r/pull/7",
			link: true,
		},
		{
			name: "not http",
			url:  "javascript:alert(1)",
			link: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.StateV, store.UnitsV = "Integrating - Agent", threeUnitsRead
			store.FlowV.PR = tc.url
			raw := overviewOf(flowModel(t, store), 200)
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
		raw := overviewOf(flowModel(t, store), 200)
		assert.Check(t, !strings.Contains(raw, "\x1b]0;"), "no injected escape")
	})
}

func TestOverviewShowsThePR(t *testing.T) {
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

func TestOverviewTimeline(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		state   string
		ended   bool
		outcome string
		flow    Flow
		want    []string
		without []string
	}{
		{
			name:    "planning",
			state:   "refine-proposal",
			want:    []string{"◌ Up next: Building", "◉ Planning", "✓ Created"},
			without: []string{"Planned"},
		},
		{
			name:  "building",
			state: "build",
			want:  []string{"◌ Up next: Integrating", "◉ Building", "✓ Planned", "✓ Created"},
		},
		{
			name:    "stand-by is planning",
			state:   "stand-by",
			want:    []string{"◉ Planning"},
			without: []string{"Standing by"},
		},
		{
			name:    "ended merged",
			state:   "ended",
			ended:   true,
			outcome: "merged",
			want:    []string{"✓ Done · merged", "✓ Integrated"},
			without: []string{"Up next"},
		},
		{
			name:    "unknown state",
			state:   "Integrating - Agent",
			flow:    Flow{WaitingOn: "session", WaitingOnWhy: "the agent works"},
			want:    []string{"Waiting on session: the agent works"},
			without: []string{"◉", "Up next"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.StateV, store.TextV = tc.state, twoParts
			store.FlowV.Ended, store.FlowV.Outcome = tc.ended, tc.outcome
			store.FlowV.WaitingOn, store.FlowV.WaitingOnWhy = tc.flow.WaitingOn, tc.flow.WaitingOnWhy
			view := ansi.Strip(overviewOf(flowModel(t, store), 80))
			at := -1
			for _, w := range tc.want {
				i := strings.Index(view, w)
				assert.Check(t, i >= 0, "%q is missing:\n%s", w, view)
				assert.Check(t, i > at, "%q is out of order:\n%s", w, view)
				at = max(at, i)
			}
			for _, w := range tc.without {
				assert.Check(t, !strings.Contains(view, w), "%q shows:\n%s", w, view)
			}
		})
	}
}

// under reports whether text is drawn after the row that starts with head and
// before the next row of a stage.
func under(view, head, text string) bool {
	i := strings.Index(view, head)
	if i < 0 {
		return false
	}
	rest := view[i+len(head):]
	if next := strings.IndexAny(rest, "◉✓"); next >= 0 {
		rest = rest[:next]
	}
	return strings.Contains(rest, text)
}

func TestOverviewLogsEachStage(t *testing.T) {
	t.Parallel()
	view := func(m Model) string { return ansi.Strip(overviewOf(m, 80)) }
	const live = "scotts-mbp · architect running"
	// planning is a new plan, sent and claimed, whose flow has reached Planning.
	planning := func(t *testing.T) (Model, *fakeStore) {
		t.Helper()
		store := newFakeStore()
		store.ClaimHost, store.StateV = "scotts-mbp", ""
		m := New(context.Background(), store, "add a flag")
		m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
		m = startPlan(t, m, "Add a flag")
		// The plan has no state yet: its request is in Created, the current stage.
		assert.Check(t, under(view(m), "◉ Created", "you  add a flag"), view(m))
		assert.Check(t, under(view(m), "◉ Created", live), view(m))

		store.mu.Lock()
		store.StateV = "refine-proposal"
		store.mu.Unlock()
		m = refreshed(t, m)
		assert.Check(t, under(view(m), "◉ Planning", live), view(m))
		assert.Check(t, !under(view(m), "✓ Created", live), view(m))
		return m, store
	}
	advance := func(t *testing.T, m Model, store *fakeStore, state string) Model {
		t.Helper()
		store.mu.Lock()
		store.StateV = state
		store.mu.Unlock()
		return refreshed(t, m)
	}

	t.Run("a finished stage is a line, and enter opens it", func(t *testing.T) {
		t.Parallel()
		m, store := planning(t)
		m = advance(t, m, store, "build")
		assert.Check(t, cmp.Contains(view(m), "✓ Planned"))
		assert.Check(t, !strings.Contains(view(m), "reply"), view(m))
		assert.Check(t, cmp.Contains(view(m), `✓ Created · "add a flag"`))
		assert.Check(t, under(view(m), "◉ Building", live), view(m))

		assert.Assert(t, cmp.Equal(m.focus, paneOverview), "a new plan keeps the keyboard in the Overview")
		assert.Check(t, !under(view(m), "✓ Created", "you  add a flag"), view(m))
		m = cursorTo(t, m, "Created")
		m = drive(t, m, press("enter"))
		assert.Check(t, under(view(m), "✓ Created", "you  add a flag"), view(m))
		m = drive(t, m, press("enter"))
		assert.Check(t, !under(view(m), "✓ Created", "you  add a flag"), view(m))
	})

	t.Run("the cursor stays on the stage it was on", func(t *testing.T) {
		t.Parallel()
		m, store := planning(t)
		m = cursorTo(t, m, "◉ Planning")
		m = drive(t, m, press("enter"))
		assert.Assert(t, m.expanded["Planning"])
		m = advance(t, m, store, "build")
		assert.Check(t, cmp.Contains(ansi.Strip(overviewOf(m, 80)), "▶ ✓ Planned"), view(m))
		assert.Check(t, m.expanded["Planning"], "the row did not stay open")
	})

	t.Run("the worker's activity is the current stage's only", func(t *testing.T) {
		t.Parallel()
		m, store := planning(t)
		m = advance(t, m, store, "build")
		assert.Check(t, !under(view(m), "✓ Planned", live), view(m))
		assert.Check(t, under(view(m), "◉ Building", live), view(m))
		store.mu.Lock()
		store.FlowV.Ended = true
		store.mu.Unlock()
		m = refreshed(t, m)
		assert.Check(t, !strings.Contains(view(m), live), "an ended plan still shows its worker's activity:\n%s", view(m))
	})

	t.Run("a state the stages do not know", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.StateV = "Integrating - Agent"
		store.FlowV.Worker = &Worker{ID: "w1", Hostname: "scotts-mbp", Live: true}
		m := flowModel(t, store)
		assert.Check(t, cmp.Contains(view(m), "scotts-mbp · no headless runner"))
		assert.Check(t, !strings.Contains(view(m), "◉"), view(m))
	})

	t.Run("the current stage shows the newest of its log", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name  string
			lines int
			cut   bool
		}{
			{
				name:  "six lines: none cut",
				lines: 6,
			},
			{
				name:  "seven lines: the last six",
				lines: 7,
				cut:   true,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				// The log is the request, a blank line, and a failure of
				// the first create, which is a line of its own and the lines
				// of its text.
				const fixed = 3
				var parts []string
				for i := range tc.lines - fixed {
					parts = append(parts, fmt.Sprintf("because %d", i))
				}
				store := newFakeStore()
				store.StateV = ""
				store.CreateErr = errors.New(strings.Join(parts, "\n"))
				m := New(context.Background(), store, "add a flag")
				m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
				m = startPlan(t, m, "Add a flag")
				store.mu.Lock()
				store.CreateErr = nil
				store.mu.Unlock()
				m = drive(t, m, press("enter"))
				assert.Assert(t, m.created)
				assert.Assert(t, cmp.Len(m.stageLog("Created", 75), tc.lines), "%q", m.stageLog("Created", 75))

				// Newest first: the request is above the failure it followed.
				if !tc.cut {
					assert.Check(t, cmp.Contains(view(m), "you  add a flag"))
					assert.Check(t, strings.Index(view(m), "you  add a flag") < strings.Index(view(m), "because 0"), view(m))
					assert.Check(t, !strings.Contains(view(m), "enter for the full log"), view(m))
					return
				}
				// The oldest line is cut, and the hint is under the newest six.
				last := fmt.Sprintf("because %d", tc.lines-fixed-1)
				assert.Check(t, cmp.Contains(view(m), "you  add a flag"), view(m))
				assert.Check(t, !strings.Contains(view(m), last), view(m))
				assert.Check(t, strings.Index(view(m), "because 0") < strings.Index(view(m), "… enter for the full log"), view(m))
				m = drive(t, m, press("enter"))
				assert.Check(t, cmp.Contains(view(m), last))
				assert.Check(t, !strings.Contains(view(m), "enter for the full log"), view(m))
			})
		}
	})
}

func TestOverviewOutcome(t *testing.T) {
	t.Parallel()
	const note = "No outcome yet · → plan"
	long := strings.Repeat("a long paragraph that wraps ", 4)
	for _, tc := range []struct {
		name string
		text string
		// lines is how many lines the Outcome renders to at the width tested.
		lines int
		// cut is whether the Outcome shows its first six lines and the hint to
		// show the rest.
		cut bool
	}{
		{
			name:  "3 lines: all shown",
			text:  "## 1. Outcome\n\nfirst\n\nsecond",
			lines: 3,
		},
		{
			name:  "7 lines: all shown, no cut",
			text:  "## 1. Outcome\n\nfirst\n\nsecond\n\nthird\n\nfourth",
			lines: 7,
		},
		{
			name:  "8 lines: cut to 6",
			text:  "## 1. Outcome\n\nfirst\n\nsecond\n\nthird\n\n" + long,
			lines: 8,
			cut:   true,
		},
		{
			name: "no blocks",
			text: "## 1. Outcome\n\n## 2. Design\n\nparse it",
		},
		{
			name: "no section",
			text: "## 2. Design\n\nparse it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.StateV, store.TextV = "refine-proposal", tc.text
			m := flowModel(t, store)
			assert.Assert(t, cmp.Equal(m.focus, panePlan))
			m = drive(t, m, press("tab"))
			assert.Assert(t, cmp.Equal(m.focus, paneOverview))
			assert.Assert(t, cmp.Len(m.outcomeLines(78), tc.lines))
			m = drive(t, m, press("up")) // to the Outcome row, when there is one

			before := ansi.Strip(overviewOf(m, 80))
			assert.Check(t, cmp.Equal(strings.Contains(before, note), tc.lines == 0), before)
			assert.Check(t, cmp.Equal(strings.Contains(before, "… enter to show all"), tc.cut), before)
			if tc.cut {
				// Six lines of the eight: the paragraph that wraps is cut off.
				assert.Check(t, !strings.Contains(before, "a long paragraph"), before)
			}

			m = drive(t, m, press("enter"))
			after := ansi.Strip(overviewOf(m, 80))
			if !tc.cut {
				assert.Check(t, cmp.Equal(after, before), "enter changed an Outcome that has no more to show")
				return
			}
			assert.Check(t, !strings.Contains(after, "enter to show all"), after)
			assert.Check(t, cmp.Contains(after, "a long paragraph"))
			m = drive(t, m, press("enter"))
			assert.Check(t, cmp.Equal(ansi.Strip(overviewOf(m, 80)), before), "enter did not close it again")
		})
	}
}

// The Outcome is the plan's text, so it sends the terminal only the links the
// plan itself would: http and https.
func TestOverviewOutcomeDrawsOnlySafeLinks(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.StateV = "build"
	store.TextV = "## 1. Outcome\n\n[docs](https://example.com/x) and [passwd](file:///etc/passwd)"
	raw := overviewOf(flowModel(t, store), 80)
	links := hyperlinkRe.FindAllStringSubmatch(raw, -1)
	assert.Assert(t, len(links) > 0, "the https link was not drawn as a link")
	for _, l := range links {
		assert.Check(t, l[2] == "" || l[2] == "https://example.com/x", "a link to %q was drawn", l[2])
	}
	assert.Check(t, cmp.Contains(ansi.Strip(raw), "passwd"), "the text of the link is gone")
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

// cursorTo moves the Overview's cursor with the arrow keys, as the developer
// would, until it is on the row that shows text.
func cursorTo(t *testing.T, m Model, text string) Model {
	t.Helper()
	onRow := func(m Model) string {
		for _, l := range strings.Split(ansi.Strip(overviewOf(m, 80)), "\n") {
			if strings.HasPrefix(l, "▶ ") {
				return l
			}
		}
		return ""
	}
	for range m.overviewRows() {
		m = drive(t, m, press("up"))
	}
	for range m.overviewRows() {
		if strings.Contains(onRow(m), text) {
			return m
		}
		m = drive(t, m, press("down"))
	}
	t.Fatalf("the cursor never reached %q; it ends on %q", text, onRow(m))
	return m
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
		store.Existing = true
		store.StateV, store.UnitsV, store.FamilyV = "build", threeUnitsRead, fam
		m := New(context.Background(), store, "").WithInbox("Plan")
		m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
		// An existing plan in build opens on the Overview.
		return refreshed(t, m), store
	}

	t.Run("enter opens the row under the cursor", func(t *testing.T) {
		t.Parallel()
		m, _ := inbox(t, family)
		view := ansi.Strip(m.Content())
		for _, w := range []string{"RELATED", "Part of", "Parent plan", "SUB-FLOWS", "Child plan", "Blocked by", "Blocker plan", "integrate"} {
			assert.Check(t, cmp.Contains(view, w))
		}
		assert.Check(t, !strings.Contains(view, "Sub-plan"), "the sub-plan is under Sub-flows only:\n%s", view)
		assert.Check(t, cmp.Contains(view, "▶ ◉ Building"), "the cursor starts on the current stage")

		m = cursorTo(t, m, "Blocked by")
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "▶ Blocked by"))
		assert.Check(t, cmp.Equal(m.keys().Open.Help().Desc, "open plan"))
		_, msg := enterOn(m)
		assert.Check(t, cmp.DeepEqual(msg, OpenMsg{ID: "b1", Title: "Blocker plan"}))

		m = cursorTo(t, m, "Part of")
		_, msg = enterOn(m)
		assert.Check(t, cmp.DeepEqual(msg, OpenMsg{ID: "p1", Title: "Parent plan"}))
	})

	t.Run("enter opens a sub-flow", func(t *testing.T) {
		t.Parallel()
		m, _ := inbox(t, family)
		m = cursorTo(t, m, "Child plan")
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "▶ ○ Child plan"))
		_, msg := enterOn(m)
		assert.Check(t, cmp.DeepEqual(msg, OpenMsg{ID: "c1", Title: "Child plan"}))
	})

	t.Run("a poll with fewer rows keeps the cursor on one", func(t *testing.T) {
		t.Parallel()
		m, store := inbox(t, family)
		m = cursorTo(t, m, "Blocked by")
		store.mu.Lock()
		store.FamilyV = Family{Parent: family.Parent}
		store.mu.Unlock()
		m = refreshed(t, m)
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "▶ Part of"))
		_, msg := enterOn(m)
		assert.Check(t, cmp.DeepEqual(msg, OpenMsg{ID: "p1", Title: "Parent plan"}))

		store.mu.Lock()
		store.FamilyV = Family{}
		store.mu.Unlock()
		m = refreshed(t, m)
		_, msg = enterOn(m)
		assert.Check(t, msg == nil, "enter on a stage with no log gave %#v", msg)
	})

	t.Run("a poll that moves the rows keeps the cursor on its row", func(t *testing.T) {
		t.Parallel()
		m, store := inbox(t, Family{Parent: family.Parent})
		m = cursorTo(t, m, "Part of")
		store.mu.Lock()
		store.FamilyV = family
		store.mu.Unlock()
		m = refreshed(t, m)
		// A sub-flow row now sits above the stages, and the cursor kept to
		// its row rather than its place.
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "▶ Part of"))
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
		m := flowModel(t, store)
		assert.Assert(t, cmp.Equal(m.focus, paneOverview))
		view := ansi.Strip(m.Content())
		assert.Check(t, cmp.Contains(view, "Parent plan"))
		assert.Check(t, cmp.Contains(view, "Child plan"))
		for _, row := range []string{"Parent plan", "Child plan"} {
			at := cursorTo(t, m, row)
			_, msg := enterOn(at)
			assert.Check(t, msg == nil, "enter on %q without an inbox gave %#v", row, msg)
			assert.Check(t, !at.keys().Open.Enabled(), "enter offered on %q without an inbox", row)
		}
	})
}

func TestFamilyErrorIsQuiet(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.TextV = twoParts
	store.FamilyErr = errors.New("boom")
	m := flowModel(t, store)
	assert.Check(t, cmp.Equal(m.refreshErr, ""))
	stripRow := func(m Model) string { return ansi.Strip(strings.Split(m.Content(), "\n")[headerRows]) }
	assert.Check(t, cmp.Contains(stripRow(m), "could not read related plans: boom"))

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
	assert.Check(t, !strings.Contains(stripRow(m), "could not read related plans"), stripRow(m))
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
		store.Existing = true
		store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
		store.StateV = "init"
		store.setWaitingOn("human")
		store.setEvents(events...)
		store.setRequired("prioritize")
		m := New(context.Background(), store, "")
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
		m, store := unprioritized(t, "prioritize", "end")
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
		m, store := unprioritized(t, "prioritize", "end")
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		m = drive(t, m, press("n"))
		assert.Check(t, !m.confirming)
		assert.Check(t, !m.pendingStart)
		m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
		assert.Check(t, cmp.Len(started(store), 0))
	})

	t.Run("not offered", func(t *testing.T) {
		t.Parallel()
		m, store := unprioritized(t, "end")
		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assert.Check(t, !m.confirming)
		assert.Check(t, cmp.Len(started(store), 0))
		assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "cannot be approved now: it is init"))
	})

	t.Run("a failed send says so", func(t *testing.T) {
		t.Parallel()
		m, store := unprioritized(t, "prioritize", "end")
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
	store.Existing = true
	store.TextV = twoParts
	store.StateV = state
	store.setEvents(events...)
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 170, Height: 40})
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

// TestEndThePlan checks that x, while the plan's flow has not ended, asks
// for a reason and ends the plan with the one picked, and that any other key
// keeps the plan.
func TestEndThePlan(t *testing.T) {
	t.Parallel()
	ended := func(store *fakeStore) []string {
		store.mu.Lock()
		defer store.mu.Unlock()
		return slices.Clone(store.Ended)
	}
	live := []string{"address-comments", "end"}
	for _, tc := range []struct {
		name   string
		state  string
		events []string
		ended  bool
		err    error
		check  func(t *testing.T, m Model, store *fakeStore)
	}{
		{
			name:  "offered for a held flow, with an empty next",
			state: "build",
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "x end plan"))
				m = pressAll(t, m, "x", "d")
				assert.Check(t, cmp.DeepEqual(ended(store), []string{"duplicate"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Ending: duplicate"))
			},
		},
		{
			name:  "not offered without a flow",
			state: "",
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, !strings.Contains(ansi.Strip(m.actionBar()), "end plan"))
				m = pressAll(t, m, "x")
				assert.Check(t, !m.askingReason)
				assert.Check(t, cmp.Len(ended(store), 0))
			},
		},
		{
			name:   "c ends as canceled",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, cmp.Contains(ansi.Strip(m.actionBar()), "x end plan"))
				m = pressAll(t, m, "x")
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "End plan as: m complete · c canceled · w won't do · d duplicate · esc keep it"))
				m = pressAll(t, m, "c")
				assert.Check(t, cmp.DeepEqual(ended(store), []string{"canceled"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Ending: canceled"))
			},
		},
		{
			name:   "m ends as complete",
			state:  "build",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "m")
				assert.Check(t, cmp.DeepEqual(ended(store), []string{"completed"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Ending: complete"))
			},
		},
		{
			name:   "w ends as won't do",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "w")
				assert.Check(t, cmp.DeepEqual(ended(store), []string{"wont-do"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Ending: won't do"))
			},
		},
		{
			name:   "d ends as duplicate",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "d")
				assert.Check(t, cmp.DeepEqual(ended(store), []string{"duplicate"}))
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Ending: duplicate"))
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
			name:   "a failed end says so",
			state:  "refine-proposal",
			events: live,
			err:    errors.New("factory is down"),
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x")
				m, cmd := m.Update(press("w"))
				assert.Assert(t, cmd != nil)
				m, _ = m.Update(cmd())
				assert.Check(t, !m.ending)
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Could not end the plan: factory is down"))
			},
		},
		{
			name:   "a refusal of the end says so",
			state:  "build",
			events: live,
			err:    errors.New("the plan has already ended"),
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x")
				m, cmd := m.Update(press("d"))
				assert.Assert(t, cmd != nil)
				m, _ = m.Update(cmd())
				assert.Check(t, !m.ending)
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Could not end the plan: the plan has already ended"))
			},
		},
		{
			name:  "not offered once the flow has ended",
			state: "ended",
			ended: true,
			check: func(t *testing.T, m Model, store *fakeStore) {
				assert.Check(t, !strings.Contains(ansi.Strip(m.actionBar()), "end plan"))
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
				assert.Check(t, cmp.Contains(ansi.Strip(m.status), "End plan as:"))
				m = pressAll(t, m, "w")
				assert.Check(t, cmp.DeepEqual(ended(store), []string{"wont-do"}))
			},
		},
		{
			name:   "x while ending asks nothing",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "w")
				assert.Assert(t, m.ending)
				m = pressAll(t, m, "x")
				assert.Check(t, !m.askingReason)
				assert.Check(t, cmp.Len(ended(store), 1))
			},
		},
		{
			name:   "an end sent refreshes the plan",
			state:  "refine-proposal",
			events: live,
			check: func(t *testing.T, m Model, store *fakeStore) {
				m = pressAll(t, m, "x", "w")
				m, cmd := m.Update(endedMsg{})
				assert.Check(t, !m.ending)
				assert.Check(t, !strings.Contains(ansi.Strip(m.status), "Ending"))
				assert.Check(t, cmd != nil, "no refresh after the end")
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
			host:  func(m Model) Model { return m.WithInbox("A plan") },
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

// resumeScreen is resumeModel's screen, refreshed again once setup, when not
// nil, has changed the store.
func resumeScreen(t *testing.T, state string, next []Step, setup func(*fakeStore)) (Model, *fakeStore) {
	t.Helper()
	m, store := resumeModel(t, state, next)
	if setup != nil {
		store.mu.Lock()
		setup(store)
		store.mu.Unlock()
	}
	return refreshed(t, m), store
}

// liveWorker is a worker that holds a plan and reports an architect running.
var liveWorker = &Worker{
	ID: "w1", Hostname: "scotts-mbp", Live: true,
	Headless: []Headless{{Role: "architect", State: "running", Health: "ok"}},
}

// offersResume is whether the footer's hints, given all the room they want,
// offer R.
func offersResume(m Model) bool {
	return strings.Contains(ansi.Strip(theme.Help(theme.New(m.dark), 1000).View(m.keys())), "R claim")
}

// offersStop is whether the footer's hints, given all the room they want,
// offer S.
func offersStop(m Model) bool {
	return strings.Contains(ansi.Strip(theme.Help(theme.New(m.dark), 1000).View(m.keys())), "S stop")
}

// flowLines is the Overview's text, line by line, without styles and without
// the cursor column.
func flowLines(m Model) []string {
	lines := strings.Split(ansi.Strip(overviewOf(m, 80)), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, strings.Repeat(" ", overviewGutter))
	}
	return lines
}

// TestOverviewSaysWhetherItResumes checks the Overview's first line, which
// says what R does or why it does nothing, and that the footer offers R only
// when it does something.
func TestOverviewSaysWhetherItResumes(t *testing.T) {
	t.Parallel()
	const claim = "No worker holds this plan · R claims it for a worker"
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
			line:    claim,
			offered: true,
		},
		{
			name:    "a plan being written nobody holds",
			state:   "refine-units",
			next:    sessionStep,
			line:    claim,
			offered: true,
		},
		{
			name:    "an init plan nobody holds",
			state:   "init",
			next:    sessionStep,
			line:    claim,
			offered: true,
		},
		{
			name:  "claimed by a session",
			state: "build",
			next:  sessionStep,
			setup: func(s *fakeStore) {
				s.FlowV.Claimed = true
				s.FlowV.Holder = "bb5a623b-8dd3-4ae8-9c91-48b4eaaf6d62"
			},
			line: "Claimed by session bb5a623b",
		},
		{
			name:  "claimed by a session while planning",
			state: "refine-units",
			next:  sessionStep,
			setup: func(s *fakeStore) {
				s.FlowV.Claimed = true
				s.FlowV.Holder = "s-1"
			},
			line: "Claimed by session s-1",
		},
		{
			name:    "waiting on the developer",
			state:   "integrate",
			line:    "Waiting on you · R claims it for a worker",
			offered: true,
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
			m, _ := resumeScreen(t, tc.state, tc.next, tc.setup)
			lines := flowLines(m)
			if tc.line == "" {
				assert.Check(t, strings.HasPrefix(lines[0], "OUTCOME"), "the Overview starts %q", lines[0])
			} else {
				assert.Assert(t, len(lines) > 2)
				assert.Check(t, cmp.Equal(lines[0], tc.line))
				assert.Check(t, cmp.Equal(lines[1], ""))
				assert.Check(t, strings.HasPrefix(lines[2], "OUTCOME"), "the Overview goes on %q", lines[2])
			}
			assert.Check(t, cmp.Equal(offersResume(m), tc.offered))
		})
	}
}

// TestRClaimsThePlan checks that R on a plan nobody holds has the default
// worker claim it, and that the flow then names the worker.
func TestRClaimsThePlan(t *testing.T) {
	t.Parallel()
	m, store := resumeScreen(t, "refine-units", sessionStep, func(s *fakeStore) { s.ClaimHost = "scotts-mbp" })
	assert.Assert(t, offersResume(m))
	assert.Check(t, cmp.Equal(store.Claims, 0), "a refresh claimed the plan on its own")

	m, cmd := m.Update(press("R"))
	assert.Check(t, m.claiming)
	assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Claiming the plan for a worker…"))
	assert.Check(t, !offersResume(m), "R offered while the claim is under way")
	m, none := m.Update(press("R"))
	assert.Check(t, none == nil)
	for _, msg := range run(t, cmd) {
		m = drive(t, m, msg)
	}
	assert.Check(t, cmp.Equal(store.Claims, 1))
	assert.Check(t, !m.claiming)
	assert.Check(t, cmp.Equal(m.workerName, "scotts-mbp"))
	assert.Assert(t, m.flow.Worker != nil)
	assert.Check(t, cmp.Equal(m.flow.Worker.Hostname, "scotts-mbp"))
	assert.Check(t, !offersResume(m), "R offered to a plan a worker holds")
	assert.Check(t, offersStop(m), "S not offered to a plan a worker holds")
}

// TestResumeClaimRefused checks that a claim factory refuses, or that no
// worker can take, shows its reason and changes nothing else; R then claims
// again.
func TestResumeClaimRefused(t *testing.T) {
	t.Parallel()
	status := func(code int, msg string) error {
		return &circleci.StatusError{Op: "claim flow", StatusCode: code, ServerMessage: msg}
	}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "flow_claimed",
			err:  status(409, "flow_claimed: saved-plan is claimed by another worker"),
			want: "flow_claimed: saved-plan is claimed by another worker",
		},
		{
			name: "claim_changed",
			err:  status(409, "claim_changed: the claim moved"),
			want: "claim_changed: the claim moved",
		},
		{
			name: "flow_ended",
			err:  status(409, "flow_ended: the flow has ended"),
			want: "flow_ended: the flow has ended",
		},
		{
			name: "not found",
			err:  status(404, "no such plan"),
			want: "no such plan",
		},
		{
			name: "no worker",
			err:  ErrNoWorker,
			want: noWorkerText,
		},
		{
			name: "a network error",
			err:  errors.New("claim the plan: connection refused"),
			want: "claim the plan: connection refused",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, store := resumeScreen(t, "refine-units", sessionStep, func(s *fakeStore) { s.ClaimErr = tc.err })
			before := len(m.messages)
			m = drive(t, m, press("R"))
			assert.Check(t, cmp.Equal(store.Claims, 1))
			assert.Check(t, cmp.Contains(ansi.Strip(m.status), tc.want))
			assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), tc.want))
			assert.Check(t, !m.claiming)
			assert.Check(t, m.flow.Worker == nil && m.workerName == "", "the refused claim changed the holder")
			assert.Check(t, cmp.Len(m.messages, before), "the refusal was logged")
			assert.Check(t, m.created && !m.approving && !m.confirming)
			assert.Check(t, offersResume(m), "R is not offered again")

			store.mu.Lock()
			store.ClaimErr, store.ClaimHost = nil, "scotts-mbp"
			store.mu.Unlock()
			m = drive(t, m, press("R"))
			assert.Check(t, cmp.Equal(store.Claims, 2))
			assert.Check(t, cmp.Equal(m.status, ""))
			assert.Assert(t, m.flow.Worker != nil)
		})
	}
}

// TestRIsIgnoredWhileBusy checks that R claims nothing while the developer is
// writing, being asked something, or before the plan exists, and that a claim
// under way is not started twice.
func TestRIsIgnoredWhileBusy(t *testing.T) {
	t.Parallel()
	t.Run("writing a comment", func(t *testing.T) {
		t.Parallel()
		m, store := resumeScreen(t, "build", sessionStep, nil)
		m = pressAll(t, m, "tab", "c")
		assert.Assert(t, m.commenting)
		assert.Check(t, !offersResume(m))
		m = pressAll(t, m, "R")
		assert.Check(t, cmp.Equal(store.Claims, 0))
		assert.Check(t, cmp.Equal(m.commentInput.Value(), "R"))
	})
	t.Run("asked why the plan is ending", func(t *testing.T) {
		t.Parallel()
		m, store := resumeScreen(t, "build", sessionStep, func(s *fakeStore) {
			s.EventsV, s.eventsSet = []string{"end"}, true
		})
		m = pressAll(t, m, "x")
		assert.Assert(t, m.askingReason)
		assert.Check(t, !offersResume(m))
		m = pressAll(t, m, "R")
		assert.Check(t, cmp.Equal(store.Claims, 0))
	})
	t.Run("asked for a y", func(t *testing.T) {
		t.Parallel()
		m, store := resumeScreen(t, "build", sessionStep, nil)
		m = pressAll(t, m, "X")
		assert.Assert(t, m.confirming)
		assert.Check(t, !offersResume(m))
		m = pressAll(t, m, "R")
		assert.Check(t, cmp.Equal(store.Claims, 0))
		store.mu.Lock()
		defer store.mu.Unlock()
		assert.Check(t, !store.PlanDeleted)
	})
	t.Run("plan not created", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		m := New(context.Background(), store, "").WithInbox("Alpha")
		m = refreshed(t, drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40}))
		assert.Check(t, !offersResume(m))
		m = pressAll(t, m, "R")
		assert.Check(t, cmp.Equal(store.Claims, 0))
		assert.Check(t, cmp.Equal(m.titleInput.Value(), "R"), "R was not typed into the title")
	})
	t.Run("a claim under way", func(t *testing.T) {
		t.Parallel()
		m, store := resumeScreen(t, "build", sessionStep, nil)
		m, first := m.Update(press("R"))
		_, second := m.Update(press("R"))
		run(t, first)
		run(t, second)
		assert.Check(t, cmp.Equal(store.Claims, 1))
		assert.Check(t, m.claiming)
	})
}

// stopScreen is a flow screen of a plan in build that a live worker holds.
func stopScreen(t *testing.T) (Model, *fakeStore) {
	t.Helper()
	m, store := resumeScreen(t, "build", sessionStep, func(s *fakeStore) {
		s.FlowV.Worker, s.FlowV.Claimed = liveWorker, true
	})
	assert.Assert(t, m.flow.Worker != nil)
	return m, store
}

// TestSReleasesThePlansClaim checks that S, while a worker holds the plan,
// asks for a y, then frees the claim, which R then claims again.
func TestSReleasesThePlansClaim(t *testing.T) {
	t.Parallel()
	m, store := stopScreen(t)
	assert.Check(t, offersStop(m))
	assert.Check(t, !offersResume(m))

	m = drive(t, m, press("S"))
	assert.Check(t, m.confirming)
	assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Stop this plan?"))
	assert.Check(t, cmp.Equal(store.Released, 0), "S released the claim before the y")

	m, cmd := m.Update(press("y"))
	assert.Check(t, cmp.Equal(ansi.Strip(m.status), "Stopping the plan…"))
	assert.Check(t, !offersStop(m))
	assert.Check(t, m.releasing)
	// The y stopped the plan; it did not fall through to an approve.
	assert.Check(t, !m.approving)
	for _, msg := range run(t, cmd) {
		m = drive(t, m, msg)
	}
	assert.Check(t, cmp.Equal(store.Released, 1))
	assert.Check(t, !m.releasing)
	assert.Check(t, cmp.Equal(m.status, ""))
	assert.Check(t, m.flow.Worker == nil, "the worker still holds the plan")
	assert.Check(t, cmp.Equal(flowLines(m)[0], "No worker holds this plan · R claims it for a worker"))
	assert.Check(t, offersResume(m))
	assert.Check(t, !offersStop(m))

	t.Run("any other key keeps it", func(t *testing.T) {
		t.Parallel()
		m, store := stopScreen(t)
		m = pressAll(t, m, "S", "n")
		assert.Check(t, !m.confirming)
		assert.Check(t, !m.pendingStop)
		assert.Check(t, cmp.Equal(store.Released, 0))
		assert.Check(t, offersStop(m))
	})

	t.Run("a click on no keeps it", func(t *testing.T) {
		t.Parallel()
		m, store := stopScreen(t)
		m = drive(t, m, press("S"))
		text := ansi.Strip(m.status)
		at := strings.Index(text, confirmNo)
		assert.Assert(t, at >= 0, "the question has no no button: %q", text)
		x := 2 + ansi.StringWidth(text[:at])
		m = drive(t, m, click(x+1, m.height-footerRows+1))
		assert.Check(t, !m.confirming)
		assert.Check(t, !m.pendingStop)
		assert.Check(t, cmp.Equal(store.Released, 0))
		m = drive(t, m, press("y"))
		assert.Check(t, cmp.Equal(store.Released, 0))
		assert.Check(t, !m.releasing)
		assert.Check(t, !m.approving)
	})

	t.Run("not offered", func(t *testing.T) {
		t.Parallel()
		t.Run("while commenting", func(t *testing.T) {
			t.Parallel()
			m, _ := stopScreen(t)
			m = pressAll(t, m, "tab", "c")
			assert.Assert(t, m.commenting)
			assert.Check(t, !offersStop(m))
		})
		t.Run("while asked why the plan is ending", func(t *testing.T) {
			t.Parallel()
			m, _ := stopScreen(t)
			m = pressAll(t, m, "x")
			assert.Assert(t, m.askingReason)
			assert.Check(t, !offersStop(m))
		})
		t.Run("while confirming", func(t *testing.T) {
			t.Parallel()
			m, _ := stopScreen(t)
			m = pressAll(t, m, "S")
			assert.Assert(t, m.confirming)
			assert.Check(t, !offersStop(m))
		})
		t.Run("with no worker", func(t *testing.T) {
			t.Parallel()
			m, store := resumeScreen(t, "build", nil, nil)
			assert.Check(t, !offersStop(m))
			m = drive(t, m, press("S"))
			assert.Check(t, !m.confirming)
			assert.Check(t, cmp.Equal(store.Released, 0))
		})
		t.Run("once the plan has ended", func(t *testing.T) {
			t.Parallel()
			m, _ := resumeScreen(t, "ended", nil, func(s *fakeStore) {
				s.FlowV.Worker, s.FlowV.Ended = liveWorker, true
			})
			assert.Check(t, !offersStop(m))
			m = drive(t, m, press("S"))
			assert.Check(t, !m.confirming)
		})
	})
}

// TestStopReleaseFails checks that a release that fails, or that factory
// answers with a 409 because the claim changed, shows the error and keeps
// showing who holds the plan.
func TestStopReleaseFails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "an error",
			err:  errors.New("release the plan's claim: connection refused"),
			want: "Could not stop the plan: release the plan's claim: connection refused",
		},
		{
			name: "claim_changed",
			err:  &circleci.StatusError{Op: "release claim", StatusCode: 409, ServerMessage: "claim_changed: another worker holds it"},
			want: "claim_changed: another worker holds it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, store := stopScreen(t)
			store.mu.Lock()
			store.ReleaseErr = tc.err
			store.mu.Unlock()
			m = pressAll(t, m, "S", "y")
			assert.Check(t, cmp.Equal(store.Released, 0))
			assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Could not stop the plan: "))
			assert.Check(t, cmp.Contains(ansi.Strip(m.status), tc.want[strings.Index(tc.want, ": ")+2:]))
			assert.Check(t, !m.releasing)
			assert.Assert(t, m.flow.Worker != nil, "the holder is gone from the screen")
			view := ansi.Strip(m.Content())
			assert.Check(t, cmp.Contains(view, "scotts-mbp · architect running"))
			assert.Check(t, offersStop(m), "S is not offered again")
		})
	}
}

// TestStopHiddenForSession checks that a plan an interactive session holds,
// alone or with a worker, offers neither S nor R, and S does nothing.
func TestStopHiddenForSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(*fakeStore)
	}{
		{
			name: "a session alone",
			setup: func(s *fakeStore) {
				s.FlowV.Claimed, s.FlowV.Holder = true, "bb5a623b-8dd3-4ae8-9c91-48b4eaaf6d62"
			},
		},
		{
			name: "a session and a worker",
			setup: func(s *fakeStore) {
				s.FlowV.Claimed, s.FlowV.Holder, s.FlowV.Worker = true, "bb5a623b-8dd3-4ae8-9c91-48b4eaaf6d62", liveWorker
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, store := resumeScreen(t, "build", sessionStep, tc.setup)
			assert.Check(t, !offersStop(m), "S is offered")
			assert.Check(t, !offersResume(m), "R is offered")
			m = drive(t, m, press("S"))
			assert.Check(t, !m.confirming)
			m = drive(t, m, press("R"))
			assert.Check(t, cmp.Equal(store.Claims, 0))
			assert.Check(t, cmp.Equal(store.Released, 0))
			assert.Check(t, cmp.Equal(flowLines(m)[0], "Claimed by session bb5a623b"))
		})
	}
}

// A worker that holds the plan but reports no headless item for it shows its
// hostname and that it has no headless runner.
func TestOverviewWorkerWithoutHeadless(t *testing.T) {
	t.Parallel()
	m, _ := resumeScreen(t, "refine-units", sessionStep, func(s *fakeStore) {
		s.FlowV.Worker = &Worker{ID: "w1", Hostname: "scotts-mbp", Live: true}
	})
	assert.Check(t, cmp.Contains(ansi.Strip(overviewOf(m, 80)), "scotts-mbp · no headless runner"))
}

// A live worker with no headless item, while a session holds the plan, says
// the session holds it rather than that the worker has no runner.
func TestOverviewWorkerHeldBySession(t *testing.T) {
	t.Parallel()
	m, _ := resumeScreen(t, "refine-units", sessionStep, func(s *fakeStore) {
		s.FlowV.Claimed, s.FlowV.Holder = true, "c7045f9f-1868-4c70-a5f7-ce3f110a6334"
		s.FlowV.Worker = &Worker{ID: "w1", Hostname: "scotts-mbp", Live: true}
	})
	view := ansi.Strip(overviewOf(m, 80))
	assert.Check(t, cmp.Contains(view, "scotts-mbp · session c7045f9f"))
	assert.Check(t, !strings.Contains(view, "no headless runner"), view)
}

func TestOverviewWorkerUnread(t *testing.T) {
	t.Parallel()
	m, _ := resumeScreen(t, "refine-units", sessionStep, func(s *fakeStore) {
		s.FlowV.Worker = &Worker{ID: "w1", Unread: "no worker lists the plan"}
	})
	view := ansi.Strip(overviewOf(m, 100))
	assert.Check(t, cmp.Contains(view, "Worker w1 can't be read: no worker lists the plan"))
	assert.Check(t, !strings.Contains(view, "scotts-mbp"), "a stale hostname is shown:\n%s", view)
	assert.Check(t, !offersResume(m))
}

func TestOverviewWorkerNotLive(t *testing.T) {
	t.Parallel()
	m, _ := resumeScreen(t, "refine-units", sessionStep, func(s *fakeStore) {
		s.FlowV.Worker = &Worker{ID: "w1", Hostname: "scotts-mbp"}
	})
	assert.Check(t, cmp.Contains(ansi.Strip(overviewOf(m, 80)), "scotts-mbp · not live"))
	assert.Check(t, !offersResume(m), "R is offered while a worker that is not live holds the plan")
	// S still frees the claim of a worker that is gone.
	assert.Check(t, offersStop(m))
}

func TestOverviewWorkerRunning(t *testing.T) {
	t.Parallel()
	m, _ := resumeScreen(t, "refine-units", sessionStep, func(s *fakeStore) {
		s.FlowV.Worker = &Worker{
			ID: "w1", Hostname: "scotts-mbp", Live: true,
			Headless: []Headless{
				{Role: "architect", State: "running", Health: "ok", HealthReason: "ignored when ok"},
				{Role: "builder", State: "stopped", Health: "degraded", HealthReason: "claude exited"},
			},
			Problem: "the headless runner cannot start",
			Fix:     "run factory daemon doctor",
		}
	})
	view := ansi.Strip(overviewOf(m, 100))
	assert.Check(t, cmp.Contains(view, "scotts-mbp · architect running, builder stopped (claude exited)"))
	assert.Check(t, !strings.Contains(view, "ignored when ok"), view)
	problem, fix := strings.Index(view, "the headless runner cannot start"), strings.Index(view, "run factory daemon doctor")
	assert.Check(t, problem > strings.Index(view, "scotts-mbp ·"), view)
	assert.Check(t, fix > problem, "the fix is not under the problem:\n%s", view)
}

func TestWorkerActivityHolder(t *testing.T) {
	t.Parallel()
	w := &Worker{ID: "w1", Hostname: "scotts-mbp", Live: true}
	assert.Check(t, cmp.DeepEqual(workerActivity(w, "c7045f9f-1868"), []string{"scotts-mbp · session c7045f9f"}))
	assert.Check(t, cmp.Equal(WorkerLine(w, "c7045f9f-1868"), "Worker scotts-mbp · session c7045f9f"))
	w.Live = false
	assert.Check(t, cmp.Equal(WorkerLine(w, "c7045f9f-1868"), "Worker scotts-mbp · not live"))
}

func TestWorkerActivity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		w    *Worker
		want []string
		line string
	}{
		{
			name: "none",
			w:    nil,
			want: nil,
			line: "",
		},
		{
			name: "unread",
			w:    &Worker{ID: "w1", Unread: "not listed"},
			want: []string{"Worker w1 can't be read: not listed"},
			line: "Worker w1 can't be read",
		},
		{
			name: "not live",
			w:    &Worker{ID: "w1", Hostname: "scotts-mbp", Headless: []Headless{{Role: "architect", State: "running"}}},
			want: []string{"scotts-mbp · not live"},
			line: "Worker scotts-mbp · not live",
		},
		{
			name: "no headless",
			w:    &Worker{ID: "w1", Hostname: "scotts-mbp", Live: true},
			want: []string{"scotts-mbp · no headless runner"},
			line: "Worker scotts-mbp · no headless runner",
		},
		{
			name: "running",
			w:    &Worker{ID: "w1", Hostname: "scotts-mbp", Live: true, Headless: []Headless{{Role: "architect", State: "running", Health: "ok"}}},
			want: []string{"scotts-mbp · architect running"},
			line: "Worker scotts-mbp · architect running",
		},
		{
			name: "unhealthy, with a problem and a fix",
			w: &Worker{
				ID: "w1", Hostname: "scotts-mbp", Live: true,
				Headless: []Headless{{Role: "architect", State: "failed", Health: "error", HealthReason: "no claude"}},
				Problem:  "cannot start", Fix: "install claude",
			},
			want: []string{"scotts-mbp · architect failed (no claude)", "cannot start", "install claude"},
			line: "Worker scotts-mbp · architect failed (no claude)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Check(t, cmp.DeepEqual(workerActivity(tc.w, ""), tc.want))
			assert.Check(t, cmp.Equal(WorkerLine(tc.w, ""), tc.line))
		})
	}
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
			m, _ := planned(t, c.width, 40, c.text)
			c.check(t, m.shown(textPlan))
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
	m := New(context.Background(), store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = refreshed(t, m)

	view := ansi.Strip(m.planView.View())
	assert.Check(t, cmp.Contains(view, "needs more detail"), "view:\n%s", view)
	assert.Check(t, cmp.Contains(view, "On parts chunk has since rewritten"), "view:\n%s", view)
}

// A design approval the server refuses as no longer accepted, as when a
// retried input was already applied, is shown as a failure.
func TestARefusedDesignApprovalSaysSo(t *testing.T) {
	t.Parallel()
	m, store := planModel(t)
	store.AddressErr = &circleci.StatusError{Op: "send the approve-for-units event", StatusCode: http.StatusConflict, ServerMessage: "approve-for-units is not accepted now"}
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	assert.Check(t, !m.approvingDesign, "still approving after the approval failed")
	view := ansi.Strip(m.Content())
	assert.Check(t, cmp.Contains(view, "Could not approve the design: send the approve-for-units event: 409 Conflict — approve-for-units is not accepted now"), view)
	assert.Check(t, m.moving == nil, "a refused event started the wait")
}
