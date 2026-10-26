package plan

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

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

func TestSSendsCommentsFromThePlan(t *testing.T) {
	var sent []string
	turn := func(_ context.Context, msg string, _ func(string)) (string, string, error) {
		sent = append(sent, msg)
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})

	// A message started in the chat, then a comment in the plan.
	m = typeText(t, m, "draft")
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))
	if bar := ansi.Strip(m.actionBar()); !strings.Contains(bar, "s Send 1 comment to chunk") {
		t.Errorf("the action bar does not offer s:\n%s", bar)
	}

	m = drive(t, m, press("s"))
	if len(sent) != 2 || !strings.Contains(sent[1], "My comment: too vague") {
		t.Fatalf("turns: %q", sent)
	}
	if strings.Contains(sent[1], "draft") {
		t.Errorf("the chat's draft, out of sight, was sent: %q", sent[1])
	}
	if m.focus != panePlan || len(m.comments) != 0 || m.input.Value() != "draft" {
		t.Errorf("focus %v, comments %+v, draft %q", m.focus, m.comments, m.input.Value())
	}
}

func TestSInThePlanWaitsForClaude(t *testing.T) {
	var turns int
	turn := func(context.Context, string, func(string)) (string, string, error) {
		turns++
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "too vague")
	m = drive(t, m, press("enter"))

	m.busy = true
	m = drive(t, m, press("s"))
	if turns != 1 || len(m.comments) != 1 || !strings.Contains(ansi.Strip(m.status), "still answering") {
		t.Errorf("turns %d, comments %+v, status %q", turns, m.comments, m.status)
	}
}

