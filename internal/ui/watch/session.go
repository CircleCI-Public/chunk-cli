package watch

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/promptrows"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// sessionInfo is one pre-PR session with the project it belongs to.
type sessionInfo struct {
	s          watchd.Session
	label      string
	projectIdx int // index into Model.projects, for finding the session's commands
}

// sessionPane is the dashboard's view of the daemon's sessions.
//
// The sessions belong to the daemon, not to this dashboard. Quitting the
// dashboard detaches from them and they carry on; the only thing here that
// changes a session is the confirmed cancel.
type sessionPane struct {
	sel    int // index into Model.sessions
	round  int // round under the cursor
	review int // review under the cursor, within that round
	// confirm is the ID of the session an 'x' has been pressed for once. A second
	// 'x' on the same session cancels it; anything else withdraws it.
	confirm string
	// note is a one-line result of the last action, shown until the next key.
	note string
}

// sessionActionMsg reports the outcome of a cancel request.
type sessionActionMsg struct {
	note string
	err  error
}

func cancelSessionCmd(id string) tea.Cmd {
	return func() tea.Msg {
		if err := watchd.CancelSession(id); err != nil {
			return sessionActionMsg{err: err}
		}
		return sessionActionMsg{note: "cancel requested"}
	}
}

// collectSessions flattens every project's sessions, live ones first and the rest
// newest first.
func collectSessions(projects []watchd.ProjectSnapshot) []sessionInfo {
	var out []sessionInfo
	for i, p := range projects {
		label := p.RepoName
		if label == "" {
			label = filepath.Base(p.Root)
		}
		for _, s := range p.Sessions {
			out = append(out, sessionInfo{s: s, label: label, projectIdx: i})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := !out[i].s.State.Finished(), !out[j].s.State.Finished()
		if li != lj {
			return li
		}
		return out[i].s.StartedAt.After(out[j].s.StartedAt)
	})
	return out
}

// anySessionLive reports whether a session is running (the spinner runs for it).
func anySessionLive(sessions []sessionInfo) bool {
	for _, s := range sessions {
		if s.s.State == watchd.SessionRunning {
			return true
		}
	}
	return false
}

func (m Model) selectedSession() *sessionInfo {
	if m.sessionView == nil || m.sessionView.sel < 0 || m.sessionView.sel >= len(m.sessions) {
		return nil
	}
	return &m.sessions[m.sessionView.sel]
}

// openSessions shows the session view, with the cursor on the newest session's
// latest round.
func (m Model) openSessions() Model {
	p := &sessionPane{}
	if len(m.sessions) > 0 {
		p.round = max(len(m.sessions[0].s.Rounds)-1, 0)
	}
	m.sessionView = p
	return m
}

// updateSessionKey handles keys while the session view is open.
func (m Model) updateSessionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := *m.sessionView
	wasConfirm := p.confirm
	p.confirm, p.note = "", ""
	sel := m.selectedSession()

	switch msg.Code {
	case 'q':
		// Quit, not "close": this is the dashboard's own quit, and it only ever
		// detaches. The sessions are the daemon's.
		return m, tea.Quit
	case 'c':
		if msg.Mod == tea.ModCtrl {
			return m, tea.Quit
		}
	case tea.KeyEscape, 'r':
		m.sessionView = nil
		return m, nil
	case tea.KeyDown, 'j', 's':
		p.sel = min(p.sel+1, max(len(m.sessions)-1, 0))
		p.round, p.review = lastRound(m.sessions, p.sel), 0
	case tea.KeyUp, 'k', 'w':
		p.sel = max(p.sel-1, 0)
		p.round, p.review = lastRound(m.sessions, p.sel), 0
	case tea.KeyTab:
		if sel != nil && len(sel.s.Rounds) > 0 {
			p.round, p.review = (p.round+1)%len(sel.s.Rounds), 0
		}
	case tea.KeyRight, 'l':
		if sel != nil && p.round < len(sel.s.Rounds) {
			p.review = min(p.review+1, max(len(sel.s.Rounds[p.round].Reviews)-1, 0))
		}
	case tea.KeyLeft, 'h':
		p.review = max(p.review-1, 0)
	case tea.KeyEnter:
		m.sessionView = &p
		return m.openReviewOutput()
	case 'x':
		m.sessionView = &p
		return m.requestSessionCancel(&p, sel, wasConfirm)
	}
	m.sessionView = &p
	return m, nil
}

