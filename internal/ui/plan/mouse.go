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

// updateMouse handles the mouse: the wheel scrolls the pane under it, and a
// left click moves the keyboard to what was clicked. Motion and releases are
// not used.
func (m Model) updateMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
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

// planLeft is the column the plan pane starts at.
func (m Model) planLeft() int {
	if m.narrow() || !m.chatShown() {
		return 0
	}
	return m.chatWidth()
}

// inPane is which pane is drawn at column x, when a row of the panes is
// clicked.
func (m Model) inPane(x int) pane {
	if !m.chatShown() {
		return panePlan
	}
	if m.narrow() {
		return m.focus
	}
	if x >= m.chatWidth() {
		return panePlan
	}
	return paneChat
}

// wheel scrolls the pane the pointer is over, whichever has the keyboard.
func (m Model) wheel(msg tea.MouseWheelMsg) (Model, tea.Cmd) {
	if msg.Y < m.topRows() || msg.Y >= m.height-footerRows {
		return m, nil
	}
	var cmd tea.Cmd
	if m.inPane(msg.X) == panePlan && m.tab == tabFlow {
		m.flowView, cmd = m.flowView.Update(msg)
		return m, cmd
	}
	if m.inPane(msg.X) == panePlan {
		m.planView, cmd = m.planView.Update(msg)
		return m, cmd
	}
	m.chatView, cmd = m.chatView.Update(msg)
	return m, cmd
}

// click handles a left click at mouse.
func (m Model) click(mouse tea.Mouse) (Model, tea.Cmd) {
	if m.width == 0 {
		return m, nil
	}
	if m.approving || m.approvingDesign {
		return m, nil
	}
	if m.confirming {
		return m.clickConfirm(mouse)
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
	case mouse.Y == 0:
		return m, nil
	case m.narrow() && mouse.Y == headerRows+stripRows:
		return m.clickTab(mouse.X)
	case mouse.Y >= m.height-footerRows:
		return m.clickBar(mouse)
	case mouse.Y < m.topRows():
		return m, nil
	}
	if mouse.Y == m.topRows() && m.inPane(mouse.X) == panePlan {
		// The right pane's title is its tabs.
		switch flow, plan := m.rightTabSpans(); {
		case flow.has(mouse.X):
			return m.showTab(tabFlow)
		case plan.has(mouse.X):
			return m.showTab(tabPlan)
		}
	}
	if m.inPane(mouse.X) == panePlan && m.tab == tabFlow {
		return m.focusOn(panePlan)
	}
	if m.inPane(mouse.X) == panePlan {
		return m.clickPlan(mouse)
	}
	if m.focus != paneChat {
		return m.focusOn(paneChat)
	}
	return m, nil
}

// clickTab picks chat, Flow or Plan from the tabs of a narrow screen.
func (m Model) clickTab(x int) (Model, tea.Cmd) {
	for i, s := range m.tabSpans() {
		i += m.tabOffset()
		switch {
		case !s.has(x) || i == m.narrowTab():
		case i == narrowChat:
			return m.focusOn(paneChat)
		case i == narrowFlow:
			return m.showTab(tabFlow)
		default:
			return m.showTab(tabPlan)
		}
	}
	return m, nil
}

// clickPlan focuses the plan and puts the cursor on the block clicked. A
// shift-click selects from the cursor to the block, and a second click on a
// block opens its comment.
func (m Model) clickPlan(mouse tea.Mouse) (Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.focus != panePlan {
		m, cmd = m.focusOn(panePlan)
	}
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
	top := m.topRows() + 1 + padY
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
	if mouse.Y != m.height-footerRows+1 {
		return m, nil
	}
	main, right := m.barRight()
	if main == "" {
		return m, nil
	}
	inner := m.width - borderSize - 2
	// The bar's text starts after its border and padding, and the buttons
	// are set against its right edge.
	left := 2 + inner - lipgloss.Width(right)
	if mouse.X < left || mouse.X >= left+lipgloss.Width(main) {
		return m, nil
	}
	switch {
	case m.busy:
		// The button only says chunk is working.
		return m, nil
	case len(queuedComments(m.comments)) > 0 && m.focus == panePlan:
		return m.deliver("")
	case len(queuedComments(m.comments)) > 0:
		return m.deliver(strings.TrimSpace(m.input.Value()))
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
	if mouse.Y != m.height-footerRows+1 || i < 0 {
		m.pendingDelete, m.pendingDesign = "", false
		return m, nil
	}
	left := 2 + ansi.StringWidth(text[:i])
	if mouse.X < left || mouse.X >= left+ansi.StringWidth(confirmYes) {
		m.pendingDelete, m.pendingDesign = "", false
		return m, nil
	}
	return m.answerYes()
}