func TestCommentEditorOpensUnderItsBlock(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
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
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
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
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
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
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
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
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
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

func TestCursorDoesNotHideTheCommentBar(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
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
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	for _, width := range []int{60, 80, 110} {
		m := New(context.Background(), turn, nil, "")
		m = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 24})
		assertFits(t, m, width, 24)
		if view := ansi.Strip(m.Content()); width >= narrowBelow && !strings.Contains(view, "here.") {
			t.Errorf("at %d wide the empty plan's hint is cut off:\n%s", width, view)
		}

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
		m := New(context.Background(), nil, nil, "").WithTerminal(dark, false)
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
		m := New(context.Background(), nil, nil, "").WithTerminal(dark, false)
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

// Every pane leaves a blank row under its title and above its bottom edge
// and a cell inside each side; in the chat, the bubbles and the message box
// line up at that cell, and so do chunk's replies.
func TestBoxesPadTheirContentEvenly(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", "", nil
	}
	m := New(context.Background(), turn, nil, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = typeText(t, m, "hello there")
	m = drive(t, m, press("enter"))

	var rows [][]rune
	for _, l := range strings.Split(ansi.Strip(m.Content()), "\n") {
		rows = append(rows, []rune(l))
	}
	cw := m.chatWidth()
	chat := func(r []rune) string { return string(r[:cw]) }
	plan := func(r []rune) string { return string(r[cw:]) }
	blank := func(w int) string { return "│" + strings.Repeat(" ", w-2) + "│" }

	// Row 0 is the header and row 1 the panes' top edges.
	assert.Check(t, cmp.Equal(chat(rows[2]), blank(cw)), "chat: no blank row under the title")
	assert.Check(t, cmp.Equal(plan(rows[2]), blank(m.planWidth())), "plan: no blank row under the title")
	bottom := -1
	for i, r := range rows {
		if strings.HasPrefix(string(r), "╰") {
			bottom = i
			break
		}
	}
	assert.Assert(t, bottom > 2, "no pane bottom edge")
	assert.Check(t, cmp.Equal(chat(rows[bottom-1]), blank(cw)), "chat: no blank row above the bottom edge")
	assert.Check(t, cmp.Equal(plan(rows[bottom-1]), blank(m.planWidth())), "plan: no blank row above the bottom edge")

	// The empty plan's hint starts where plan text would, past the gutter.
	assert.Check(t, strings.HasPrefix(plan(rows[3]), "│"+strings.Repeat(" ", padX+gutterWidth)+"No plan yet."),
		"the empty plan's hint is not at the plan's text column: %q", plan(rows[3]))

	var reply, bubbleTop, bubbleText, boxTop, boxText string
	for i, r := range rows {
		if i < 2 || i >= bottom { // row 0, the header, also says "chunk"
			continue
		}
		s := chat(r)
		switch {
		case reply == "" && strings.Contains(s, "chunk") && !strings.Contains(s, "─"):
			reply = s
		case bubbleTop == "" && strings.Contains(s, "╭") && !strings.Contains(s, "Message"):
			bubbleTop, bubbleText = s, chat(rows[i+1])
		case strings.Contains(s, "Message"):
			boxTop, boxText = s, chat(rows[i+1])
		}
	}
	assert.Check(t, strings.HasPrefix(reply, "│ chunk"), "chunk's reply is not one cell in: %q", reply)
	// The column, not the byte offset: the box's edges are several bytes a cell.
	col := func(s, sub string) int { return len([]rune(s[:max(strings.LastIndex(s, sub), 0)])) }
	assert.Check(t, cmp.Equal(col(bubbleTop, "╮"), col(boxTop, "╮")),
		"the bubble's right edge %q does not line up with the message box's %q", bubbleTop, boxTop)
	assert.Check(t, strings.HasPrefix(boxTop, "│ ╭"), "the message box is not one cell in: %q", boxTop)
	assert.Check(t, strings.HasPrefix(boxText, "│ │ "), "the message box's text is not padded: %q", boxText)
	assert.Check(t, cmp.Contains(bubbleText, "│ hello there"), "the bubble's text is not padded")
}

// The message box, like the panes, sits on the terminal's own background: no
// ground behind it and no band on the cursor's line.
func TestMessageBoxSitsOnTheTerminalsBackground(t *testing.T) {
	for _, dark := range []bool{true, false} {
		m := New(context.Background(), nil, nil, "").WithTerminal(dark, false)
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
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", "", nil
	}
	for _, dark := range []bool{true, false} {
		m := New(context.Background(), turn, nil, "").WithTerminal(dark, false)
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
		m := New(context.Background(), nil, nil, "").WithTerminal(dark, false)
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

	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m2 := New(context.Background(), turn, nil, "plan it")
	m2 = drive(t, m2, tea.WindowSizeMsg{Width: 160, Height: 30})
	m2 = drive(t, m2, press("tab"))
	m2 = drive(t, m2, press("c"))
	if view := ansi.Strip(m2.Content()); !strings.Contains(view, "ctrl+j new line") {
		t.Errorf("the comment editor's footer should hint ctrl+j on a terminal that can't tell shift+enter apart:\n%s", view)
	}
}

func TestActionBarKeepsApprovingThePlanInReach(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 110, Height: 30})
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

func TestCommentOnARewrittenPartIsStillShown(t *testing.T) {
	m := New(context.Background(), nil, nil, "")
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
	m := New(context.Background(), nil, nil, "")
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
	m := New(context.Background(), nil, nil, "")
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
	m := New(context.Background(), nil, nil, "")
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
	m := New(context.Background(), nil, nil, "")
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

func TestCursorLeavesARewrittenPartsCommentWhenItIsSent(t *testing.T) {
	// A turn that does not answer while the test looks at the screen, so the
	// cursor is seen as it is for the length of the turn.
	hold := make(chan struct{})
	defer close(hold)
	turn := func(ctx context.Context, _ string, _ func(string)) (string, string, error) {
		<-hold
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
	m.renderPlan()
	m.comments = []comment{{block: -1, quote: "gone", text: "x"}}
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("G"))
	if m.cursor != len(m.blocks) {
		t.Fatalf("cursor is not on the comment: %d", m.cursor)
	}

	// Sending takes the comment, and the stop it was drawn at, with it.
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))
	if len(m.comments) != 0 || !m.busy {
		t.Fatalf("comments: %+v, busy: %v", m.comments, m.busy)
	}
	if m.cursor >= len(m.blocks) {
		t.Fatalf("cursor is %d, past the last of %d blocks", m.cursor, len(m.blocks))
	}

	// It is on the last block, the nearest stop to where it was, and back in
	// the plan it is drawn there and moves from there.
	if m.cursor != len(m.blocks)-1 {
		t.Fatalf("cursor is %d, want the last block %d", m.cursor, len(m.blocks)-1)
	}
	m = drive(t, m, press("tab"))
	if view := ansi.Strip(m.planView.View()); !strings.Contains(view, "▌") {
		t.Errorf("the focused plan pane draws no cursor:\n%s", view)
	}
	m = drive(t, m, press("up"))
	if m.cursor != len(m.blocks)-2 {
		t.Errorf("cursor after up: %d, want %d", m.cursor, len(m.blocks)-2)
	}
}

