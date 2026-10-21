package plan

import (
	"context"
	"errors"
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
		next, cmd := m.Update(queue[0])
		m = next.(Model)
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
	view := ansi.Strip(m.View().Content)
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
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "use cobra flags") {
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
	lines := strings.Split(m.View().Content, "\n")
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
		if view := ansi.Strip(m.View().Content); width >= 80 && !strings.Contains(view, "here.") {
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
	next, _ := m.Update(m.spin.Tick())
	m = next.(Model)
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
	next, cmd := m.Update(ctrlC)
	m = next.(Model)
	if cmd != nil {
		t.Fatal("ctrl+c with an unsent comment quit at once")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "1 unsent comment") {
		t.Errorf("no warning about the unsent comment:\n%s", ansi.Strip(m.View().Content))
	}
	if _, cmd := m.Update(ctrlC); cmd == nil {
		t.Error("a second ctrl+c did not quit")
	}

	// Any other key in between starts over.
	next, _ = m.Update(press("j"))
	if _, cmd := next.(Model).Update(ctrlC); cmd != nil {
		t.Error("ctrl+c after another key quit without asking")
	}
}

func TestNewlineHintMatchesTheTerminal(t *testing.T) {
	m := New(context.Background(), nil, nil, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 30})
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "ctrl+j new line") {
		t.Errorf("a terminal that sends shift+enter as enter should be told ctrl+j:\n%s", view)
	}
	m = drive(t, m, tea.KeyboardEnhancementsMsg{Flags: ansi.KittyDisambiguateEscapeCodes})
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "shift+enter new line") {
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
		if view := ansi.Strip(m.View().Content); !strings.Contains(view, "ctrl+s send plan to factory") {
			t.Errorf("the footer dropped ctrl+s:\n%s", view)
		}
		m = drive(t, m, press("tab"))
	}
}
