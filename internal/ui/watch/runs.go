package watch

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// maxRuns caps the runs the left pane lists. Live runs are always listed; the
// ended ones fill what is left, newest first, so a day of factory runs does
// not push every sidecar off the screen.
const maxRuns = 5

// sessionInfo is one of the daemon's runs with the project it belongs to.
type sessionInfo struct {
	s          chunkd.Session
	label      string
	projectIdx int // index into Model.projects, for finding the run's commands
}

// sessionActionMsg reports the outcome of a cancel request.
type sessionActionMsg struct {
	note string
	err  error
}

func cancelSessionCmd(id string) tea.Cmd {
	return func() tea.Msg {
		if err := chunkd.CancelSession(id); err != nil {
			return sessionActionMsg{err: err}
		}
		return sessionActionMsg{note: "cancel requested"}
	}
}

// collectSessions flattens every project's sessions, live ones first and the rest
// newest first.
func collectSessions(projects []chunkd.ProjectSnapshot) []sessionInfo {
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

// visibleRuns is the runs the left pane lists, from collectSessions' order:
// every live run, then ended ones that ended within staleAfter, up to maxRuns.
func visibleRuns(all []sessionInfo, now time.Time) []sessionInfo {
	var out []sessionInfo
	for _, r := range all {
		if !r.s.State.Finished() {
			out = append(out, r)
			continue
		}
		if len(out) >= maxRuns {
			continue
		}
		if r.s.EndedAt != nil && now.Sub(*r.s.EndedAt) > staleAfter {
			continue
		}
		out = append(out, r)
	}
	return out
}

// splitRunSidecars takes the sidecars of the listed factory runs out of the
// sidecar list, keyed by run. A run's pool is saved as factory-<run id>, so
// its sidecars are named after it; shown as rows of their own they would read
// as loose sidecars nobody owns.
func splitRunSidecars(sidecars []sidecarInfo, runs []sessionInfo) (rest []sidecarInfo, byRun map[string][]sidecarInfo) {
	pools := map[string]string{}
	for _, r := range runs {
		if f := r.s.Factory; f != nil && f.RunID != "" {
			pools[factory.PoolName(f.RunID)] = r.s.ID
		}
	}
	byRun = map[string][]sidecarInfo{}
	for _, sc := range sidecars {
		if id, ok := pools[poolOf(sc.name)]; ok {
			byRun[id] = append(byRun[id], sc)
			continue
		}
		rest = append(rest, sc)
	}
	return rest, byRun
}

// poolOf strips the member suffix a multi-sidecar pool adds to each name.
func poolOf(name string) string {
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		if _, err := strconv.Atoi(name[i+1:]); err == nil {
			return name[:i]
		}
	}
	return name
}

// anySessionLive reports whether a run is running (the spinner runs for it).
func anySessionLive(sessions []sessionInfo) bool {
	for _, s := range sessions {
		if s.s.State == chunkd.SessionRunning {
			return true
		}
	}
	return false
}

// selectedRun is the run the dashboard is showing, or nil when a sidecar is
// selected.
func (m Model) selectedRun() *sessionInfo {
	if i := m.runIndex(); i >= 0 {
		return &m.sessions[i]
	}
	return nil
}

// runIndex is the index of the selected run in m.sessions, -1 when none is.
func (m Model) runIndex() int {
	if m.selRun == "" {
		return -1
	}
	for i, r := range m.sessions {
		if r.s.ID == m.selRun {
			return i
		}
	}
	return -1
}

// reselectRun keeps the selection valid after a poll. Until a sidecar has
// been selected, or while there are none, the newest run is; a run that has
// dropped off the list gives way to the newest run, or to the sidecars when
// there are none.
func (m Model) reselectRun() Model {
	if m.selRun == "" {
		if len(m.sessions) > 0 && (m.selectedID == noSelection || len(m.sidecars) == 0) {
			m.selRun = m.sessions[0].s.ID
		}
		return m
	}
	if m.runIndex() >= 0 {
		return m
	}
	m.selRun, m.runItem = "", 0
	if len(m.sessions) > 0 {
		m.selRun = m.sessions[0].s.ID
	}
	return m
}

