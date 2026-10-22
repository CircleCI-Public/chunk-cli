package watch

import (
	tea "charm.land/bubbletea/v2"
)

const (
	// wheelStep is how many lines of output one notch of the wheel scrolls.
	wheelStep = 3
	// firstRunRow is the screen row of the first run in the list: the header,
	// the left box's top edge and the search row come before it.
	firstRunRow = 3
)

// updateMouse handles a click or a turn of the wheel on the two-pane
// dashboard. A click on a run in the left pane selects it, and a click on
// either pane focuses it. The wheel moves the selection in the pane under the
// cursor, or scrolls the output pane when that is open.
//
// A prompt, a draft or a search in progress owns the screen, as it does the
// keyboard, so the mouse does nothing then.
func (m Model) updateMouse(msg tea.MouseMsg) Model {
	mouse := msg.Mouse()
	if m.output != nil {
		if delta := wheelDelta(msg); delta != 0 {
			m.output.scrollBy(delta*wheelStep, m.outputHeight())
		}
		return m
	}
	if m.draft != draftNone || m.searching {
		return m
	}
	onLeft := mouse.X < leftPaneWidth+2
	if delta := wheelDelta(msg); delta != 0 {
		m.runConfirm = ""
		if onLeft {
			return m.moveRunSelection(delta)
		}
		if run := m.selectedRun(); run != nil {
			m.runItem = min(max(m.runItem+delta, 0), max(len(m.runItems(run.s))-1, 0))
		}
		return m
	}
	if _, ok := msg.(tea.MouseClickMsg); !ok || mouse.Button != tea.MouseLeft {
		return m
	}
	m.runConfirm = ""
	if onLeft {
		m.focusedPane = paneLeft
		if i := m.runAtRow(mouse.Y); i >= 0 {
			m = m.selectRun(i)
		}
		return m
	}
	run := m.selectedRun()
	if run == nil || m.handingOff() {
		return m
	}
	// A click on a row selects it and does what Enter does there: fold or
	// unfold it, open its output, or open the pull request.
	if i := m.runItemAtRow(mouse.Y); i >= 0 {
		m.focusedPane, m.runItem = paneRight, i
		next, _ := m.openRunItem(run, m.runItems(run.s))
		return next.(Model)
	}
	m.focusedPane = paneRight
	return m
}

// wheelDelta is -1 for a notch up, 1 for a notch down, and 0 for any other
// mouse message.
func wheelDelta(msg tea.MouseMsg) int {
	if _, ok := msg.(tea.MouseWheelMsg); !ok {
		return 0
	}
	if b := msg.Mouse().Button; b == tea.MouseWheelUp {
		return -1
	} else if b == tea.MouseWheelDown {
		return 1
	}
	return 0
}

// runAtRow is the index in the run list of the run drawn on screen row y, or
// -1 when no run is there. Each run takes two lines, and a list taller than
// the pane is windowed, so the first run drawn is not always the first one.
func (m Model) runAtRow(y int) int {
	last := m.contentHeight() + 1 // the pane's last row, inside its box
	if y < firstRunRow || y > last || len(m.sessions) == 0 {
		return -1
	}
	first := 0
	if lines := 3 + 2*len(m.sessions); lines > m.contentHeight()+1 {
		first = firstWindowedRun(m.contentHeight()+1, m.runIndex())
	}
	i := first + (y-firstRunRow)/2
	if i >= len(m.sessions) {
		return -1
	}
	return i
}

// runItemAtRow is the index of the selectable row of the selected run drawn on
// screen row y, or -1. The pane does not record where each row went, so each
// row is laid out as the selected one to learn which lines are its own, and
// those are set against where the pane is scrolled to now.
func (m Model) runItemAtRow(y int) int {
	run := m.selectedRun()
	if run == nil {
		return -1
	}
	st, height := m.styles(), m.contentHeight()+1
	_, now := m.layoutRunPane(st, *run, height)
	line := y - 1 // the pane's title sits in the box's top edge, on row 1
	for i := range m.runItems(run.s) {
		probe := m
		probe.runItem, probe.focusedPane = i, paneRight
		_, g := probe.layoutRunPane(st, *run, height)
		switch {
		case g.tailSel >= 0:
			if now.tailTop >= 0 && line == now.tailTop+g.tailSel {
				return i
			}
		case g.selStart >= 0:
			for b := g.selStart; b < g.selEnd; b++ {
				if b >= now.first && b < now.first+now.shown && line == now.top+b-now.first {
					return i
				}
			}
		}
	}
	return -1
}
