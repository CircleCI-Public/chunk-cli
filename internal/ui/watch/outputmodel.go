package watch

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// clip cuts a line to at most width display columns, measuring with lipgloss so
// ANSI sequences the remote command emitted are not counted as visible width and
// are not cut mid-escape.
func clip(s string, width int) string {
	if width < 1 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// updateOutputKey handles keys while the output pane is open.
func (m Model) updateOutputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	height := m.outputHeight()
	k := newOutputKeyMap()
	switch {
	case key.Matches(msg, k.Close):
		// Closing returns to the dashboard rather than quitting it. Quitting from
		// here would make Esc mean two different things depending on state.
		//
		// No tick is scheduled: the dashboard's own poll chain kept running the
		// whole time the pane was open, and starting another here would leave two
		// chains polling, doubling the rate on every open/close.
		m.output = nil
		return m, nil
	case key.Matches(msg, k.ScrollDn):
		m.output.scrollBy(1, height)
	case key.Matches(msg, k.ScrollUp):
		m.output.scrollBy(-1, height)
	case key.Matches(msg, k.PageDown):
		m.output.scrollBy(height, height)
	case key.Matches(msg, k.PageUp):
		m.output.scrollBy(-height, height)
	case key.Matches(msg, k.Top):
		m.output.scroll = 0
		m.output.pinned = false
	case key.Matches(msg, k.Follow):
		m.output.pinned = true
	case key.Matches(msg, k.ForceQuit):
		return m, tea.Quit
	}
	return m, nil
}

// withOutputChunk folds a fetched chunk into the open pane.
func (m Model) withOutputChunk(msg outputMsg) (tea.Model, tea.Cmd) {
	if m.output == nil || m.output.commandID != msg.commandID {
		return m, nil // pane closed or switched while the request was in flight
	}
	if msg.err != nil {
		m.output.err = msg.err
		return m, nil
	}
	if !msg.chunk.Found {
		m.output.err = fmt.Errorf("the chunk daemon has no output for this command")
		// Evicted buffers do not come back, so the tail has nothing left to wait
		// for. Leaving running set would keep the chain polling for a command the
		// daemon has already forgotten.
		m.output.running = false
		return m, nil
	}
	m.output.feed(msg.chunk.Data)
	m.output.offset = msg.chunk.NextOffset
	m.output.running = msg.chunk.Running
	m.output.exitCode = msg.chunk.ExitCode
	if msg.chunk.Truncated {
		m.output.truncated = true
	}
	// A stream that failed leaves real output behind but no exit code. Reporting
	// why keeps an empty pane from reading as "this command printed nothing".
	if msg.chunk.Error != "" {
		m.output.err = errors.New(msg.chunk.Error)
	} else {
		m.output.err = nil
	}
	return m, nil
}

// outputHeight is the number of scrollback lines the pane can draw.
func (m Model) outputHeight() int {
	// header + the rule with the pane's title in it + footer
	h := m.height - 3
	if h < 1 {
		h = 1
	}
	return h
}

// renderOutputPane draws the full-width scrollback view.
func (m Model) renderOutputPane(st watchStyles) string {
	p := m.output
	height := m.outputHeight()
	// An error line comes out of the scrollback budget rather than on top of it.
	// Added on top it makes the pane one line taller than the dashboard it
	// replaces, which scrolls the terminal by a line on every render — the
	// fixed-height layout the rest of this view is careful to hold.
	if p.err != nil && height > 1 {
		height--
	}
	lines, atEnd := p.visibleLines(height)

	var status string
	switch {
	case p.text != "":
		// Text has no command behind it, so no exit to report.
	case p.err != nil:
		status = st.err("error")
	case p.running:
		status = st.running(spinFrames[m.spinIdx%len(spinFrames)] + " running")
	case p.exitCode != nil && *p.exitCode == 0:
		status = st.success(ui.IconOK + " exit 0")
	case p.exitCode != nil:
		status = st.err(fmt.Sprintf("%s exit %d", ui.IconFail, *p.exitCode))
	default:
		status = st.muted("ended")
	}

	title := st.emphasis("output") + "  " + st.muted(truncate(p.name, max(10, m.width/2))) + "  " + status
	if p.truncated {
		title += "  " + st.warning("(earlier output dropped)")
	}
	switch {
	case atEnd:
	case p.text != "":
		title += "  " + st.vdim("↓ more")
	default:
		title += "  " + st.vdim("↑ scrolled")
	}

	var b strings.Builder
	b.WriteString(theme.EdgeSegment(st.vdim, title, m.width) + "\n")
	if p.err != nil {
		b.WriteString(" " + st.err(p.err.Error()) + "\n")
	}
	for i := 0; i < height; i++ {
		if i < len(lines) {
			b.WriteString(" " + clip(lines[i], m.width-1) + "\n")
			continue
		}
		b.WriteString("\n")
	}
	k := newOutputKeyMap()
	if p.text != "" {
		k.Top.SetHelp("g/G", "top/end") // fixed text has no tail to follow
	}
	// Clipped as well as fitted: help adds a hint that overflows when there is
	// no room left for its ellipsis.
	b.WriteString(clip(theme.Help(st.Styles, m.width).View(k), m.width) + "\n")
	return b.String()
}
