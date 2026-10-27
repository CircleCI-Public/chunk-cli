package planlist

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// minPreview is the narrowest preview pane worth drawing. Below it the list
// takes the whole screen; Enter still opens the workspace.
const minPreview = 30

// Content draws the inbox, its footer included, within the last
// WindowSizeMsg. The host draws the header above it.
func (m Model) Content() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	footer := m.footerLines()
	// The panes' top and bottom edges.
	contentHeight := m.height - len(footer) - 2
	if contentHeight < 1 {
		return strings.Join(fit(footer, m.width, min(len(footer), m.height)), "\n")
	}
	h := contentHeight + 2
	listWidth := m.width - 2
	if m.width-theme.ListWidth-4 >= minPreview {
		listWidth = theme.ListWidth
	}
	left := m.styles.Pane(true, listWidth+2, h, m.styles.Emphasis.Render("plans"),
		strings.Join(fit(m.listLines(listWidth, contentHeight), listWidth, contentHeight), "\n"))
	body := left
	if listWidth == theme.ListWidth {
		// The right box's border, then one leading space.
		rightWidth := m.width - listWidth - 5
		lines := fit(m.previewLines(rightWidth), rightWidth, contentHeight)
		for i, l := range lines {
			lines[i] = " " + l
		}
		title := m.styles.VDim.Render("preview")
		if m.id != "" {
			title += m.styles.VDim.Render(" · " + oneLine(m.id))
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top, left,
			m.styles.Pane(false, m.width-listWidth-2, h, title, strings.Join(lines, "\n")))
	}
	rows := append(strings.Split(body, "\n"), footer...)
	return strings.Join(fit(rows, m.width, m.height), "\n")
}

// footerLines is the help bar, then the host's note and a list error, each on
// a line of its own.
func (m Model) footerLines() []string {
	bar := theme.Help(m.styles, m.width-2).View(m.keys())
	rows := []string{"  " + bar}
	if m.note != "" {
		rows = append(rows, "  "+m.styles.Muted.Render(m.note))
	}
	if m.listErr != nil {
		note := "plans unavailable"
		if m.loaded {
			note += " (stale)"
		}
		rows = append(rows, "  "+m.styles.Error.Render(note+": "+oneLine(m.listErr.Error())))
	}
	return rows
}

func (m Model) listLines(width, height int) []string {
	st := m.styles
	var rows []string
	selected, lastGroup := 0, -1
	labels := []string{"Needs your review", "With agent", "Not started", "Other"}
	for _, p := range m.visible() {
		g := group(p.State)
		if g != lastGroup {
			if lastGroup != -1 {
				rows = append(rows, "")
			}
			rows = append(rows, st.Dim.Render(labels[g]))
			lastGroup = g
		}
		title := oneLine(p.Title)
		if title == "" {
			title = "Untitled plan"
		}
		title = ansi.Truncate(title, max(width-2, 1), "…")
		if p.ID == m.id {
			selected = len(rows)
			rows = append(rows, st.Success.Render("▶ ")+st.Emphasis.Render(title))
		} else {
			rows = append(rows, "  "+title)
		}
		chip := stateChip(st, p.State, width-4)
		var detail []string
		if p.OpenComments > 0 {
			detail = append(detail, st.Warning.Render(fmt.Sprintf("● %d", p.OpenComments)))
		}
		if !p.UpdatedAt.IsZero() {
			detail = append(detail, st.VDim.Render(ago(p.UpdatedAt)))
		}
		line := "  " + chip
		if len(detail) > 0 {
			line += " " + strings.Join(detail, st.VDim.Render(" · "))
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		message := []string{"  " + st.Dim.Render("no open plans"), "  " + st.Dim.Render("n starts one")}
		switch {
		case !m.loaded && m.listErr != nil:
			message = []string{"  " + st.Dim.Render("retrying…")}
		case !m.loaded:
			message = []string{"  " + st.Dim.Render("loading plans…")}
		}
		rows = message
	}
	// The rows scroll to keep the selected plan's title and state line in view.
	start := max(0, selected+2-height)
	start = min(start, max(len(rows)-height, 0))
	return rows[start:]
}

// stateChip is the plan's state as a coloured block, as the run list shows a
// run's: amber when it waits on you, the brand yellow while an agent has it.
func stateChip(st theme.Styles, state string, width int) string {
	text := strings.ToLower(oneLine(state))
	text = strings.TrimSuffix(strings.TrimSuffix(text, " - human"), " - agent")
	if text == "" {
		text = "unknown"
	}
	var bg color.Color
	switch group(state) {
	case 0:
		bg = lipgloss.Color("3")
	case 1:
		bg = theme.Yellow
	case 2:
		bg = lipgloss.Color("250")
	default:
		bg = theme.Green
	}
	return st.Chip.Background(bg).Render(" " + ansi.Truncate(text, max(width-2, 1), "…") + " ")
}

// ago is how long since t, as the run list says it.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (m Model) previewLines(width int) []string {
	var rows []string
	if m.id == "" {
		return append(rows, m.styles.Dim.Render("Select a plan to preview."))
	}
	// Select may be called between updates. Never display the previous plan's
	// content under the newly selected row while its request is being started.
	if m.previewID != m.id || !m.previewLoaded && m.previewErr == nil {
		return append(rows, m.styles.Dim.Render("Loading preview…"))
	}
	if m.previewErr != nil {
		note := "Preview error"
		if m.previewLoaded {
			note += " (stale)"
		}
		note += ": " + oneLine(m.previewErr.Error())
		rows = append(rows, m.styles.Warning.Render(note))
	}
	commentsHeading := false
	for _, c := range m.comments {
		if c.ResolvedAt != nil {
			continue
		}
		if !commentsHeading {
			rows = append(rows, m.styles.Warning.Render("Open comments (not plan text)"))
			commentsHeading = true
		}
		for _, line := range strings.Split(ansi.Hardwrap(clean(c.Body), max(width-2, 1), true), "\n") {
			rows = append(rows, m.styles.Warning.Render("> "+line))
		}
		if c.QuotedText != "" {
			rows = append(rows, m.styles.Dim.Render("  About: "+oneLine(c.QuotedText)))
		}
		rows = append(rows, "")
	}
	rows = append(rows, m.styles.Emphasis.Render("Plan"))
	if m.previewLoaded && strings.TrimSpace(m.text) == "" {
		return append(rows, m.styles.Dim.Render("No plan text yet."))
	}
	style := "light"
	if m.dark {
		style = "dark"
	}
	text := clean(m.text)
	renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(max(width-4, 1)))
	if err == nil {
		if rendered, renderErr := renderer.Render(text); renderErr == nil {
			text = strings.TrimSpace(rendered)
		}
	}
	return append(rows, strings.Split(text, "\n")...)
}

func oneLine(s string) string { return strings.Join(strings.Fields(clean(s)), " ") }

func fit(rows []string, width, height int) []string {
	out := make([]string, height)
	for i := range min(len(rows), height) {
		out[i] = ansi.Truncate(rows[i], width, "…")
		// Glamour and lipgloss may append a newline while styling a line.
		out[i] = strings.TrimRight(out[i], "\n")
	}
	return out
}
