package plan

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/textsel"
)

// Text in either pane is selected by dragging the mouse over it, and what is
// selected is put on the clipboard when the button is let go. The screen
// asks the terminal to report the mouse, which is why the terminal's own
// selection does not work on it; this is the one the screen has in its place.

// textView is a view of text that can be selected: the chat, or the plan or
// the flow, whichever the right pane shows.
type textView int

const (
	textChat textView = iota
	textPlan
	textFlow
)

// textDrag is the text selected with the mouse, in the view it began in.
type textDrag struct {
	view textView
	sel  textsel.Selection
	// held is whether the button is down on a selection.
	held bool
	// copied is whether the status says what was copied, so that it goes
	// when the selection does.
	copied bool
}

// textViewAt is the view drawn at screen column x.
func (m *Model) textViewAt(x int) textView {
	switch {
	case m.inPane(x) == paneChat:
		return textChat
	case m.tab == tabFlow:
		return textFlow
	}
	return textPlan
}

// viewOf is the scrolling view t draws its text in.
func (m *Model) viewOf(t textView) *viewport.Model {
	switch t {
	case textPlan:
		return &m.planView
	case textFlow:
		return &m.flowView
	case textChat:
		return &m.chatView
	}
	return &m.chatView
}

// gutterOf is the cells at the left of t's text that are not part of it: the
// plan's cursor and comment bars, and the room each side of the chat.
func gutterOf(t textView) int {
	switch t {
	case textPlan:
		return gutterWidth
	case textChat:
		return chatGutter
	case textFlow:
		return 0
	}
	return 0
}

// textOrigin is the screen cell t's view is drawn from: inside its pane's
// border and padding.
func (m *Model) textOrigin(t textView) (x, y int) {
	x = 1 + padX
	if t != textChat {
		x += m.planLeft()
	}
	return x, m.topRows() + 1 + padY
}

// overText is whether the screen cell (x, y) is in t's view.
func (m *Model) overText(t textView, x, y int) bool {
	ox, oy := m.textOrigin(t)
	v := m.viewOf(t)
	return x >= ox && x < ox+v.Width() && y >= oy && y < oy+v.Height()
}

// cellAt is the cell of t's text drawn at the screen cell (x, y), kept to
// the text it has.
func (m *Model) cellAt(t textView, x, y int) textsel.Point {
	ox, oy := m.textOrigin(t)
	v := m.viewOf(t)
	line := min(y-oy+v.YOffset(), v.TotalLineCount()-1)
	return textsel.Point{Line: max(line, 0), Col: max(x-ox, 0)}
}

// beginText starts a selection where the mouse was pressed, if that is on
// text. A shift-click is the plan's, for selecting its parts.
func (m Model) beginText(mouse tea.Mouse) Model {
	t := m.textViewAt(mouse.X)
	if mouse.Mod.Contains(tea.ModShift) || !m.overText(t, mouse.X, mouse.Y) || m.viewOf(t).TotalLineCount() == 0 {
		return m
	}
	m.text.view, m.text.held = t, true
	m.text.sel.Begin(m.cellAt(t, mouse.X, mouse.Y), gutterOf(t))
	return m
}

// dragText moves the end of the selection to the mouse. Past the top or
// bottom of the pane it scrolls, a line for each move of the mouse, so that
// text out of view can be reached.
func (m Model) dragText(mouse tea.Mouse) Model {
	if !m.text.held {
		return m
	}
	t := m.text.view
	v := m.viewOf(t)
	_, top := m.textOrigin(t)
	switch {
	case mouse.Y < top:
		v.ScrollUp(1)
	case mouse.Y >= top+v.Height():
		v.ScrollDown(1)
	}
	m.text.sel.Extend(m.cellAt(t, mouse.X, min(max(mouse.Y, top), top+v.Height()-1)))
	return m
}

// endText finishes a selection when the button is let go, and puts what it
// holds on the clipboard. A click that did not drag has selected nothing.
func (m Model) endText() (Model, tea.Cmd) {
	if !m.text.held {
		return m, nil
	}
	m.text.held = false
	text := m.selectedText()
	if text == "" {
		m.text.sel.Clear()
		return m, nil
	}
	if m.status == "" {
		m.status = fmt.Sprintf("Copied %d characters", utf8.RuneCountInString(text))
		m.text.copied = true
	}
	return m, tea.SetClipboard(text)
}

// clearText drops the selection, and the note of having copied it.
func (m *Model) clearText() {
	m.text.sel.Clear()
	m.text.held = false
	if m.text.copied {
		m.status, m.text.copied = "", false
	}
}

// selectedText is what is selected, as plain text.
func (m *Model) selectedText() string {
	t := m.text.view
	var lines []string
	for _, l := range m.text.sel.Lines(strings.Split(m.viewOf(t).GetContent(), "\n")) {
		if t == textChat {
			var ok bool
			if l, ok = unframed(l); !ok {
				continue
			}
		}
		lines = append(lines, l)
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// unframed is a line of the chat without the frame of the bubble a message
// is drawn in, which would otherwise be copied with the message. It is false
// for a line that is only the top or bottom of a frame.
func unframed(line string) (string, bool) {
	t := strings.TrimLeft(line, " ")
	if strings.ContainsAny(t, "╭╮╰╯") && strings.Trim(t, "╭╮╰╯─") == "" {
		return "", false
	}
	if rest, ok := strings.CutPrefix(t, "│"); ok {
		line = strings.TrimPrefix(rest, " ")
	}
	line, _ = strings.CutSuffix(line, "│")
	return strings.TrimRight(line, " "), true
}

// shown is what t's view draws: its text, with the selection, if it is in
// this view, set apart.
func (m *Model) shown(t textView) string {
	v := m.viewOf(t)
	if m.text.view != t || m.text.sel.Empty() {
		return v.View()
	}
	lines := strings.Split(v.View(), "\n")
	return strings.Join(m.text.sel.Highlight(lines, v.YOffset(), lipgloss.NewStyle().Reverse(true)), "\n")
}

// setContent puts content in t's view. The selection is of the text it was
// made on, so it is dropped when that text changes.
func (m *Model) setContent(t textView, content string) {
	v := m.viewOf(t)
	previous := strings.Split(v.GetContent(), "\n")
	v.SetContent(content)
	if m.text.view == t {
		m.text.sel.Keep(previous, strings.Split(content, "\n"))
	}
}
