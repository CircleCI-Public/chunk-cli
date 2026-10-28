// Package theme is the shared palette and pane drawing for chunk's full-screen
// TUIs (chunk watch and the planning screen), computed for the terminal's
// current background.
package theme

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/lipgloss/v2"
)

// Chunk brand colours. The accent is navy on a light terminal, where yellow
// would vanish, and yellow on a dark one.
var (
	Yellow = lipgloss.Color("#FFF700")
	Navy   = lipgloss.Color("#2E3C52")
	Green  = lipgloss.Color("#00DB73")
)

// ListWidth is the text width of the list pane on the left of chunk watch's
// tabs, so the panes keep their place when switching between them.
const ListWidth = 32

// Styles holds lipgloss styles computed for the current terminal background,
// shared by the full-screen TUIs.
type Styles struct {
	Dim      lipgloss.Style
	Emphasis lipgloss.Style
	Success  lipgloss.Style
	Warning  lipgloss.Style
	Running  lipgloss.Style
	// Brand is chunk's accent colour as text.
	Brand lipgloss.Style
	Error lipgloss.Style
	Muted lipgloss.Style
	VDim  lipgloss.Style
	// Chip is the base of a coloured label; the caller sets its background.
	Chip lipgloss.Style
	// Accent is filled with the brand colour, for the one primary action a
	// screen offers (e.g. sending a draft to the factory), so it reads as a
	// button next to the dimmed, bracketed hints for everything else.
	Accent lipgloss.Style
	// Field is the faint ground of a text input, so where to type is easy to
	// find on the terminal's own background.
	Field lipgloss.Style
}

// New returns the styles for a terminal with a dark background, or a light
// one.
func New(dark bool) Styles {
	ld := lipgloss.LightDark(dark)
	return Styles{
		Dim:      lipgloss.NewStyle().Foreground(ld(lipgloss.Color("248"), lipgloss.Color("246"))),
		Emphasis: lipgloss.NewStyle().Bold(true),
		Success:  lipgloss.NewStyle().Foreground(Green),
		Warning:  lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		Running:  lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
		Brand:    lipgloss.NewStyle().Foreground(ld(Navy, Yellow)),
		Error:    lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		Muted:    lipgloss.NewStyle().Foreground(ld(lipgloss.Color("252"), lipgloss.Color("250"))),
		VDim:     lipgloss.NewStyle().Foreground(ld(lipgloss.Color("244"), lipgloss.Color("242"))),
		Chip:     lipgloss.NewStyle().Bold(true).Foreground(Navy),
		Accent:   lipgloss.NewStyle().Bold(true).Foreground(ld(lipgloss.Color("255"), Navy)).Background(ld(Navy, Yellow)),
		Field:    lipgloss.NewStyle().Background(ld(lipgloss.Color("254"), lipgloss.Color("236"))),
	}
}

// Help is a help view styled with st, fitting width.
func Help(st Styles, width int) help.Model {
	h := help.New()
	h.ShortSeparator = "  ·  "
	h.Styles.ShortKey = st.Brand
	h.Styles.ShortDesc = st.Dim
	h.Styles.ShortSeparator = st.VDim
	h.Styles.Ellipsis = st.VDim
	h.SetWidth(max(width, 1))
	return h
}

// Tabs names a screen's tabs, the one showing in the accent and the others
// quiet.
func (s Styles) Tabs(active int, names ...string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = s.Dim.Render(n)
		if i == active {
			out[i] = s.Brand.Bold(true).Underline(true).Render(n)
		}
	}
	return strings.Join(out, "  ")
}

// clip cuts a line to at most width display columns, measuring with lipgloss so
// ANSI sequences are not counted as visible width and are not cut mid-escape.
func clip(s string, width int) string {
	if width < 1 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// EdgeSegment is n cells of a frame edge drawn with line, with title set into
// it as in "─ runs ─────" and cut to leave at least one cell of edge after it.
func EdgeSegment(line func(string) string, title string, n int) string {
	if title == "" || n < 5 {
		return line(strings.Repeat("─", n))
	}
	title = clip(title, n-4)
	return line("─ ") + title + line(" "+strings.Repeat("─", max(n-3-lipgloss.Width(title), 0)))
}

// Pane draws body in a rounded box w cells wide and h tall, border included,
// with title set into its top edge. The focused pane's border is the accent
// and the others' are quiet. body must already fit inside the border.
func (s Styles) Pane(focused bool, w, h int, title, body string) string {
	edge := s.VDim
	if focused {
		edge = s.Brand
	}
	// Render is variadic; EdgeSegment draws with a func(string) string.
	line := func(t string) string { return edge.Render(t) }
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(edge.GetForeground()).Width(w).Height(h).MaxHeight(h).Render(body)
	top, rest, _ := strings.Cut(box, "\n")
	n := lipgloss.Width(top) - 2
	if n < 5 {
		return box
	}
	return line("╭") + EdgeSegment(line, title, n) + line("╮") + "\n" + rest
}
