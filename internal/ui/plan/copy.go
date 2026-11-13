package plan

import (
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/textsel"
)

// Text in either pane is selected by dragging the mouse over it, and what is
// selected is put on the clipboard when the button is let go. The screen
// asks the terminal to report the mouse, which is why the terminal's own
// selection does not work on it; this is the one the screen has in its place.

// textView is a view of text that can be selected: the left pane, the request
// or the Overview, or the plan.
type textView int

const (
	textOverview textView = iota
	textPlan
)

// textDrag is the text selected with the mouse, in the view it began in.
type textDrag struct {
	view textView
	drag textsel.Drag
	// copied is whether the status says what was copied, so that it goes
	// when the selection does.
	copied bool
}

// textViewAt is the view drawn at screen column x.
func (m *Model) textViewAt(x int) textView {
	if m.inPane(x) == paneOverview {
		return textOverview
	}
	return textPlan
}

// viewOf is the scrolling view t draws its text in.
func (m *Model) viewOf(t textView) *viewport.Model {
	switch t {
	case textPlan:
		return &m.planView
	case textOverview:
		return &m.overviewView
	}
	return &m.overviewView
}

// gutterOf is the cells at the left of t's text that are not part of it: the
// plan's cursor and comment bars, the room each side of the request, and the
// Overview's cursor column.
func (m *Model) gutterOf(t textView) int {
	switch {
	case t == textPlan:
		return gutterWidth
	case m.created:
		return overviewGutter
	}
	return chatGutter
}

// textOrigin is the screen cell t's view is drawn from: inside its pane's
// border and padding.
func (m *Model) textOrigin(t textView) (x, y int) {
	x = 1 + padX
	if t == textPlan {
		x += m.planLeft()
	}
	return x, m.topRows() + 1 + padY
}

// pane is t's view where the screen draws it.
func (m *Model) pane(t textView) textsel.Pane {
	x, y := m.textOrigin(t)
	return textsel.Pane{View: m.viewOf(t), X: x, Y: y, Margin: m.gutterOf(t), Framed: t == textOverview && !m.created}
}

// beginText starts a selection where the mouse was pressed, if that is on
// text. A shift-click is the plan's, for selecting its parts.
func (m Model) beginText(mouse tea.Mouse) Model {
	t := m.textViewAt(mouse.X)
	if mouse.Mod.Contains(tea.ModShift) {
		return m
	}
	if m.text.drag.Press(m.pane(t), mouse.X, mouse.Y) {
		m.text.view = t
	}
	return m
}

// dragText moves the end of the selection to the mouse. Past the top or
// bottom of the pane it scrolls, a line for each move of the mouse, so that
// text out of view can be reached.
func (m Model) dragText(mouse tea.Mouse) Model {
	m.text.drag.Move(m.pane(m.text.view), mouse.X, mouse.Y)
	return m
}

// endText finishes a selection when the button is let go, and puts what it
// holds on the clipboard. A click that did not drag has selected nothing.
func (m Model) endText() (Model, tea.Cmd) {
	text := m.text.drag.Release(m.pane(m.text.view))
	if text == "" {
		return m, nil
	}
	if m.status == "" {
		m.status = textsel.Note(text)
		m.text.copied = true
	}
	return m, m.copyText(text)
}

// clearText drops the selection, and the note of having copied it.
func (m *Model) clearText() {
	m.text.drag.Clear()
	if m.text.copied {
		m.status, m.text.copied = "", false
	}
}

// selectedText is what is selected, as plain text.
func (m *Model) selectedText() string { return m.text.drag.Text(m.pane(m.text.view)) }

// shown is what t's view draws: its text, with the selection, if it is in
// this view, set apart.
func (m *Model) shown(t textView) string {
	if m.text.view != t {
		return m.viewOf(t).View()
	}
	return m.text.drag.Draw(m.pane(t))
}

// setContent puts content in t's view. The selection is of the text it was
// made on, so it is dropped when that text changes.
func (m *Model) setContent(t textView, content string) {
	if m.text.view != t {
		m.viewOf(t).SetContent(content)
		return
	}
	m.text.drag.SetContent(m.pane(t), content)
}
