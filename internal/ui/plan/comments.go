package plan

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// comment is the developer's note on a run of blocks of the plan, sent to
// Claude with their next message.
type comment struct {
	// block is the index of the first block the comment is on, or -1 once
	// Claude has rewritten those blocks and they are gone. Such a comment is
	// still sent.
	block int
	// span is how many blocks the comment is on, from block.
	span int
	// quote is the blocks' markdown, which is how Claude finds what the
	// comment is about.
	quote string
	text  string
}

// last is the index of the last block c is on.
func (c comment) last() int { return c.block + max(c.span, 1) - 1 }

// quoteOf is the markdown of blocks lo to hi as it is in the plan, or, when
// it cannot be found there as it is, their markdown a blank line apart.
func (m *Model) quoteOf(lo, hi int) string {
	first, last := m.blocks[lo], m.blocks[hi]
	if first.at >= 0 && last.at >= first.at && last.at+len(last.source) <= len(m.renderedFrom) {
		return m.renderedFrom[first.at : last.at+len(last.source)]
	}
	sources := make([]string, 0, hi-lo+1)
	for _, b := range m.blocks[lo : hi+1] {
		sources = append(sources, b.source)
	}
	return strings.Join(sources, "\n\n")
}

// reanchor puts each comment back on the blocks it quotes after the plan has
// been split again. Two comments never share a block, so of several runs of
// blocks with the same markdown each comment takes a different one.
func (m *Model) reanchor() {
	taken := map[int]bool{}
	free := func(lo, n int) bool {
		for i := lo; i < lo+n; i++ {
			if taken[i] {
				return false
			}
		}
		return true
	}
	take := func(lo, n int) {
		for i := lo; i < lo+n; i++ {
			taken[i] = true
		}
	}
	fits := func(c *comment, lo int) bool {
		n := max(c.span, 1)
		return lo >= 0 && lo+n <= len(m.blocks) && free(lo, n) && m.quoteOf(lo, lo+n-1) == c.quote
	}
	var lost []int
	for i := range m.comments {
		c := &m.comments[i]
		if fits(c, c.block) {
			take(c.block, max(c.span, 1))
			continue
		}
		lost = append(lost, i)
	}
	for _, i := range lost {
		c := &m.comments[i]
		c.block = -1
		for j := range m.blocks {
			if fits(c, j) {
				c.block = j
				take(j, max(c.span, 1))
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
		if c.block >= 0 && c.block <= i && i <= c.last() {
			return j
		}
	}
	return -1
}

// selecting is whether a run of blocks is selected to comment on.
func (m *Model) selecting() bool { return m.anchor >= 0 && m.anchor != m.cursor }

// selection is the first and last stop the next comment is on: the blocks
// from the anchor to the cursor, or the cursor alone.
func (m *Model) selection() (lo, hi int) {
	if m.anchor < 0 {
		return m.cursor, m.cursor
	}
	return min(m.anchor, m.cursor), max(m.anchor, m.cursor)
}

// selected is whether stop i is in the selection.
func (m *Model) selected(i int) bool {
	lo, hi := m.selection()
	return lo <= i && i <= hi
}

// extend moves the cursor by delta and keeps the selection's other end where
// it was, so the selection grows or shrinks with it. A selection is of
// blocks: it does not reach comments on parts Claude has since rewritten.
func (m *Model) extend(delta int) {
	if m.cursor >= len(m.blocks) {
		m.anchor = -1
		m.moveCursor(m.cursor + delta)
		return
	}
	if m.anchor < 0 {
		m.anchor = m.cursor
	}
	m.moveCursor(min(m.cursor+delta, len(m.blocks)-1))
}

// widen grows lo to hi to take in the whole of every comment it touches, and
// returns them, in order, as the indexes in m.comments of the comments a new
// one on lo to hi replaces.
func (m *Model) widen(lo, hi int) (int, int, []int) {
	var in []int
	for j, c := range m.comments {
		if c.block >= 0 && c.block <= hi && c.last() >= lo {
			in = append(in, j)
			lo, hi = min(lo, c.block), max(hi, c.last())
		}
	}
	return lo, hi, in
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
	// Only the keys that grow or shrink the selection keep it.
	if !key.Matches(msg, k.ExtendUp, k.ExtendDown, k.Comment) {
		m.anchor = -1
	}
	switch {
	case key.Matches(msg, k.ExtendUp):
		m.extend(-1)
	case key.Matches(msg, k.ExtendDown):
		m.extend(1)
	case key.Matches(msg, k.ClearSelection):
		m.drawPlan()
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
		var texts []string
		if m.cursor < len(m.blocks) {
			// The editor opens on the selection and every comment it
			// touches, with their text, and goes under the last block.
			lo, hi, in := m.widen(m.selection())
			for _, j := range in {
				texts = append(texts, m.comments[j].text)
			}
			m.anchor, m.cursor = lo, hi
		} else if i := m.commentAt(m.cursor); i >= 0 {
			texts = []string{m.comments[i].text}
		}
		if len(texts) > 0 {
			m.commentInput.SetValue(strings.Join(texts, "\n\n"))
			m.commentInput.MoveToEnd()
		}
		m.moveCursor(m.cursor)
		return m, m.commentInput.Focus()
	case key.Matches(msg, k.SendComments):
		if m.busy {
			m.status = warn("chunk is still answering; your comments are kept until it finishes.")
			return m, nil
		}
		// What is typed in the chat stays there: it is out of sight from
		// the plan, so it is not sent unseen.
		return m.send("")
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
		m.commenting, m.anchor = false, -1
		m.commentInput.Blur()
		m.drawPlan()
		return m, nil
	case key.Matches(msg, k.SaveComment):
		text := strings.TrimSpace(m.commentInput.Value())
		if m.cursor < len(m.blocks) {
			m.saveOnBlocks(text)
		} else if i := m.commentAt(m.cursor); i >= 0 {
			// A comment on a part Claude has since rewritten.
			if text == "" {
				m.comments = append(m.comments[:i], m.comments[i+1:]...)
			} else {
				m.comments[i].text = text
			}
		}
		m.commenting, m.anchor = false, -1
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

// saveOnBlocks saves text as the comment on the selection, in place of the
// comments it took in when the editor opened; empty text deletes them. A
// comment keeps its place among the others, so it is sent where it was.
func (m *Model) saveOnBlocks(text string) {
	lo, hi, in := m.widen(m.selection())
	c := comment{block: lo, span: hi - lo + 1, quote: m.quoteOf(lo, hi), text: text}
	at := len(m.comments)
	if len(in) > 0 {
		at = in[0]
	}
	var kept []comment
	for j, old := range m.comments {
		if j == at && text != "" {
			kept = append(kept, c)
		}
		if !slices.Contains(in, j) {
			kept = append(kept, old)
		}
	}
	if at == len(m.comments) && text != "" {
		kept = append(kept, c)
	}
	m.comments = kept
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

// commentMark is the gutter's right column, the comment's mark. The cursor's
// mark on the left is in the accent, so it follows the terminal's background.
var commentMark = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("┃")

// gutter is the two cells left of a line: the cursor's mark, then the
// comment's.
func (m *Model) gutter(cursor, commented bool) string {
	g := " "
	if cursor {
		g = m.style(m.teal()).Render("▌")
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

// commentCard is a comment as it hangs under its block: a thread on a rail
// of its own, with a line saying what it is above body, on the note's ground,
// width w cells across.
func (m *Model) commentCard(head, body string, w int) []string {
	rail := m.style(m.amber()).Render("┃")
	card := m.style(m.dim()).Render(head) + "\n" + body
	lines := strings.Split(paint(card, w-1, m.note()), "\n")
	for j, l := range lines {
		lines[j] = rail + l
	}
	return lines
}

// commentLines is the card for comment ci, or for the editor when it is the
// comment being written, width w cells across.
func (m *Model) commentLines(ci int, editing bool, w int) []string {
	if editing {
		return m.commentCard("your comment", m.commentInput.View(), w)
	}
	return m.commentCard("your comment · queued", m.style(m.amber()).Width(w-2).Render(m.comments[ci].text), w)
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
		ci := m.commentAt(i)
		// While a comment is written its blocks are the selection, and the
		// editor is drawn in place of the comments it takes in.
		editing := m.commenting && m.selected(i)
		if editing {
			ci = -1
		}
		marked := m.selected(i) && m.focus == panePlan
		g := m.gutter(marked, ci >= 0 || editing)
		if i > 0 && !joinsPrevious(m.blocks[i-1].block, b.block) {
			// The blank line between two blocks of one comment or one
			// selection carries their bars, so they read as one.
			sep := ""
			if (ci >= 0 && ci == m.commentAt(i-1)) || (m.selected(i-1) && m.selected(i) && (editing || marked)) {
				sep = g
			}
			lines = append(lines, sep)
		}
		m.blockTop[i] = len(lines)
		for _, l := range b.lines {
			// The blocks under the cursor are raised off the pane.
			if marked {
				l = paint(l, w-1, m.raised())
			}
			lines = append(lines, g+l)
		}
		// A comment hangs under the last of its blocks, and the editor under
		// the cursor's.
		if (ci >= 0 && i == m.comments[ci].last()) || (editing && i == m.cursor) {
			for _, l := range m.commentLines(ci, editing, w-4) {
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
			g := m.gutter(s == m.cursor && m.focus == panePlan, true)
			m.blockTop[s] = len(lines)
			lines = append(lines, g+"  "+ui.HelperStyle.Render(excerpt(m.comments[ci].quote, w-2)))
			for _, l := range m.commentLines(ci, editing, w-4) {
				lines = append(lines, g+"  "+l)
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
	case m.selecting() && !m.commenting:
		lo, hi := m.selection()
		return "  " + ui.HelperStyle.Render(fmt.Sprintf("%d parts selected · c to comment on them", hi-lo+1))
	case n == 0 && m.focus == panePlan && !m.commenting:
		return "  " + ui.HelperStyle.Render("c to comment on the highlighted part")
	case n == 0:
		return ""
	}
	s := fmt.Sprintf("%d %s to send with your next message", n, plural(n, "comment", "comments"))
	if orphaned > 0 {
		s += fmt.Sprintf(" (%d on parts Claude has since rewritten)", orphaned)
	}
	return ansi.Truncate("  "+m.style(m.amber()).Render(s), m.planView.Width(), "…")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
