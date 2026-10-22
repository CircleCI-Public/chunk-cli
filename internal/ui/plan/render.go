package plan

import (
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// The plan Claude writes has a fixed shape (see planSystemPrompt): numbered
// sections, the last of which, Units, is the work. The screen sets the
// sections apart and shows each unit as a step; the rest is markdown as
// glamour renders it.

var (
	sectionNumRe = regexp.MustCompile(`^\d+\.\s*`)
	unitNumRe    = regexp.MustCompile(`(?i)^unit\s*\d*\s*[:.\-–—]?\s*`)
)

// planStats is what the walk over the plan's blocks learns on the way.
type planStats struct {
	section          string
	units, questions int
}

// styled draws a heading in the screen's own style, or reports false for a
// block that is left to glamour.
func (m Model) styled(b block, st *planStats, w int) ([]string, bool) {
	if b.kind != kindHeading {
		if b.kind == kindListItem && strings.Contains(st.section, "question") {
			st.questions++
		}
		return nil, false
	}
	level := len(b.source) - len(strings.TrimLeft(b.source, "#"))
	text := strings.TrimSpace(strings.TrimLeft(b.source, "#"))
	switch level {
	case 1:
		return []string{lipgloss.NewStyle().Bold(true).Foreground(m.hue("#1f2328", "#ffffff")).Render(text)}, true
	case 2:
		name := sectionNumRe.ReplaceAllString(text, "")
		st.section = strings.ToLower(name)
		c := m.dim()
		if strings.Contains(st.section, "question") {
			c = m.amber()
		}
		label := strings.ToUpper(name)
		rule := max(w-ansi.StringWidth(label)-2, 0)
		return []string{m.style(c).Bold(true).Render(label) + " " + m.style(m.faint()).Render(strings.Repeat("─", rule))}, true
	}
	if !strings.Contains(st.section, "unit") {
		return nil, false
	}
	st.units++
	name := unitNumRe.ReplaceAllString(text, "")
	if name == "" {
		name = text
	}
	num := m.style(m.faint()).Render(strconv.Itoa(st.units))
	return []string{m.style(m.dim()).Render("○ ") + num + "  " + lipgloss.NewStyle().Bold(true).Render(name)}, true
}

// skeleton is the plan pane while Claude writes the first plan: the shape of
// what is coming, in place of an empty box.
func (m Model) skeleton(w int) string {
	bar := func(n int) string {
		return m.style(m.hue("#d0d7de", "#1d2330")).Render(strings.Repeat("█", min(n, w)))
	}
	rows := []string{
		bar(34), "",
		m.style(m.faint()).Render("OUTCOME"), bar(60), bar(44), "",
		m.style(m.faint()).Render("SPEC/DECISIONS/SCOPE"), bar(52), bar(38), "",
		m.style(m.faint()).Render("UNITS"),
		m.style(m.faint()).Render("○ ") + bar(30), m.style(m.faint()).Render("○ ") + bar(42), m.style(m.faint()).Render("○ ") + bar(26),
	}
	return strings.Join(rows, "\n")
}

var (
	inlineRe   = regexp.MustCompile("(`[^`]+`|\\*\\*[^*]+\\*\\*|\\[(?:USER|AGENT)\\]|\\[[^\\]]+\\]\\([^)]+\\))")
	linkRe     = regexp.MustCompile(`^\[([^\]]+)\]\(([^)]+)\)$`)
	markerRe   = regexp.MustCompile(`^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$`)
	checkboxRe = regexp.MustCompile(`^\[( |x|X)\]\s+`)
	langRe     = regexp.MustCompile("^\\s*(?:```+|~~~+)\\s*(\\S*)")
)

// inline styles what a line of markdown marks up: code, bold, links, and the
// [USER] and [AGENT] tags that say who made a decision.
func (m Model) inline(s string) string {
	return inlineRe.ReplaceAllStringFunc(s, func(t string) string {
		switch {
		case t == "[USER]":
			return m.style(m.teal()).Bold(true).Render(t)
		case t == "[AGENT]":
			return m.style(m.purple()).Bold(true).Render(t)
		case strings.HasPrefix(t, "`"):
			return lipgloss.NewStyle().Foreground(m.green()).Background(m.sunk()).Render(" " + strings.Trim(t, "`") + " ")
		case strings.HasPrefix(t, "**"):
			return lipgloss.NewStyle().Bold(true).Render(strings.Trim(t, "*"))
		}
		if l := linkRe.FindStringSubmatch(t); l != nil {
			return lipgloss.NewStyle().Underline(true).Foreground(m.teal()).Render(l[1])
		}
		return t
	})
}

// wrapped is s inline-styled and wrapped to w, as lines.
func (m Model) wrapped(s string, w int) []string {
	return strings.Split(ansi.Wrap(m.inline(s), max(w, 1), ""), "\n")
}

// codeCard is code set on a ground of its own, with the language, if given,
// in the top corner.
func (m Model) codeCard(lang string, code []string, w int) []string {
	fg := lipgloss.NewStyle().Foreground(m.hue("#24292f", "#c9d1d9"))
	head := ""
	if lang != "" {
		head = m.style(m.faint()).Render(lang)
	}
	rows := []string{head}
	for _, l := range code {
		l = strings.ReplaceAll(l, "\t", "    ")
		for _, part := range strings.Split(ansi.Hardwrap(l, max(w-2, 1), true), "\n") {
			rows = append(rows, fg.Render(part))
		}
	}
	rows = append(rows, "")
	for i, r := range rows {
		rows[i] = paint(" "+r, w, m.sunk())
	}
	return rows
}

// row is one line of a list: its depth, its marker, and its words.
type row struct {
	depth  int
	marker string
	text   string
}

// blockLines draws a paragraph, list item, code block or rule in the
// screen's own style, or reports false for a block that is left to glamour:
// tables and quotes.
func (m Model) blockLines(b block, w int) ([]string, bool) {
	src := strings.Split(b.source, "\n")
	switch b.kind {
	case kindRule:
		return []string{m.style(m.faint()).Render(strings.Repeat("─", w))}, true
	case kindCode:
		var lang string
		if l := langRe.FindStringSubmatch(src[0]); l != nil {
			lang = l[1]
		}
		body := src[1:]
		if n := len(body); n > 0 && fenceRe.MatchString(body[n-1]) {
			body = body[:n-1]
		}
		return m.codeCard(lang, body, w), true
	case kindParagraph:
		if strings.HasPrefix(strings.TrimSpace(src[0]), "|") || strings.HasPrefix(strings.TrimSpace(src[0]), ">") {
			return nil, false
		}
		return m.wrapped(strings.Join(trimAll(src), " "), w), true
	case kindListItem:
		return m.listLines(src, w), true
	case kindHeading:
		// Drawn by styled.
	}
	return nil, false
}

func trimAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.TrimSpace(s)
	}
	return out
}

