package watch

import (
	tea "charm.land/bubbletea/v2"
)

// updateMouse forwards the mouse, and pastes, to the pane they are for: a
// press to the pane under it, which takes the keyboard; a drag's motion and
// release to the pane it began in; the wheel to the pane under it; a paste
// to the pane with the keyboard. The rule between the panes is neither's.
func (m Model) updateMouse(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.chatShown && !m.sideBySide() {
		// The chat covers the screen, so it has the mouse.
		switch msg.(type) {
		case tea.MouseClickMsg:
			m.drag = dragChat
		case tea.MouseReleaseMsg:
			m.drag = dragNone
		}
		return m.forwardChat(msg, 0)
	}
	if _, ok := msg.(tea.PasteMsg); ok {
		if m.chatFocused {
			return m.forwardChat(msg, 0)
		}
		return m.forwardScreen(msg)
	}
	mouse, ok := msg.(tea.MouseMsg)
	if !ok || !m.chatShown {
		return m.forwardScreen(msg)
	}
	edge, x := m.mainWidth(), mouse.Mouse().X
	offset := edge + 1
	switch msg.(type) {
	case tea.MouseClickMsg:
		switch {
		case x < edge:
			m.drag = dragMain
			return m.focusMain().forwardScreen(msg)
		case x > edge:
			m.drag = dragChat
			focused, focus := m.focusChat()
			next, cmd := focused.forwardChat(msg, offset)
			return next, tea.Batch(focus, cmd)
		}
		return m, nil
	case tea.MouseMotionMsg, tea.MouseReleaseMsg:
		drag := m.drag
		if _, ok := msg.(tea.MouseReleaseMsg); ok {
			m.drag = dragNone
		}
		switch {
		case drag == dragChat:
			return m.forwardChat(msg, offset)
		case drag == dragMain, x < edge:
			return m.forwardScreen(msg)
		}
		return m, nil
	case tea.MouseWheelMsg:
		switch {
		case x < edge:
			return m.forwardScreen(msg)
		case x > edge:
			return m.forwardChat(msg, offset)
		}
	}
	return m, nil
}

// forwardChat hands msg to the chat with its X dx columns left, clamped
// to its first column.
func (m Model) forwardChat(msg tea.Msg, dx int) (Model, tea.Cmd) {
	var cmd tea.Cmd
	*m.chatView, cmd = m.chatView.Update(shiftX(msg, dx))
	return m, cmd
}

// forwardScreen hands msg to the plan screen or the flow list.
func (m Model) forwardScreen(msg tea.Msg) (Model, tea.Cmd) {
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

// shiftX is msg with a mouse event's X dx columns left, clamped to 0.
func shiftX(msg tea.Msg, dx int) tea.Msg {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		msg.X = max(msg.X-dx, 0)
		return msg
	case tea.MouseMotionMsg:
		msg.X = max(msg.X-dx, 0)
		return msg
	case tea.MouseReleaseMsg:
		msg.X = max(msg.X-dx, 0)
		return msg
	case tea.MouseWheelMsg:
		msg.X = max(msg.X-dx, 0)
		return msg
	}
	return msg
}
