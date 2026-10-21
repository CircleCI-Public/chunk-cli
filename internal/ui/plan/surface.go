package plan

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The grounds the screen is built from. The panes sit on one, and what is
// set apart from a pane (the block the cursor is on, a comment, a code
// block) sits on another, so the screen reads as surfaces and not as text in
// boxes.
func (m Model) surface() color.Color { return m.hue("#f6f8fa", "#11151c") }
func (m Model) raised() color.Color  { return m.hue("#ddf4ff", "#16222a") }
func (m Model) note() color.Color    { return m.hue("#fff8c5", "#221e14") }
func (m Model) sunk() color.Color    { return m.hue("#eaeef2", "#0b0e13") }

// bgSeq is the escape sequence that sets c as the background.
func bgSeq(c color.Color) string {
	s := lipgloss.NewStyle().Background(c).Render("x")
	before, _, _ := strings.Cut(s, "x")
	return before
}

// paint sets every line of s on the background bg, out to width w. Text
// that has been styled ends with a reset, which would drop the background
// from what follows, so each reset is followed by the background again.
func paint(s string, w int, bg color.Color) string {
	seq := bgSeq(bg)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.ReplaceAll(l, "\x1b[m", "\x1b[m"+seq)
		l = strings.ReplaceAll(l, "\x1b[0m", "\x1b[0m"+seq)
		pad := max(w-ansi.StringWidth(l), 0)
		lines[i] = seq + l + strings.Repeat(" ", pad) + "\x1b[m"
	}
	return strings.Join(lines, "\n")
}
