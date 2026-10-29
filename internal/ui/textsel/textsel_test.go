package textsel

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

var text = []string{
	"first line",
	"",
	"  third  ",
	"last",
}

func selected(from, to Point, margin int) Selection {
	var s Selection
	s.Begin(from, margin)
	s.Extend(to)
	return s
}

func TestNothingIsSelectedUntilTheDragLeavesItsCell(t *testing.T) {
	var s Selection
	assert.Check(t, s.Empty(), "the zero value selects something")

	s.Begin(Point{Line: 0, Col: 3}, 0)
	assert.Check(t, s.Empty(), "a press selects before any drag")
	assert.Check(t, cmp.Nil(s.Lines(text)))

	s.Extend(Point{Line: 0, Col: 3})
	assert.Check(t, s.Empty(), "a drag that stayed on its cell selects it")

	s.Extend(Point{Line: 0, Col: 4})
	assert.Check(t, !s.Empty())
}

func TestExtendBeforeBeginDoesNothing(t *testing.T) {
	var s Selection
	s.Extend(Point{Line: 1, Col: 1})
	assert.Check(t, s.Empty())
}

func TestLinesRunsFromTheFirstCellToTheLastIncludingBoth(t *testing.T) {
	tests := []struct {
		name     string
		from, to Point
		margin   int
		want     []string
	}{
		{"within a line", Point{0, 2}, Point{0, 6}, 0, []string{"rst l"}},
		{"backwards", Point{0, 6}, Point{0, 2}, 0, []string{"rst l"}},
		{"across lines keeps the blank one", Point{0, 6}, Point{2, 4}, 0, []string{"line", "", "  thi"}},
		{"to the end of a line", Point{0, 6}, Point{0, 40}, 0, []string{"line"}},
		{"padding is not selected", Point{2, 0}, Point{2, 40}, 0, []string{"  third"}},
		{"the margin is skipped on the first line", Point{2, 0}, Point{2, 6}, 2, []string{"third"}},
		{"and on the lines after it", Point{0, 6}, Point{2, 6}, 2, []string{"line", "", "third"}},
		{"a drag past the last line stops at it", Point{2, 2}, Point{9, 0}, 0, []string{"third", "last"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selected(tt.from, tt.to, tt.margin).Lines(text)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func TestLinesIgnoresStylesAndCutsWideCharactersByCell(t *testing.T) {
	styled := []string{
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2")).Render("bold") + " plain",
		"日本語 text",
	}
	got := selected(Point{0, 2}, Point{1, 3}, 0).Lines(styled)
	assert.DeepEqual(t, got, []string{"ld plain", "日本"})
}

func TestHighlightDrawsOnlyWhatIsSelected(t *testing.T) {
	style := lipgloss.NewStyle().Underline(true)
	s := selected(Point{Line: 1, Col: 1}, Point{Line: 2, Col: 3}, 0)

	// The lines shown start at line 1 of the text, as when it is scrolled.
	got := s.Highlight(text[1:], 1, style)

	assert.Check(t, cmp.Equal(ansi.Strip(strings.Join(got, "\n")), strings.Join(text[1:], "\n")),
		"highlighting changed the text")
	assert.Check(t, cmp.Equal(got[0], text[1]), "a blank line was drawn on")
	assert.Check(t, cmp.Equal(got[1], style.Render("  th")+"ird  "), "got %q", got[1])
	assert.Check(t, cmp.Equal(got[2], text[3]), "a line outside the selection was drawn on")
}

func TestHighlightLeavesTheLinesItWasGiven(t *testing.T) {
	lines := []string{"abcdef"}
	selected(Point{0, 1}, Point{0, 2}, 0).Highlight(lines, 0, lipgloss.NewStyle().Underline(true))
	assert.Check(t, cmp.Equal(lines[0], "abcdef"))
}

func TestHighlightWithNothingSelectedIsTheLines(t *testing.T) {
	var s Selection
	assert.DeepEqual(t, s.Highlight(text, 0, lipgloss.NewStyle().Underline(true)), text)
}

func TestKeepClearsTheSelectionOnlyWhenItsTextChanged(t *testing.T) {
	changed := func(i int, to string) []string {
		out := append([]string(nil), text...)
		out[i] = to
		return out
	}
	tests := []struct {
		name    string
		updated []string
		kept    bool
	}{
		{"the same text", text, true},
		{"a line added after it", append(append([]string(nil), text...), "more"), true},
		{"a line below it changed", changed(3, "other"), true},
		{"a line in it changed", changed(2, "other"), false},
		{"a line above it changed", changed(0, "other"), true},
		{"the text got shorter than it", text[:2], false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := selected(Point{1, 0}, Point{2, 3}, 0)
			s.Keep(text, tt.updated)
			assert.Check(t, cmp.Equal(!s.Empty(), tt.kept))
		})
	}
}

func TestClearSelectsNothing(t *testing.T) {
	s := selected(Point{0, 0}, Point{1, 1}, 0)
	s.Clear()
	assert.Check(t, s.Empty())
	s.Extend(Point{2, 2})
	assert.Check(t, s.Empty(), "a cleared selection took an extension")
}
