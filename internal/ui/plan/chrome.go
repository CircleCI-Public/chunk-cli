package plan

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// The screen's palette: what each colour means is the same everywhere it is
// used, so the header, the plan, the chat and the action bar read as one.
// Each has a darker twin for a light terminal.
func (m Model) hue(light, dark string) color.Color {
	return lipgloss.LightDark(m.dark)(lipgloss.Color(light), lipgloss.Color(dark))
}

// teal is what has the keyboard, in chunk's brand accent so the screen looks
// like chunk watch's (the name is watch's too); quiet is the border of what
// doesn't have it. amber is the developer's comments; purple is Claude at
// work; green is a plan that is ready.
func (m Model) teal() color.Color   { return theme.New(m.dark).Teal.GetForeground() }
func (m Model) quiet() color.Color  { return theme.New(m.dark).VDim.GetForeground() }
func (m Model) amber() color.Color  { return m.hue("#9a6700", "#f0c16a") }
func (m Model) purple() color.Color { return m.hue("#8250df", "#c792ea") }
func (m Model) green() color.Color  { return m.hue("#1a7f37", "#7ee08a") }
func (m Model) dim() color.Color    { return m.hue("#57606a", "#8c96a8") }
func (m Model) faint() color.Color  { return m.hue("#8c959f", "#5f6a7d") }

func (m Model) style(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// pill is text on a tinted ground, for a state.
func (m Model) pill(fg color.Color, bgLight, bgDark, text string) string {
	return lipgloss.NewStyle().Foreground(fg).Background(m.hue(bgLight, bgDark)).Padding(0, 1).Render(text)
}

// narrowBelow is the width under which the panes take turns on the screen
// instead of sharing it.
const narrowBelow = 100

func (m Model) narrow() bool { return m.width < narrowBelow }

// header is the row above the panes: where the developer is, what Claude is
// doing, and whether the plan can go. The state is the one thing here that
// changes what the developer does next, so it is the part in colour.
func (m Model) header() string {
	var state string
	switch {
	case m.sending:
		state = m.pill(m.teal(), "#ddf4ff", "#10222a", m.spin.View()+"Sending to the factory")
	case m.busy:
		text := "✦ chunk is drafting"
		if m.narrow() {
			text = "✦ Drafting"
		}
		state = m.pill(m.purple(), "#fbefff", "#22162b", text)
	case strings.TrimSpace(m.plan) == "":
		state = m.style(m.dim()).Render("No plan yet")
	case len(m.comments) > 0:
		n := len(m.comments)
		text := fmt.Sprintf("● %d %s waiting for chunk", n, plural(n, "comment", "comments"))
		if m.narrow() {
			text = fmt.Sprintf("● %d %s", n, plural(n, "comment", "comments"))
		}
		state = m.pill(m.amber(), "#fff8c5", "#2a2210", text)
	default:
		state = m.pill(m.green(), "#dafbe1", "#162226", ui.IconOK+" Plan ready")
	}

	left := " " + ui.TitleStyle.Render("chunk") + " " + m.style(m.dim()).Render("plan")
	if m.branch != "" {
		left += m.style(m.dim()).Render(" / " + m.branch)
	}
	var facts []string
	if m.units > 0 {
		facts = append(facts, fmt.Sprintf("%d %s", m.units, plural(m.units, "unit", "units")))
	}
	if m.questions > 0 {
		facts = append(facts, fmt.Sprintf("%d open %s", m.questions, plural(m.questions, "question", "questions")))
	}
	right := "  " + m.style(m.dim()).Render(strings.Join(facts, " · ")) + " "
	if m.busy || len(facts) == 0 {
		right = ""
	}

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(state) - lipgloss.Width(right)
	if gap < 2 {
		right, gap = "", m.width-lipgloss.Width(left)-lipgloss.Width(state)
	}
	if gap < 2 {
		return ansi.Truncate(left+"  "+state, m.width, "…")
	}
	return left + strings.Repeat(" ", gap) + state + right
}

// tabs is the row that stands in for the second pane on a screen too narrow
// for both: which pane is showing, and what the other one has for it.
func (m Model) tabs() string {
	tab := func(on bool, text string) string {
		s := lipgloss.NewStyle().Padding(0, 2)
		if on {
			return s.Bold(true).Foreground(m.teal()).Background(m.hue("#ddf4ff", "#16222a")).Render(text)
		}
		return s.Foreground(m.dim()).Render(text)
	}
	chat := "Chat"
	if m.busy {
		chat += " " + m.spin.View()
	}
	plan := "Plan"
	if m.units > 0 {
		plan += fmt.Sprintf(" · %d %s", m.units, plural(m.units, "unit", "units"))
	}
	return ansi.Truncate(" "+tab(m.focus == paneChat, chat)+" "+tab(m.focus == panePlan, plan), m.width, "…")
}

// actionBar is the box under the panes: the keys at hand on the left and, on
// the right, the one thing the developer most likely does next, which changes
// with the state: send comments first, and only then approve.
func (m Model) actionBar() string {
	inner := m.width - borderSize - 2
	edge := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.quiet()).Padding(0, 1)
	if m.status != "" {
		return edge.Width(m.width).Render(ansi.Truncate(m.status, inner, "…"))
	}

	main, side := m.actions()
	right := main
	if side != "" && lipgloss.Width(main)+lipgloss.Width(side)+2 < inner/2 {
		right = main + "  " + side
	}
	k := m.keys()
	// What the buttons say is left out of the hints.
	k.Stop.SetEnabled(false)
	k.SendPlan.SetEnabled(false)
	k.SendComments.SetEnabled(false)
	room := max(inner-lipgloss.Width(right)-2, 0)
	hints := ansi.Truncate(theme.Help(theme.New(m.dark), room).View(k), room, "…")
	gap := max(inner-lipgloss.Width(hints)-lipgloss.Width(right), 1)
	return edge.Width(m.width).Render(hints + strings.Repeat(" ", gap) + right)
}

// actions is the bar's main button and the one beside it.
func (m Model) actions() (main, side string) {
	button := func(c color.Color, text string) string {
		return lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(m.hue("#ffffff", "#0d1015")).Background(c).Render(text)
	}
	outline := func(c color.Color, text string) string {
		return lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(c).Render(text)
	}
	n := len(m.comments)
	approve := "ctrl+s Approve plan"
	switch {
	// While a comment is written enter saves it and ctrl+s does nothing, so
	// neither button is on offer: the hints name the keys that work.
	case m.sending, m.commenting:
		return "", ""
	case m.busy:
		return outline(m.purple(), "esc Stop chunk"), m.style(m.faint()).Render(approve)
	case strings.TrimSpace(m.plan) == "":
		return "", ""
	case n > 0:
		send := fmt.Sprintf("enter Send %d %s to chunk", n, plural(n, "comment", "comments"))
		if m.focus == panePlan {
			send = "s " + strings.TrimPrefix(send, "enter ")
		}
		return button(m.teal(), send), m.style(m.faint()).Render(approve + " · resolve comments first")
	}
	return button(m.green(), approve), ""
}
