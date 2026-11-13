package watch

import (
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
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
		_, released := msg.(tea.MouseReleaseMsg)
		var cmd tea.Cmd
		switch {
		case drag == dragChat:
			m, cmd = m.forwardChat(msg, offset)
		case drag == dragMain, drag == dragDetail, x < edge:
			m, cmd = m.forwardScreen(msg)
		}
		if released {
			m.drag = dragNone
		}
		return m, cmd
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

// forwardScreen hands msg to the plan screen, or the flow list and the pane
// beside it.
func (m Model) forwardScreen(msg tea.Msg) (Model, tea.Cmd) {
	if m.fullScreen() {
		return m.forwardPlan(msg)
	}
	if m.flows == nil {
		return m, nil
	}
	if _, ok := msg.(tea.PasteMsg); ok && m.focus == focusPane {
		return m.forwardPlan(msg)
	}
	if mouse, ok := msg.(tea.MouseMsg); ok {
		if w, _ := m.paneSize(); w > 0 {
			return m.forwardFlows(mouse)
		}
	}
	var cmd tea.Cmd
	*m.flows, cmd = m.flows.Update(msg)
	return m, cmd
}

// The pane beside the flow list starts under the dashboard's header, a rule
// and a column right of the list.
const (
	paneX = theme.ListWidth + 2
	paneY = 1
)

// forwardFlows hands a mouse event to the list or the pane beside it,
// whichever it is over; the motion and release of a press go to the side the
// press began in. A press in the pane gives it the keyboard, and one in the
// list takes it back.
func (m Model) forwardFlows(msg tea.MouseMsg) (Model, tea.Cmd) {
	mouse := msg.Mouse()
	over := dragMain
	if mouse.X >= paneX && mouse.Y >= paneY {
		over = dragDetail
	}
	switch msg.(type) {
	case tea.MouseClickMsg:
		m.drag = over
		if over == dragMain {
			m = m.blurPane()
		}
	case tea.MouseMotionMsg, tea.MouseReleaseMsg:
		if m.drag != dragNone {
			over = m.drag
		}
		if _, ok := msg.(tea.MouseReleaseMsg); ok {
			m.drag = dragNone
		}
	}
	if over == dragMain {
		var cmd tea.Cmd
		*m.flows, cmd = m.flows.Update(msg)
		return m, cmd
	}
	return m.forwardPane(msg)
}

// forwardPane hands a mouse event in the pane to its workspace, as the
// workspace would have it: relative to its own corner. A press takes the
// keyboard for the workspace as Enter does, making it first when there is none
// or its open failed.
func (m Model) forwardPane(msg tea.MouseMsg) (Model, tea.Cmd) {
	pane := shiftY(shiftX(msg, paneX), paneY)
	if m.focus == focusPane {
		return m.forwardPlan(pane)
	}
	if _, click := msg.(tea.MouseClickMsg); !click {
		// The wheel scrolls the plan without taking the keyboard.
		return m.updateWorkspace(m.shown(), pane)
	}
	id, title := m.flows.Selected()
	if id == "" {
		return m, nil
	}
	var act, cmd tea.Cmd
	m, act = m.openPlanID(id, title)
	m, cmd = m.forwardPlan(pane)
	return m, tea.Batch(act, cmd)
}

// shiftY is msg with a mouse event's Y dy rows up, clamped to 0.
func shiftY(msg tea.Msg, dy int) tea.Msg {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		msg.Y = max(msg.Y-dy, 0)
		return msg
	case tea.MouseMotionMsg:
		msg.Y = max(msg.Y-dy, 0)
		return msg
	case tea.MouseReleaseMsg:
		msg.Y = max(msg.Y-dy, 0)
		return msg
	case tea.MouseWheelMsg:
		msg.Y = max(msg.Y-dy, 0)
		return msg
	}
	return msg
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
