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

// key is a key press of k, named as tea names it.
func key(k string) tea.KeyPressMsg {
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
		m = drive(t, m, key(string(r)))
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
	m = drive(t, m, key("tab"))
	for range 3 {
		m = drive(t, m, key("down"))
	}
	m = drive(t, m, key("c"))
	m = typeText(t, m, "use cobra flags")
	m = drive(t, m, key("enter"))
	if len(m.comments) != 1 || m.comments[0].quote != "* [AGENT] parse it in main.go" {
		t.Fatalf("comments: %+v", m.comments)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "use cobra flags") {
		t.Errorf("the comment is not shown under its block:\n%s", view)
	}

	// Enter in the chat with nothing typed sends the comment alone.
	m = drive(t, m, key("tab"))
	m = drive(t, m, key("enter"))
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
	m = drive(t, m, key("tab"))
	m = drive(t, m, key("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, key("enter"))

	fail = true
	m = drive(t, m, key("tab"))
	m = drive(t, m, key("enter"))
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
