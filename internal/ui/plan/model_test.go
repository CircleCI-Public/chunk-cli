package plan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

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
	}
	return m
}

func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	// Timers (the cursor's blink, the spinner) are dropped rather than
	// waited for; everything else the screen does answers at once.
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
	case turnDoneMsg, submittedMsg:
		return []tea.Msg{msg}
	}
	return nil
}

func TestFirstMessageRendersPlanAndReply(t *testing.T) {
	var got string
	turn := func(_ context.Context, message string, _ func(string)) (string, string, error) {
		got = message
		return "Wrote a first plan.", "# Add a flag\n\n## 1. Outcome\n\nThe flag exists.", nil
	}
	m := New(context.Background(), turn, nil, "add a --dry-run flag")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	if got != "add a --dry-run flag" {
		t.Fatalf("turn got %q", got)
	}
	view := ansi.Strip(m.Content())
	for _, want := range []string{"Add a flag", "The flag exists.", "Wrote a first plan.", "add a --dry-run flag"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
}

func TestApprovingSendsThePlan(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", "# Plan", nil
	}
	var sent string
	submit := func(p string) (string, error) {
		sent = p
		return "run-1", nil
	}
	m := New(context.Background(), turn, submit, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !m.confirming {
		t.Fatal("ctrl+s should ask to confirm")
	}
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if sent != "# Plan" || m.RunID != "run-1" {
		t.Fatalf("sent %q, run %q", sent, m.RunID)
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

func TestCommentGoesToClaudeWithTheBlockItQuotes(t *testing.T) {
	var sent []string
	turn := func(_ context.Context, msg string, _ func(string)) (string, string, error) {
		sent = append(sent, msg)
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})

	// To the plan, down to the bullet, and comment on it.
	m = drive(t, m, press("tab"))
	for range 3 {
		m = drive(t, m, press("down"))
	}
	m = drive(t, m, press("c"))
	m = typeText(t, m, "use cobra flags")
	m = drive(t, m, press("enter"))
	if len(m.comments) != 1 || m.comments[0].quote != "* [AGENT] parse it in main.go" {
		t.Fatalf("comments: %+v", m.comments)
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "use cobra flags") {
		t.Errorf("the comment is not shown under its block:\n%s", view)
	}

	// Enter in the chat with nothing typed sends the comment alone.
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))
	if len(sent) != 2 {
		t.Fatalf("turns: %d", len(sent))
	}
	for _, want := range []string{"> * [AGENT] parse it in main.go", "My comment: use cobra flags"} {
		if !strings.Contains(sent[1], want) {
			t.Errorf("Claude was sent %q, missing %q", sent[1], want)
		}
	}
	if len(m.comments) != 0 {
		t.Errorf("sent comments are still waiting: %+v", m.comments)
	}
}

func TestCommentsComeBackWhenTheTurnFails(t *testing.T) {
	fail := false
	turn := func(context.Context, string, func(string)) (string, string, error) {
		if fail {
			return "", "", errors.New("claude exited")
		}
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))

	fail = true
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))
	if len(m.comments) != 1 || m.comments[0].text != "too vague" {
		t.Fatalf("comments after a failed turn: %+v", m.comments)
	}
}

func TestCommentFollowsItsBlockWhenThePlanChanges(t *testing.T) {
	m := New(context.Background(), nil, nil, "")
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
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	for _, width := range []int{60, 80, 110} {
		m := New(context.Background(), turn, nil, "")
		m = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 24})
		assertFits(t, m, width, 24)
		if view := ansi.Strip(m.Content()); width >= 80 && !strings.Contains(view, "here.") {
			t.Errorf("at %d wide the empty plan's hint is cut off:\n%s", width, view)
		}

		m = drive(t, m, press("x"))
		m = drive(t, m, press("enter"))
		m = drive(t, m, press("tab"))
		m = drive(t, m, press("c"))
		m = typeText(t, m, "too vague")
		m = drive(t, m, press("enter"))
		assertFits(t, m, width, 24)

		m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		assertFits(t, m, width, 24)
	}
}

func TestChatStaysWhereTheDeveloperScrolled(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return strings.Repeat("A long reply.\n\n", 30), "", nil
	}
	m := New(context.Background(), turn, nil, "first")
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
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
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

