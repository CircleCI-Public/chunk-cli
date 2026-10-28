package plan

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// clipboardOf is the text a command puts on the clipboard, or "" when it
// puts none.
func clipboardOf(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	// The message is tea's own, of an unexported type that is a string.
	if v := reflect.ValueOf(cmd()); v.Kind() == reflect.String {
		return v.String()
	}
	return ""
}

func motion(x, y int) tea.MouseMotionMsg {
	return tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func release(x, y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// dragged drags the mouse from one screen cell to another, and returns the
// screen as it is held at the end of the drag and what the release copies.
func dragged(t *testing.T, m Model, fromX, fromY, toX, toY int) (held Model, copied string) {
	t.Helper()
	m = drive(t, m, click(fromX, fromY))
	m = drive(t, m, motion(toX, toY))
	held, cmd := m.Update(release(toX, toY))
	return held, clipboardOf(cmd)
}

// planText is the screen column of the first character of the plan's text.
func planText(m Model) int { return m.planLeft() + 2 + gutterWidth }

func TestDraggingOverThePlanCopiesTheText(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	y := rowOf(t, m, "The flag exists.")
	x := planText(m)

	// "flag" is the fifth to the eighth character of the line.
	m, got := dragged(t, m, x+4, y, x+7, y)

	assert.Check(t, cmp.Equal(got, "flag"))
	assert.Check(t, cmp.Equal(ansi.Strip(m.status), "Copied 4 characters"))
}

func TestDraggingBackwardsCopiesTheSameText(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	y := rowOf(t, m, "The flag exists.")
	x := planText(m)

	_, got := dragged(t, m, x+7, y, x+4, y)

	assert.Check(t, cmp.Equal(got, "flag"))
}

func TestDraggingAcrossPlanPartsCopiesEachLine(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	from, to := rowOf(t, m, "The flag exists."), rowOf(t, m, "SPEC")
	x := planText(m)

	_, got := dragged(t, m, x+4, from, x+3, to)

	lines := strings.Split(got, "\n")
	assert.Check(t, cmp.Equal(lines[0], "flag exists."))
	assert.Check(t, cmp.Equal(lines[len(lines)-1], "SPEC"), "the last line should end where the drag did: %q", got)
	assert.Check(t, cmp.Contains(got, "\n\n"), "the blank line between the parts was dropped: %q", got)
}

func TestDraggingDoesNotCopyThePlansGutter(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	y := rowOf(t, m, "The flag exists.")

	// The drag starts on the cursor's gutter, left of the text.
	_, got := dragged(t, m, m.planLeft()+2, y, planText(m)+2, y)

	assert.Check(t, cmp.Equal(got, "The"))
}

func TestAClickWithoutADragCopiesNothing(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	y := rowOf(t, m, "The flag exists.")
	x := planText(m) + 4

	m, got := dragged(t, m, x, y, x, y)

	assert.Check(t, cmp.Equal(got, ""))
	assert.Check(t, cmp.Equal(m.status, ""))
	assert.Check(t, m.text.sel.Empty())
}

func TestDraggingOverTheChatCopiesTheMessageWithoutItsFrame(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	y := rowOf(t, m, "plan it")

	// From above the bubble to below it, across its border and padding.
	_, got := dragged(t, m, 2, y-1, m.chatWidth()-3, y+1)

	assert.Check(t, cmp.Contains(got, "plan it"))
	assert.Check(t, !strings.ContainsAny(got, "╭╮╰╯│─"), "the bubble's frame was copied: %q", got)
}

func TestTheSelectionIsDrawnOverTheText(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	y := rowOf(t, m, "The flag exists.")
	x := planText(m)

	m, _ = dragged(t, m, x+4, y, x+7, y)

	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "The flag exists."), "the selection changed the text")
	assert.Check(t, strings.Contains(m.Content(), "\x1b[7mflag"), "the selected text was not drawn in reverse")
}

