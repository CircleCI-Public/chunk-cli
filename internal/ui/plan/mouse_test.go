package plan

import (
	"context"
	"fmt"
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

// planned is a screen of the given size showing text, approved by approve,
// on the Plan view.
func planned(t *testing.T, width, height int, text string, approve Approver) (Model, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	store.TextV = text
	m := New(context.Background(), turnReturning("ok"), approve, store, "plan it")
	// The tests are not to touch the clipboard of the machine they run on.
	m.copyText = tea.SetClipboard
	m = drive(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	return drive(t, m, press("right")), store
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

func TestClickingABlockMovesTheCursorToIt(t *testing.T) {
	t.Parallel()
	m, _ := planned(t, 140, 40, twoParts, nil)
	m = drive(t, m, click(10, rowOf(t, m, "SPEC")))
	assert.Check(t, cmp.Equal(m.cursor, 2))
}

func TestShiftClickSelectsFromTheCursor(t *testing.T) {
	t.Parallel()
	m, _ := planned(t, 140, 40, twoParts, nil)
	m = drive(t, m, click(10, rowOf(t, m, "OUTCOME")))
	shift := click(10, rowOf(t, m, "SPEC"))
	shift.Mod = tea.ModShift
	m = drive(t, m, shift)
	lo, hi := m.selection()
	assert.Check(t, m.selecting())
	assert.Check(t, cmp.Equal(lo, 0))
	assert.Check(t, cmp.Equal(hi, 2))
}

func TestDoubleClickOpensTheComment(t *testing.T) {
	t.Parallel()
	m, _ := planned(t, 140, 40, twoParts, nil)
	y := rowOf(t, m, "The flag exists.")
	m = drive(t, m, click(10, y))
	assert.Assert(t, !m.commenting, "one click opened the editor")
	m = drive(t, m, click(10, y))
	assert.Assert(t, m.commenting, "a double click did not open the editor")
	assert.Check(t, cmp.Equal(m.cursor, 1))
	// Clicks are ignored while the comment is written.
	m = drive(t, m, click(5, 10+stripRows))
	assert.Check(t, m.commenting, "a click left the comment being written")
}

// A click on the Flow view moves no plan cursor: the plan is not drawn there.
func TestClickingTheFlowViewLeavesThePlan(t *testing.T) {
	t.Parallel()
	m, _ := planned(t, 140, 40, twoParts, nil)
	m = drive(t, m, press("left"))
	m = drive(t, m, click(10, m.topRows()+2))
	assert.Check(t, cmp.Equal(m.tab, tabFlow))
	assert.Check(t, cmp.Equal(m.cursor, 0))
	assert.Check(t, !m.commenting)
}

func TestClickingApproveAsksThenYesApprovesThePlan(t *testing.T) {
	t.Parallel()
	approved := false
	approve := func(func(string)) (string, error) {
		approved = true
		return "build-1", nil
	}
	m, store := planned(t, 140, 40, "# Plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n", approve)
	store.UnitsV = []Unit{{Name: "Unit 1: Add the flag"}}
	m = refreshed(t, m)
	bar := m.height - 1
	_, right := m.barRight()
	m = drive(t, m, click(m.width-4, bar))
	assert.Assert(t, m.confirming, "clicking Approve did not ask (right=%q)", ansi.Strip(right))
	// A click elsewhere is a no.
	n := drive(t, m, click(3, 5+stripRows))
	assert.Assert(t, !n.confirming && !approved, "a click elsewhere did not decline")
	text := ansi.Strip(m.status)
	at := strings.Index(text, confirmYes)
	assert.Assert(t, at >= 0, "the question has no yes button: %q", text)
	x := ansi.StringWidth(text[:at])
	m = drive(t, m, click(x+1, bar))
	assert.Check(t, approved)
	assert.Check(t, cmp.Equal(m.BuildID, "build-1"))
}

func TestClickingSaveCommentsSavesThem(t *testing.T) {
	t.Parallel()
	m, store := planned(t, 140, 40, twoParts, nil)
	m = drive(t, m, press("c"))
	m = typeText(t, m, "tighten this")
	m = drive(t, m, press("enter"))
	// The button is set against the bar's right edge, with a hint beside it.
	main, right := m.barRight()
	left := m.width - lipgloss.Width(right)
	assert.Assert(t, strings.Contains(ansi.Strip(main), "Save 1 comment"), "the bar offers %q", ansi.Strip(main))
	m = drive(t, m, click(left+1, m.height-1))
	assert.Check(t, cmp.Len(store.Added, 1))
	assert.Check(t, cmp.Len(queuedComments(m.comments), 0))
}

func TestWheelScrollsTheViewShown(t *testing.T) {
	t.Parallel()
	m, _ := planned(t, 140, 20, longPlan(), nil)
	m = drive(t, m, tea.MouseWheelMsg{X: 10, Y: 8 + stripRows, Button: tea.MouseWheelDown})
	assert.Check(t, m.planView.YOffset() != 0, "the wheel over the plan did not scroll it")

	// A Flow view taller than the screen scrolls under the wheel too.
	store := newFakeStore()
	store.Created, store.StateV = true, "build"
	for i := range 20 {
		store.FamilyV.Children = append(store.FamilyV.Children, Relative{ID: fmt.Sprintf("c%d", i), Title: fmt.Sprintf("Part %d", i+1), State: "build"})
	}
	f := New(context.Background(), newFakeAgent(), nil, store, "")
	f = drive(t, f, tea.WindowSizeMsg{Width: 132, Height: 20})
	f = refreshed(t, f)
	assert.Assert(t, cmp.Equal(f.tab, tabFlow))
	f = drive(t, f, tea.MouseWheelMsg{X: 10, Y: f.topRows() + 2, Button: tea.MouseWheelDown})
	assert.Check(t, f.flowView.YOffset() > 0, "the wheel over the Flow view did not scroll it")
}
