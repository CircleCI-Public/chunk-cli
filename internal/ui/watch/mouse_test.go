package watch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

func mouseModel(n, height int) Model {
	var runs []sessionInfo
	for i := range n {
		r := factoryRun(fmt.Sprintf("run-%d", i), chunkd.SessionRunning, time.Now().Add(-time.Duration(i)*time.Minute))
		r.s.Factory.Prompt = fmt.Sprintf("# Task number %d", i)
		runs = append(runs, r)
	}
	m := sessModel(runs...)
	m.height = height
	m.allRuns = runs
	m.sessions = runs
	return m.reselectRun()
}

func clickAt(m Model, x, y int) Model {
	next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return next.(Model)
}

// rowWith is the screen row the dashboard draws s on.
func rowWith(t *testing.T, m Model, s string) int {
	t.Helper()
	for y, l := range strings.Split(ansi.Strip(m.render()), "\n") {
		if strings.Contains(l, s) {
			return y
		}
	}
	t.Fatalf("no row has %q:\n%s", s, ansi.Strip(m.render()))
	return -1
}

func TestMouse_clickingARunSelectsIt(t *testing.T) {
	m := mouseModel(4, 40)
	m.focusedPane = paneRight
	m = clickAt(m, 5, rowWith(t, m, "Task number 2"))
	assert.Check(t, cmp.Equal(m.selRun, "run-2"))
	assert.Check(t, cmp.Equal(m.focusedPane, paneLeft))
}

// With more runs than rows the list scrolls to keep the selection in view,
// and a click lands on the run drawn there, not the one that would be there
// were the list unscrolled.
func TestMouse_clickingARunInAScrolledListSelectsTheRunDrawn(t *testing.T) {
	m := mouseModel(12, 16)
	m.selRun = "run-11"
	y := rowWith(t, m, "Task number 9")
	m = clickAt(m, 5, y)
	assert.Check(t, cmp.Equal(m.selRun, "run-9"))
}

func TestMouse_clickingTheRightPaneFocusesIt(t *testing.T) {
	m := mouseModel(2, 40)
	m = clickAt(m, 60, 10)
	assert.Check(t, cmp.Equal(m.focusedPane, paneRight))
	m = clickAt(m, 5, rowWith(t, m, "Task number 1"))
	assert.Check(t, cmp.Equal(m.focusedPane, paneLeft))
}

func TestMouse_clickingTheSearchRowStartsASearch(t *testing.T) {
	m := mouseModel(2, 40)
	m = clickAt(m, 5, rowWith(t, m, "search runs"))
	assert.Assert(t, m.searching)
	m = clickAt(m, 5, rowWith(t, m, "Task number 1"))
	assert.Assert(t, !m.searching, "clicking a run ends the search")
	assert.Check(t, cmp.Equal(m.selRun, "run-1"))
}

func TestMouse_wheelStepsThroughTheRunsUnderThePointer(t *testing.T) {
	m := mouseModel(4, 40)
	next, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 6, Button: tea.MouseWheelDown})
	m = next.(Model)
	assert.Check(t, cmp.Equal(m.selRun, "run-1"))
	next, _ = m.Update(tea.MouseWheelMsg{X: 5, Y: 6, Button: tea.MouseWheelUp})
	assert.Check(t, cmp.Equal(next.(Model).selRun, "run-0"))
}

func TestMouse_dashboardAsksForMouseEvents(t *testing.T) {
	m := mouseModel(1, 40)
	assert.Check(t, cmp.Equal(m.View().MouseMode, tea.MouseModeCellMotion))
}