func lastRound(sessions []sessionInfo, i int) int {
	if i < 0 || i >= len(sessions) {
		return 0
	}
	return max(len(sessions[i].s.Rounds)-1, 0)
}

// requestSessionCancel implements the two-press cancel. The first press asks,
// the second on the same session sends the request. A deliberate second key is
// the only way the dashboard stops a session.
func (m Model) requestSessionCancel(p *sessionPane, sel *sessionInfo, wasConfirm string) (tea.Model, tea.Cmd) {
	switch {
	case sel == nil:
		return m, nil
	case sel.s.State.Finished():
		p.note = "that session has already ended"
		return m, nil
	case wasConfirm != sel.s.ID:
		p.confirm = sel.s.ID
		return m, nil
	}
	p.note = "cancelling…"
	return m, cancelSessionCmd(sel.s.ID)
}

// openReviewOutput opens the scrollback pane for the review under the cursor,
// through the same /output the sidecar activity pane uses.
func (m Model) openReviewOutput() (tea.Model, tea.Cmd) {
	sel := m.selectedSession()
	p := m.sessionView
	if sel == nil || p.round >= len(sel.s.Rounds) || p.review >= len(sel.s.Rounds[p.round].Reviews) {
		return m, nil
	}
	rv := sel.s.Rounds[p.round].Reviews[p.review]
	if rv.CommandID == "" {
		p.note = "no output yet for " + rv.Name
		return m, nil
	}
	return m.openOutput(rv.CommandID, fmt.Sprintf("round %d review: %s", sel.s.Rounds[p.round].Number, rv.Name), rv.State == watchd.PromptRunning)
}

// openOutput opens the output pane for a buffered command.
func (m Model) openOutput(commandID, name string, running bool) (tea.Model, tea.Cmd) {
	m.output = &outputPane{commandID: commandID, name: name, pinned: true, running: running}
	m.outputSeq++
	return m, tea.Batch(fetchOutput(commandID, 0), outputTick(m.outputSeq))
}

// withSessionAction folds a cancel result into the view's note.
func (m Model) withSessionAction(msg sessionActionMsg) Model {
	if m.sessionView == nil {
		return m
	}
	if msg.err != nil {
		m.sessionView.note = msg.err.Error()
		return m
	}
	m.sessionView.note = msg.note
	return m
}

// ---- rendering --------------------------------------------------------------

// stageLabels name a session's stages for display.
var stageLabels = map[watchd.StageID]string{
	watchd.StageFactoryLoop: "Factory loop",
}

// sessionTag is the header's note of a session in flight, so one started
// elsewhere is visible without opening the view.
func (m Model) sessionTag(st watchStyles) string {
	for _, s := range m.sessions {
		switch s.s.State {
		case watchd.SessionRunning:
			return st.running(spinFrames[m.spinIdx%len(spinFrames)]+" session running") + "  "
		case watchd.SessionDone, watchd.SessionFailed, watchd.SessionCancelled:
			// Ended sessions are not worth the header.
		}
	}
	return ""
}

func (m Model) sessionStateIcon(st watchStyles, s watchd.SessionState) string {
	switch s {
	case watchd.SessionRunning:
		return st.running(spinFrames[m.spinIdx%len(spinFrames)])
	case watchd.SessionDone:
		return st.success(ui.IconOK)
	case watchd.SessionFailed:
		return st.err(ui.IconFail)
	case watchd.SessionCancelled:
		return st.warning(ui.IconWarn)
	}
	return st.muted("?")
}

