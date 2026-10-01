package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/reviewprogress"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// maxSessionRows bounds the review sessions the left pane lists. Every live
// session is listed regardless; finished ones fill the rest, newest first.
// Finished sessions pile up until the daemon restarts, and `chunk session
// list` has the full record.
const maxSessionRows = 4

// sessionInfo is one pre-PR session with the project it belongs to.
type sessionInfo struct {
	s          watchd.Session
	label      string
	projectIdx int // index into Model.projects, for finding the session's commands
}

// sessionPane is the dashboard's state for the selected review session.
//
// The sessions belong to the daemon, not to this dashboard. Quitting the
// dashboard detaches from them and they carry on; the only things that change a
// session are the explicit keys: resume (safe: it only continues) and the
// confirmed cancel.
type sessionPane struct {
	// cursor is the review under the cursor, counting through every round's
	// reviews in order.
	cursor int
	// confirm is the ID of the session an 'x' has been pressed for once. A second
	// 'x' on the same session cancels it; anything else withdraws it.
	confirm string
	// startConfirm is the project root an 'n' has been pressed for once. A
	// second 'n' starts a session there; anything else withdraws it.
	startConfirm string
	// note is a one-line result of the last action, shown until the next key.
	note string
}

// sessionActionMsg reports the outcome of a resume or cancel request.
type sessionActionMsg struct {
	note string
	err  error
}

func resumeSessionCmd(id string) tea.Cmd {
	return func() tea.Msg {
		if err := watchd.ResumeSession(id); err != nil {
			return sessionActionMsg{err: err}
		}
		return sessionActionMsg{note: "resumed: reviewing your files as they are now"}
	}
}

func cancelSessionCmd(id string) tea.Cmd {
	return func() tea.Msg {
		if err := watchd.CancelSession(id); err != nil {
			return sessionActionMsg{err: err}
		}
		return sessionActionMsg{note: "cancel requested"}
	}
}

// sessionStartedMsg reports the outcome of starting a session with n.
type sessionStartedMsg struct {
	id  string
	err error
}

// startSessionCmd starts a session for the project at root, with the same
// defaults as `chunk session start`.
func startSessionCmd(root string) tea.Cmd {
	return func() tea.Msg {
		// The sandboxes clone from origin. Without it the daemon accepts the
		// session and it fails deep in pool setup with a git exit code, so check
		// here, as `chunk session start` does.
		if _, err := gitremote.URL(context.Background(), root, "origin"); err != nil {
			return sessionStartedMsg{err: fmt.Errorf("%s has no git remote named origin (add one with: git remote add origin <url>)", displayPath(root))}
		}
		id, err := watchd.StartSession(watchd.SessionRequest{ProjectRoot: root, Parallelism: watchd.DefaultParallelism})
		return sessionStartedMsg{id: id, err: err}
	}
}

// displayPath shortens a path under the home directory to ~/...
func displayPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join("~", rel)
	}
	return p
}

// startTarget is the project n starts a session for: the one the selected row
// belongs to, or the only project when nothing is selected.
func (m Model) startTarget() (root string, ok bool) {
	idx := -1
	switch sel, sc := m.selectedSession(), m.selectedSidecar(); {
	case sel != nil:
		idx = sel.projectIdx
	case sc != nil:
		idx = sc.projectIdx
	case len(m.projects) == 1:
		idx = 0
	}
	if idx < 0 || idx >= len(m.projects) || m.projects[idx].ProjectRoot == "" {
		return "", false
	}
	return m.projects[idx].ProjectRoot, true
}

// requestSessionStart implements the two-press start. A session applies fixes
// to the working tree it reviews, so like cancel it takes a deliberate second
// key, and the first names the working tree it would change.
func (m Model) requestSessionStart(wasStart string) (Model, tea.Cmd) {
	if m.conn.Remote != "" {
		m.sessPane.note = "sessions run on the local daemon: start one on the machine with the files"
		return m, nil
	}
	root, ok := m.startTarget()
	switch {
	case !ok:
		m.sessPane.note = "select a sidecar or session in the project to review"
		return m, nil
	case wasStart != root:
		m.sessPane.startConfirm = root
		return m, nil
	}
	m.sessPane.note = "starting a session…"
	return m, startSessionCmd(root)
}

