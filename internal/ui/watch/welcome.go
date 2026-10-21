package watch

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

// welcoming is whether the dashboard has nothing to show and nothing has ever
// run: it then drops the run list for a single framed welcome, since a list
// with nothing in it only takes room from the two ways to begin.
func (m Model) welcoming() bool {
	return m.output == nil &&
		len(m.allRuns) == 0 &&
		m.draft == draftNone &&
		!m.handingOff() &&
		m.selectedRun() == nil
}

// renderWelcome is the one frame around the logo, the greeting and a button
// for each way to begin, centred in the space between the header and footer.
func (m Model) renderWelcome(st watchStyles) string {
	inner := max(m.width-2, 0)
	height := m.contentHeight()
	rows := m.welcomeRows(st, true)
	if len(rows) > height {
		rows = m.welcomeRows(st, false)
	}
	top := max((height-len(rows))/2, 0)

	edge := st.vdim
	var b strings.Builder
	b.WriteString(edge("╭"+strings.Repeat("─", inner)+"╮") + "\n")
	for i := 0; i < height; i++ {
		line := ""
		if j := i - top; j >= 0 && j < len(rows) {
			line = rows[j]
		}
		pad := max((inner-lipgloss.Width(line))/2, 0)
		line = padLine(clip(strings.Repeat(" ", pad)+line, inner), inner)
		b.WriteString(edge("│") + line + edge("│") + "\n")
	}
	return b.String()
}

func (m Model) welcomeRows(st watchStyles, big bool) []string {
	rows := make([]string, 0, len(logoLines)+8)
	logo := logoLines
	if big {
		logo = scaleLogo(logo)
	}
	// Every row is padded to the widest so the rows are centred as one block;
	// centred one by one the ragged right edge would shear the mascot.
	width := 0
	for _, l := range logo {
		width = max(width, lipgloss.Width(l))
	}
	for _, l := range logo {
		rows = append(rows, st.vdim(padLine(l, width)))
	}
	rows = append(rows, "")

	k := m.keys()
	var buttons []string
	for _, b := range []key.Binding{k.New, k.Plan} {
		if !b.Enabled() {
			continue
		}
		h := b.Help()
		label := st.Teal.Bold(true).Render(h.Key) + "  " + st.muted(h.Desc)
		buttons = append(buttons, lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(st.Teal.GetForeground()).
			Padding(0, 1).
			Render(label))
	}
	if len(buttons) > 0 {
		parts := make([]string, 0, 2*len(buttons))
		for i, bt := range buttons {
			if i > 0 {
				parts = append(parts, "   ")
			}
			parts = append(parts, bt)
		}
		rows = append(rows, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, parts...), "\n")...)
	}
	return rows
}

// scaleLogo draws the logo twice as big: every cell is doubled across and every
// row down, so each pixel of the mascot stays the shape it was drawn in.
func scaleLogo(lines []string) []string {
	out := make([]string, 0, 2*len(lines))
	for _, l := range lines {
		var b strings.Builder
		for _, r := range l {
			b.WriteRune(r)
			b.WriteRune(r)
		}
		out = append(out, b.String(), b.String())
	}
	return out
}

// quitOnly is the footer's help while welcoming: the buttons already name the
// other keys.
type quitOnly struct{ quit key.Binding }

func (q quitOnly) ShortHelp() []key.Binding  { return []key.Binding{q.quit} }
func (q quitOnly) FullHelp() [][]key.Binding { return [][]key.Binding{q.ShortHelp()} }
