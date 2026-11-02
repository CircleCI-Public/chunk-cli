package textsel

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

// Pane is a viewport's text where the screen draws it.
type Pane struct {
	View *viewport.Model
	// X and Y are the screen cell the view's first visible line starts at.
	X, Y int
	// Margin is the cells at the left of the text that are not part of it.
	Margin int
	// Framed is whether messages are drawn in rounded boxes whose frame
	// is not copied.
	Framed bool
}

// Over is whether the screen cell (x, y) is in p's view.
func (p Pane) Over(x, y int) bool {
	return x >= p.X && x < p.X+p.View.Width() && y >= p.Y && y < p.Y+p.View.Height()
}

// cellAt is the cell of p's text drawn at the screen cell (x, y), kept to
// the text it has.
func (p Pane) cellAt(x, y int) Point {
	line := min(y-p.Y+p.View.YOffset(), p.View.TotalLineCount()-1)
	return Point{Line: max(line, 0), Col: max(x-p.X, 0)}
}

// Drag is text selected by dragging the mouse over a pane.
type Drag struct {
	sel  Selection
	held bool
}

// Press begins a selection at (x, y) if that is over p's text; it is
// whether one began.
func (d *Drag) Press(p Pane, x, y int) bool {
	if !p.Over(x, y) || p.View.TotalLineCount() == 0 {
		return false
	}
	d.held = true
	d.sel.Begin(p.cellAt(x, y), p.Margin)
	return true
}

// Move extends a held selection to (x, y). Past p's top or bottom it
// scrolls, a line for each move, so that text out of view can be reached.
func (d *Drag) Move(p Pane, x, y int) {
	if !d.held {
		return
	}
	switch {
	case y < p.Y:
		p.View.ScrollUp(1)
	case y >= p.Y+p.View.Height():
		p.View.ScrollDown(1)
	}
	d.sel.Extend(p.cellAt(x, min(max(y, p.Y), p.Y+p.View.Height()-1)))
}

// Release lets the button go and is the text selected. It is "" when
// nothing was held, leaving the selection as it is, and "", with the
// selection cleared, when the press did not drag.
func (d *Drag) Release(p Pane) string {
	if !d.held {
		return ""
	}
	d.held = false
	text := d.Text(p)
	if text == "" {
		d.sel.Clear()
	}
	return text
}

// Text is what is selected in p, as plain text, without the frame of a
// message when p is Framed.
func (d *Drag) Text(p Pane) string {
	var lines []string
	for _, l := range d.sel.Lines(strings.Split(p.View.GetContent(), "\n")) {
		if p.Framed {
			var ok bool
			if l, ok = unframed(l); !ok {
				continue
			}
		}
		lines = append(lines, l)
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// Draw is p's view with the selection drawn in reverse.
func (d *Drag) Draw(p Pane) string {
	if d.sel.Empty() {
		return p.View.View()
	}
	lines := strings.Split(p.View.View(), "\n")
	return strings.Join(d.sel.Highlight(lines, p.View.YOffset(), lipgloss.NewStyle().Reverse(true)), "\n")
}

// SetContent puts content in p's view, keeping the selection only if the
// lines under it did not change.
func (d *Drag) SetContent(p Pane, content string) {
	previous := strings.Split(p.View.GetContent(), "\n")
	p.View.SetContent(content)
	d.sel.Keep(previous, strings.Split(content, "\n"))
}

// Clear drops the selection and lets go of any drag.
func (d *Drag) Clear() { d.sel.Clear(); d.held = false }

// Empty is whether nothing is selected.
func (d *Drag) Empty() bool { return d.sel.Empty() }

// Held is whether the button is down on a selection.
func (d *Drag) Held() bool { return d.held }

// Note is what a screen says after copying text.
func Note(text string) string {
	return fmt.Sprintf("Copied %d characters", utf8.RuneCountInString(text))
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
