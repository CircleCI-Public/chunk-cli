package flowlist

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// minPreview is the narrowest preview pane worth drawing. Below it the list
// takes the whole screen; Enter still opens the workspace.
const minPreview = 30

// Content draws the flow list, its footer included, within the last
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
	rightWidth := m.previewWidth()
	if rightWidth > 0 {
		listWidth = theme.ListWidth
	}
	left := m.styles.Pane(true, listWidth+2, h, m.styles.Emphasis.Render("flows"),
		strings.Join(fit(m.listLines(listWidth, contentHeight), listWidth, contentHeight), "\n"))
	body := left
	if rightWidth > 0 {
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
		note := "flows unavailable"
		if m.loaded {
			note += " (stale)"
		}
		rows = append(rows, "  "+m.styles.Error.Render(note+": "+oneLine(m.listErr.Error())))
	}
	if m.parentsErr != nil {
		rows = append(rows, "  "+m.styles.Warning.Render("sub-plans unavailable: "+oneLine(m.parentsErr.Error())))
	}
	return rows
}

func (m Model) listLines(width, height int) []string {
	st := m.styles
	var rows []string
	selected, lastGroup, lastPhase := 0, -1, ""
	flows, depth := m.visible()
	counts := make([]int, 4)
	for _, f := range flows {
		counts[group(f.State)]++
	}
	for _, f := range flows {
		g := group(f.State)
		if g != lastGroup {
			if lastGroup != -1 {
				rows = append(rows, "")
			}
			rows = append(rows, sectionHeading(st, g, counts[g]))
			lastGroup, lastPhase = g, ""
		}
		// A sub-heading is spelled as the first flow in it spells its phase.
		if p := phase(f.State); p != "" && !samePhase(p, lastPhase) {
			rows = append(rows, " "+st.Dim.Render(oneLine(p)))
			lastPhase = p
		}
		title := oneLine(f.Title)
		if title == "" {
			title = "Untitled plan"
		}
		indent := strings.Repeat("  ", depth[f.ID])
		title = ansi.Truncate(title, max(width-2-len(indent), 1), "…")
		if f.ID == m.id {
			selected = len(rows)
			rows = append(rows, st.Success.Render("▶ ")+indent+st.Emphasis.Render(title))
		} else {
			rows = append(rows, "  "+indent+title)
		}
		var detail []string
		// Only Other's heading does not say the state.
		if g == 3 {
			detail = append(detail, st.VDim.Render(stateText(f.State)))
		}
		if p := m.parentTitle(f.ID); p != "" {
			detail = append(detail, st.VDim.Render("↳ "+p))
		}
		if slices.Contains(f.ApplicableEvents, circleci.ResolveConflicts) {
			detail = append(detail, st.Warning.Render("⚠"))
		}
		if f.Task != nil && f.Task.Category != "" {
			detail = append(detail, st.VDim.Render(oneLine(f.Task.Category)))
		}
		if f.SessionID != "" {
			detail = append(detail, st.VDim.Render("claimed"))
		}
		if !f.UpdatedAt.IsZero() {
			detail = append(detail, st.VDim.Render(ago(f.UpdatedAt)))
		}
		// A flow with no details keeps its blank second row, so the scroll
		// below can count on two rows a flow.
		rows = append(rows, "  "+indent+strings.Join(detail, st.VDim.Render(" · ")))
	}
	if len(rows) == 0 {
		message := []string{"  " + st.Dim.Render("no open flows"), "  " + st.Dim.Render("n starts one")}
		switch {
		case !m.loaded && m.listErr != nil:
			message = []string{"  " + st.Dim.Render("retrying…")}
		case !m.loaded:
			message = []string{"  " + st.Dim.Render("loading flows…")}
		}
		rows = message
	}
	// The rows scroll to keep the selected flow's title and state line in
	// view. A phase sub-heading is above the title, so may scroll off.
	start := max(0, selected+2-height)
	start = min(start, max(len(rows)-height, 0))
	return rows[start:]
}

// sectionHeading is section g's label, coloured by who its flows wait on,
// and how many flows it holds.
func sectionHeading(st theme.Styles, g, n int) string {
	labels := []string{"Needs your review", "With agent", "Not started", "Other"}
	style := st.Emphasis
	switch g {
	case 0:
		style = st.Warning.Bold(true)
	case 1:
		style = st.Brand.Bold(true)
	}
	return style.Render(labels[g]) + "  " + st.VDim.Render(strconv.Itoa(n))
}

// stateText is the state of a flow no heading names, for the Other section:
// its whole state lowercased, or "unknown".
func stateText(state string) string {
	phase, _ := planstate.Parse(oneLine(state))
	if phase == "" {
		return "unknown"
	}
	return strings.ToLower(phase)
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
		return append(rows, m.styles.Dim.Render("Select a flow to preview."))
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
	entry := m.cache[m.id]
	commentsHeading := false
	for _, c := range entry.comments {
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
	if m.previewLoaded && strings.TrimSpace(entry.text) == "" {
		return append(rows, m.styles.Dim.Render("No plan text yet."))
	}
	return append(rows, entry.lines...)
}

// previewWidth is the preview pane's width inside its border and leading
// space, as Content draws it, or 0 when the pane is not drawn.
func (m Model) previewWidth() int {
	if m.width-theme.ListWidth-4 < minPreview {
		return 0
	}
	return m.width - theme.ListWidth - 5
}

// glamourLines is text rendered as markdown wrapped at width, in the dark or
// light style, as lines; text as it is when glamour fails.
func glamourLines(text string, width int, dark bool) []string {
	style := "light"
	if dark {
		style = "dark"
	}
	text = clean(text)
	if r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(max(width, 1))); err == nil {
		if rendered, err := r.Render(text); err == nil {
			text = strings.TrimSpace(rendered)
		}
	}
	return strings.Split(text, "\n")
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
