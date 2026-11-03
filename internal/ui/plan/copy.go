package plan

import (
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/textsel"
)

// Text in the view shown is selected by dragging the mouse over it, and what
// is selected is put on the clipboard when the button is let go. The screen
// asks the terminal to report the mouse, which is why the terminal's own
// selection does not work on it; this is the one the screen has in its place.

// textView is a view of text that can be selected: the plan or the flow,
// whichever the body shows.
type textView int

const (
	textPlan textView = iota
	textFlow
)

// textDrag is the text selected with the mouse, in the view it began in.
type textDrag struct {
	view textView
	drag textsel.Drag
	// copied is whether the status says what was copied, so that it goes
	// when the selection does.
	copied bool
}

// textShown is the view the body draws.
func (m *Model) textShown() textView {
	if m.tab == tabFlow {
		return textFlow
	}
	return textPlan
}

// viewOf is the scrolling view t draws its text in.
func (m *Model) viewOf(t textView) *viewport.Model {
	if t == textFlow {
		return &m.flowView
	}
	return &m.planView
}

// gutterOf is the cells at the left of t's text that are not part of it: the
// plan's cursor and comment bars.
func gutterOf(t textView) int {
	if t == textPlan {
		return gutterWidth
	}
	return 0
}

// textOrigin is the screen cell t's view is drawn from: under the top line
// and its rule, and for the plan the strip; the Flow view has its margin.
func (m *Model) textOrigin(t textView) (x, y int) {
	if t == textFlow {
		return flowMargin, headerRows + ruleRows
	}
	return 0, headerRows + ruleRows + stripRows
}

// pane is t's view where the screen draws it.
func (m *Model) pane(t textView) textsel.Pane {
	x, y := m.textOrigin(t)
	return textsel.Pane{View: m.viewOf(t), X: x, Y: y, Margin: gutterOf(t)}
}

// beginText starts a selection where the mouse was pressed, if that is on
// text. A shift-click is the plan's, for selecting its parts.
func (m Model) beginText(mouse tea.Mouse) Model {
	t := m.textShown()
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