// selectRun moves the selection to run i, or to the first sidecar when i is
// past the last run.
func (m Model) selectRun(i int) Model {
	m.runItem, m.runConfirm, m.runNote = 0, "", ""
	if i >= 0 && i < len(m.sessions) {
		m.selRun = m.sessions[i].s.ID
		return m
	}
	m.selRun = ""
	return m
}

// moveLeftSelection steps through the left pane, runs first and sidecars after
// them, as one list.
func (m Model) moveLeftSelection(delta int) Model {
	if i := m.runIndex(); i >= 0 {
		next := i + delta
		switch {
		case next < 0:
			return m
		case next < len(m.sessions):
			return m.selectRun(next)
		case len(m.sidecars) == 0:
			return m
		}
		m = m.selectRun(-1)
		m.selectedIdx = 0
		m.selectedID = selectedSidecarID(m.sidecars, 0)
		m.rightSelectedIdx = 0
		return m.adjustLeftScroll()
	}
	if delta < 0 && m.selectedIdx == 0 && len(m.sessions) > 0 {
		return m.selectRun(len(m.sessions) - 1)
	}
	m.selectedIdx = min(max(m.selectedIdx+delta, 0), max(len(m.sidecars)-1, 0))
	m.selectedID = selectedSidecarID(m.sidecars, m.selectedIdx)
	m.rightSelectedIdx = 0
	return m.adjustLeftScroll()
}

// requestRunCancel implements the two-press cancel. The first press asks, the
// second on the same run sends the request.
func (m Model) requestRunCancel(run *sessionInfo, wasConfirm string) (tea.Model, tea.Cmd) {
	if wasConfirm != run.s.ID {
		m.runConfirm, m.runNote = run.s.ID, ""
		return m, nil
	}
	m.runNote = "cancelling…"
	return m, cancelSessionCmd(run.s.ID)
}

// withSessionAction folds a cancel result into the run pane's note.
func (m Model) withSessionAction(msg sessionActionMsg) Model {
	if msg.err != nil {
		m.runNote = msg.err.Error()
		return m
	}
	m.runNote = msg.note
	return m
}

// ---- run items ---------------------------------------------------------------

type runItemKind int

const (
	itemImplement runItemKind = iota
	itemCheck
	itemReview
)

// runItem is one selectable row of the run pane.
type runItem struct {
	kind  runItemKind
	round int // index into Session.Rounds
	idx   int // index into the round's Checks or Reviews
}

// runItems lists a run's selectable rows in the order the pane draws them.
func runItems(s chunkd.Session) []runItem {
	var items []runItem
	for ri, r := range s.Rounds {
		if r.Implement != nil {
			items = append(items, runItem{kind: itemImplement, round: ri})
		}
		for i := range r.Checks {
			items = append(items, runItem{kind: itemCheck, round: ri, idx: i})
		}
		for i := range r.Reviews {
			items = append(items, runItem{kind: itemReview, round: ri, idx: i})
		}
	}
	return items
}

// openRunItem opens the output behind the selected row: a review's buffered
// Claude run, a check's kept output, or the implementer's account of its
// turn.
func (m Model) openRunItem(run *sessionInfo, items []runItem) (tea.Model, tea.Cmd) {
	if m.runItem >= len(items) {
		return m, nil
	}
	it := items[m.runItem]
	r := run.s.Rounds[it.round]
	switch it.kind {
	case itemReview:
		rv := r.Reviews[it.idx]
		if rv.CommandID == "" {
			m.runNote = "no output for this review yet"
			return m, nil
		}
		return m.openOutput(rv.CommandID, fmt.Sprintf("round %d review: %s", r.Number, rv.Name), rv.State == chunkd.PromptRunning)
	case itemCheck:
		c := r.Checks[it.idx]
		text := strings.TrimRight(c.Output, "\n")
		if c.Error != "" {
			text = strings.TrimSpace(c.Error + "\n\n" + text)
		}
		if text == "" {
			m.runNote = "no output kept for " + c.Name + ": only a failing command's is"
			return m, nil
		}
		return m.openText(fmt.Sprintf("round %d check: %s", r.Number, c.Name), text), nil
	case itemImplement:
		impl := r.Implement
		var parts []string
		for _, s := range []string{impl.Error, impl.Summary, impl.Stat} {
			if s = strings.TrimSpace(s); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) == 0 {
			m.runNote = "the implementer has not reported on this turn yet"
			return m, nil
		}
		return m.openText(fmt.Sprintf("round %d implementer", r.Number), strings.Join(parts, "\n\n")), nil
	}
	return m, nil
}

