package plan

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// doubleClick is how soon a second click on a block opens its comment.
const doubleClick = 400 * time.Millisecond

// confirmButtons are the answers to a question the status bar asks. They are
// drawn as text and found again by their position in it, so a click on one is
// a press of its key.
const (
	confirmYes     = "[y] yes"
	confirmNo      = "[n] no"
	confirmButtons = confirmYes + "  " + confirmNo
)

// lastClick is the block the previous click landed on, and when.
type lastClick struct {
	stop int
	at   time.Time
}

// updateMouse handles the mouse: the wheel scrolls the view shown, a left
// click on the plan moves its cursor there, and dragging selects text to
// copy.
func (m Model) updateMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		return m.wheel(msg)
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		return m.click(msg.Mouse())
	case tea.MouseMotionMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		return m.dragText(msg.Mouse()), nil
	case tea.MouseReleaseMsg:
		return m.endText()
	}
	return m, nil
}

// wheel scrolls the view shown, when the pointer is over it.
func (m Model) wheel(msg tea.MouseWheelMsg) (Model, tea.Cmd) {
	if msg.Y < m.topRows() || msg.Y >= m.height-footerRows {
		return m, nil
	}
	var cmd tea.Cmd
	if m.showsFlow() {
		m.flowView, cmd = m.flowView.Update(msg)
		return m, cmd
	}
	m.planView, cmd = m.planView.Update(msg)
	return m, cmd
}

// click handles a left click at mouse.
func (m Model) click(mouse tea.Mouse) (Model, tea.Cmd) {
	if m.width == 0 {
		return m, nil
	}
	m.clearText()
	if m.approving || m.approvingDesign {
		return m, nil
	}
	if m.confirming {
		return m.clickConfirm(mouse)
	}
	if m.askingReason {
		// A click keeps the plan, as any key but a reason does.
		m.askingReason, m.status = false, ""
		return m, nil
	}
	if m.quitting {
		m.quitting, m.status = false, ""
	}
	// A comment being written keeps the screen until it is saved or
	// cancelled: a click elsewhere would otherwise throw it away.
	if m.commenting {
		return m, nil
	}
	switch {
	case mouse.Y >= m.height-footerRows:
		return m.clickBar(mouse)
	case mouse.Y == 0:
		return m.clickTab(mouse)
	case mouse.Y < m.topRows():
		return m, nil
	}
	var cmd tea.Cmd
	if !m.showsFlow() && m.created {
		m, cmd = m.clickPlan(mouse)
	}
	// The press may also begin a selection of text, if it is dragged.
	return m.beginText(mouse), cmd
}

// clickTab shows the view whose tab in the top line is clicked. The Plan
// tab cannot be picked before the plan exists.
func (m Model) clickTab(mouse tea.Mouse) (Model, tea.Cmd) {
	t, ok := m.tabAt(mouse.X)
	if !ok {
		return m, nil
	}
	// The click closes the help, as a key it does not take does.
	m.helping = false
	return m.showTab(t)
}

// clickPlan puts the plan's cursor on the block clicked. A shift-click
// selects from the cursor to the block, and a second click on a block opens
// its comment.
func (m Model) clickPlan(mouse tea.Mouse) (Model, tea.Cmd) {
	var cmd tea.Cmd
	stop := m.stopAt(mouse.Y)
	if stop < 0 {
		return m, cmd
	}
	now := time.Now()
	double := m.lastClick != nil && m.lastClick.stop == stop && now.Sub(m.lastClick.at) < doubleClick
	m.lastClick = &lastClick{stop: stop, at: now}

	switch {
	case mouse.Mod.Contains(tea.ModShift) && stop < len(m.blocks) && m.cursor < len(m.blocks):
		if m.anchor < 0 {
			m.anchor = m.cursor
		}
		m.moveCursor(stop)
		return m, cmd
	case double:
		m.anchor = -1
		m.moveCursor(stop)
		m.lastClick = nil
		if !m.keys().Comment.Enabled() {
			return m, cmd
		}
		return m, tea.Batch(cmd, m.openCommentEditor())
	}
	m.anchor = -1
	m.moveCursor(stop)
	return m, cmd
}

// stopAt is the plan's stop drawn at screen row y, or -1 when that row is not
// in the plan's view. The rows between two blocks belong to the block above.
func (m Model) stopAt(y int) int {
	top := m.topRows()
	if y < top || y >= top+m.planView.Height() || len(m.blockTop) == 0 {
		return -1
	}
	line := y - top + m.planView.YOffset()
	at := -1
	for i := range m.blockTop {
		if m.blockTop[i] <= line {
			at = i
		}
	}
	return at
}

// clickBar presses the action bar's main button when it is the one clicked.
func (m Model) clickBar(mouse tea.Mouse) (Model, tea.Cmd) {
	if mouse.Y != m.height-1 {
		return m, nil
	}
	main, right := m.barRight()
	if main == "" {
		return m, nil
	}
	// The buttons are set against the footer's right edge.
	left := m.width - lipgloss.Width(right)
	if mouse.X < left || mouse.X >= left+lipgloss.Width(main) {
		return m, nil
	}
	switch {
	case m.busy:
		// The button only says chunk is working.
		return m, nil
	case len(queuedComments(m.comments)) > 0:
		return m.deliver()
	}
	return m.askToApprove(), nil
}

// clickConfirm answers the question in the status bar: a click on its yes
// confirms, and a click anywhere else says no, as any key but y does.
func (m Model) clickConfirm(mouse tea.Mouse) (Model, tea.Cmd) {
	m.confirming = false
	text := ansi.Strip(m.status)
	m.status = ""
	i := strings.Index(text, confirmYes)
	if mouse.Y != m.height-1 || i < 0 {
		m.pendingDelete, m.pendingDesign, m.pendingDeletePlan, m.pendingStop = "", false, false, ""
		return m, nil
	}
	left := ansi.StringWidth(text[:i])
	if mouse.X < left || mouse.X >= left+ansi.StringWidth(confirmYes) {
		m.pendingDelete, m.pendingDesign, m.pendingDeletePlan, m.pendingStop = "", false, false, ""
		return m, nil
	}
	return m.answerYes()
}
