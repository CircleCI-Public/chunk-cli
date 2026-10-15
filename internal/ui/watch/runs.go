package watch

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
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
	id   string
	note string
	err  error
}

func cancelSessionCmd(id string) tea.Cmd {
	return func() tea.Msg {
		if err := chunkd.CancelSession(id); err != nil {
			return sessionActionMsg{id: id, err: err}
		}
		return sessionActionMsg{id: id, note: "cancel requested"}
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

// splitRunSidecars takes the sidecars the daemon assigned to listed factory
// runs out of the sidecar list. Shown as rows of their own they would read as
// loose sidecars nobody owns.
func splitRunSidecars(sidecars []sidecarInfo, runs []sessionInfo) (rest []sidecarInfo, byRun map[string][]sidecarInfo) {
	owners := map[string]string{}
	for _, r := range runs {
		if f := r.s.Factory; f != nil {
			for _, id := range f.SidecarIDs {
				owners[id] = r.s.ID
			}
		}
	}
	byRun = map[string][]sidecarInfo{}
	for _, sc := range sidecars {
		if id, ok := owners[sc.id]; ok {
			byRun[id] = append(byRun[id], sc)
			continue
		}
		rest = append(rest, sc)
	}
	return rest, byRun
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
	run := m.selectedRun()
	if run == nil || run.s.ID != msg.id {
		return m
	}
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
	itemRound
)

// runItem is one selectable row of the run pane: a round, which opens and
// closes, or a row inside an open one.
type runItem struct {
	kind  runItemKind
	round int // index into Session.Rounds
	idx   int // index into the round's Checks or Reviews
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
	case itemRound:
		return m.toggleRound(run), nil
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

// copyRunFailure puts a report of the selected row's failure on the clipboard,
// ready to paste to an agent; on a round's header, the round's failures. A
// row that did not fail falls back to the failures in the latest round, since
// an earlier round's were what the next one set out to fix. The copy travels as an OSC 52 escape sequence, so it
// reaches the user's clipboard over SSH, though a terminal that ignores OSC 52
// drops it silently.
func (m Model) copyRunFailure(run *sessionInfo) (tea.Model, tea.Cmd) {
	if run == nil {
		return m, nil
	}
	// roundReports is every failure in round ri, folded or not.
	roundReports := func(ri int) []string {
		var parts []string
		for _, it := range roundItems(run.s, ri) {
			if r := itemFailureReport(run.s, it); r != "" {
				parts = append(parts, r)
			}
		}
		return parts
	}
	var parts []string
	if items := m.runItems(run.s); m.runItem < len(items) {
		if it := items[m.runItem]; it.kind == itemRound {
			parts = roundReports(it.round)
		} else if r := itemFailureReport(run.s, it); r != "" {
			parts = append(parts, r)
		}
	}
	if len(parts) == 0 && len(run.s.Rounds) > 0 {
		parts = roundReports(len(run.s.Rounds) - 1)
	}
	if len(parts) == 0 && run.s.Error != "" {
		parts = append(parts, "Error: "+run.s.Error)
	}
	if len(parts) == 0 {
		m.runNote = "nothing failed in this run"
		return m, nil
	}
	head := fmt.Sprintf("chunk factory run (%s)\n", sessionTitle(*run))
	if run.s.State == chunkd.SessionFailed {
		head = fmt.Sprintf("chunk factory run failed (%s)\n", sessionTitle(*run))
	}
	m.runNote = "copied failure to clipboard"
	return m, tea.SetClipboard(head + "\n" + strings.Join(parts, "\n\n"))
}

// itemFailureReport describes why a run row failed, in text to paste to an
// agent, or "" when the row did not fail.
func itemFailureReport(s chunkd.Session, it runItem) string {
	r := s.Rounds[it.round]
	var title, reason, output string
	switch it.kind {
	case itemCheck:
		c := r.Checks[it.idx]
		if c.Status != chunkd.CheckFailed && c.Status != chunkd.CheckErrored {
			return ""
		}
		title, reason, output = "check: "+c.Name, c.Error, strings.TrimRight(c.Output, "\n")
	case itemReview:
		p := r.Reviews[it.idx]
		switch {
		case p.State == chunkd.PromptFailed || p.Status == chunkd.CheckErrored:
			reason = p.Error
		case p.Status == chunkd.CheckFailed:
			reason = fmt.Sprintf("%d issue%s worth changing", p.Worth, plural(p.Worth))
		default:
			return ""
		}
		title = "review: " + p.Name
	case itemImplement:
		if r.Implement == nil || r.Implement.State != chunkd.ImplementFailed {
			return ""
		}
		title, reason = "implementer", r.Implement.Error
	case itemRound:
		return ""
	}
	out := fmt.Sprintf("Round %d %s failed", r.Number, title)
	if reason = strings.TrimSpace(reason); reason != "" {
		out += ": " + reason
	}
	if output != "" {
		out += "\n\n```\n" + output + "\n```"
	}
	return out
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

// filterRuns keeps the runs whose title, project, branch or status contains
// every word of query, ignoring case. An empty query keeps them all.
func filterRuns(all []sessionInfo, query string) []sessionInfo {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return all
	}
	var out []sessionInfo
	for _, r := range all {
		hay := strings.ToLower(strings.Join([]string{runTitle(r.s), r.label, r.s.Branch, runStatus(r.s)}, " "))
		match := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				match = false
				break
			}
		}
		if match {
			out = append(out, r)
		}
	}
	return out
}

// setQuery filters the run list by query and keeps the selection on a run
// that is still listed.
func (m Model) setQuery(query string) Model {
	m.query = query
	m.sessions = filterRuns(m.allRuns, query)
	prev := m.selRun
	m = m.reselectRun()
	if m.selRun != prev {
		m.runItem, m.runConfirm, m.runNote = 0, "", ""
	}
	return m.adjustLeftScroll()
}

// updateSearchKey handles a key while the run search has the keyboard: text
// narrows the list as it is typed, Enter keeps the filter and Esc drops it.
func (m Model) updateSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		m.searching = false
		return m, nil
	case "esc":
		m.searching = false
		return m.setQuery(""), nil
	case "backspace":
		if r := []rune(m.query); len(r) > 0 {
			return m.setQuery(string(r[:len(r)-1])), nil
		}
		return m, nil
	}
	if msg.Text != "" {
		return m.setQuery(m.query + msg.Text), nil
	}
	return m, nil
}

