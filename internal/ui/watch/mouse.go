package watch

import (
	tea "charm.land/bubbletea/v2"
)

// updateMouse forwards the mouse, and pastes, to what is showing: the plan
// screen scrolls its plan and chat with the wheel.
func (m Model) updateMouse(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.chatOpen {
		// The chat takes pastes and scrolls with the wheel; clicks do
		// nothing there.
		switch msg.(type) {
		case tea.PasteMsg, tea.MouseWheelMsg:
			var cmd tea.Cmd
			*m.chatView, cmd = m.chatView.Update(msg)
			return m, cmd
		}
		return m, nil
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
