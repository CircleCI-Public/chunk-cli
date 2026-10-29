// Package textsel is a selection of text in a block of lines, made by
// dragging from one cell to another as in a terminal. It knows nothing of the
// screen: a point is a line and a column of the text, so a selection holds
// still while the text scrolls under it, and what is selected can be drawn
// over the lines and read back out of them.
package textsel

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Point is a cell of the text: a line and a column, both counted from zero.
// A column counts cells, not characters, so a wide character takes two.
type Point struct {
	Line, Col int
}

func (p Point) before(q Point) bool {
	return p.Line < q.Line || (p.Line == q.Line && p.Col < q.Col)
}

// Selection is the text between where a drag began and where it is now, both
// ends included, the way a terminal selects. The zero value selects nothing.
type Selection struct {
	anchor, head Point
	// margin is the column the text starts at: what is left of it, a gutter
	// that is not part of the text, is never selected.
	margin int
	begun  bool
}

// Begin starts a selection at p. It selects nothing until Extend moves the
// other end off p. Columns left of margin are not selected.
func (s *Selection) Begin(p Point, margin int) {
	*s = Selection{anchor: p, head: p, margin: margin, begun: true}
}

// Extend moves the end of the selection that is being dragged to p.
func (s *Selection) Extend(p Point) {
	if s.begun {
		s.head = p
	}
}

// Clear selects nothing.
func (s *Selection) Clear() {
	*s = Selection{}
}

// Empty is whether nothing is selected: before a drag has begun, or when it
// has not left the cell it began on.
func (s Selection) Empty() bool {
	return !s.begun || s.anchor == s.head
}

// Keep clears the selection unless the lines it covers are the same in
// updated as they were in previous, so that text added below it or changed
// above it does not leave it on other words than those that were chosen.
func (s *Selection) Keep(previous, updated []string) {
	if s.Empty() {
		return
	}
	lo, hi := s.bounds()
	if hi.Line >= len(previous) || hi.Line >= len(updated) ||
		!slices.Equal(previous[lo.Line:hi.Line+1], updated[lo.Line:hi.Line+1]) {
		s.Clear()
	}
}

// bounds are the two ends in the order they come in the text.
func (s Selection) bounds() (lo, hi Point) {
	if s.head.before(s.anchor) {
		return s.head, s.anchor
	}
	return s.anchor, s.head
}

// span is the cells [from, to) of a line the selection covers, or false when
// it covers none. width is the line's width without its trailing spaces,
// which are never selected.
func (s Selection) span(line, width int) (from, to int, ok bool) {
	if s.Empty() {
		return 0, 0, false
	}
	lo, hi := s.bounds()
	if line < lo.Line || line > hi.Line {
		return 0, 0, false
	}
	from, to = s.margin, width
	if line == lo.Line {
		from = max(lo.Col, s.margin)
	}
	if line == hi.Line {
		to = min(hi.Col+1, width)
	}
	return from, to, to > from
}

// Highlight is lines, which start at line first of the text, with what is
// selected drawn in style.
func (s Selection) Highlight(lines []string, first int, style lipgloss.Style) []string {
	if s.Empty() {
		return lines
	}
	out := slices.Clone(lines)
	for i, l := range lines {
		from, to, ok := s.span(first+i, trimmedWidth(l))
		if !ok {
			continue
		}
		out[i] = lipgloss.StyleRanges(l, lipgloss.NewRange(from, to, style))
	}
	return out
}

// Lines is the selected text, a string for each line of it, without the
// styling or the spaces the lines were padded with. A line that is in the
// selection but has nothing selected in it is an empty string, so that the
// paragraphs of what is selected stay apart.
func (s Selection) Lines(lines []string) []string {
	if s.Empty() {
		return nil
	}
	lo, hi := s.bounds()
	hi.Line = min(hi.Line, len(lines)-1)
	var out []string
	for i := lo.Line; i <= hi.Line; i++ {
		plain := strings.TrimRight(ansi.Strip(lines[i]), " ")
		from, to, ok := s.span(i, ansi.StringWidth(plain))
		if !ok {
			out = append(out, "")
			continue
		}
		out = append(out, strings.TrimRight(ansi.Cut(plain, from, to), " "))
	}
	return out
}

// trimmedWidth is the width of s, not counting the spaces that end it.
func trimmedWidth(s string) int {
	return ansi.StringWidth(strings.TrimRight(ansi.Strip(s), " "))
}