func (m Model) openOutput(commandID, name string, running bool) (tea.Model, tea.Cmd) {
	m.output = &outputPane{commandID: commandID, name: name, pinned: true, running: running}
	m.outputSeq++
	return m, tea.Batch(fetchOutput(commandID, 0), outputTick(m.outputSeq))
}

// openText opens the output pane over text the snapshot already holds. With
// no command behind it there is nothing to fetch or tail, and it opens at the
// top, since it is read from the start.
func (m Model) openText(name, text string) Model {
	m.output = &outputPane{name: name, text: text}
	m.output.wrap(m.width - 1)
	m.outputSeq++
	return m
}

// ---- left pane: the run list ---------------------------------------------------

// renderRunList draws the runs at the top of the left pane, or nothing when
// there are none, so a dashboard with no runs looks as it always has.
func (m Model) renderRunList(st watchStyles) []string {
	if len(m.sessions) == 0 {
		return nil
	}
	head := st.vdim("runs")
	if m.focusedPane == paneLeft && m.selRun != "" {
		head = st.emphasis("runs")
	}
	lines := []string{head, ""}
	for _, r := range m.sessions {
		title := truncate(runTitle(r.s), leftPaneWidth-5)
		icon := m.runIcon(st, r.s)
		switch {
		case r.s.ID == m.selRun && m.focusedPane == paneLeft:
			lines = append(lines, st.success("▶ ")+icon+" "+st.emphasis(title))
		case r.s.ID == m.selRun:
			lines = append(lines, st.vdim("▶ ")+icon+" "+st.muted(title))
		default:
			lines = append(lines, "  "+icon+" "+title)
		}
		status := runStatus(r.s)
		if len(m.projects) > 1 {
			status = r.label + " · " + status
		}
		lines = append(lines, "    "+st.vdim(truncate(status, leftPaneWidth-4)))
	}
	return append(lines, "")
}

// runTitle names a run by the first line of its prompt.
func runTitle(s chunkd.Session) string {
	if f := s.Factory; f != nil {
		for _, l := range strings.Split(f.Prompt, "\n") {
			if l = strings.TrimSpace(strings.TrimLeft(l, "# ")); l != "" {
				return l
			}
		}
	}
	return "factory run"
}

// runStatus is a run's one-line state for the run list.
func runStatus(s chunkd.Session) string {
	switch s.State {
	case chunkd.SessionRunning:
		parts := []string{}
		if f := s.Factory; f != nil && f.Attempts > 0 {
			parts = append(parts, fmt.Sprintf("round %d/%d", max(len(s.Rounds), 1), f.Attempts))
		} else if len(s.Rounds) > 0 {
			parts = append(parts, fmt.Sprintf("round %d", len(s.Rounds)))
		}
		return strings.Join(append(parts, ui.FormatDuration(sessionElapsed(s).Truncate(time.Second))), " · ")
	case chunkd.SessionDone, chunkd.SessionFailed, chunkd.SessionCancelled:
	}
	out := runOutcome(s)
	if s.EndedAt != nil {
		out += " · " + ago(*s.EndedAt)
	}
	return out
}

// runOutcome is how an ended run came out, in a word or two.
func runOutcome(s chunkd.Session) string {
	switch s.State {
	case chunkd.SessionCancelled:
		return "cancelled"
	case chunkd.SessionFailed:
		return "failed"
	case chunkd.SessionRunning, chunkd.SessionDone:
	}
	if f := s.Factory; f != nil {
		switch f.Result {
		case chunkd.ResultPassed:
			return "passed"
		case chunkd.ResultExhausted:
			return "still failing"
		case chunkd.ResultStuck:
			return "stuck"
		case chunkd.ResultNoChange:
			return "no change"
		}
	}
	if loopFailed(s) {
		return "failed"
	}
	return "done"
}