func TestAKeyPressDropsTheSelectionAndItsNote(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	y := rowOf(t, m, "The flag exists.")
	x := planText(m)
	m, _ = dragged(t, m, x+4, y, x+7, y)
	assert.Assert(t, m.status != "")

	m = drive(t, m, press("tab"))

	assert.Check(t, m.text.sel.Empty())
	assert.Check(t, cmp.Equal(m.status, ""))
	assert.Check(t, !strings.Contains(m.Content(), "\x1b[7m"))
}

func TestAClickDropsTheSelection(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	y := rowOf(t, m, "The flag exists.")
	x := planText(m)
	m, _ = dragged(t, m, x+4, y, x+7, y)

	m = drive(t, m, click(x, y+2))

	assert.Check(t, m.text.sel.Empty())
}

func TestTheCopyNoteDoesNotReplaceAStatusThatWasThere(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	m.status = warn("Wait for chunk to finish.")
	y := rowOf(t, m, "The flag exists.")
	x := planText(m)

	m = drive(t, m, click(x+4, y))
	m = drive(t, m, motion(x+7, y))
	m, cmd := m.Update(release(x+7, y))

	assert.Check(t, cmp.Equal(clipboardOf(cmd), "flag"), "the text is copied all the same")
	assert.Check(t, strings.Contains(m.status, "Wait for chunk"), "the note replaced the status: %q", m.status)
	m = drive(t, m, press("tab"))
	assert.Check(t, strings.Contains(m.status, "Wait for chunk"), "dropping the selection cleared the status")
}

func TestDraggingPastTheEdgeScrollsAndCopiesWhatCameInto(t *testing.T) {
	m, _ := planned(t, 140, 20, longPlan(), nil)
	x := planText(m)
	top := rowOf(t, m, "OUTCOME")
	bottom := m.height - footerRows - 1
	before := m.planView.YOffset()

	m = drive(t, m, click(x, top))
	for range 25 {
		m = drive(t, m, motion(x+3, bottom+1))
	}
	_, cmd := m.Update(release(x+3, bottom+1))

	assert.Check(t, m.planView.YOffset() > before, "the drag past the bottom did not scroll the plan")
	got := clipboardOf(cmd)
	assert.Check(t, cmp.Contains(got, "OUTCOME"))
	assert.Check(t, cmp.Contains(got, "Paragraph 9 of the plan."), "text scrolled into view was not copied:\n%s", got)
}

func TestTheSelectionStaysOnItsTextWhileThePlanScrolls(t *testing.T) {
	m, _ := planned(t, 140, 20, longPlan(), nil)
	x := planText(m)
	y := rowOf(t, m, "Paragraph 0 of the plan.")
	m, _ = dragged(t, m, x, y, x+8, y)

	m = drive(t, m, tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelDown})

	assert.Check(t, cmp.Equal(m.selectedText(), "Paragraph"), "scrolling moved the selection off its text")
}

func TestAShiftClickDoesNotBeginASelectionOfText(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	shift := click(planText(m), rowOf(t, m, "The flag exists."))
	shift.Mod = tea.ModShift

	m = drive(t, m, shift)

	assert.Check(t, !m.text.held)
}

func TestUnframedTakesTheBubbleOffAMessage(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"   ╭──────────────╮", "", false},
		{"   ╰──────────────╯", "", false},
		{"   │ give me a test │", "give me a test", true},
		{"ive me a test │", "ive me a test", true},
		{"   │ give me", "give me", true},
		{"    indented code", "    indented code", true},
		{"", "", true},
		{"────", "────", true},
	}
	for _, tt := range tests {
		got, ok := unframed(tt.in)
		assert.Check(t, cmp.Equal(got, tt.want), "unframed(%q)", tt.in)
		assert.Check(t, cmp.Equal(ok, tt.ok), "unframed(%q)", tt.in)
	}
}