// listLines draws a list item and what is under it: nested items, the item's
// own further paragraphs, and code.
func (m Model) listLines(src []string, w int) []string {
	var (
		out   []string
		cur   *row
		fence bool
		lang  string
		code  []string
		depth int
	)
	flush := func() {
		if cur == nil {
			return
		}
		pad := strings.Repeat("  ", cur.depth)
		lead := pad + cur.marker + " "
		hang := strings.Repeat(" ", lipgloss.Width(lead))
		for i, l := range m.wrapped(cur.text, w-lipgloss.Width(lead)) {
			if i == 0 {
				out = append(out, lead+l)
				continue
			}
			out = append(out, hang+l)
		}
		cur = nil
	}
	for _, line := range src {
		if fence {
			if fenceRe.MatchString(line) {
				fence = false
				pad := strings.Repeat("  ", depth+1)
				for _, l := range m.codeCard(lang, code, w-len(pad)) {
					out = append(out, pad+l)
				}
				code = nil
				continue
			}
			code = append(code, strings.TrimPrefix(line, strings.Repeat(" ", min(len(line)-len(strings.TrimLeft(line, " ")), 2*(depth+1)))))
			continue
		}
		if fenceRe.MatchString(line) {
			flush()
			fence = true
			lang = langRe.FindStringSubmatch(line)[1]
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if mk := markerRe.FindStringSubmatch(line); mk != nil {
			flush()
			depth = len(mk[1]) / 2
			text, marker := mk[3], m.style(m.dim()).Render("•")
			if depth > 0 {
				marker = m.style(m.dim()).Render("◦")
			}
			if len(mk[2]) > 1 || mk[2][0] >= '0' && mk[2][0] <= '9' {
				marker = m.style(m.dim()).Render(mk[2])
			}
			if cb := checkboxRe.FindStringSubmatch(text); cb != nil {
				text = strings.TrimPrefix(text, cb[0])
				marker = m.style(m.dim()).Render("○")
				if cb[1] != " " {
					marker = m.style(m.green()).Render(ui.IconOK)
				}
			}
			cur = &row{depth: depth, marker: marker, text: text}
			continue
		}
		if cur != nil {
			cur.text += " " + strings.TrimSpace(line)
			continue
		}
		cur = &row{depth: depth + 1, marker: " ", text: strings.TrimSpace(line)}
	}
	flush()
	return out
}
