package plan

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
	case eventMsg, sentMsg, submittedMsg, refreshedMsg, commentSavedMsg, queueSavedMsg, createdMsg:
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
	if store.Title != "add a --dry-run flag" {
		t.Errorf("title is %q, want the first line", store.Title)
	}
	if !createdBeforeTurn {
		t.Error("the turn started before the plan was created")
	}
}

func TestApprovingSendsThePlan(t *testing.T) {
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	agent := turnReturning("ok")
	sent := false
	submit := func() (string, error) {
		sent = true
		return "run-1", nil
	}
	m := New(context.Background(), agent, submit, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !m.confirming {
		t.Fatal("ctrl+s should ask to confirm")
	}
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if !sent || m.RunID != "run-1" {
		t.Fatalf("sent %v, run %q", sent, m.RunID)
	}
}

// A plan still with the agent, its review among what it waits on, is not
// sent: the factory would build what the review has not passed.
func TestAPlanNotReadyIsNotSent(t *testing.T) {
	store := newFakeStore()
	store.TextV = "# Plan"
	store.StateV = "Planning - Agent"
	sent := false
	submit := func() (string, error) {
		sent = true
		return "run-1", nil
	}
	m := New(context.Background(), turnReturning("ok"), submit, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	if !strings.Contains(view, "In review") || strings.Contains(view, "Plan ready") {
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

// A plan waits in ReadyState after early-plan too, for its design to be
// reviewed: with no units yet there is nothing to build, so it is not sent.
func TestAPlanWithNoUnitsIsNotSent(t *testing.T) {
	store := newFakeStore()
	store.TextV = "# Plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n"
	sent := false
	submit := func() (string, error) {
		sent = true
		return "run-1", nil
	}
	m := New(context.Background(), turnReturning("ok"), submit, store, "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	if !strings.Contains(view, "Design ready") || strings.Contains(view, "Plan ready") {
		t.Errorf("header does not say only the design is ready:\n%s", view)
	}
	if !strings.Contains(view, "comment so chunk writes the units") {
		t.Errorf("the action bar offers to approve a plan with no units:\n%s", view)
	}
	m = drive(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.confirming {
		t.Fatal("ctrl+s asked to send a plan with no units")
	}
	m = drive(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if sent {
		t.Fatal("a plan with no units was sent")
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
		top := strings.Split(m.Content(), "\n")[1]
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
		if view := ansi.Strip(m.Content()); !strings.Contains(view, "ctrl+s Approve plan") {
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

func TestSendingThePlanClosesTheScreenWithItsRun(t *testing.T) {
	m := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	_, cmd := m.Update(submittedMsg{runID: "run-1"})
	if got, ok := closedBy(cmd); !ok || got.RunID != "run-1" {
		t.Fatalf("sending the plan closed with %+v, %v", got, ok)
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
	submit := func() (string, error) {
		sent = true
		return "run-1", nil
	}
	m := New(context.Background(), turnReturning("ok"), submit, store, "do it")
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

func TestActivityWithNothingSentIsAFactoryTask(t *testing.T) {
	m := New(context.Background(), turnReturning("ok"), nil, newFakeStore(), "do it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.busy {
		t.Fatal("still busy after the reply")
	}

	m = drive(t, m, eventFor(m, Event{Activity: "reading main.go"}))
	if !m.busy || !m.byTask {
		t.Fatalf("busy %v, byTask %v; want a factory task's turn", m.busy, m.byTask)
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "working on a factory task") {
		t.Errorf("view does not say chunk is on a factory task:\n%s", view)
	}

	m = drive(t, m, eventFor(m, Event{Reply: &Reply{Text: "Wrote the units.", ByTask: true}}))
	if m.busy {
		t.Error("still busy after the task's reply")
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "chunk · factory task") || !strings.Contains(view, "Wrote the units.") {
		t.Errorf("the task's reply is not marked as a factory task's:\n%s", view)
	}
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
	m.sending = true
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

func TestHeaderShowsPlanState(t *testing.T) {
	store := newFakeStore()
	m := New(context.Background(), turnReturning("Wrote a plan."), nil, store, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "No plan yet") {
		t.Errorf("header does not say there is no plan:\n%s", view)
	}

	store.TextV = "# Add a flag\n\n## 1. Outcome\n\nThe flag exists."
	m, _ = m.send("add a flag")
	m = drive(t, m, eventFor(m, Event{Reply: &Reply{Text: "Wrote a plan."}}))
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "Design ready") {
		t.Errorf("header does not say the design is ready:\n%s", view)
	}

	store.TextV += "\n\n## 5. Units\n\n### Unit 1: Add the flag\n"
	m = drive(t, m, eventFor(m, Event{Reply: &Reply{Text: "Wrote the units."}}))
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "Plan ready") {
		t.Errorf("header does not say the plan is ready:\n%s", view)
	}

	m.comments = []comment{{block: 0, quote: "# Add a flag", text: "name it --dry"}}
	m.renderPlan()
	view := ansi.Strip(m.Content())
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

### Unit 1: Add RetryPolicy

Code.

### Unit 2: Wire it in

More code.`

func TestPlanShowsUnitsAsSteps(t *testing.T) {
	store := newFakeStore()
	store.TextV = planWithUnits
	m := New(context.Background(), turnReturning("ok"), nil, store, "plan it").WithBranch("hanabel/retry-429")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	for _, want := range []string{"hanabel/retry-429", "OUTCOME ─", "OPEN QUESTIONS", "○ 1  Add RetryPolicy", "○ 2  Wire it in", "2 units", "1 open question", "ctrl+s Approve plan"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
}

func TestNarrowScreenTakesTurnsWithTabs(t *testing.T) {
	store := newFakeStore()
	store.TextV = planWithUnits
	m := New(context.Background(), turnReturning("Wrote it."), nil, store, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 70, Height: 30})
	view := ansi.Strip(m.Content())
	if !strings.Contains(view, "Chat") || !strings.Contains(view, "Plan · 2 units") {
		t.Errorf("no tab strip:\n%s", view)
	}
	if strings.Contains(view, "Retry on 429") {
		t.Errorf("the plan shows beside the chat on a narrow screen:\n%s", view)
	}
	assertFits(t, m, 70, 30)

	m = drive(t, m, press("tab"))
	view = ansi.Strip(m.Content())
	if !strings.Contains(view, "Retry on 429") || strings.Contains(view, "Wrote it.") {
		t.Errorf("tab did not bring the plan up in place of the chat:\n%s", view)
	}
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
	if !strings.Contains(view, "Plan · Planning - Agent") {
		t.Errorf("title does not show the state:\n%s", view)
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

	store.StateErr = errors.New("network blip")
	m = refreshed(t, m)
	if m.plan != before {
		t.Errorf("plan changed on a failed poll")
	}
	if !strings.Contains(m.refreshErr, "network blip") {
		t.Errorf("refreshErr: %q", m.refreshErr)
	}
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "could not refresh the plan") {
		t.Errorf("no quiet note in the footer:\n%s", view)
	}

	store.StateErr = nil
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

func TestNoHandOffOutsideHumanStates(t *testing.T) {
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
	m = drive(t, m, press("tab"))
	drive(t, m, press("enter"))

	if len(store.Added) != 1 {
		t.Fatalf("comments saved: %+v", store.Added)
	}
	if len(store.HandedOff) != 0 {
		t.Errorf("handed off: %+v", store.HandedOff)
	}
}

// TestHandOffRetriedOnNextSendWithNothingQueued checks that an
// address-comments event failing after every comment was saved is retried on
// the next send, even with nothing queued, rather than leaving the plan stuck
// in its Human state.
func TestHandOffRetriedOnNextSendWithNothingQueued(t *testing.T) {
	store := newFakeStore()
	store.TextV = twoParts
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
	if len(store.HandedOff) != 0 {
		t.Fatalf("hand-off should have failed: %+v", store.HandedOff)
	}
	if len(agent.Sent()) != 0 {
		t.Fatalf("a turn started despite the hand-off failing")
	}
	if strings.TrimSpace(m.input.Value()) != "go" {
		t.Errorf("message not kept in the input: %q", m.input.Value())
	}
	if len(queuedComments(m.comments)) != 0 {
		t.Fatalf("the saved comment should not stay queued: %+v", m.comments)
	}

	store.AddressErr = nil
	m = drive(t, m, press("enter"))

	if len(store.HandedOff) != 1 || store.HandedOff[0] != "Planning - Human" {
		t.Fatalf("hand-off not retried: %+v", store.HandedOff)
	}
	if len(store.Added) != 1 {
		t.Fatalf("the already-saved comment was saved again: %+v", store.Added)
	}
	if len(agent.Sent()) != 1 {
		t.Fatalf("turn calls = %d, want 1", len(agent.Sent()))
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

// TestSendHandsOffFromEveryHumanState checks that sending comments hands
// them to factory from each Human state, as factory's address-comments event
// moves any of them to its phase's Agent state, not just from the Planning
// state every other hand-off test exercises.
func TestSendHandsOffFromEveryHumanState(t *testing.T) {
	for _, state := range []string{"Building - Human", "Integrating - Human"} {
		t.Run(state, func(t *testing.T) {
			store := newFakeStore()
			store.TextV = twoParts
			store.StateV = state
			m := New(context.Background(), turnReturning("ok"), nil, store, "plan it")
			m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})

			m = drive(t, m, press("tab"))
			m = drive(t, m, press("down"))
			m = drive(t, m, press("c"))
			m = typeText(t, m, "first")
			m = drive(t, m, press("enter"))
			m = drive(t, m, press("tab"))
			drive(t, m, press("enter"))

			if len(store.Added) != 1 {
				t.Fatalf("comment not saved: %+v", store.Added)
			}
			if len(store.HandedOff) != 1 || store.HandedOff[0] != state {
				t.Fatalf("handed off: %+v, want [%s]", store.HandedOff, state)
			}
		})
	}
}

// TestTitleOfCutsLongFirstLineTo80Runes checks the 80-rune cut by runes, not
// bytes, so a multi-byte title is not cut mid-character.
func TestTitleOfCutsLongFirstLineTo80Runes(t *testing.T) {
	line := strings.Repeat("é", 90)
	title := titleOf(line + "\nsecond line")
	if r := []rune(title); len(r) != 80 {
		t.Fatalf("title is %d runes, want 80: %q", len(r), title)
	}
	if title != strings.Repeat("é", 80) {
		t.Errorf("title = %q, want the first 80 runes of the line", title)
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
	agent := newFakeAgent()
	m := New(context.Background(), agent, nil, store, "add a --dry-run flag")
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

	store.mu.Lock()
	store.CreateErr = nil
	store.mu.Unlock()
	m = typeText(t, m, "add a flag")
	drive(t, m, press("enter"))
	if got := agent.Sent(); len(got) != 1 || got[0] != "add a flag" {
		t.Errorf("agent got %q", got)
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