// withSessionStarted selects a started session as soon as a poll lists it.
func (m Model) withSessionStarted(msg sessionStartedMsg) Model {
	if msg.err != nil {
		m.sessPane.note = "could not start a session: " + msg.err.Error()
		return m
	}
	m.focusSession = msg.id
	m.sessPane.note = "session started; it appears here with the next update"
	return m
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

// visibleSessions is how many of sessions, which collectSessions has put live
// first, the left pane lists.
func visibleSessions(sessions []sessionInfo) int {
	live := 0
	for _, s := range sessions {
		if !s.s.State.Finished() {
			live++
		}
	}
	return min(len(sessions), max(live, maxSessionRows))
}

// sessionIndex is the index of the selected session among the listed ones, or
// -1 when a sidecar is selected.
func (m Model) sessionIndex() int {
	if m.sessionSel == "" {
		return -1
	}
	return listedSessionIndex(m.sessions, m.sessionSel)
}

// listedSessionIndex is the index of the session id among those the left pane
// lists, or -1.
func listedSessionIndex(sessions []sessionInfo, id string) int {
	for i := range visibleSessions(sessions) {
		if sessions[i].s.ID == id {
			return i
		}
	}
	return -1
}

// selectedSession is the session selected in the left pane, nil when a sidecar
// is selected.
func (m Model) selectedSession() *sessionInfo {
	i := m.sessionIndex()
	if i < 0 {
		return nil
	}
	return &m.sessions[i]
}

// selectSession selects the i-th listed session, with the cursor on its latest
// round.
func (m Model) selectSession(i int) Model {
	s := m.sessions[i].s
	m.sessionSel = s.ID
	m.sessPane = sessionPane{cursor: latestRoundCursor(s)}
	return m
}

// latestRoundCursor is the cursor on the first review of a session's latest
// round.
func latestRoundCursor(s watchd.Session) int {
	n := 0
	for i := 0; i < len(s.Rounds)-1; i++ {
		n += len(s.Rounds[i].Reviews)
	}
	return n
}

func reviewCount(s watchd.Session) int {
	n := 0
	for _, r := range s.Rounds {
		n += len(r.Reviews)
	}
	return n
}

// cursorAt maps the review cursor to a round and a review within it. A session
// with no reviews yet puts it on the latest round, with no review (-1).
func cursorAt(s watchd.Session, cursor int) (round, review int) {
	latest := max(len(s.Rounds)-1, 0)
	n := reviewCount(s)
	if n == 0 {
		return latest, -1
	}
	cursor = min(max(cursor, 0), n-1)
	for i, r := range s.Rounds {
		if cursor < len(r.Reviews) {
			return i, cursor
		}
		cursor -= len(r.Reviews)
	}
	return latest, -1
}

// updateSessionKey handles the keys that act on the selected session. handled
// is false for a key it leaves to the dashboard.
func (m Model) updateSessionKey(msg tea.KeyPressMsg, sel *sessionInfo, wasConfirm string) (next Model, cmd tea.Cmd, handled bool) {
	right := m.focusedPane == paneRight
	switch {
	case msg.Code == 'c' && msg.Mod != tea.ModCtrl:
		next, cmd = m.resumeSelected(sel)
		return next, cmd, true
	case msg.Code == 'x':
		next, cmd = m.requestSessionCancel(sel, wasConfirm)
		return next, cmd, true
	case msg.Code == 'f':
		next, cmd = m.openFixOutput(sel)
		return next, cmd, true
	case msg.Code == tea.KeyEnter && right:
		next, cmd = m.openReviewOutput(sel)
		return next, cmd, true
	case msg.Code == tea.KeySpace && right:
		// Space toggles a sidecar's invocations, which are not on screen.
		return m, nil, true
	case (msg.Code == tea.KeyDown || msg.Code == 's') && right:
		m.sessPane.cursor = min(m.sessPane.cursor+1, max(reviewCount(sel.s)-1, 0))
		return m, nil, true
	case (msg.Code == tea.KeyUp || msg.Code == 'w') && right:
		m.sessPane.cursor = max(min(m.sessPane.cursor, reviewCount(sel.s)-1)-1, 0)
		return m, nil, true
	}
	return m, nil, false
}

func (m Model) resumeSelected(sel *sessionInfo) (Model, tea.Cmd) {
	if sel.s.State != watchd.SessionPaused {
		return m, nil
	}
	m.sessPane.note = "resuming…"
	return m, resumeSessionCmd(sel.s.ID)
}

// requestSessionCancel implements the two-press cancel. The first press asks,
// the second on the same session sends the request. A deliberate second key is
// the only way the dashboard stops a session.
func (m Model) requestSessionCancel(sel *sessionInfo, wasConfirm string) (Model, tea.Cmd) {
	switch {
	case sel.s.State.Finished():
		m.sessPane.note = "that session has already ended"
		return m, nil
	case wasConfirm != sel.s.ID:
		m.sessPane.confirm = sel.s.ID
		return m, nil
	}
	m.sessPane.note = "cancelling…"
	return m, cancelSessionCmd(sel.s.ID)
}

// openReviewOutput opens the scrollback pane for the review under the cursor,
// through the same /output the sidecar activity pane uses.
func (m Model) openReviewOutput(sel *sessionInfo) (Model, tea.Cmd) {
	ri, vi := cursorAt(sel.s, m.sessPane.cursor)
	if vi < 0 {
		return m, nil
	}
	round := sel.s.Rounds[ri]
	rv := round.Reviews[vi]
	if rv.CommandID == "" {
		m.sessPane.note = "no output yet for " + rv.Name
		return m, nil
	}
	return m.openOutput(rv.CommandID, fmt.Sprintf("round %d review: %s", round.Number, rv.Name), rv.State == watchd.PromptRunning)
}

// openFixOutput opens the log of the fixing agent for the round under the
// cursor, found among the project's buffered commands by name.
func (m Model) openFixOutput(sel *sessionInfo) (Model, tea.Cmd) {
	ri, _ := cursorAt(sel.s, m.sessPane.cursor)
	if ri >= len(sel.s.Rounds) || sel.projectIdx >= len(m.commands) {
		return m, nil
	}
	name := fmt.Sprintf("round %d fix", sel.s.Rounds[ri].Number)
	var found *watchd.CommandState
	for i := range m.commands[sel.projectIdx] {
		c := &m.commands[sel.projectIdx][i]
		if c.Name == name && (found == nil || c.SubmittedAt.After(found.SubmittedAt)) {
			found = c
		}
	}
	if found == nil {
		m.sessPane.note = "no fix output for this round"
		return m, nil
	}
	return m.openOutput(found.CommandID, name, found.Running)
}

// openOutput opens the output pane for a buffered command.
func (m Model) openOutput(commandID, name string, running bool) (Model, tea.Cmd) {
	m.output = &outputPane{commandID: commandID, name: name, pinned: true, running: running}
	m.outputSeq++
	return m, tea.Batch(fetchOutput(commandID, 0), outputTick(m.outputSeq))
}

// withSessionAction folds a resume or cancel result into the footer's note.
func (m Model) withSessionAction(msg sessionActionMsg) Model {
	if msg.err != nil {
		m.sessPane.note = msg.err.Error()
		return m
	}
	m.sessPane.note = msg.note
	return m
}

// sessionFooterLine is the footer's extra line for a pending cancel or the
// result of the last action, "" when there is neither.
func (m Model) sessionFooterLine(st watchStyles) string {
	switch {
	case m.sessPane.startConfirm != "":
		return st.warning("press n again to start a review session in " + displayPath(m.sessPane.startConfirm) + " — its fixes change your files there")
	case m.sessPane.confirm != "":
		return st.warning("press x again to cancel this session — q only detaches")
	case m.sessPane.note != "":
		return st.muted(m.sessPane.note)
	}
	return ""
}

// hasSessionFooterLine reports whether the footer carries sessionFooterLine.
func (m Model) hasSessionFooterLine() bool {
	return m.sessPane.startConfirm != "" || m.sessPane.confirm != "" || m.sessPane.note != ""
}

// sessionKeys are the footer hints for acting on a session in its state.
func sessionKeys(s watchd.Session) []footerKey {
	var keys []footerKey
	if s.State == watchd.SessionPaused {
		keys = append(keys, footerKey{"c", "resume"})
	}
	if !s.State.Finished() {
		keys = append(keys, footerKey{"x", "cancel"})
	}
	return keys
}

// ---- rendering --------------------------------------------------------------

// stageLabels name the stages of the flow for display.
var stageLabels = map[watchd.StageID]string{
	watchd.StageReviewLoop: "Review loop",
	watchd.StageRebase:     "Rebase onto main",
	watchd.StageCI:         "CI run",
	watchd.StageApproval:   "Your approval",
	watchd.StagePR:         "Open pull request",
}

func (m Model) spinFrame() string {
	return spinFrames[m.spinIdx%len(spinFrames)]
}

func (m Model) sessionStateIcon(st watchStyles, s watchd.SessionState) string {
	switch s {
	case watchd.SessionRunning:
		return st.running(m.spinFrame())
	case watchd.SessionPaused:
		return st.warning(ui.IconWarn)
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
		return st.running(m.spinFrame())
	case watchd.StageDone:
		return st.success(ui.IconOK)
	case watchd.StageFailed:
		return st.err(ui.IconFail)
	case watchd.StagePaused:
		return st.warning(ui.IconWarn)
	case watchd.StageNotBuilt, watchd.StagePending, watchd.StageSkipped:
		return st.vdim("·")
	}
	return st.vdim("·")
}

// stageText is a stage's state, with a failure's note cut to width.
func stageText(st watchStyles, s watchd.Stage, width int) string {
	switch s.State {
	case watchd.StageNotBuilt:
		return st.vdim("not built yet")
	case watchd.StageFailed:
		return st.err(truncate(oneLine(s.Note), width))
	case watchd.StagePaused:
		return st.warning("paused")
	case watchd.StageRunning:
		return st.muted("running")
	case watchd.StageDone:
		if s.Note != "" {
			return st.muted(truncate("done · "+s.Note, width))
		}
		return st.muted("done")
	case watchd.StagePending, watchd.StageSkipped:
		return st.muted(string(s.State))
	}
	return ""
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
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
func reviewRows(round watchd.Round) []reviewprogress.Row {
	rows := make([]reviewprogress.Row, 0, len(round.Reviews))
	for _, p := range round.Reviews {
		rows = append(rows, reviewprogress.Row{
			Name:      p.Name,
			SidecarID: p.SidecarID,
			State:     p.State.Progress(),
			Duration:  time.Duration(p.DurationMS) * time.Millisecond,
			Err:       p.Error,
		})
	}
	return rows
}

func roundStateText(st watchStyles, r watchd.Round) string {
	switch r.State {
	case watchd.RoundReviewing:
		return st.running("reviewing")
	case watchd.RoundFixing:
		return st.running("fixing")
	case watchd.RoundApplying:
		return st.running("applying fixes to your files")
	case watchd.RoundDone:
		return st.muted("done")
	case watchd.RoundFailed:
		return st.err("failed")
	case watchd.RoundSuperseded:
		return st.warning("abandoned: your files changed")
	}
	return string(r.State)
}

// sessionRowStatus is the second line of a session's row in the left pane: what
// it is doing now, or how it ended and when.
func (m Model) sessionRowStatus(st watchStyles, s watchd.Session) string {
	icon := m.sessionStateIcon(st, s.State)
	switch s.State {
	case watchd.SessionRunning:
		what := "starting"
		if n := len(s.Rounds); n > 0 {
			r := s.Rounds[n-1]
			what = fmt.Sprintf("round %d %s", r.Number, r.State)
		}
		// The elapsed time is the first thing to go: the round is what says how
		// far along the session is.
		elapsed := " · " + ui.FormatDuration(sessionElapsed(s))
		if 4+utf8.RuneCountInString(what+elapsed) > leftPaneWidth {
			elapsed = ""
		}
		return icon + " " + st.running(what) + st.muted(elapsed)
	case watchd.SessionPaused:
		return icon + " " + st.warning("paused") + st.muted(" · needs you")
	case watchd.SessionDone, watchd.SessionFailed, watchd.SessionCancelled:
		text := string(s.State)
		if s.EndedAt != nil {
			text += " · " + ago(*s.EndedAt)
		}
		return icon + " " + st.muted(text)
	}
	return icon + " " + st.muted(string(s.State))
}

// renderSessionSection is the review sessions section at the top of the left
// pane, nothing when the daemon has no sessions.
func (m Model) renderSessionSection(st watchStyles) []string {
	if len(m.sessions) == 0 {
		return nil
	}
	selIdx := m.sessionIndex()
	title := st.vdim("review sessions")
	if m.focusedPane == paneLeft && selIdx >= 0 {
		title = st.emphasis("review sessions")
	}
	lines := []string{title, ""}
	n := visibleSessions(m.sessions)
	for i := range n {
		info := m.sessions[i]
		name := info.label
		if info.s.Branch != "" {
			name += " · " + info.s.Branch
		}
		name = truncate(name, leftPaneWidth-3)
		switch {
		case i == selIdx && m.focusedPane == paneLeft:
			lines = append(lines, st.success("▶ ")+st.emphasis(name))
		case i == selIdx:
			lines = append(lines, st.vdim("▶ ")+st.muted(name))
		default:
			lines = append(lines, "  "+name)
		}
		lines = append(lines, "  "+m.sessionRowStatus(st, info.s))
	}
	if older := len(m.sessions) - n; older > 0 {
		lines = append(lines, "  "+st.vdim(fmt.Sprintf("+%d older (chunk session list)", older)))
	}
	return append(lines, "")
}

// renderRound draws one round: its header, its reviews with the shared row
// renderer, and what its fixes changed. reviewSel is the review under the
// cursor, -1 for none.
func (m Model) renderRound(st watchStyles, r watchd.Round, reviewSel, width int) []string {
	rst := reviewprogress.NewStyles(m.hasDarkBG)
	head := fmt.Sprintf("   Round %d  %s", r.Number, roundStateText(st, r))
	if r.Findings > 0 || r.State == watchd.RoundDone {
		head += st.dim(fmt.Sprintf("  ·  %d finding%s, %d worth changing", r.Findings, plural(r.Findings), r.Worth))
	}
	lines := []string{head}

	rows := reviewRows(r)
	nameWidth := reviewprogress.NameWidth(rows)
	for i, row := range rows {
		marker := "     "
		if i == reviewSel {
			marker = "   › "
			if m.focusedPane == paneRight {
				marker = "   " + st.success("›") + " "
			}
		}
		lines = append(lines, marker+reviewprogress.RenderRow(rst, row, nameWidth, m.spinIdx))
	}

	if f := r.Fix; f != nil {
		lines = append(lines, "       "+m.fixLine(st, f))
		const showFiles = 5
		for i, file := range f.Files {
			if i == showFiles {
				lines = append(lines, "         "+st.vdim(fmt.Sprintf("… and %d more", len(f.Files)-showFiles)))
				break
			}
			lines = append(lines, "         "+st.muted(truncate(file.Path, width-20))+"  "+st.success(fmt.Sprintf("+%d", file.Insertions))+" "+st.err(fmt.Sprintf("−%d", file.Deletions)))
		}
		if f.Error != "" {
			lines = append(lines, "         "+st.err(truncate(oneLine(f.Error), width-9)))
		}
	}
	if r.Note != "" && r.State != watchd.RoundSuperseded {
		lines = append(lines, "       "+st.vdim(truncate(oneLine(r.Note), width-7)))
	}
	return lines
}

func (m Model) fixLine(st watchStyles, f *watchd.RoundFix) string {
	switch f.State {
	case watchd.FixRunning:
		return st.running(m.spinFrame()) + " " + st.muted("fixing the findings worth changing")
	case watchd.FixApplied:
		return st.success(ui.IconOK) + " " + st.muted(fmt.Sprintf("fixes applied to your files: %d file%s, +%d −%d", len(f.Files), plural(len(f.Files)), f.Insertions, f.Deletions))
	case watchd.FixEmpty:
		return st.muted("the agent made no changes")
	case watchd.FixFailed:
		return st.err(ui.IconFail + " fix not applied")
	}
	return ""
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// renderSessionLines draws a session for the right pane: its state, restore
// point and pause banner, then the timeline of stages with the review loop's
// rounds inside. selStart and selEnd bound the round under the cursor.
func (m Model) renderSessionLines(st watchStyles, info sessionInfo, width int) (lines []string, selStart, selEnd int) {
	s := info.s
	lines = append(lines, fmt.Sprintf(" %s %s  %s",
		m.sessionStateIcon(st, s.State), st.muted(string(s.State)), st.dim(ui.FormatDuration(sessionElapsed(s)))))
	if rp := s.Restore; rp != nil {
		// The command first, so a narrow pane cuts the count rather than the ID.
		note := fmt.Sprintf("undo: chunk session restore %s · %d file%s changed", s.ID[:min(8, len(s.ID))], len(rp.Paths), plural(len(rp.Paths)))
		if rp.Restored {
			note = "restored: everything the session changed has been put back"
		}
		lines = append(lines, " "+st.vdim(truncate(note, width-1)))
	}
	if s.State == watchd.SessionPaused {
		lines = append(lines, "",
			" "+st.warning(truncate(ui.IconWarn+" Paused: "+s.PauseReason, width-1)),
			" "+st.muted(truncate("It stopped rather than overwrite files you changed.", width-1)),
			" "+st.vdim("c")+" "+st.dim("continue with your files as they are now")+"  "+st.vdim("·")+"  "+st.vdim("x x")+" "+st.dim("cancel"))
	}
	if s.Error != "" {
		lines = append(lines, " "+st.err(truncate(oneLine(s.Error), width-1)))
	}
	lines = append(lines, "")

	selRound, reviewSel := cursorAt(s, m.sessPane.cursor)
	for _, stage := range s.Stages {
		lines = append(lines, fmt.Sprintf(" %s %-18s %s", m.stageIcon(st, stage.State), stageLabels[stage.ID], stageText(st, stage, width-22)))
		if stage.ID != watchd.StageReviewLoop {
			continue
		}
		for i, r := range s.Rounds {
			start := len(lines)
			sel := -1
			if i == selRound {
				sel = reviewSel
			}
			lines = append(lines, m.renderRound(st, r, sel, width)...)
			if i == selRound {
				selStart, selEnd = start, len(lines)
			}
		}
	}
	return lines, selStart, selEnd
}

// renderSessionPane is the right pane for the selected session, windowed so the
// round under the cursor stays on screen.
func (m Model) renderSessionPane(st watchStyles, info sessionInfo, maxLines int) []string {
	title := st.vdim("session")
	if m.focusedPane == paneRight {
		title = st.emphasis("session")
	}
	title += "  " + st.muted(sessionTitle(info))

	height := max(maxLines-2, 1)
	body, selStart, selEnd := m.renderSessionLines(st, info, m.rightPaneWidth())
	if len(body) > height {
		first := 0
		if selEnd > height {
			first = min(selStart, selEnd-height)
		}
		body = body[first:min(first+height, len(body))]
	}
	return append([]string{title, ""}, body...)
}
