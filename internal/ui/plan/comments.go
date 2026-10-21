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
// been split again. Two comments never share a block, so of several blocks
// with the same markdown each comment takes a different one.
func (m *Model) reanchor() {
	taken := map[int]bool{}
	var lost []int
	for i := range m.comments {
		c := &m.comments[i]
		if c.block >= 0 && c.block < len(m.blocks) && m.blocks[c.block].source == c.quote && !taken[c.block] {
			taken[c.block] = true
			continue
		}
		lost = append(lost, i)
	}
	for _, i := range lost {
		c := &m.comments[i]
		c.block = -1
		for j, b := range m.blocks {
			if b.source == c.quote && !taken[j] {
				c.block, taken[j] = j, true
				break
			}
		}
	}
}

// orphans are the indexes in m.comments of comments whose block Claude has
// rewritten, in the order they are drawn after the last block.
func (m *Model) orphans() []int {
	var out []int
	for i, c := range m.comments {
		if c.block < 0 {
			out = append(out, i)
		}
	}
	return out
}

// stops is how many places the plan's cursor can be: every block, then every
// orphaned comment.
func (m *Model) stops() int { return len(m.blocks) + len(m.orphans()) }

// commentAt is the index of the comment on stop i, or -1. A stop past the
// last block is an orphaned comment.
func (m *Model) commentAt(i int) int {
	if i >= len(m.blocks) {
		if o := m.orphans(); i-len(m.blocks) < len(o) {
			return o[i-len(m.blocks)]
		}
		return -1
	}
	for j, c := range m.comments {
		if c.block == i {
			return j
		}
	}
	return -1
}

