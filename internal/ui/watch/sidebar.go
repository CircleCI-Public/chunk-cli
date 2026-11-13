package watch

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	// sideBySideFrom is the terminal width from which the chat sits beside
	// the screen; below it the chat covers the screen.
	sideBySideFrom = 100
	sidebarMin     = 36
	sidebarMax     = 60
)

// dragPane is the pane a mouse button press began in.
type dragPane int

const (
	dragNone dragPane = iota
	dragMain
	dragChat
	// dragDetail is the flow list's pane, beside the list in dragMain.
	dragDetail
)

func (m Model) sideBySide() bool { return m.chatShown && m.width >= sideBySideFrom }

// sidebarWidth is the chat's column, 0 when it is not beside the screen.
func (m Model) sidebarWidth() int {
	if !m.sideBySide() {
		return 0
	}
	return min(max(m.width/3, sidebarMin), sidebarMax)
}

// mainWidth is the flow list's or plan screen's width: what the sidebar and
// its rule leave.
func (m Model) mainWidth() int {
	if !m.sideBySide() {
		return m.width
	}
	return m.width - m.sidebarWidth() - 1
}

// resize sizes the flow list, the plan workspaces and the chat for the
// terminal.
func (m Model) resize() (Model, tea.Cmd) {
	m.drag = dragNone
	size := tea.WindowSizeMsg{Width: m.mainWidth(), Height: m.height}
	var cmds []tea.Cmd
	var cmd tea.Cmd
	if m.flows != nil {
		// The flow list draws under the dashboard's header.
		*m.flows, cmd = m.flows.Update(tea.WindowSizeMsg{Width: size.Width, Height: max(size.Height-1, 0)})
		cmds = append(cmds, cmd)
	}
	m, cmd = m.syncSizes(true)
	cmds = append(cmds, cmd)
	if m.chatView != nil {
		w := m.width
		if m.sideBySide() {
			w = m.sidebarWidth()
		}
		*m.chatView = m.chatView.SetSize(w, m.height)
	}
	if m.chatShown && !m.sideBySide() && !m.chatFocused {
		m, cmd = m.focusChat()
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// workspaceSize is the size a plan workspace is drawn at: the pane's, or the
// main area's where no pane fits.
func (m Model) workspaceSize() (width, height int) {
	if w, h := m.paneSize(); w > 0 {
		return w, h
	}
	return m.mainWidth(), m.height
}

// syncSizes gives every workspace its drawn size, when that has changed or
// when force.
func (m Model) syncSizes(force bool) (Model, tea.Cmd) {
	w, h := m.workspaceSize()
	if !force && [2]int{w, h} == m.sentSize {
		return m, nil
	}
	m.sentSize = [2]int{w, h}
	var cmds []tea.Cmd
	for _, ws := range m.planWorkspaces {
		var cmd tea.Cmd
		m, cmd = m.updateWorkspace(ws, tea.WindowSizeMsg{Width: w, Height: h})
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// beside is h rows: each of left's padded or clipped to lw columns, the
// rule, then right's clipped to rw.
func beside(left string, lw int, right string, rw, h int, rule string) string {
	l, r := strings.Split(left, "\n"), strings.Split(right, "\n")
	rows := make([]string, h)
	for i := range rows {
		var a, b string
		if i < len(l) {
			a = clip(l[i], lw)
		}
		if i < len(r) {
			b = clip(r[i], rw)
		}
		rows[i] = a + strings.Repeat(" ", max(lw-lipgloss.Width(a), 0)) + rule + b
	}
	return strings.Join(rows, "\n")
}