// loopFailed reports whether the run's loop ended without passing, which a
// factory run does while the run itself finishes cleanly.
func loopFailed(s chunkd.Session) bool {
	return len(s.Stages) > 0 && s.Stages[0].State == chunkd.StageFailed
}

func (m Model) runIcon(st watchStyles, s chunkd.Session) string {
	switch s.State {
	case chunkd.SessionRunning:
		return st.running(spinFrames[m.spinIdx%len(spinFrames)])
	case chunkd.SessionCancelled:
		return st.warning(ui.IconWarn)
	case chunkd.SessionFailed:
		return st.err(ui.IconFail)
	case chunkd.SessionDone:
		if loopFailed(s) {
			return st.err(ui.IconFail)
		}
		return st.success(ui.IconOK)
	}
	return st.muted("?")
}

func sessionElapsed(s chunkd.Session) time.Duration {
	if s.EndedAt != nil {
		return s.EndedAt.Sub(s.StartedAt)
	}
	return time.Since(s.StartedAt)
}

// runHeadline says where a run stands, for the top of its pane.
func runHeadline(st watchStyles, s chunkd.Session) string {
	note := ""
	if len(s.Stages) > 0 {
		note = strings.Join(strings.Fields(s.Stages[0].Note), " ")
	}
	switch s.State {
	case chunkd.SessionRunning:
		if f := s.Factory; f != nil && f.Attempts > 0 {
			return st.running(fmt.Sprintf("running · round %d of at most %d", max(len(s.Rounds), 1), f.Attempts))
		}
		return st.running("running")
	case chunkd.SessionCancelled:
		return st.warning("cancelled")
	case chunkd.SessionFailed:
		return st.err("failed")
	case chunkd.SessionDone:
	}
	if note == "" {
		note = runOutcome(s)
	}
	if loopFailed(s) {
		return st.err(note)
	}
	return st.success(note)
}

// ---- right pane: the selected run ----------------------------------------------

// renderRunPane draws the selected run: where it stands and where its work
// is, then each round's implementer turn, checks and reviews, then the
// run's latest progress and its sidecars. It is windowed so the selected row
// stays on screen.
func (m Model) renderRunPane(st watchStyles, run sessionInfo, maxLines int) []string {
	s := run.s
	title := st.vdim("run")
	if m.focusedPane == paneRight {
		title = st.emphasis("run")
	}
	title += "  " + st.muted(sessionTitle(run))
	head := make([]string, 0, maxLines)
	head = append(head, title, "")

	body := []string{"  " + m.runIcon(st, s) + " " + runHeadline(st, s) + "  " + st.dim(ui.FormatDuration(sessionElapsed(s).Truncate(time.Second)))}
	if f := s.Factory; f != nil {
		body = append(body, "  "+st.muted(truncate(strings.Join(strings.Fields(f.Prompt), " "), 200)))
		if f.Worktree != "" && !f.WorktreeRemoved {
			body = append(body, "  "+st.vdim("worktree ")+st.dim(f.Worktree))
		}
	}
	if s.Error != "" {
		body = append(body, "  "+st.err(truncate(strings.Join(strings.Fields(s.Error), " "), 200)))
	}

	focused := m.focusedPane == paneRight
	selStart, selEnd := -1, -1
	item := 0
	for ri, r := range s.Rounds {
		body = append(body, "")
		rows, at := m.renderRunRound(st, r, item, m.runItem, focused)
		if at >= 0 {
			selStart, selEnd = len(body)+at, len(body)+at+1
		}
		body = append(body, rows...)
		item += len(runItems(chunkd.Session{Rounds: s.Rounds[ri : ri+1]}))
	}
	body = append(body, m.runFooterLines(st, run)...)

	room := maxLines - len(head)
	if len(body) > room && room > 0 {
		first := 0
		if selEnd > room {
			first = min(selStart, selEnd-room)
		}
		body = body[first:min(first+room, len(body))]
	}
	return append(head, body...)
}

// sessionTitle is the run's project, branch and starting commit.
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