func TestNewlineHintMatchesTheTerminal(t *testing.T) {
	m := New(context.Background(), nil, nil, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 30})
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "ctrl+j new line") {
		t.Errorf("a terminal that sends shift+enter as enter should be told ctrl+j:\n%s", view)
	}
	m = drive(t, m, tea.KeyboardEnhancementsMsg{Flags: ansi.KittyDisambiguateEscapeCodes})
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "shift+enter new line") {
		t.Errorf("a terminal that tells shift+enter apart should be told it:\n%s", view)
	}
}

func TestFooterKeepsSendingThePlanInReach(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 110, Height: 30})
	for range 2 {
		if view := ansi.Strip(m.Content()); !strings.Contains(view, "ctrl+s send plan to factory") {
			t.Errorf("the footer dropped ctrl+s:\n%s", view)
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

func TestSendingThePlanClosesTheScreenWithItsRun(t *testing.T) {
	m := New(context.Background(), nil, nil, "")
	_, cmd := m.Update(submittedMsg{runID: "run-1"})
	if got, ok := closedBy(cmd); !ok || got.RunID != "run-1" {
		t.Fatalf("sending the plan closed with %+v, %v", got, ok)
	}
}

func TestBackClosesAtOnceAndKeepsWhatIsUnsent(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it").WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "esc back to runs") {
		t.Errorf("the footer does not say esc goes back:\n%s", view)
	}

	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got, ok := closedBy(cmd); !ok || got.RunID != "" {
		t.Fatalf("esc closed with %+v, %v", got, ok)
	}
	if len(m.comments) != 1 {
		t.Errorf("closing dropped the comment: %+v", m.comments)
	}
}

func TestBackLeavesEscToStopClaude(t *testing.T) {
	m := New(context.Background(), nil, nil, "").WithBack()
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	stopped := false
	m.busy, m.cancelTurn = true, func() { stopped = true }

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, ok := closedBy(cmd); ok || !stopped {
		t.Fatalf("esc while Claude works closed %v, stopped %v", ok, stopped)
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if _, ok := closedBy(cmd); !ok {
		t.Fatal("ctrl+c while Claude works did not go back")
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

func TestDOnABlockWithoutACommentDoesNotScroll(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", longPlan(), nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = drive(t, m, press("tab"))
	for _, k := range []string{"d", "u", "f", "b", " "} {
		m = drive(t, m, press(k))
		if m.planView.YOffset() != 0 || m.cursor != 0 {
			t.Fatalf("%q moved the plan: offset %d, cursor %d", k, m.planView.YOffset(), m.cursor)
		}
	}
}

func TestPagingMovesTheCursorWithTheView(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", longPlan(), nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
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
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return strings.Repeat("A long reply.\n\n", 30), "", nil
	}
	m := New(context.Background(), turn, nil, "first")
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
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", "# Plan", nil
	}
	sent := false
	submit := func(string) (string, error) {
		sent = true
		return "run-1", nil
	}
	m := New(context.Background(), turn, submit, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = drive(t, m, press("enter"))
	if sent || m.confirming {
		t.Fatalf("enter answered the question: sent %v, still asking %v", sent, m.confirming)
	}
}

func TestTwoCommentsOnIdenticalBlocksStayOnSeparateBlocks(t *testing.T) {
	m := New(context.Background(), nil, nil, "")
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

func TestActivityAfterTheTurnEndsIsDropped(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", "# Plan", nil
	}
	m := New(context.Background(), turn, nil, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, activityMsg("reading main.go"))
	if m.activity != "" {
		t.Errorf("activity after the turn is %q, want none", m.activity)
	}
}

func TestPlanIsSentOnceWhileTheFactoryAnswers(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", "# Plan", nil
	}
	m := New(context.Background(), turn, nil, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.sending = true
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.confirming {
		t.Error("ctrl+s asked to send the plan again while it is being sent")
	}
}

func TestBubbleFitsAShortMessageAndWrapsALongOne(t *testing.T) {
	m := &Model{}
	const w = 60
	short := m.userBubble(&message{fromUser: true, text: "hi"}, w)
	top, _, _ := strings.Cut(ansi.Strip(short), "\n")
	if got := ansi.StringWidth(strings.TrimSpace(top)); got != len("hi")+bubbleChrome {
		t.Errorf("bubble for %q is %d wide, want %d:\n%s", "hi", got, len("hi")+bubbleChrome, ansi.Strip(short))
	}

	long := m.userBubble(&message{fromUser: true, text: strings.Repeat("word ", 30)}, w)
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
