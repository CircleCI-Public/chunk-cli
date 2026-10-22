package watch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

func click(m Model, x, y int) Model {
	next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return next.(Model)
}

func wheel(m Model, x, y int, b tea.MouseButton) Model {
	next, _ := m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: b})
	return next.(Model)
}

func numberedRuns(n int) []sessionInfo {
	var runs []sessionInfo
	for i := range n {
		run := factoryRun(fmt.Sprintf("run-%d", i), chunkd.SessionRunning, time.Now())
		run.s.Factory.Prompt = fmt.Sprintf("task %d", i)
		runs = append(runs, run)
	}
	return runs
}

// Whichever run is drawn on a row is the one a click on that row selects, with
// room for every run and with a list too tall for the pane.
func TestClickingARunSelectsIt(t *testing.T) {
	for _, height := range []int{40, 10} {
		t.Run(fmt.Sprintf("height %d", height), func(t *testing.T) {
			m := sessModel(numberedRuns(6)...)
			m.height = height
			m.selRun = "run-5"
			rows := strings.Split(view(m), "\n")
			clicked := 0
			for y, row := range rows {
				left := row[:min(len(row), leftPaneWidth+2)]
				for i := range 6 {
					if !strings.Contains(left, fmt.Sprintf("task %d", i)) {
						continue
					}
					next := click(m, 4, y)
					assert.Equal(t, next.selRun, fmt.Sprintf("run-%d", i), "row %d:\n%s", y, view(m))
					assert.Equal(t, next.focusedPane, paneLeft)
					clicked++
				}
			}
			assert.Assert(t, clicked >= 2, "found runs to click:\n%s", view(m))
		})
	}
}

func TestClickingAPaneFocusesIt(t *testing.T) {
	m := sessModel(numberedRuns(2)...)
	m.height = 30
	m.selRun = "run-0"
	m = click(m, leftPaneWidth+10, 6)
	assert.Equal(t, m.focusedPane, paneRight)
	m = click(m, 3, 1) // the box's edge, no run
	assert.Equal(t, m.focusedPane, paneLeft)
	assert.Equal(t, m.selRun, "run-0")
}

func TestWheelMovesTheSelectionUnderTheCursor(t *testing.T) {
	m := sessModel(numberedRuns(3)...)
	m.height = 30
	m.selRun = "run-0"
	m = wheel(m, 3, 6, tea.MouseWheelDown)
	assert.Equal(t, m.selRun, "run-1")
	m = wheel(m, 3, 6, tea.MouseWheelUp)
	assert.Equal(t, m.selRun, "run-0")
}

func TestMouseIsIgnoredWhilePromptingForARun(t *testing.T) {
	m := sessModel(numberedRuns(2)...)
	m.height = 30
	m.selRun = "run-0"
	m.draft = draftPrompting
	m = click(m, 4, 3+2)
	assert.Equal(t, m.selRun, "run-0")
}

func TestDashboardAsksForTheMouse(t *testing.T) {
	m := sessModel(numberedRuns(1)...)
	assert.Assert(t, m.View().MouseMode == tea.MouseModeCellMotion)
}

func rowWith(t *testing.T, m Model, text string) int {
	t.Helper()
	for y, row := range strings.Split(view(m), "\n") {
		if strings.Contains(row[min(len(row), leftPaneWidth+2):], text) {
			return y
		}
	}
	t.Fatalf("no row has %q:\n%s", text, view(m))
	return -1
}

func roundModel() Model {
	r := factoryRun("abc", chunkd.SessionRunning, time.Now())
	r.s.Rounds = []chunkd.Round{{
		Number: 1, State: chunkd.RoundDone,
		Implement: &chunkd.RoundImplement{State: chunkd.ImplementApplied, Summary: "Added the flag."},
		Checks:    []chunkd.RoundCheck{{Name: "lint", Status: "failed", Output: "app.go:3: unused"}, {Name: "test", Status: "passed"}},
	}}
	m := sessModel(r)
	m.height = 40
	m.selRun = "abc"
	return m
}

// A click on a row selects it and does what Enter would there.
func TestClickingARowOpensIt(t *testing.T) {
	m := roundModel()
	x := leftPaneWidth + 10

	m = click(m, x, rowWith(t, m, "lint"))
	assert.Equal(t, m.focusedPane, paneRight)
	assert.Assert(t, m.output != nil, "a failed check opens its output")
	assert.Equal(t, m.output.text, "app.go:3: unused")

	m.output = nil
	m = click(m, x, rowWith(t, m, "▾ Round 1"))
	assert.Equal(t, m.selectedRunItem().kind, itemRound)
	assert.Assert(t, !m.roundOpen(m.selectedRun().s, 0), "a click on an open round folds it")
	m = click(m, x, rowWith(t, m, "▸ Round 1"))
	assert.Assert(t, m.roundOpen(m.selectedRun().s, 0), "and a second one opens it")
}

// With the pane too short for the rows, the one scrolled into view is still
// the one drawn under the click.
func TestClickingARowInAScrolledPane(t *testing.T) {
	m := roundModel()
	m.height = 12
	m, _ = press(m, tea.KeyRight)
	for m.selectedRunItem().kind != itemDetails {
		m, _ = press(m, tea.KeyDown)
	}
	out := view(m)
	assert.Assert(t, !strings.Contains(out, "✓  implement"), "the pane has scrolled past the top rows:\n%s", out)

	id := m.selectedRun().s.ID
	m = click(m, leftPaneWidth+10, rowWith(t, m, "details"))
	assert.Assert(t, m.detailsShown(id), "the details row opened")
}
