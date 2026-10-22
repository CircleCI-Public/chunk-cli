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

// maxRuns caps the ended runs the left pane lists. Live runs are always
// listed; the ended ones fill what is left, newest first, so a long day's
// history does not bury the runs that are still going.
const maxRuns = 20

// staleAfter is how long an ended run keeps its place in the list. A run
// nobody has looked at for a day is history, not something to watch.
const staleAfter = 24 * time.Hour

// sessionInfo is one of the daemon's runs with the project it belongs to.
type sessionInfo struct {
	s     chunkd.Session
	label string
}

// sessionActionMsg reports the outcome of a cancel or delete request.
type sessionActionMsg struct {
	id      string
	note    string
	err     error
	deleted bool
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
	for _, p := range projects {
		label := p.RepoName
		if label == "" {
			label = filepath.Base(p.Root)
		}
		for _, s := range p.Sessions {
			out = append(out, sessionInfo{s: s, label: label})
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

// anySessionLive reports whether a run is running (the spinner runs for it).
func anySessionLive(sessions []sessionInfo) bool {
	for _, s := range sessions {
		if s.s.State == chunkd.SessionRunning {
			return true
		}
	}
	return false
}

// selectedRun is the run the dashboard is showing, or nil when there is none
// to show.
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

// reselectRun keeps the selection valid after a poll: the newest run until
// one has been chosen, and again when the chosen one drops off the list.
func (m Model) reselectRun() Model {
	if m.runIndex() >= 0 {
		return m
	}
	m.selRun, m.runItem = "", 0
	if len(m.sessions) > 0 {
		m.selRun = m.sessions[0].s.ID
	}
	return m
}

// selectRun moves the selection to run i, if there is one there.
func (m Model) selectRun(i int) Model {
	if i < 0 || i >= len(m.sessions) {
		return m
	}
	m.runItem, m.runConfirm, m.runNote = 0, "", ""
	m.selRun = m.sessions[i].s.ID
	return m
}

// moveRunSelection steps through the run list, stopping at either end.
func (m Model) moveRunSelection(delta int) Model {
	i := m.runIndex()
	if i < 0 {
		return m.selectRun(0)
	}
	return m.selectRun(i + delta)
}

// requestRunCancel implements the two-press cancel. The first press asks, the
// second on the same run sends the request.
func (m Model) requestRunCancel(run *sessionInfo, wasConfirm string) (tea.Model, tea.Cmd) {
	if wasConfirm != run.s.ID {
		m.runConfirm, m.confirmDelete, m.runNote = run.s.ID, false, ""
		return m, nil
	}
	m.runNote = "cancelling…"
	return m, cancelSessionCmd(run.s.ID)
}

// listedRuns is the runs the left pane lists: the live ones, plus the finished
// ones when showEnded is set, narrowed by the search. The selected run stays
// listed after it ends, so one being watched does not vanish at the moment its
// result lands.
func (m Model) listedRuns() []sessionInfo {
	runs := m.allRuns
	if !m.showEnded {
		runs = nil
		for _, r := range m.allRuns {
			if !r.s.State.Finished() || r.s.ID == m.selRun {
				runs = append(runs, r)
			}
		}
	}
	return filterRuns(runs, m.query)
}

// endedCount is how many finished runs the toggle would reveal.
func (m Model) endedCount() int {
	n := 0
	for _, r := range m.allRuns {
		if r.s.State.Finished() {
			n++
		}
	}
	return n
}

// toggleEnded shows or hides the finished runs, keeping the selection on a run
// that is still listed. Hiding hides the selected run too if it has finished.
func (m Model) toggleEnded() Model {
	m.showEnded = !m.showEnded
	prev := m.selRun
	if run := m.selectedRun(); !m.showEnded && run != nil && run.s.State.Finished() {
		// Hiding was asked for, so the selected run is not kept listed.
		m.selRun = ""
	}
	m.sessions = m.listedRuns()
	m = m.reselectRun()
	if m.selRun != prev {
		m.runItem, m.runConfirm, m.runNote = 0, "", ""
	}
	return m
}

// requestRunDelete implements the two-press delete of a finished run's record.
// An x pressed once for the run before it ended does not count as the first D.
func (m Model) requestRunDelete(run *sessionInfo, wasConfirm string) (tea.Model, tea.Cmd) {
	if wasConfirm != run.s.ID || !m.confirmDelete {
		m.runConfirm, m.confirmDelete, m.runNote = run.s.ID, true, ""
		return m, nil
	}
	m.runNote = "deleting…"
	return m, deleteSessionCmd(run.s.ID)
}

func deleteSessionCmd(id string) tea.Cmd {
	return func() tea.Msg {
		if err := chunkd.DeleteSession(id); err != nil {
			return sessionActionMsg{id: id, err: err}
		}
		return sessionActionMsg{id: id, note: "deleted", deleted: true}
	}
}

// withSessionAction folds a cancel or delete result into the run pane.
func (m Model) withSessionAction(msg sessionActionMsg) Model {
	if msg.deleted && msg.err == nil {
		// Drop the row now rather than leave it listed until the next poll.
		kept := m.allRuns[:0:0]
		for _, r := range m.allRuns {
			if r.s.ID != msg.id {
				kept = append(kept, r)
			}
		}
		m.allRuns = kept
		m.sessions = m.listedRuns()
		prev := m.selRun
		m = m.reselectRun()
		if m.selRun != prev {
			m.runItem, m.runConfirm, m.runNote = 0, "", ""
		}
		return m
	}
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
	// itemTask is the run's prompt, shown collapsed or in full.
	itemTask runItemKind = iota
	itemImplement
	itemCheck
	itemReview
	itemRound
	// itemDetails is the fold under the rounds holding where the run put its
	// work and what it said setting up.
	itemDetails
	// itemBranch and itemPR are the run's branch and pull request, under its
	// rounds.
	itemBranch
	itemPR
)

// runItem is one selectable row of the run pane: the task, a round, both of
// which open and close, or a row inside an open round.
type runItem struct {
	kind  runItemKind
	round int // index into Session.Rounds, unused for itemTask
	idx   int // index into the round's Checks or Reviews
}

// openRunItem opens the output behind the selected row: a review's buffered
// Claude run, a check's kept output, or the implementer's account of its
// turn. On the task or a round's header it expands or collapses it instead,
// since there is no output behind either.
func (m Model) openRunItem(run *sessionInfo, items []runItem) (tea.Model, tea.Cmd) {
	if m.runItem >= len(items) {
		return m, nil
	}
	it := items[m.runItem]
	if it.kind == itemTask || it.kind == itemRound || it.kind == itemDetails {
		return m.toggleRunRow(run)
	}
	if it.kind == itemPR {
		return m.openPullRequest(run)
	}
	if it.kind == itemBranch {
		return m, nil // the branch: nothing to open
	}
	r := run.s.Rounds[it.round]
	switch it.kind { //nolint:exhaustive // the rows above are handled there
	case itemReview:
		rv := r.Reviews[it.idx]
		title := fmt.Sprintf("round %d review: %s", r.Number, rv.Name)
		// A finished review's Claude run printed nothing but one JSON result,
		// so what it found is read from the run's detail instead.
		if rv.State == chunkd.PromptDone || rv.State == chunkd.PromptFailed {
			m.outputSeq++
			return m, fetchReviewDetail(m.detailFn, m.outputSeq, run.s.ID, r.Number, rv, title)
		}
		if rv.CommandID == "" {
			m.runNote = "no output for this review yet"
			return m, nil
		}
		return m.openOutput(rv.CommandID, title, rv.State == chunkd.PromptRunning)
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
// ready to paste to an agent; on a round's header, the round's failures, and
// on the task row, the task's full prompt. A row that did not fail falls back
// to the failures in the latest round, since an earlier round's were what the
// next one set out to fix. The copy travels as an OSC 52 escape sequence, so
// it reaches the user's clipboard over SSH, though a terminal that ignores
// OSC 52 drops it silently.
func (m Model) copyRunFailure(run *sessionInfo) (tea.Model, tea.Cmd) {
	if run == nil {
		return m, nil
	}
	if items := m.runItems(run.s); m.runItem < len(items) && items[m.runItem].kind == itemTask {
		m.runNote = "copied task to clipboard"
		return m, tea.SetClipboard(run.s.Factory.Prompt)
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
	if it.kind == itemTask || it.kind == itemRound || it.kind == itemDetails || it.kind == itemBranch || it.kind == itemPR {
		return ""
	}
	r := s.Rounds[it.round]
	var title, reason, output string
	switch it.kind { //nolint:exhaustive // the rows above return
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

// reviewDetailMsg carries the run detail fetched to show a finished review.
// seq is the pane opening it was fetched for, so an answer arriving after
// another pane opened is dropped.
type reviewDetailMsg struct {
	seq    int
	round  int // the round's Number
	review chunkd.ReviewPrompt
	title  string
	detail chunkd.SessionDetail
	err    error
}

func fetchReviewDetail(fetch func(string) (chunkd.SessionDetail, error), seq int, runID string, round int, rv chunkd.ReviewPrompt, title string) tea.Cmd {
	return func() tea.Msg {
		d, err := fetch(runID)
		return reviewDetailMsg{seq: seq, round: round, review: rv, title: title, detail: d, err: err}
	}
}

// withReviewDetail opens a finished review's findings. Until its round is
// checked the daemon holds no findings for it, so the raw output is shown,
// as it is while the review runs.
func (m Model) withReviewDetail(msg reviewDetailMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.outputSeq {
		return m, nil
	}
	if msg.err != nil {
		m.runNote = "could not read the review: " + msg.err.Error()
		return m, nil
	}
	if res, ok := reviewResult(msg.detail, msg.round, msg.review.Name); ok {
		return m.openText(msg.title, reviewText(res)), nil
	}
	if msg.review.Error != "" {
		return m.openText(msg.title, "Could not run: "+msg.review.Error), nil
	}
	if msg.review.CommandID == "" {
		m.runNote = "no output for this review yet"
		return m, nil
	}
	return m.openOutput(msg.review.CommandID, msg.title, false)
}

// reviewResult finds what the review named prompt said in round n.
func reviewResult(d chunkd.SessionDetail, n int, prompt string) (chunkd.ReviewResult, bool) {
	for _, rd := range d.Details {
		if rd.Number != n {
			continue
		}
		for _, res := range rd.Results {
			if res.Prompt == prompt {
				return res, true
			}
		}
	}
	return chunkd.ReviewResult{}, false
}

// reviewText is a review's outcome and findings as the pane shows them.
func reviewText(res chunkd.ReviewResult) string {
	if res.Error != "" {
		return "Could not run: " + res.Error
	}
	if len(res.Findings) == 0 {
		return "No findings."
	}
	parts := []string{fmt.Sprintf("%d finding%s", len(res.Findings), plural(len(res.Findings)))}
	for _, f := range res.Findings {
		part := fmt.Sprintf("[%s] %s\n%s", f.Severity, f.Location(), strings.TrimSpace(f.Body))
		if p := strings.TrimRight(f.Patch, "\n"); p != "" {
			part += "\n\n" + p
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "\n\n")
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
	m.sessions = m.listedRuns()
	prev := m.selRun
	m = m.reselectRun()
	if m.selRun != prev {
		m.runItem, m.runConfirm, m.runNote = 0, "", ""
	}
	return m
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

// renderRunList draws the left pane: every run the daemon is tracking, or,
// when there are none, where runs come from.
func (m Model) renderRunList(st watchStyles) []string {
	head := st.vdim("runs")
	if m.focusedPane == paneLeft {
		head = st.emphasis("runs")
	}
	if len(m.allRuns) == 0 {
		return []string{head, "", st.muted("no runs yet"), ""}
	}
	lines := []string{head, m.searchRow(st)}
	if len(m.sessions) == 0 {
		if m.query == "" && !m.showEnded {
			return append(lines, "  "+st.dim("no active runs"), "  "+st.dim("e shows finished"), "")
		}
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
			status = strings.TrimSuffix(r.label+" · "+status, " · ")
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
	out := runLabel(s)
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
	text, bg := "running", chunkYellow
	if s.State != chunkd.SessionRunning {
		text = truncate(runLabel(s), 14)
		switch text {
		case "cancelled", "no change", "stuck", prLabelFeedback, prLabelClosed:
			bg = lipgloss.Color("3")
		case outcomeFailed, "still failing", prLabelChecksFailing:
			bg = lipgloss.Color("1")
		case prLabelMerged:
			bg = lipgloss.Color("5")
		default:
			bg = chunkGreen
		}
	}
	return st.Chip.Background(bg).Render(" " + text + " ")
}

// runDetail is runStatus without the word the chip already says. An ended run
// leads with its pull request, which is what there is to go and look at.
func runDetail(s chunkd.Session) string {
	if s.State == chunkd.SessionRunning {
		return runStatus(s)
	}
	var parts []string
	if f := s.Factory; f != nil && f.PullRequest.Opened() {
		parts = append(parts, fmt.Sprintf("PR #%d", f.PullRequest.Number))
	}
	if s.EndedAt != nil {
		parts = append(parts, ago(*s.EndedAt))
	}
	return strings.Join(parts, " · ")
}

// How the run list names a pull request that has got somewhere, for
// prLabel. They are chip text, so each fits in the chip.
const (
	prLabelUp            = "PR up"
	prLabelInReview      = "in review"
	prLabelFeedback      = "feedback"
	prLabelChecksFailing = "checks failing"
	prLabelMerged        = "merged"
	prLabelClosed        = "closed"
)

// prLabel names where a run's open pull request stands, "" when it is not
// open or the daemon has not looked it up yet.
func prLabel(pr *chunkd.PullRequest) string {
	if !pr.Opened() {
		return ""
	}
	switch pr.Status {
	case chunkd.PRStatusUp:
		return prLabelUp
	case chunkd.PRStatusInReview:
		return prLabelInReview
	case chunkd.PRStatusFeedback:
		return prLabelFeedback
	case chunkd.PRStatusChecksFailing:
		return prLabelChecksFailing
	case chunkd.PRStatusMerged:
		return prLabelMerged
	case chunkd.PRStatusClosed:
		return prLabelClosed
	}
	return ""
}

// runLabel is what the run list says of an ended run: where its pull request
// stands once there is one, which is what happens to the work after the run,
// and otherwise how the run came out.
func runLabel(s chunkd.Session) string {
	if s.State == chunkd.SessionDone && s.Factory != nil && !loopFailed(s) {
		if l := prLabel(s.Factory.PullRequest); l != "" {
			return l
		}
	}
	return runOutcome(s)
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
		rows = append(rows, m.checkRow(st, c))
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
			row.detail = st.dim(strings.Join(ui.TurnFacts(r, time.Now()), " · "))
		}
		// What the implementer last said, under the row: what it is doing,
		// in its own words.
		if said := impl.Progress.Said; said != "" {
			row.extra = append(row.extra, "        "+st.muted(truncate(said, max(m.width-leftPaneWidth-16, 20))))
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

func (m Model) checkRow(st watchStyles, c chunkd.RoundCheck) runRow {
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
	case chunkd.CheckRunning:
		row.icon, row.detail = m.spinner(st), st.running("running")
	default:
		row.icon, row.detail, row.waiting = st.vdim("·"), st.dim("waiting"), true
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
	row := runRow{name: p.Name}
	switch p.State {
	case chunkd.PromptQueued:
		row.icon, row.detail, row.waiting = st.vdim("·"), st.dim("waiting"), true
	case chunkd.PromptRunning:
		row.icon, row.detail = m.spinner(st), st.running("running")
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

// ---- the branch and pull request ---------------------------------------------

// rowNoteFor is how long a row says what was done with it.
const rowNoteFor = 4 * time.Second

// rowNote is what the last open or copy of a run's branch or pull request
// said, shown on that row until it expires.
type rowNote struct {
	runID string
	kind  runItemKind
	text  string
	seq   int
}

// rowNoteExpiredMsg clears the row note numbered seq.
type rowNoteExpiredMsg struct{ seq int }

// prOpenedMsg is the outcome of asking the browser to open a pull request.
type prOpenedMsg struct {
	runID, url string
	err        error
}

// noteRow shows text on run's row of the given kind for a few seconds.
func (m Model) noteRow(runID string, kind runItemKind, text string) (Model, tea.Cmd) {
	m.noteSeq++
	seq := m.noteSeq
	m.rowNote = rowNote{runID: runID, kind: kind, text: text, seq: seq}
	return m, tea.Tick(rowNoteFor, func(time.Time) tea.Msg { return rowNoteExpiredMsg{seq: seq} })
}

// openPullRequest opens the run's pull request in the browser. A run with no
// open pull request has nothing to open.
func (m Model) openPullRequest(run *sessionInfo) (tea.Model, tea.Cmd) {
	if run == nil || run.s.Factory == nil || !run.s.Factory.PullRequest.Opened() {
		return m, nil
	}
	id, url, open := run.s.ID, run.s.Factory.PullRequest.URL, m.openURL
	return m, func() tea.Msg { return prOpenedMsg{runID: id, url: url, err: open(url)} }
}

// withPullRequestOpened says on the pull request's row how opening it went. With
// no browser to open it in (over SSH, say), it copies the link instead.
func (m Model) withPullRequestOpened(msg prOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil {
		return m.noteRow(msg.runID, itemPR, "opened in your browser")
	}
	next, cmd := m.noteRow(msg.runID, itemPR, "could not open a browser, URL copied")
	return next, tea.Batch(tea.SetClipboard(msg.url), cmd)
}

// copyRunItem copies what has the cursor: the pull request's URL, the
// branch's name, or for any other row the failure it stands for.
func (m Model) copyRunItem(run *sessionInfo) (tea.Model, tea.Cmd) {
	it := m.selectedRunItem()
	if run == nil || run.s.Factory == nil || it == nil {
		return m.copyRunFailure(run)
	}
	var text string
	switch it.kind {
	case itemPR:
		text = run.s.Factory.PullRequest.URL
	case itemBranch:
		text = run.s.Factory.Branch
	case itemTask, itemImplement, itemCheck, itemReview, itemRound, itemDetails:
		return m.copyRunFailure(run)
	}
	next, cmd := m.noteRow(run.s.ID, it.kind, "copied")
	return next, tea.Batch(tea.SetClipboard(text), cmd)
}

// copyRunRow copies the run's branch name or pull request URL whatever has the
// cursor, and says so on that row.
func (m Model) copyRunRow(run *sessionInfo, kind runItemKind) (tea.Model, tea.Cmd) {
	if run == nil || run.s.Factory == nil {
		return m, nil
	}
	text := run.s.Factory.Branch
	if kind == itemPR {
		// Only an open pull request has a link worth copying.
		text = ""
		if pr := run.s.Factory.PullRequest; pr.Opened() {
			text = pr.URL
		}
	}
	if text == "" {
		return m, nil
	}
	next, cmd := m.noteRow(run.s.ID, kind, "copied")
	return next, tea.Batch(tea.SetClipboard(text), cmd)
}

// updateRowNote handles what the branch and pull request rows hear about: what
// the terminal can send, how opening the pull request went, and a note's timer.
func (m Model) updateRowNote(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyboardEnhancementsMsg:
		// A terminal that disambiguates modified keys sends Cmd+Enter as its own
		// key; the rest keep it for themselves.
		m.superEnter = msg.SupportsKeyDisambiguation()
	case prOpenedMsg:
		return m.withPullRequestOpened(msg)
	case rowNoteExpiredMsg:
		if m.rowNote.seq == msg.seq {
			m.rowNote = rowNote{}
		}
	}
	return m, nil
}
