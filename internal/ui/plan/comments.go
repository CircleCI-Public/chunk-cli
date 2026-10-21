package plan

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// comment is the developer's note on one block of the plan, sent to Claude
// with their next message.
type comment struct {
	// block is the index of the block the comment is on, or -1 once Claude
	// has rewritten that block and it is gone. Such a comment is still sent.
	block int
	// quote is the block's markdown, which is how Claude finds what the
	// comment is about.
	quote string
	text  string
}

// reanchor puts each comment back on the block it quotes after the plan has
// been split again.
func (m *Model) reanchor() {
	for i := range m.comments {
		c := &m.comments[i]
		if c.block >= 0 && c.block < len(m.blocks) && m.blocks[c.block].source == c.quote {
			continue
		}
		c.block = -1
		for j, b := range m.blocks {
			if b.source == c.quote {
				c.block = j
				break
			}
		}
	}
}

// commentAt is the index of the comment on block i, or -1.
func (m *Model) commentAt(i int) int {
	for j, c := range m.comments {
		if c.block == i {
			return j
		}
	}
	return -1
}

// composeMessage is what Claude is sent: each comment under the markdown it
// is about, then whatever the developer typed in the chat.
func composeMessage(text string, cs []comment) string {
	if len(cs) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString("I left comments on parts of the plan. Address each one in the plan, and reply about any you disagree with.\n")
	for i, c := range cs {
		fmt.Fprintf(&b, "\n%d. On this part of the plan:\n\n%s\n\nMy comment: %s\n", i+1, quoteLines(c.quote), c.text)
	}
	if text != "" {
		b.WriteString("\n" + text)
	}
	return b.String()
}

// quoteLines makes md a markdown quote, line by line.
func quoteLines(md string) string {
	lines := strings.Split(md, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+l, " ")
	}
	return strings.Join(lines, "\n")
}

// updatePlanKey handles a key while the plan pane has the keyboard.
func (m Model) updatePlanKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := m.keys()
	switch {
	case key.Matches(msg, k.Up):
		m.moveCursor(m.cursor - 1)
	case key.Matches(msg, k.Down):
		m.moveCursor(m.cursor + 1)
	case key.Matches(msg, k.Top):
		m.moveCursor(0)
	case key.Matches(msg, k.Bottom):
		m.moveCursor(len(m.blocks) - 1)
	case key.Matches(msg, k.SectionBack):
		m.moveCursor(m.nextHeading(-1))
	case key.Matches(msg, k.SectionNext):
		m.moveCursor(m.nextHeading(1))
	case key.Matches(msg, k.Next):
		for _, c := range m.comments {
			if c.block > m.cursor {
				m.moveCursor(c.block)
				break
			}
		}
	case key.Matches(msg, k.Comment):
		m.commenting, m.status = true, ""
		m.commentInput.Reset()
		if i := m.commentAt(m.cursor); i >= 0 {
			m.commentInput.SetValue(m.comments[i].text)
			m.commentInput.CursorEnd()
		}
		return m, m.commentInput.Focus()
	case key.Matches(msg, k.Delete):
		i := m.commentAt(m.cursor)
		m.comments = append(m.comments[:i], m.comments[i+1:]...)
		m.drawPlan()
		m.syncPlaceholder()
	default:
		var cmd tea.Cmd
		m.planView, cmd = m.planView.Update(msg)
		return m, cmd
	}
	return m, nil
}

// nextHeading is the next heading's block from the cursor in direction dir,
// or the cursor when there is none.
func (m *Model) nextHeading(dir int) int {
	for i := m.cursor + dir; i >= 0 && i < len(m.blocks); i += dir {
		if m.blocks[i].kind == kindHeading {
			return i
		}
	}
	return m.cursor
}

// updateCommentKey handles a key while a comment is being written.
func (m Model) updateCommentKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := m.keys()
	switch {
	case key.Matches(msg, k.CancelComment):
		m.commenting = false
		m.commentInput.Blur()
		return m, nil
	case key.Matches(msg, k.SaveComment):
		text := strings.TrimSpace(m.commentInput.Value())
		i := m.commentAt(m.cursor)
		switch {
		case text == "" && i >= 0:
			m.comments = append(m.comments[:i], m.comments[i+1:]...)
		case text == "":
		case i >= 0:
			m.comments[i].text = text
		default:
			m.comments = append(m.comments, comment{block: m.cursor, quote: m.blocks[m.cursor].source, text: text})
		}
		m.commenting = false
		m.commentInput.Blur()
		m.drawPlan()
		m.syncPlaceholder()
		return m, nil
	}
	var cmd tea.Cmd
	m.commentInput, cmd = m.commentInput.Update(msg)
	return m, cmd
}

// moveCursor puts the plan's cursor on block i and scrolls to keep the whole
// block in view, or its top when it is taller than the pane.
func (m *Model) moveCursor(i int) {
	if len(m.blocks) == 0 {
		return
	}
	m.cursor = min(max(i, 0), len(m.blocks)-1)
	m.drawPlan()
	top, end := m.blockTop[m.cursor], m.blockEnd[m.cursor]
	h := m.planView.Height()
	switch {
	case top < m.planView.YOffset():
		m.planView.SetYOffset(top)
	case end > m.planView.YOffset()+h:
		m.planView.SetYOffset(min(top, end-h))
	}
}

var (
	cursorBar   = lipgloss.NewStyle().Foreground(accentColor).Render("▌ ")
	commentBar  = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("┃ ")
	noBar       = "  "
	commentText = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

// gutterWidth is the cells left of the plan for the cursor and comment bars.
const gutterWidth = 2

// drawPlan lays out the rendered blocks with the cursor and comments marked,
// and notes where each block starts and ends for scrolling to it.
func (m *Model) drawPlan() {
	if len(m.blocks) == 0 {
		return
	}
	w := m.planView.Width() - gutterWidth
	var lines []string
	m.blockTop = make([]int, len(m.blocks))
	m.blockEnd = make([]int, len(m.blocks))
	for i, b := range m.blocks {
		if i > 0 && !joinsPrevious(m.blocks[i-1].block, b.block) {
			lines = append(lines, "")
		}
		m.blockTop[i] = len(lines)
		ci := m.commentAt(i)
		bar := noBar
		switch {
		case i == m.cursor && m.focus == panePlan:
			bar = cursorBar
		case ci >= 0:
			bar = commentBar
		}
		for _, l := range b.lines {
			lines = append(lines, bar+l)
		}
		if ci >= 0 {
			note := commentText.Width(w - 4).Render("💬 " + m.comments[ci].text)
			for _, l := range strings.Split(note, "\n") {
				lines = append(lines, bar+"  "+l)
			}
		}
		m.blockEnd[i] = len(lines)
	}
	m.planView.SetContentLines(lines)
}

// planFooter is the row under the plan: the comment being written, or what
// is waiting to be sent.
func (m *Model) planFooter() string {
	if m.commenting {
		return m.commentInput.View()
	}
	n, orphaned := len(m.comments), 0
	for _, c := range m.comments {
		if c.block < 0 {
			orphaned++
		}
	}
	switch {
	case n == 0 && m.focus == panePlan:
		return ui.HelperStyle.Render("c to comment on the highlighted part")
	case n == 0:
		return ""
	}
	s := fmt.Sprintf("%d %s to send with your next message", n, plural(n, "comment", "comments"))
	if orphaned > 0 {
		s += fmt.Sprintf(" (%d on parts Claude has since rewritten)", orphaned)
	}
	return ansi.Truncate(commentText.Render(s), m.planView.Width(), "…")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