func TestCursorStaysOnARewrittenPartsCommentWhenATurnEnds(t *testing.T) {
	m := New(context.Background(), nil, nil, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m.plan = twoParts
	m.renderPlan()
	m.comments = []comment{{block: -1, quote: "gone", text: "x"}}
	m = drive(t, m, press("tab"))
	m.moveCursor(len(m.blocks))
	if m.commentAt(m.cursor) != 0 {
		t.Fatalf("cursor is not on the orphan: %d", m.cursor)
	}
	m = drive(t, m, press("c"))

	newPlan := "## 0. New\n\nAdded.\n\n" + twoParts
	m, _ = m.Update(turnDoneMsg{plan: newPlan})

	ci := m.commentAt(m.cursor)
	if ci < 0 || m.comments[ci].text != "x" {
		t.Fatalf("cursor is not on the orphan after the turn: cursor=%d comments=%+v", m.cursor, m.comments)
	}

	m = typeText(t, m, "z")
	m = drive(t, m, press("enter"))
	if len(m.comments) != 1 || m.comments[0].text != "xz" {
		t.Fatalf("comments after saving: %+v", m.comments)
	}
}

func TestCommentUnderItsBlockHasNoExcerpt(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
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

func TestSentCommentNamesItsTarget(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = drive(t, m, press("tab"))
	for range 3 {
		m = drive(t, m, press("down"))
	}
	m = drive(t, m, press("c"))
	m = typeText(t, m, "use cobra flags")
	m = drive(t, m, press("enter"))

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))

	if view := ansi.Strip(m.chatView.View()); !strings.Contains(view, `on "* [AGENT] parse it in main.go"`) {
		t.Errorf("chat is missing the comment's target:\n%s", view)
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

func TestHeaderShowsPlanState(t *testing.T) {
	turn := func(_ context.Context, _ string, _ func(string)) (string, string, error) {
		return "Wrote a plan.", "# Add a flag\n\n## 1. Outcome\n\nThe flag exists.", nil
	}
	m := New(context.Background(), turn, nil, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "No plan yet") {
		t.Errorf("header does not say there is no plan:\n%s", view)
	}

	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = m.send("add a flag")
	m = drive(t, m, turnDoneMsg{reply: "Wrote a plan.", plan: "# Add a flag\n\n## 1. Outcome\n\nThe flag exists."})
	if view := ansi.Strip(m.Content()); !strings.Contains(view, "Plan ready") {
		t.Errorf("header does not say the plan is ready:\n%s", view)
	}

	m.comments = []comment{{block: 0, quote: "# Add a flag", text: "name it --dry"}}
	m.renderPlan()
	view := ansi.Strip(m.Content())
	for _, want := range []string{"1 comment waiting for chunk", "your comment · queued", "name it --dry"} {
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
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", planWithUnits, nil
	}
	m := New(context.Background(), turn, nil, "plan it").WithBranch("hanabel/retry-429")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.Content())
	for _, want := range []string{"hanabel/retry-429", "OUTCOME ─", "OPEN QUESTIONS", "○ 1  Add RetryPolicy", "○ 2  Wire it in", "2 units", "1 open question", "ctrl+s Approve plan"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
}

func TestNarrowScreenTakesTurnsWithTabs(t *testing.T) {
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "Wrote it.", planWithUnits, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
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
	release := make(chan struct{})
	turn := func(_ context.Context, _ string, activity func(string)) (string, string, error) {
		activity("Read create.go")
		<-release
		return "ok", planWithUnits, nil
	}
	m := New(context.Background(), turn, nil, "")
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = m.send("plan it")
	m = drive(t, m, activityMsg("Read create.go"))
	view := ansi.Strip(m.Content())
	for _, want := range []string{"chunk is drafting", "Read create.go", "███", "esc Stop chunk"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
	close(release)
}

// While a comment is written enter saves it, so the bar does not offer enter
// as the key that sends comments to Claude.
func TestActionBarOffersNoButtonWhileACommentIsWritten(t *testing.T) {
	m := New(context.Background(), nil, nil, "")
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
	for _, not := range []string{"Send 1 comment", "Approve plan"} {
		if strings.Contains(view, not) {
			t.Errorf("the bar offers %q while a comment is written:\n%s", not, view)
		}
	}
}

func TestShiftSelectsSeveralBlocksForOneComment(t *testing.T) {
	var sent []string
	turn := func(_ context.Context, msg string, _ func(string)) (string, string, error) {
		sent = append(sent, msg)
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})

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

	m = drive(t, m, press("tab"))
	m = drive(t, m, press("enter"))
	if !strings.Contains(sent[1], "> The flag exists.\n>\n> ## 2. Spec") {
		t.Errorf("Claude was not sent both parts:\n%s", sent[1])
	}
}

func TestCommentOnSeveralBlocksHangsUnderTheLast(t *testing.T) {
	m := New(context.Background(), nil, nil, "")
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
	m := New(context.Background(), nil, nil, "")
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
	m := New(context.Background(), nil, nil, "")
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
	m := New(context.Background(), nil, nil, "").WithBack()
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
	m := New(context.Background(), nil, nil, "")
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
	m := New(context.Background(), nil, nil, "")
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

func idlePlan(t *testing.T) Model {
	t.Helper()
	turn := func(context.Context, string, func(string)) (string, string, error) {
		return "ok", twoParts, nil
	}
	m := New(context.Background(), turn, nil, "plan it")
	return drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
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