// stopOf is the cursor stop comment c is drawn at, or -1.
func (m *Model) stopOf(c comment) int {
	if c.block >= 0 {
		return c.block
	}
	for k, j := range m.orphans() {
		if m.comments[j] == c {
			return len(m.blocks) + k
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
func (m Model) updatePlanKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	k := m.keys()
	switch {
	case key.Matches(msg, k.Up):
		m.moveCursor(m.cursor - 1)
	case key.Matches(msg, k.Down):
		m.moveCursor(m.cursor + 1)
	case key.Matches(msg, k.Top):
		m.moveCursor(0)
	case key.Matches(msg, k.Bottom):
		m.moveCursor(m.stops() - 1)
	case key.Matches(msg, k.PageUp):
		m.page(-m.planView.Height())
	case key.Matches(msg, k.PageDown):
		m.page(m.planView.Height())
	case key.Matches(msg, k.HalfUp):
		m.page(-m.planView.Height() / 2)
	case key.Matches(msg, k.HalfDown):
		m.page(m.planView.Height() / 2)
	case key.Matches(msg, k.SectionBack):
		m.moveCursor(m.nextHeading(-1))
	case key.Matches(msg, k.SectionNext):
		m.moveCursor(m.nextHeading(1))
	case key.Matches(msg, k.Next):
		m.moveCursor(m.nextComment())
	case key.Matches(msg, k.Comment):
		m.commenting, m.status = true, ""
		m.commentInput.Reset()
		if i := m.commentAt(m.cursor); i >= 0 {
			m.commentInput.SetValue(m.comments[i].text)
			m.commentInput.MoveToEnd()
		}
		m.moveCursor(m.cursor)
		return m, m.commentInput.Focus()
	case key.Matches(msg, k.Delete):
		i := m.commentAt(m.cursor)
		m.comments = append(m.comments[:i], m.comments[i+1:]...)
		m.moveCursor(m.cursor)
		m.syncPlaceholder()
	default:
		var cmd tea.Cmd
		m.planView, cmd = m.planView.Update(msg)
		return m, cmd
	}
	return m, nil
}

// nextComment is the stop n moves to: the nearest commented block below the
// cursor, and past the last of those the comments on parts Claude has since
// rewritten, which are the stops after the blocks. With nothing commented
// below it is the cursor itself, which keeps n in place.
func (m *Model) nextComment() int {
	next := -1
	for _, c := range m.comments {
		if c.block > m.cursor && (next < 0 || c.block < next) {
			next = c.block
		}
	}
	if next >= 0 {
		return next
	}
	if s := max(m.cursor+1, len(m.blocks)); s < m.stops() {
		return s
	}
	return m.cursor
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
func (m Model) updateCommentKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	k := m.keys()
	switch {
	case key.Matches(msg, k.CancelComment):
		m.commenting = false
		m.commentInput.Blur()
		m.drawPlan()
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
		case m.cursor < len(m.blocks):
			m.comments = append(m.comments, comment{block: m.cursor, quote: m.blocks[m.cursor].source, text: text})
		}
		m.commenting = false
		m.commentInput.Blur()
		m.moveCursor(m.cursor)
		m.syncPlaceholder()
		return m, nil
	}
	var cmd tea.Cmd
	m.commentInput, cmd = m.commentInput.Update(msg)
	m.moveCursor(m.cursor)
	return m, cmd
}

// page scrolls the plan by delta lines and puts the cursor on the first stop
// that starts in view, or on the first or last stop when the plan cannot
// scroll that way, so the highlight moves with the view.
func (m *Model) page(delta int) {
	if len(m.blocks) == 0 {
		return
	}
	before := m.planView.YOffset()
	m.planView.SetYOffset(before + delta)
	top := m.planView.YOffset()
	switch {
	case top == before && delta > 0:
		m.moveCursor(m.stops() - 1)
		return
	case top == before:
		m.moveCursor(0)
		return
	}
	for i := range m.stops() {
		if m.blockTop[i] >= top {
			m.moveCursor(i)
			return
		}
	}
	m.moveCursor(m.stops() - 1)
}

// moveCursor puts the plan's cursor on stop i, a block or a comment on a part
// Claude has since rewritten, and scrolls to keep it in view, or its top when
// it is taller than the pane.
func (m *Model) moveCursor(i int) {
	if len(m.blocks) == 0 {
		return
	}
	m.cursor = min(max(i, 0), m.stops()-1)
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

// gutterWidth is the cells left of the plan for the cursor and comment
// marks.
const gutterWidth = 2

// maxCommentRows is how tall the comment editor grows with its text before
// it scrolls inside itself.
const maxCommentRows = 6

var (
	// cursorMark and commentMark are the gutter's two columns: the cursor's
	// mark on the left, the comment's on the right.
	cursorMark  = lipgloss.NewStyle().Foreground(accentColor).Render("▌")
	commentMark = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("┃")
	commentText = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

// gutter is the two cells left of a line: the cursor's mark, then the
// comment's.
func gutter(cursor, commented bool) string {
	g := " "
	if cursor {
		g = cursorMark
	}
	if commented {
		return g + commentMark
	}
	return g + " "
}

// excerpt is `on "<first line of quote>"`, cut to width cells.
func excerpt(quote string, width int) string {
	first := strings.SplitN(quote, "\n", 2)[0]
	return ansi.Truncate(`on "`+first+`"`, width, "…")
}

// commentBody is a comment's text as drawn: 💬 before the first line, the
// rest indented under it, wrapped to width.
func commentBody(text string, width int) []string {
	split := strings.Split(commentText.Width(width-3).Render(text), "\n")
	out := make([]string, len(split))
	for j, l := range split {
		lead := "💬 "
		if j > 0 {
			lead = "   "
		}
		out[j] = lead + l
	}
	return out
}

// editorLines is the comment editor's own view with the same 💬/three-space
// lead as a saved comment's body, so the two line up.
func editorLines(view string) []string {
	lines := strings.Split(view, "\n")
	for j, l := range lines {
		lead := "💬 "
		if j > 0 {
			lead = "   "
		}
		lines[j] = lead + l
	}
	return lines
}

// drawPlan lays out the rendered blocks with the cursor and comments marked,
// and notes where each stop starts and ends for scrolling to it. A stop past
// the last block is a comment on a part Claude has since rewritten.
func (m *Model) drawPlan() {
	if len(m.blocks) == 0 {
		return
	}
	w := m.planView.Width() - gutterWidth
	var lines []string
	m.blockTop = make([]int, m.stops())
	m.blockEnd = make([]int, m.stops())
	for i, b := range m.blocks {
		if i > 0 && !joinsPrevious(m.blocks[i-1].block, b.block) {
			lines = append(lines, "")
		}
		m.blockTop[i] = len(lines)
		ci := m.commentAt(i)
		editing := m.commenting && i == m.cursor
		g := gutter(i == m.cursor && m.focus == panePlan, ci >= 0 || editing)
		for _, l := range b.lines {
			lines = append(lines, g+l)
		}
		switch {
		case editing:
			for _, l := range editorLines(m.commentInput.View()) {
				lines = append(lines, g+"  "+l)
			}
		case ci >= 0:
			for _, l := range commentBody(m.comments[ci].text, w-2) {
				lines = append(lines, g+"  "+l)
			}
		}
		m.blockEnd[i] = len(lines)
	}
	if orphans := m.orphans(); len(orphans) > 0 {
		lines = append(lines, "", "  "+ui.HelperStyle.Render("On parts Claude has since rewritten"))
		for k, ci := range orphans {
			s := len(m.blocks) + k
			editing := m.commenting && m.cursor == s
			g := gutter(s == m.cursor && m.focus == panePlan, true)
			m.blockTop[s] = len(lines)
			lines = append(lines, g+"  "+ui.HelperStyle.Render(excerpt(m.comments[ci].quote, w-2)))
			switch {
			case editing:
				for _, l := range editorLines(m.commentInput.View()) {
					lines = append(lines, g+"  "+l)
				}
			default:
				for _, l := range commentBody(m.comments[ci].text, w-2) {
					lines = append(lines, g+"  "+l)
				}
			}
			m.blockEnd[s] = len(lines)
		}
	}
	m.planView.SetContentLines(lines)
}

// planFooter is the row under the plan: what is waiting to be sent, or a
// hint when there is nothing. While a comment is being written, its editor
// lives under its block in the plan pane itself.
func (m *Model) planFooter() string {
	n, orphaned := len(m.comments), 0
	for _, c := range m.comments {
		if c.block < 0 {
			orphaned++
		}
	}
	switch {
	case n == 0 && m.focus == panePlan && !m.commenting:
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
