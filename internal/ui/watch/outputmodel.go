package watch

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
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

// commandForInvocation resolves which buffered command produced an invocation.
//
// The join is a heuristic: match the sidecar, then take the command whose submit
// time falls inside the invocation's span. It holds because a sidecar runs its
// validate commands sequentially, so overlapping candidates are rare. When
// nothing matches, no output affordance is shown — which is the honest outcome,
// since the daemon genuinely has nothing for that invocation.
//
// Recording the command ID on the events themselves would make this exact rather
// than probable; that is what the event log's command_id field is for once it
// lands.
func (m Model) commandForInvocation(g invocationGroup) *watchd.CommandState {
	if len(g.events) == 0 || m.selectedIdx >= len(m.sidecars) {
		return nil
	}
	sc := m.sidecars[m.selectedIdx]
	if sc.projectIdx >= len(m.commands) {
		return nil
	}
	start := g.events[0].Ts
	end := invocEndTime(g)

	var best *watchd.CommandState
	for i := range m.commands[sc.projectIdx] {
		cs := &m.commands[sc.projectIdx][i]
		if !hasSidecarID(sc.sidecarIDs, cs.SidecarID) {
			continue
		}
		// Submitted within the invocation's span, allowing a small margin at the
		// front: the "$ <cmd>" status event is written just before submission.
		if cs.SubmittedAt.Before(start.Add(-2*time.Second)) || cs.SubmittedAt.After(end.Add(2*time.Second)) {
			continue
		}
		// Latest match wins, so a re-run inside one group resolves to the newer.
		if best == nil || cs.SubmittedAt.After(best.SubmittedAt) {
			best = cs
		}
	}
	return best
}

// openSelectedOutput opens the scrollback pane for the right-pane selection.
// Returns nil when the selected invocation has no buffered output.
func (m Model) openSelectedOutput() (*Model, tea.Cmd) {
	groups := m.currentInvocGroups()
	gi := len(groups) - 1 - m.rightSelectedIdx
	if gi < 0 || gi >= len(groups) {
		return nil, nil
	}
	cs := m.commandForInvocation(groups[gi])
	if cs == nil {
		return nil, nil
	}
	name := cs.Name
	if name == "" {
		name = invocLabel(groups[gi])
	}
	m.output = &outputPane{
		commandID: cs.CommandID,
		name:      name,
		pinned:    true,
		running:   cs.Running,
		exitCode:  cs.ExitCode,
	}
	m.outputSeq++
	return &m, tea.Batch(fetchOutput(cs.CommandID, 0), outputTick(m.outputSeq))
}

// invocLabel names an invocation for the pane header, falling back to its op.
func invocLabel(g invocationGroup) string {
	for _, e := range g.events {
		if strings.HasPrefix(e.Msg, "$ ") {
			return strings.TrimPrefix(e.Msg, "$ ")
		}
	}
	if len(g.events) > 0 {
		return string(g.events[0].Op)
	}
	return "command"
}

// updateOutputKey handles keys while the output pane is open.
func (m Model) updateOutputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	height := m.outputHeight()
	switch msg.Code {
	case tea.KeyEscape, 'q':
		// Closing returns to the dashboard rather than quitting it. Quitting from
		// here would make Esc mean two different things depending on state.
		//
		// No tick is scheduled: the dashboard's own poll chain kept running the
		// whole time the pane was open, and starting another here would leave two
		// chains polling, doubling the rate on every open/close.
		m.output = nil
		return m, nil
	case tea.KeyDown, 'j', 's':
		m.output.scrollBy(1, height)
	case tea.KeyUp, 'k', 'w':
		m.output.scrollBy(-1, height)
	case tea.KeyPgDown:
		m.output.scrollBy(height, height)
	case tea.KeyPgUp:
		m.output.scrollBy(-height, height)
	case 'g':
		m.output.scroll = 0
		m.output.pinned = false
	case 'G':
		m.output.pinned = true
	case 'c':
		if msg.Mod == tea.ModCtrl {
			return m, tea.Quit
		}
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
		m.output.err = fmt.Errorf("the watch daemon has no output for this command")
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
	// header + separator + pane title + pane separator + footer
	h := m.height - 5
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
	if !atEnd {
		title += "  " + st.vdim("↑ scrolled")
	}

	var b strings.Builder
	b.WriteString(title + "\n")
	b.WriteString(st.vdim(strings.Repeat("─", m.width)) + "\n")
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
	b.WriteString(st.vdim("esc") + st.muted(" back  ") +
		st.vdim("↑/↓") + st.muted(" scroll  ") +
		st.vdim("g/G") + st.muted(" top/follow  ") +
		st.vdim("ctrl-c") + st.muted(" quit") + "\n")
	return b.String()
}
