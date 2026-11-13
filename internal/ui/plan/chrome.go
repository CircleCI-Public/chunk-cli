package plan

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// The screen's palette: what each colour means is the same everywhere it is
// used, so the header, the plan, the Overview and the action bar read as one.
// Each has a darker twin for a light terminal.
func (m Model) hue(light, dark string) color.Color {
	return lipgloss.LightDark(m.dark)(lipgloss.Color(light), lipgloss.Color(dark))
}

// brand is chunk's accent, for what is focused or selected: the focused
// pane's edge, the active tab, the cursor gutter, the send button. quiet is
// the border of the pane without the keyboard. dim is one step down from
// brand, for the active input inside the focused pane; faint is a further
// step down, for the developer's past messages. amber is the developer's comments; purple is Claude at work;
// green is a plan that is ready.
func (m Model) brand() color.Color  { return theme.New(m.dark).Brand.GetForeground() }
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

func (m Model) narrow() bool { return m.tabbed || m.width < narrowBelow }

// header is the row above the panes: where the developer is, and what is
// under way that the developer is waiting on, in colour. Where the flow is
// and what comes next is the strip's.
func (m Model) header() string {
	var state string
	switch {
	case m.approving:
		state = m.pill(m.brand(), "#ddf4ff", "#10222a", m.spin.View()+"Approving")
	case m.approvingDesign:
		state = m.pill(m.brand(), "#ddf4ff", "#10222a", m.spin.View()+"Approving design")
	case m.busy:
		text := "✦ chunk is drafting"
		if m.narrow() {
			text = "✦ Drafting"
		}
		state = m.pill(m.purple(), "#fbefff", "#22162b", text)
	case m.moving != nil:
		state = m.style(m.dim()).Render("Waiting for factory")
	case strings.TrimSpace(m.plan) == "":
		state = m.style(m.dim()).Render("No plan yet")
	case len(queuedComments(m.comments)) > 0:
		n := len(queuedComments(m.comments))
		text := fmt.Sprintf("● %d %s queued", n, plural(n, "comment", "comments"))
		if m.narrow() {
			text = fmt.Sprintf("● %d %s", n, plural(n, "comment", "comments"))
		}
		state = m.pill(m.amber(), "#fff8c5", "#2a2210", text)
	}

	left := " " + ui.TitleStyle.Render("chunk") + " " + m.style(m.dim()).Render("plan")
	if m.title != "" {
		left += " / " + ansi.Truncate(m.title, max(m.width/2-16, 8), "…")
	} else if m.branch != "" {
		left += m.style(m.dim()).Render(" / " + m.branch)
	}
	var facts []string
	if len(m.structure.Units) > 0 {
		facts = append(facts, fmt.Sprintf("%d %s", len(m.structure.Units), plural(len(m.structure.Units), "unit", "units")))
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

// FlowText is where the flow stands as the status strip says it, "" until
// the screen has read a flow.
func (m Model) FlowText() string {
	if m.state == "" {
		return ""
	}
	f := m.flow
	f.State = m.state
	step, now, next := status(f, m.family, m.inReview())
	parts := []string{step}
	if now != "" {
		parts = append(parts, "Now: "+now)
	}
	if next != "" {
		parts = append(parts, "Next: "+next)
	}
	return strings.Join(parts, " › ")
}

// stripRows is the status strip's height in rows.
const stripRows = 1

// strip is the row under the header: the stage the flow is at, in bold,
// then what is happening now and what comes next.
func (m Model) strip() string {
	f := m.flow
	f.State = m.state
	step, now, next := status(f, m.family, m.inReview())
	if m.moving != nil {
		// The developer has made the move the state asks for; factory has
		// yet to apply it, and the header says so.
		next = ""
	}

	parts := []string{m.style(m.brand()).Bold(true).Render(step)}
	if now != "" {
		parts = append(parts, m.style(m.faint()).Render("Now:")+" "+now)
	}
	if next != "" {
		parts = append(parts, m.style(m.faint()).Render("Next:")+" "+next)
	}
	line := " " + strings.Join(parts, m.style(m.faint()).Render(" · "))
	if m.familyErr != nil {
		// Only Related and the sub-plans go without; the rest of the strip
		// still holds.
		note := " · could not read related plans: " + clean(m.familyErr.Error())
		line += m.style(m.faint()).Render(note)
	}
	return ansi.Truncate(line, m.width, "…")
}

// tabs is the row that stands in for the right pane on a screen too narrow
// for both: which of the Overview and the plan is showing, and what the plan
// has.
func (m Model) tabs() string {
	labels := m.tabLabels()
	tab := func(on bool, text string) string {
		s := lipgloss.NewStyle().Padding(0, tabPad)
		if on {
			return s.Bold(true).Foreground(m.brand()).Background(m.hue("#ddf4ff", "#16222a")).Render(text)
		}
		return s.Foreground(m.dim()).Render(text)
	}
	on := m.narrowTab()
	row := ""
	for i, l := range labels {
		row += " " + tab(i == on, l)
	}
	return ansi.Truncate(row, m.width, "…")
}

// tabPad is the cells either side of a tab's text.
const tabPad = 2

// span is the columns something is drawn in, from start up to end.
type span struct{ start, end int }

func (s span) has(x int) bool { return x >= s.start && x < s.end }

// The narrow screen's tabs, in the order tabs draws them.
const (
	narrowOverview = iota
	narrowPlan
)

// narrowTab is the narrow screen's tab that is showing.
func (m Model) narrowTab() int {
	if m.focus == paneOverview {
		return narrowOverview
	}
	return narrowPlan
}

// tabSpans is the columns each of the narrow screen's tabs takes, as tabs
// draws them.
func (m Model) tabSpans() []span {
	labels := m.tabLabels()
	spans := make([]span, 0, len(labels))
	x := 0
	for _, l := range labels {
		start := x + 1
		x = start + lipgloss.Width(l) + 2*tabPad
		spans = append(spans, span{start, x})
	}
	return spans
}

// tabLabels is the text of the narrow screen's two tabs.
func (m Model) tabLabels() []string {
	left := "Request"
	if m.created {
		left = "Overview"
	}
	if m.busy {
		left += " " + m.spin.View()
	}
	plan := "Plan"
	if len(m.structure.Units) > 0 {
		plan += fmt.Sprintf(" · %d %s", len(m.structure.Units), plural(len(m.structure.Units), "unit", "units"))
	}
	return []string{left, plan}
}

// actionBar is the box under the panes: the keys at hand on the left and, on
// the right, the one thing the developer most likely does next, which changes
// with the state: send comments first, and only then approve.
func (m Model) actionBar() string {
	inner := m.width - borderSize - 2
	edge := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.quiet()).Padding(0, 1)
	if m.moving != nil {
		// A status during the wait is a prompt or an error, so it shows
		// over the waiting line; the border still says factory has yet to
		// move the plan.
		edge = edge.BorderForeground(m.purple())
		if m.status == "" {
			return edge.Width(m.width).Render(m.waitingLine(inner))
		}
	}
	if m.status != "" {
		return edge.Width(m.width).Render(ansi.Truncate(m.status, inner, "…"))
	}

	if m.unfocused {
		// The keyboard is elsewhere, so the bar offers no keys and no button.
		return edge.Width(m.width).Render("")
	}

	_, right := m.barRight()
	k := m.keys()
	// What the buttons say is left out of the hints.
	k.SendPlan.SetEnabled(false)
	k.SendComments.SetEnabled(false)
	room := max(inner-lipgloss.Width(right)-2, 0)
	hints := ansi.Truncate(theme.Help(theme.New(m.dark), room).View(k), room, "…")
	gap := max(inner-lipgloss.Width(hints)-lipgloss.Width(right), 1)
	return edge.Width(m.width).Render(hints + strings.Repeat(" ", gap) + right)
}

// waitingLine is the action bar while factory has yet to apply the event
// the developer sent: what was sent and what happens next, and how long it
// has taken once that is long enough to matter.
func (m Model) waitingLine(w int) string {
	var text string
	switch m.moving.kind {
	case movingComments:
		text = "Comments sent · waiting for factory to hand them to chunk…"
	case movingDesign:
		text = "Design approved · waiting for factory to start the units…"
	case movingApprove:
		text = "Plan approved · waiting for factory to start the build…"
	}
	if m.narrow() {
		text = "Waiting for factory…"
	}
	line := m.style(m.purple()).Bold(true).Render(m.spin.View() + text)
	var took string
	if d := time.Since(m.moving.since); d >= slowWait {
		took = m.style(m.dim()).Render(fmt.Sprintf("%ds", int(d.Seconds())))
	}
	gap := max(w-lipgloss.Width(line)-lipgloss.Width(took), 1)
	return ansi.Truncate(line+strings.Repeat(" ", gap)+took, w, "…")
}

// slowWait is how long the wait for factory goes before the bar says how
// long it has been.
const slowWait = 15 * time.Second

// barRight is the bar's main button and everything on its right: the button
// with the one beside it, when there is room for both.
func (m Model) barRight() (main, right string) {
	inner := m.width - borderSize - 2
	main, side := m.actions()
	right = main
	if side != "" && lipgloss.Width(main)+lipgloss.Width(side)+2 < inner/2 {
		right = main + "  " + side
	}
	return main, right
}

// actions is the bar's main button and the one beside it.
func (m Model) actions() (main, side string) {
	button := func(c color.Color, text string) string {
		return lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(m.hue("#ffffff", "#0d1015")).Background(c).Render(text)
	}
	outline := func(c color.Color, text string) string {
		return lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(c).Render(text)
	}
	queued, open := 0, 0
	for _, c := range m.comments {
		if c.id == "" {
			queued++
		} else {
			open++
		}
	}
	approve := "ctrl+s Approve plan"
	switch {
	// While a comment is written enter saves it and ctrl+s does nothing, so
	// neither button is on offer: the hints name the keys that work.
	case m.approving, m.approvingDesign, m.commenting, m.moving != nil:
		return "", ""
	case m.busy:
		return outline(m.purple(), "chunk is working"), m.style(m.faint()).Render(approve)
	case strings.TrimSpace(m.plan) == "":
		return "", ""
	case queued > 0 && !m.takesComments():
		held := fmt.Sprintf("%d %s held until factory is waiting on you", queued, plural(queued, "comment", "comments"))
		return "", m.style(m.faint()).Render(held)
	case queued > 0:
		save := fmt.Sprintf("s Save %d %s", queued, plural(queued, "comment", "comments"))
		return button(m.brand(), save), m.style(m.faint()).Render(approve + " · resolve comments first")
	case open > 0:
		return "", m.style(m.faint()).Render(approve + " · resolve comments first")
	case !ReadyToApprove(m.flow) && questionsOpen(m.flow):
		return "", m.style(m.faint()).Render("ctrl+s Approve · answer 3. Open Questions first")
	case !ReadyToApprove(m.flow):
		return "", m.style(m.faint()).Render(approve + " · " + m.notReady())
	case approval(m.flow) == planstate.ApproveForUnits:
		return button(m.green(), "ctrl+s Approve design"), ""
	}
	return button(m.green(), approve), ""
}

// inReview is whether factory is still reviewing the plan, so the developer
// waits for that review before the plan can be approved. A plan just made,
// with no state yet, is in review too.
func (m Model) inReview() bool {
	state := clean(m.state)
	if state == "" {
		return !m.flow.Ended
	}
	if state != planstate.RefineProposal && state != planstate.RefineUnits {
		return false
	}
	w := clean(m.flow.WaitingOn)
	return w == planstate.Session || w == planstate.Daemon
}

// notReady says why a plan not ReadyToApprove cannot be approved yet: its
// review is running, or factory's reason, or the state factory has it in,
// named as factory gives it, since chunk may not know it.
func (m Model) notReady() string {
	if m.inReview() {
		return "wait for the review"
	}
	if why := clean(m.flow.WaitingOnWhy); why != "" {
		return why
	}
	return "the plan is " + m.stateText()
}

// notReadyWhy is why ctrl+s cannot approve a plan not ReadyToApprove and
// not in review: factory's reason, or the state it has the plan in.
func (m Model) notReadyWhy() string {
	if why := clean(m.flow.WaitingOnWhy); why != "" {
		return why
	}
	return "it is " + m.stateText()
}

// stateText is the plan's state as factory names it, safe to draw on one
// line.
func (m Model) stateText() string { return clean(m.state) }

// movingText is the move factory has yet to make, for the plan's title.
func (m Model) movingText() string {
	return m.stateText() + " → …"
}