// ---- left pane: the run list ---------------------------------------------------

// renderRunList draws the runs at the top of the left pane, or nothing when
// there are none, so a dashboard with no runs looks as it always has.
func (m Model) renderRunList(st watchStyles) []string {
	if len(m.allRuns) == 0 {
		return nil
	}
	head := st.vdim("runs")
	if m.focusedPane == paneLeft && m.selRun != "" {
		head = st.emphasis("runs")
	}
	lines := []string{head, m.searchRow(st)}
	if len(m.sessions) == 0 {
		return append(lines, "  "+st.dim("no runs match"), "")
	}
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
		status := runDetail(r.s)
		if len(m.projects) > 1 {
			status = r.label + " · " + status
		}
		chip := runChip(st, r.s)
		lines = append(lines, "    "+chip+" "+st.vdim(truncate(status, leftPaneWidth-5-lipgloss.Width(chip))))
	}
	return append(lines, "")
}

// searchRow is the line under the runs heading that is always there, so the
// search can be found before it is used: a hint, then what is being typed, then
// the filter that is kept.
func (m Model) searchRow(st watchStyles) string {
	switch {
	case m.searching:
		return st.teal("⌕ ") + truncate(m.query, leftPaneWidth-4) + st.teal("▏")
	case m.query != "":
		return st.teal("⌕ ") + st.muted(truncate(m.query, leftPaneWidth-14)) + "  " + st.vdim("Esc clear")
	}
	return st.vdim("⌕ search runs") + "  " + st.vdim("/")
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

// outcomeFailed is how a run that failed is named in the run list.
const outcomeFailed = "failed"

// runChip is the run's state as a coloured block: the one thing in the run
// list the eye should find first.
func runChip(st watchStyles, s chunkd.Session) string {
	text, bg := "running", "4"
	if s.State != chunkd.SessionRunning {
		text = truncate(runOutcome(s), 14)
		switch text {
		case "cancelled", "no change", "stuck":
			bg = "3"
		case outcomeFailed, "still failing":
			bg = "1"
		default:
			bg = "2"
		}
	}
	return st.Chip.Background(lipgloss.Color(bg)).Render(" " + text + " ")
}

// runDetail is runStatus without the word the chip already says.
func runDetail(s chunkd.Session) string {
	if s.State == chunkd.SessionRunning {
		return runStatus(s)
	}
	if s.EndedAt != nil {
		return ago(*s.EndedAt)
	}
	return ""
}

// runOutcome is how an ended run came out, in a word or two.
func runOutcome(s chunkd.Session) string {
	switch s.State {
	case chunkd.SessionCancelled:
		return "cancelled"
	case chunkd.SessionFailed:
		return outcomeFailed
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
		return outcomeFailed
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
		for _, l := range tailLines(c.Output, failTailLines) {
			row.extra = append(row.extra, "        "+st.muted(truncate(l, 120)))
		}
	case chunkd.CheckErrored:
		row.icon, row.detail = st.err(ui.IconFail), st.err(truncate(strings.Join(strings.Fields(c.Error), " "), 80))
	default:
		row.icon, row.detail = st.vdim("·"), st.muted(c.Status)
	}
	return row
}

// failTailLines is how many of a failed check's last output lines the run pane
// shows under its row, enough to see why it failed without opening the output.
const failTailLines = 3

// tailLines returns the last n non-blank lines of output as plain text. The
// output is raw, so colour is stripped and carriage-return rewrites resolved,
// as the output pane does: a bare \r inside the split pane would overwrite the
// left pane from the screen's first column.
func tailLines(output string, n int) []string {
	var lines []string
	for _, l := range strings.Split(output, "\n") {
		if l = strings.TrimSpace(resolveCR(ansi.Strip(l))); l != "" {
			lines = append(lines, l)
		}
	}
	return lines[max(len(lines)-n, 0):]
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
		row.icon, row.detail = reviewOutcome(st, p)
		row.detail = st.dim(ui.FormatDuration(time.Duration(p.DurationMS)*time.Millisecond)) + row.detail
	case chunkd.PromptFailed:
		row.icon = st.err(ui.IconFail)
		row.detail = st.err(truncate(strings.Join(strings.Fields(p.Error), " "), 80))
	}
	return row
}