func (m Model) stageIcon(st watchStyles, s watchd.StageState) string {
	switch s {
	case watchd.StageRunning:
		return st.running(spinFrames[m.spinIdx%len(spinFrames)])
	case watchd.StageDone:
		return st.success(ui.IconOK)
	case watchd.StageFailed:
		return st.err(ui.IconFail)
	}
	return st.vdim("·")
}

func stageText(st watchStyles, s watchd.Stage) string {
	switch s.State {
	case watchd.StageFailed:
		return st.err(truncate(strings.Join(strings.Fields(s.Note), " "), 90))
	case watchd.StageRunning:
		return st.muted("running")
	case watchd.StageDone:
		if s.Note != "" {
			return st.muted("done · " + s.Note)
		}
		return st.muted("done")
	}
	return ""
}

func sessionElapsed(s watchd.Session) time.Duration {
	if s.EndedAt != nil {
		return s.EndedAt.Sub(s.StartedAt)
	}
	return time.Since(s.StartedAt)
}

func sessionTitle(info sessionInfo) string {
	parts := []string{info.label}
	if info.s.Branch != "" {
		parts = append(parts, info.s.Branch)
	}
	if info.s.HeadSHA != "" {
		parts = append(parts, info.s.HeadSHA[:min(7, len(info.s.HeadSHA))])
	}
	return strings.Join(parts, " · ")
}

// reviewRows converts a round's reviews for the shared row renderer.
func reviewRows(round watchd.Round) []promptrows.Row {
	rows := make([]promptrows.Row, 0, len(round.Reviews))
	for _, p := range round.Reviews {
		rows = append(rows, promptrows.Row{
			Name:      p.Name,
			SidecarID: p.SidecarID,
			State:     rowState(p.State),
			Duration:  time.Duration(p.DurationMS) * time.Millisecond,
			Err:       p.Error,
		})
	}
	return rows
}

// rowState is a review's state as the row renderer shows it. An unknown value
// reads as queued: it comes from a daemon that may be newer than this client.
func rowState(s watchd.PromptRunState) promptrows.State {
	switch s {
	case watchd.PromptRunning:
		return promptrows.Running
	case watchd.PromptDone:
		return promptrows.Done
	case watchd.PromptFailed:
		return promptrows.Failed
	case watchd.PromptQueued:
	}
	return promptrows.Queued
}

func roundStateText(st watchStyles, r watchd.Round) string {
	switch r.State {
	case watchd.RoundStarted:
		return st.running("starting")
	case watchd.RoundImplementing:
		return st.running("implementing")
	case watchd.RoundChecking:
		return st.running("reviewing and validating")
	case watchd.RoundDone:
		return st.muted("done")
	case watchd.RoundFailed:
		return st.err("failed")
	}
	return string(r.State)
}

