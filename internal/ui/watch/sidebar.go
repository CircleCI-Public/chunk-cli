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
)

func (m Model) sideBySide() bool { return m.chatShown && m.width >= sideBySideFrom }

// sidebarWidth is the chat's column, 0 when it is not beside the screen.
func (m Model) sidebarWidth() int {
	if !m.sideBySide() {
		return 0
	}
	return min(max(m.width/3, sidebarMin), sidebarMax)
}

// mainWidth is the flow list's, onboarding's or plan screen's width: what the sidebar and
// its rule leave.
func (m Model) mainWidth() int {
	if !m.sideBySide() {
		return m.width
	}
	return m.width - m.sidebarWidth() - 1
}

// resize lays the screens out for the terminal and the sidebar: the main
// size to every plan workspace and to the flow list under the header, and
// the chat its column, or the whole screen when it covers it. A chat that
// covers the screen has the keyboard, so keys never go to a hidden screen.
func (m Model) resize() (Model, tea.Cmd) {
	m.drag = dragNone
	size := tea.WindowSizeMsg{Width: m.mainWidth(), Height: m.height}
	var cmds []tea.Cmd
	for key := range m.planWorkspaces {
		var cmd tea.Cmd
		m, cmd = m.updateWorkspace(key, size)
		cmds = append(cmds, cmd)
	}
	if m.flows != nil {
		// The flow list draws under the dashboard's header.
		var cmd tea.Cmd
		*m.flows, cmd = m.flows.Update(tea.WindowSizeMsg{Width: size.Width, Height: max(size.Height-1, 0)})
		cmds = append(cmds, cmd)
	}
	if m.onboarding != nil {
		// Onboarding draws under the header too.
		var cmd tea.Cmd
		*m.onboarding, cmd = m.onboarding.Update(tea.WindowSizeMsg{Width: size.Width, Height: max(size.Height-1, 0)})
		cmds = append(cmds, cmd)
	}
	if m.chatView != nil {
		w := m.width
		if m.sideBySide() {
			w = m.sidebarWidth()
		}
		*m.chatView = m.chatView.SetSize(w, m.height)
	}
	if m.chatShown && !m.sideBySide() && !m.chatFocused {
		var cmd tea.Cmd
		m, cmd = m.focusChat()
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