// runRow is one selectable row as the run pane draws it.
type runRow struct {
	icon, name, detail string
	extra              []string // lines under the row, not selectable
}

// renderRunRound draws one round's header and rows. first is the run-wide
// index of the round's first row; at is the line within the returned rows of
// the selected one, -1 when it is in another round.
func (m Model) renderRunRound(st watchStyles, r chunkd.Round, first, sel int, focused bool) (lines []string, at int) {
	head := fmt.Sprintf("  Round %d  %s", r.Number, roundStateText(st, r))
	if len(r.Reviews) > 0 && (r.Findings > 0 || r.State == chunkd.RoundDone) {
		head += st.dim(fmt.Sprintf("  ·  %d finding%s, %d worth changing", r.Findings, plural(r.Findings), r.Worth))
	}
	lines = []string{head}

	rows := m.roundRows(st, r)
	width := 0
	for _, row := range rows {
		width = max(width, len(row.name))
	}
	at = -1
	for i, row := range rows {
		prefix := "    "
		if first+i == sel {
			at = len(lines)
			if focused {
				prefix = "  " + st.success("›") + " "
			}
		}
		line := prefix + row.icon + "  " + fmt.Sprintf("%-*s", width, row.name)
		if row.detail != "" {
			line += "   " + row.detail
		}
		lines = append(lines, line)
		lines = append(lines, row.extra...)
	}
	if r.Note != "" {
		lines = append(lines, "      "+st.vdim(r.Note))
	}
	return lines, at
}

// roundRows builds a round's rows in runItems order.
func (m Model) roundRows(st watchStyles, r chunkd.Round) []runRow {
	var rows []runRow
	if impl := r.Implement; impl != nil {
		rows = append(rows, m.implementRow(st, r, impl))
	}
	for _, c := range r.Checks {
		rows = append(rows, checkRow(st, c))
	}
	for _, p := range r.Reviews {
		rows = append(rows, m.reviewRow(st, p))
	}
	return rows
}

func (m Model) spinner(st watchStyles) string {
	return st.running(spinFrames[m.spinIdx%len(spinFrames)])
}

func (m Model) implementRow(st watchStyles, r chunkd.Round, impl *chunkd.RoundImplement) runRow {
	row := runRow{name: "implement"}
	switch impl.State {
	case chunkd.ImplementRunning:
		row.icon = m.spinner(st)
		if !r.StartedAt.IsZero() {
			row.detail = st.dim(ui.FormatDuration(time.Since(r.StartedAt).Truncate(time.Second)))
		}
	case chunkd.ImplementApplied:
		row.icon = st.success(ui.IconOK)
		parts := []string{ui.FormatDuration(time.Duration(impl.DurationMS) * time.Millisecond)}
		if impl.CostUSD > 0 {
			parts = append(parts, fmt.Sprintf("$%.2f", impl.CostUSD))
		}
		if impl.Stat != "" {
			parts = append(parts, impl.Stat)
		}
		row.detail = st.dim(strings.Join(parts, " · "))
	case chunkd.ImplementEmpty:
		row.icon = st.warning(ui.IconWarn)
		row.detail = st.muted("made no changes")
	case chunkd.ImplementFailed:
		row.icon = st.err(ui.IconFail)
		row.detail = st.err(truncate(strings.Join(strings.Fields(impl.Error), " "), 80))
	default:
		row.icon = st.vdim("·")
	}
	return row
}

func checkRow(st watchStyles, c chunkd.RoundCheck) runRow {
	row := runRow{name: c.Name}
	dur := ui.FormatDuration(time.Duration(c.DurationMS) * time.Millisecond)
	switch c.Status {
	case chunkd.CheckPassed:
		row.icon, row.detail = st.success(ui.IconOK), st.dim(dur)
	case chunkd.CheckFailed:
		row.icon, row.detail = st.err(ui.IconFail), st.dim(dur)+"  "+st.err("failed")
	case chunkd.CheckErrored:
		row.icon, row.detail = st.err(ui.IconFail), st.err(truncate(strings.Join(strings.Fields(c.Error), " "), 80))
	default:
		row.icon, row.detail = st.vdim("·"), st.muted(c.Status)
	}
	return row
}