// renderRound draws one round: its header and its reviews with the shared row
// renderer.
func (m Model) renderRound(st watchStyles, r watchd.Round, selected bool, reviewSel int) []string {
	rst := promptrows.NewStyles(m.hasDarkBG)
	head := fmt.Sprintf("    Round %d  %s", r.Number, roundStateText(st, r))
	if r.Findings > 0 || r.State == watchd.RoundDone {
		head += st.dim(fmt.Sprintf("  ·  %d finding%s, %d worth changing", r.Findings, plural(r.Findings), r.Worth))
	}
	lines := []string{head}

	rows := reviewRows(r)
	nameWidth := promptrows.NameWidth(rows)
	for i, row := range rows {
		line := "    " + promptrows.RenderRow(rst, row, nameWidth, m.spinIdx)
		if selected && i == reviewSel {
			line = "  ›" + line[3:]
		}
		lines = append(lines, line)
	}

	if r.Note != "" {
		lines = append(lines, "        "+st.vdim(r.Note))
	}
	return lines
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// renderSessionLines draws the selected session: header and the timeline of
// stages with the factory loop's rounds inside.
func (m Model) renderSessionLines(st watchStyles, info sessionInfo, selectedRound, reviewSel int) (lines []string, selStart, selEnd int) {
	s := info.s
	lines = append(lines, fmt.Sprintf(" ▶ %s  %s  %s  %s",
		m.sessionStateIcon(st, s.State), st.emphasis(sessionTitle(info)), st.muted(string(s.State)), st.dim(ui.FormatDuration(sessionElapsed(s)))))
	if s.Error != "" {
		lines = append(lines, "   "+st.err(truncate(strings.Join(strings.Fields(s.Error), " "), 110)))
	}
	lines = append(lines, "")

	for _, stage := range s.Stages {
		lines = append(lines, fmt.Sprintf("   %s %-18s %s", m.stageIcon(st, stage.State), stageLabels[stage.ID], stageText(st, stage)))
		if stage.ID != watchd.StageFactoryLoop {
			continue
		}
		for i, r := range s.Rounds {
			start := len(lines)
			lines = append(lines, m.renderRound(st, r, i == selectedRound, reviewSel)...)
			if i == selectedRound {
				selStart, selEnd = start, len(lines)
			}
		}
	}
	return lines, selStart, selEnd
}

// renderSessionBody is the whole session screen body, windowed so the selected
// round stays on screen.
func (m Model) renderSessionBody(st watchStyles, height int) []string {
	if len(m.sessions) == 0 {
		lines := []string{"", "  " + st.muted("No sessions yet."), "  " + st.dim("Start one with: chunk factory")}
		if m.reviewAuthErr != "" {
			lines = append(lines, "", "  "+st.warning(ui.IconWarn+" "+m.reviewAuthErr))
		}
		return lines
	}
	info := m.sessions[min(max(m.sessionView.sel, 0), len(m.sessions)-1)]
	lines, selStart, selEnd := m.renderSessionLines(st, info, m.sessionView.round, m.sessionView.review)
	if len(m.sessions) > 1 {
		lines = append(lines, "", "  "+st.vdim(fmt.Sprintf("session %d of %d  ↑/↓ switch", m.sessionView.sel+1, len(m.sessions))))
	}
	if len(lines) > height {
		first := 0
		if selEnd > height {
			first = min(selStart, selEnd-height)
		}
		lines = lines[first:min(first+height, len(lines))]
	}
	return lines
}

func (m Model) renderSessionFooter(st watchStyles) string {
	keys := []struct{ key, action string }{
		{"↑/↓", "switch"},
		{"Tab", "round"},
		{"←/→", "review"},
		{"Enter", "output"},
		{"x", "cancel"},
		{"Esc", "back"},
		{"q", "detach"},
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, st.vdim(k.key)+" "+st.dim(k.action))
	}
	sep := "  " + st.vdim("·") + "  "
	bar := strings.Join(parts, sep)
	for len(parts) > 1 && lipgloss.Width(bar) > m.width-2 {
		parts = parts[:len(parts)-1]
		bar = strings.Join(parts, sep)
	}
	footer := st.vdim(strings.Repeat("─", m.width)) + "\n" + "  " + clip(bar, m.width-2) + "\n"
	if m.sessionView != nil {
		switch {
		case m.sessionView.confirm != "":
			footer += "  " + st.warning("press x again to cancel this session — q only detaches") + "\n"
		case m.sessionView.note != "":
			footer += "  " + st.muted(m.sessionView.note) + "\n"
		}
	}
	if m.daemonErr != nil {
		footer += "  " + st.err("daemon unavailable: "+m.daemonErr.Error()) + "\n"
	}
	return footer
}

// renderSessionView is the whole session screen: header, separator, body, footer.
func (m Model) renderSessionView(st watchStyles) string {
	height := m.contentHeight()
	if m.sessionView != nil && (m.sessionView.confirm != "" || m.sessionView.note != "") {
		height-- // the footer gains a line for the confirmation or note
	}
	height = max(height, 1)
	lines := m.renderSessionBody(st, height)
	var b strings.Builder
	for i := 0; i < height; i++ {
		if i < len(lines) {
			b.WriteString(clip(lines[i], m.width))
		}
		b.WriteString("\n")
	}
	return m.renderHeader(st) + m.renderSeparator(st) + b.String() + m.renderSessionFooter(st)
}