// reviewOutcome is the icon and the note after the duration for a finished
// review. A factory review is a check, and passes unless it found something
// worth changing, so findings that are not worth changing do not fail it.
// Until its status is known, any finding is worth a second look.
func reviewOutcome(st watchStyles, p chunkd.ReviewPrompt) (icon, note string) {
	switch p.Status {
	case chunkd.CheckPassed:
		if p.Findings == 0 {
			return st.success(ui.IconOK), ""
		}
		return st.success(ui.IconOK), "  " + st.dim(fmt.Sprintf("%d issue%s, none worth changing", p.Findings, plural(p.Findings)))
	case chunkd.CheckFailed:
		return st.err(ui.IconFail), "  " + st.err(fmt.Sprintf("%d issue%s, %d worth changing", p.Findings, plural(p.Findings), p.Worth))
	case chunkd.CheckErrored:
		return st.err(ui.IconFail), "  " + st.err(truncate(strings.Join(strings.Fields(p.Error), " "), 80))
	}
	if p.Findings > 0 {
		return st.warning(ui.IconWarn), "  " + st.warning(fmt.Sprintf("%d issue%s found", p.Findings, plural(p.Findings)))
	}
	return st.success(ui.IconOK), ""
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
		if len(failedNames(r)) > 0 {
			return st.err("checks failed")
		}
		return st.muted("done")
	case chunkd.RoundFailed:
		return st.err("failed")
	}
	return string(r.State)
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
	for _, s := range m.allRuns {
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
