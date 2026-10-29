package plan

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func click(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// planned is a screen of the given size showing text, approved by approve.
func planned(t *testing.T, width, height int, text string, approve Approver) (Model, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	store.TextV = text
	m := New(context.Background(), turnReturning("ok"), approve, store, "plan it")
	return drive(t, m, tea.WindowSizeMsg{Width: width, Height: height}), store
}

// rowOf is the screen row whose text contains s.
func rowOf(t *testing.T, m Model, s string) int {
	t.Helper()
	for y, l := range strings.Split(ansi.Strip(m.Content()), "\n") {
		if strings.Contains(l, s) {
			return y
		}
	}
	t.Fatalf("no row has %q:\n%s", s, ansi.Strip(m.Content()))
	return -1
}

func TestClickingAPaneGivesItTheKeyboard(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	assert.Assert(t, cmp.Equal(m.focus, paneChat), "the chat should start with the keyboard")
	m = drive(t, m, click(m.planLeft()+10, 10+stripRows))
	assert.Check(t, cmp.Equal(m.focus, panePlan), "clicking the plan did not focus it")
	m = drive(t, m, click(5, 10+stripRows))
	assert.Check(t, cmp.Equal(m.focus, paneChat), "clicking the chat did not focus it")
}

func TestClickingABlockMovesTheCursorToIt(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	m = drive(t, m, click(m.planLeft()+10, rowOf(t, m, "SPEC")))
	assert.Check(t, cmp.Equal(m.focus, panePlan))
	assert.Check(t, cmp.Equal(m.cursor, 2))
}

func TestShiftClickSelectsFromTheCursor(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	m = drive(t, m, click(m.planLeft()+10, rowOf(t, m, "OUTCOME")))
	shift := click(m.planLeft()+10, rowOf(t, m, "SPEC"))
	shift.Mod = tea.ModShift
	m = drive(t, m, shift)
	lo, hi := m.selection()
	assert.Check(t, m.selecting())
	assert.Check(t, cmp.Equal(lo, 0))
	assert.Check(t, cmp.Equal(hi, 2))
}

func TestDoubleClickOpensTheComment(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	y := rowOf(t, m, "The flag exists.")
	m = drive(t, m, click(m.planLeft()+10, y))
	assert.Assert(t, !m.commenting, "one click opened the editor")
	m = drive(t, m, click(m.planLeft()+10, y))
	assert.Assert(t, m.commenting, "a double click did not open the editor")
	assert.Check(t, cmp.Equal(m.cursor, 1))
	// Clicks are ignored while the comment is written.
	m = drive(t, m, click(5, 10+stripRows))
	assert.Check(t, m.commenting, "a click left the comment being written")
	assert.Check(t, cmp.Equal(m.focus, panePlan))
}

func TestClickingTheTabsSwitchesPanes(t *testing.T) {
	m, _ := planned(t, 70, 30, twoParts, nil)
	y := rowOf(t, m, "Activity")
	m = drive(t, m, click(5, y))
	assert.Assert(t, cmp.Equal(m.focus, paneChat), "clicking the activity tab left the activity")
	m = drive(t, m, click(18, y))
	assert.Assert(t, cmp.Equal(m.focus, panePlan), "clicking the plan tab did not open the plan")
	m = drive(t, m, click(3, y))
	assert.Check(t, cmp.Equal(m.focus, paneChat), "clicking the chat tab did not bring the chat back")
}

func TestClickingApproveAsksThenYesApprovesThePlan(t *testing.T) {
	approved := false
	approve := func(func(string)) (string, error) {
		approved = true
		return "build-1", nil
	}
	m, _ := planned(t, 140, 40, "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n", approve)
	bar := m.height - footerRows + 1
	_, right := m.barRight()
	m = drive(t, m, click(m.width-4, bar))
	assert.Assert(t, m.confirming, "clicking Approve did not ask (right=%q)", ansi.Strip(right))
	// A click elsewhere is a no.
	n := drive(t, m, click(3, 5+stripRows))
	assert.Assert(t, !n.confirming && !approved, "a click elsewhere did not decline")
	text := ansi.Strip(m.status)
	at := strings.Index(text, confirmYes)
	assert.Assert(t, at >= 0, "the question has no yes button: %q", text)
	x := 2 + ansi.StringWidth(text[:at])
	m = drive(t, m, click(x+1, bar))
	assert.Check(t, approved)
	assert.Check(t, cmp.Equal(m.BuildID, "build-1"))
}

func TestClickingSaveCommentsSavesThem(t *testing.T) {
	m, store := planned(t, 140, 40, twoParts, nil)
	m = drive(t, m, press("tab"))
	m = drive(t, m, press("c"))
	m = typeText(t, m, "tighten this")
	m = drive(t, m, press("enter"))
	// The button is set against the bar's right edge, with a hint beside it.
	main, right := m.barRight()
	left := 2 + m.width - borderSize - 2 - lipgloss.Width(right)
	assert.Assert(t, strings.Contains(ansi.Strip(main), "Save 1 comment"), "the bar offers %q", ansi.Strip(main))
	m = drive(t, m, click(left+1, m.height-footerRows+1))
	assert.Check(t, cmp.Len(store.Added, 1))
	assert.Check(t, cmp.Len(queuedComments(m.comments), 0))
}

func TestWheelScrollsThePaneUnderThePointer(t *testing.T) {
	m, _ := planned(t, 140, 20, longPlan(), nil)
	// The chat has the keyboard, but the pointer is over the plan.
	m = drive(t, m, tea.MouseWheelMsg{X: m.planLeft() + 10, Y: 8 + stripRows, Button: tea.MouseWheelDown})
	assert.Check(t, m.planView.YOffset() != 0, "the wheel over the plan did not scroll it")
	assert.Check(t, cmp.Equal(m.focus, paneChat), "scrolling took the keyboard")
}

func TestClickingATabWordSwitchesTheTab(t *testing.T) {
	m, _ := planned(t, 140, 40, twoParts, nil)
	flow, plan := m.rightTabSpans()
	m = drive(t, m, click(flow.start+1, m.topRows()))
	assert.Check(t, cmp.Equal(m.tab, tabFlow), "a click on Flow did not switch")
	assert.Check(t, cmp.Equal(m.focus, panePlan))
	assert.Check(t, cmp.Contains(ansi.Strip(m.Content()), "PROGRESS"))
	m = drive(t, m, click(plan.start+1, m.topRows()))
	assert.Check(t, cmp.Equal(m.tab, tabPlan), "a click on Plan did not switch back")

	// A narrow screen's tab row picks any of the three.
	m, _ = planned(t, 70, 30, twoParts, nil)
	y := headerRows + stripRows
	spans := m.tabSpans()
	assert.Assert(t, cmp.Len(spans, 3))
	m = drive(t, m, click(spans[narrowFlow].start+1, y))
	assert.Check(t, cmp.Equal(m.focus, panePlan))
	assert.Check(t, cmp.Equal(m.tab, tabFlow))
	m = drive(t, m, click(m.tabSpans()[narrowPlan].start+1, y))
	assert.Check(t, cmp.Equal(m.tab, tabPlan))
	m = drive(t, m, click(m.tabSpans()[narrowChat].start+1, y))
	assert.Check(t, cmp.Equal(m.focus, paneChat))
}
