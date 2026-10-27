package watch

import (
	tea "charm.land/bubbletea/v2"
)

// wheelRows is how many rows the output pane scrolls for a turn of the wheel.
const wheelRows = 3

// updateInput handles msg, the mouse included.
func (m Model) updateInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if mouse, ok := msg.(tea.MouseMsg); ok {
		return m.updateMouse(mouse)
	}
	return m.update(msg)
}

// updateMouse handles the mouse on the dashboard: a left click gives a pane
// the keyboard, and selects the run clicked in the list or starts its search;
// the wheel scrolls the output, or steps through the pane it is over. Motion
// and releases are not used.
func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		return m.wheel(msg)
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		return m.click(msg.Mouse())
	}
	return m, nil
}

// wheel scrolls what the pointer is over. A draft being made or shown has
// keys of its own, and the welcome has nothing to scroll.
func (m Model) wheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.output != nil {
		if delta := wheelDelta(msg); delta != 0 {
			m.output.scrollBy(delta*wheelRows, m.outputHeight())
		}
		return m, nil
	}
	if m.draft != draftNone || m.welcoming() || m.searching {
		return m, nil
	}
	delta := wheelDelta(msg)
	if delta == 0 {
		return m, nil
	}
	if msg.X < leftPaneWidth+2 {
		return withSelectedChange(m.moveRunSelection(delta), nil)
	}
	return withSelectedChange(m.moveSelection(m.selectedRun(), delta), nil)
}

// click handles a left click at mouse.
func (m Model) click(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	if m.output != nil || m.draft != draftNone || m.welcoming() || m.width == 0 {
		return m, nil
	}
	m.runConfirm = ""
	if mouse.Y < 1 || mouse.Y > m.contentHeight()+1 {
		return m, nil
	}
	if mouse.X >= leftPaneWidth+2 {
		m.searching = false
		if m.selectedRun() != nil {
			m.focusedPane = paneRight
		}
		return m, nil
	}
	m.focusedPane = paneLeft
	if mouse.Y == listTop && len(m.allRuns) > 0 {
		m.searching = true
		return m, nil
	}
	i, ok := m.runAt(mouse.Y)
	if !ok {
		return m, nil
	}
	m.searching = false
	return withSelectedChange(m.selectRun(i), nil)
}

// runAt is the index in m.sessions of the run drawn at screen row y.
func (m Model) runAt(y int) (int, bool) {
	rows := y - listTop - 1
	if rows < 0 || len(m.sessions) == 0 || y-listTop >= m.contentHeight() {
		return 0, false
	}
	total := 2 + 2*len(m.sessions) + 1
	first := firstListedRun(total, m.contentHeight()+1, m.runIndex())
	i := first + rows/2
	return i, i < len(m.sessions)
}

// wheelDelta is -1 for a turn of the wheel up, 1 for down, and 0 for sideways.
func wheelDelta(msg tea.MouseWheelMsg) int {
	if msg.Button == tea.MouseWheelUp {
		return -1
	}
	if msg.Button == tea.MouseWheelDown {
		return 1
	}
	return 0
}
