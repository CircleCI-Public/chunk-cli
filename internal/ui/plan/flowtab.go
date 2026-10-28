package plan

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// The Flow tab is where the plan is in factory, beside the plan's text:
// its progress through its units or sub-plans, its pull request, and the
// plans related to it.

// relatedRow is a row of the Flow tab's Related section: a relative, and
// how it is related.
type relatedRow struct {
	label string
	Relative
}

// relatedRows is the plan's relatives as Related lists them: its parent,
// then its open sub-plans, then the plans blocking it.
func (m Model) relatedRows() []relatedRow {
	var rows []relatedRow
	if p := m.family.Parent; p != nil {
		rows = append(rows, relatedRow{"Part of", *p})
	}
	for _, c := range m.family.Children {
		rows = append(rows, relatedRow{"Sub-plan", c})
	}
	for _, b := range m.family.Blockers {
		rows = append(rows, relatedRow{"Blocked by", b})
	}
	return rows
}

// updateFlowKey handles a key for the Flow tab: ↑/↓ move over the Related
// rows, the page keys scroll, and enter opens the row under the cursor. The
// plan's own keys do nothing here.
func (m Model) updateFlowKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	k := m.keys()
	rows := m.relatedRows()
	switch {
	case key.Matches(msg, k.Up):
		m.related = max(m.related-1, 0)
	case key.Matches(msg, k.Down):
		m.related = max(min(m.related+1, len(rows)-1), 0)
	case key.Matches(msg, k.PageUp):
		m.flowView.PageUp()
	case key.Matches(msg, k.PageDown):
		m.flowView.PageDown()
	case key.Matches(msg, k.Open):
		if m.related >= len(rows) {
			return m, nil
		}
		r := rows[m.related]
		return m, func() tea.Msg { return OpenMsg{ID: r.ID, Title: r.Title} }
	default:
		return m, nil
	}
	m.renderFlow()
	return m, nil
}

// renderFlow draws the Flow tab into its view.
func (m *Model) renderFlow() {
	w := m.flowView.Width()
	if w <= 0 {
		return
	}
	m.flowView.SetContent(m.flowContent(w))
}

// flowContent is the Flow tab, w cells wide.
func (m Model) flowContent(w int) string {
	fit := func(s string) string { return ansi.Truncate(s, w, "…") }
	lines := []string{m.sectionHeading("Progress", m.dim(), w)}
	lines = append(lines, m.progress()...)

	if pr := clean(m.flow.PR); pr != "" {
		lines = append(lines, "", m.sectionHeading("Pull request", m.dim(), w),
			lipgloss.NewStyle().Underline(true).Foreground(m.brand()).Render(pr))
		for _, d := range m.flow.Deviations {
			lines = append(lines, m.style(m.dim()).Render("• ")+clean(d))
		}
	}

	if rows := m.relatedRows(); len(rows) > 0 {
		lines = append(lines, "", m.sectionHeading("Related", m.dim(), w))
		cursor := m.inbox && m.focus == panePlan && m.tab == tabFlow
		for i, r := range rows {
			g := "  "
			if cursor && i == m.related {
				g = m.style(m.brand()).Render("▶ ")
			}
			label := m.style(m.faint()).Render(fmt.Sprintf("%-10s", r.label))
			lines = append(lines, g+label+" "+lipgloss.NewStyle().Bold(true).Render(clean(r.Title))+
				"  "+m.style(m.dim()).Render(clean(r.State)))
		}
	}

	for i, l := range lines {
		lines[i] = fit(l)
	}
	return strings.Join(lines, "\n")
}

// progress is the Progress section's rows: the plan's open sub-plans when
// it has any, or else its units, each marked done, under way or to do.
func (m Model) progress() []string {
	if kids := m.family.Children; len(kids) > 0 {
		n := len(kids)
		rows := make([]string, 0, 1+n)
		rows = append(rows, m.style(m.dim()).Render(fmt.Sprintf("Progress · %d open %s", n, plural(n, "sub-plan", "sub-plans"))))
		for _, c := range kids {
			marker := m.style(m.dim()).Render("○ ")
			if phase, _ := planstate.Parse(clean(c.State)); strings.EqualFold(phase, "Building") || strings.EqualFold(phase, "Integrating") {
				marker = m.style(m.brand()).Render("▸ ")
			}
			rows = append(rows, marker+lipgloss.NewStyle().Bold(true).Render(clean(c.Title))+
				"  "+m.style(m.dim()).Render(clean(c.State)))
		}
		return rows
	}
	units := Units(m.plan)
	if len(units) == 0 {
		return []string{m.style(m.dim()).Render("No units")}
	}
	rows := make([]string, len(units))
	for i, u := range units {
		marker := m.style(m.dim()).Render("○ ")
		switch {
		case u.Done:
			marker = m.style(m.green()).Render(ui.IconOK + " ")
		case u.Name == m.flow.Unit:
			marker = m.style(m.brand()).Render("▸ ")
		}
		name := unitNumRe.ReplaceAllString(u.Name, "")
		if name == "" {
			name = u.Name
		}
		num := m.style(m.faint()).Render(strconv.Itoa(i + 1))
		rows[i] = marker + num + "  " + lipgloss.NewStyle().Bold(true).Render(name)
	}
	return rows
}

// sectionHeading is name as a section of the plan is headed: in capitals, in
// c, with a rule to w.
func (m Model) sectionHeading(name string, c color.Color, w int) string {
	label := strings.ToUpper(name)
	rule := max(w-ansi.StringWidth(label)-2, 0)
	return m.style(c).Bold(true).Render(label) + " " + m.style(m.faint()).Render(strings.Repeat("─", rule))
}
