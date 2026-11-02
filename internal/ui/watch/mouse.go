package watch

import (
	tea "charm.land/bubbletea/v2"
)

// updateMouse forwards the mouse, and pastes, to what is showing: the open
// chat, the plan screen, or the flow list.
func (m Model) updateMouse(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.chatOpen {
		// The chat has the whole screen, so it has the mouse.
		var cmd tea.Cmd
		*m.chatView, cmd = m.chatView.Update(msg)
		return m, cmd
	}
	if m.planOpen {
		return m.forwardPlan(msg)
	}
	if m.flowsOpen && m.flows != nil {
		var cmd tea.Cmd
		*m.flows, cmd = m.flows.Update(msg)
		return m, cmd
	}
	return m, nil
}