func (m Model) reviewRow(st watchStyles, p chunkd.ReviewPrompt) runRow {
	row := runRow{name: "review: " + p.Name}
	switch p.State {
	case chunkd.PromptQueued:
		row.icon = st.vdim("·")
	case chunkd.PromptRunning:
		row.icon = m.spinner(st)
		row.detail = st.running("↪ " + p.SidecarID[:min(8, len(p.SidecarID))])
	case chunkd.PromptDone:
		row.icon = st.success(ui.IconOK)
		row.detail = st.dim(ui.FormatDuration(time.Duration(p.DurationMS) * time.Millisecond))
		if p.Findings > 0 {
			row.icon = st.warning(ui.IconWarn)
			row.detail += "  " + st.warning(fmt.Sprintf("%d finding%s", p.Findings, plural(p.Findings)))
		}
	case chunkd.PromptFailed:
		row.icon = st.err(ui.IconFail)
		row.detail = st.err(truncate(strings.Join(strings.Fields(p.Error), " "), 80))
	}
	return row
}

func roundStateText(st watchStyles, r chunkd.Round) string {
	switch r.State {
	case chunkd.RoundStarted:
		return st.running("starting")
	case chunkd.RoundImplementing:
		return st.running("implementing")
	case chunkd.RoundChecking:
		return st.running("reviewing and validating")
	case chunkd.RoundDone:
		return st.muted("done")
	case chunkd.RoundFailed:
		return st.err("failed")
	}
	return string(r.State)
}

// runFooterLines are the lines under a run's rounds: while it runs, what it
// last said; once it ends, where its work is; and its sidecars.
func (m Model) runFooterLines(st watchStyles, run sessionInfo) []string {
	s := run.s
	var lines []string
	if f := s.Factory; f != nil {
		if s.State.Finished() {
			lines = append(lines, "", "  "+st.emphasis("work"))
			if f.Branch != "" {
				what := "nothing committed"
				switch {
				case f.Committed && f.Stat != "":
					what = "committed · " + f.Stat
				case f.Committed:
					what = "committed"
				}
				lines = append(lines, "    "+st.vdim("branch   ")+st.muted(f.Branch)+"  "+st.dim(what))
			}
			if f.Log != "" {
				lines = append(lines, "    "+st.vdim("log      ")+st.dim(f.Log))
			}
			if len(f.KeptSidecars) > 0 {
				lines = append(lines, "    "+st.vdim("kept     ")+st.dim(strings.Join(f.KeptSidecars, ", ")))
			}
		} else if feed := f.Progress.Lines; len(feed) > 0 {
			lines = append(lines, "", "  "+st.emphasis("progress"))
			for _, l := range feed[max(len(feed)-3, 0):] {
				lines = append(lines, "    "+feedText(st, l))
			}
		}
	}
	if scs := m.runSidecars[s.ID]; len(scs) > 0 {
		lines = append(lines, "", "  "+st.emphasis("sidecars"))
		for _, sc := range scs {
			name := sc.name
			if name == "" {
				name = sc.id
			}
			line := "    " + st.muted(name) + "  " + st.vdim(shortUUID(sc.id)) + "  " + m.rowStatus(st, sc)
			if res := renderResources(st, sc.resources); res != "" {
				line += "  " + res
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func feedText(st watchStyles, l chunkd.FeedLine) string {
	text := truncate(strings.Join(strings.Fields(l.Text), " "), 200)
	switch l.Level {
	case chunkd.FeedError:
		return st.err(text)
	case chunkd.FeedWarn:
		return st.warning(text)
	case chunkd.FeedDone:
		return st.success(text)
	case chunkd.FeedStep, chunkd.FeedInfo:
	}
	return st.dim(text)
}

// runTag is the header's note of runs in flight, so one started elsewhere is
// visible whatever is selected.
func (m Model) runTag(st watchStyles) string {
	running := 0
	for _, s := range m.sessions {
		if s.s.State == chunkd.SessionRunning {
			running++
		}
	}
	if running == 0 {
		return ""
	}
	return st.running(fmt.Sprintf("%s %d running", spinFrames[m.spinIdx%len(spinFrames)], running)) + "  "
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
