package watch

import (
	"fmt"
	"maps"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// The run pane reads top to bottom as the run does: what was asked, where the
// run stands, each round, then where the work ended up. Rounds that are done
// collapse to a line, so the one that matters — the latest, or on a run that
// ended failing the one that failed — has the room. Where the run put its work
// and what it said while setting up are behind "details".

// maxGoalLines caps how much of the prompt the pane spends lines on.
const maxGoalLines = 3

// minBodyLines is the least the rounds are left with before the outcome stops
// being pinned under them and scrolls with them instead.
const minBodyLines = 4

// failedNames lists what failed in a round: its implementer turn, its checks
// and its reviews, by name.
func failedNames(r chunkd.Round) []string {
	var names []string
	if r.Implement != nil && r.Implement.State == chunkd.ImplementFailed {
		names = append(names, "implement")
	}
	for _, c := range r.Checks {
		if c.Status == chunkd.CheckFailed || c.Status == chunkd.CheckErrored {
			names = append(names, c.Name)
		}
	}
	for _, p := range r.Reviews {
		if p.State == chunkd.PromptFailed || p.Status == chunkd.CheckFailed || p.Status == chunkd.CheckErrored {
			names = append(names, "review: "+p.Name)
		}
	}
	return names
}

func roundFailing(r chunkd.Round) bool {
	return r.State == chunkd.RoundFailed || len(failedNames(r)) > 0
}

// lastFailingRound is the index of the latest round that failed, -1 if none did.
func lastFailingRound(s chunkd.Session) int {
	for i := len(s.Rounds) - 1; i >= 0; i-- {
		if roundFailing(s.Rounds[i]) {
			return i
		}
	}
	return -1
}

// defaultOpenRound is the round shown in full unless the user chose otherwise:
// the latest while the run goes, and once it has ended failing the round that
// failed, since that is the one to read. A run that came good opens its last
// round, the one that fixed what earlier rounds failed.
func defaultOpenRound(s chunkd.Session) int {
	if s.State.Finished() && loopFailed(s) {
		if i := lastFailingRound(s); i >= 0 {
			return i
		}
	}
	return len(s.Rounds) - 1
}

// roundOpen reports whether round ri is drawn in full.
func (m Model) roundOpen(s chunkd.Session, ri int) bool {
	if open, ok := m.runOpen[s.ID][ri]; ok {
		return open
	}
	return ri == defaultOpenRound(s)
}

// hasTask reports whether s has a prompt worth a task row. Older runs carry
// none.
func hasTask(s chunkd.Session) bool {
	return s.Factory != nil && s.Factory.Prompt != ""
}

// runItems lists a run's selectable rows in the order the pane draws them:
// the task, when there is one, then each round's header, then, when it is
// open, its rows.
func (m Model) runItems(s chunkd.Session) []runItem {
	var items []runItem
	if hasTask(s) {
		items = append(items, runItem{kind: itemTask})
	}
	for ri := range s.Rounds {
		items = append(items, runItem{kind: itemRound, round: ri})
		if m.roundOpen(s, ri) {
			items = append(items, roundItems(s, ri)...)
		}
	}
	if hasDetails(s) {
		items = append(items, runItem{kind: itemDetails, round: -1})
	}
	return append(items, outcomeItems(s)...)
}

// outcomeItems are the selectable rows under the rounds: the branch once the
// run has ended, and the pull request once it is open. A pull request still
// being pushed, or one that failed or was skipped, has no link to act on, so
// it is drawn but cannot take the cursor.
func outcomeItems(s chunkd.Session) []runItem {
	f := s.Factory
	if f == nil {
		return nil
	}
	var items []runItem
	if s.State.Finished() && f.Branch != "" {
		items = append(items, runItem{kind: itemBranch, round: -1})
	}
	if f.PullRequest.Opened() {
		items = append(items, runItem{kind: itemPR, round: -1})
	}
	return items
}

// roundItems lists round ri's rows under its header, open or not.
func roundItems(s chunkd.Session, ri int) []runItem {
	r := s.Rounds[ri]
	var items []runItem
	if r.Implement != nil {
		items = append(items, runItem{kind: itemImplement, round: ri})
	}
	for i := range r.Checks {
		items = append(items, runItem{kind: itemCheck, round: ri, idx: i})
	}
	for i := range r.Reviews {
		items = append(items, runItem{kind: itemReview, round: ri, idx: i})
	}
	return items
}

// selectedRunItem is the selected run's selected row, nil when no run is
// selected or the selection is past its rows.
func (m Model) selectedRunItem() *runItem {
	run := m.selectedRun()
	if run == nil {
		return nil
	}
	items := m.runItems(run.s)
	if m.runItem >= len(items) {
		return nil
	}
	return &items[m.runItem]
}

// findRunItem is the index of row it among s's rows: the row itself, else its
// round's header when the round has closed over it, else the last row.
func (m Model) findRunItem(s chunkd.Session, it runItem) int {
	items := m.runItems(s)
	header := -1
	for i, cand := range items {
		if cand == it {
			return i
		}
		if cand.kind == itemRound && cand.round == it.round {
			header = i
		}
	}
	if header >= 0 {
		return header
	}
	return max(len(items)-1, 0)
}

// toggleRound opens or closes the selected row's round and leaves the
// selection on its header, which is still there when the rows under it are not.
func (m Model) toggleRound(run *sessionInfo) Model {
	items := m.runItems(run.s)
	if m.runItem >= len(items) {
		return m
	}
	ri := items[m.runItem].round
	if ri < 0 {
		return m
	}
	byRun := maps.Clone(m.runOpen)
	if byRun == nil {
		byRun = map[string]map[int]bool{}
	}
	rounds := maps.Clone(byRun[run.s.ID])
	if rounds == nil {
		rounds = map[int]bool{}
	}
	rounds[ri] = !m.roundOpen(run.s, ri)
	byRun[run.s.ID] = rounds
	m.runOpen = byRun
	m.runItem = m.findRunItem(run.s, runItem{kind: itemRound, round: ri})
	return m
}

// taskExpanded reports whether runID's task row is read in full.
func (m Model) taskExpanded(runID string) bool {
	return m.taskOpen[runID]
}

// toggleTask flips whether runID's task is read in full. The state is kept
// for as long as the dashboard runs, by run ID, so switching to another run
// and back leaves it as it was.
func (m Model) toggleTask(runID string) Model {
	open := maps.Clone(m.taskOpen)
	if open == nil {
		open = map[string]bool{}
	}
	open[runID] = !m.taskExpanded(runID)
	m.taskOpen = open
	return m
}

// toggleRunRow flips the expand state of the selected row: the task, a
// round, details, or a row inside a round, which folds open under itself.
func (m Model) toggleRunRow(run *sessionInfo) (Model, tea.Cmd) {
	items := m.runItems(run.s)
	if m.runItem >= len(items) {
		return m, nil
	}
	it := items[m.runItem]
	switch it.kind {
	case itemTask:
		return m.toggleTask(run.s.ID), nil
	case itemRound:
		return m.toggleRound(run), nil
	case itemDetails:
		return m.toggleDetails(run.s.ID), nil
	case itemImplement, itemCheck, itemReview:
		return m.toggleRow(run, it)
	case itemBranch, itemPR:
	}
	return m, nil
}

// ---- status ------------------------------------------------------------------

// runBlockers names what stopped a failed run, from its last failing round.
func runBlockers(s chunkd.Session) string {
	i := lastFailingRound(s)
	if i < 0 {
		return ""
	}
	names := failedNames(s.Rounds[i])
	if len(names) > 3 {
		names = append(names[:3], fmt.Sprintf("+%d more", len(names)-3))
	}
	return strings.Join(names, ", ")
}

// runPhase is what a running run is doing now.
func runPhase(s chunkd.Session) string {
	if len(s.Rounds) == 0 {
		return "starting"
	}
	switch s.Rounds[len(s.Rounds)-1].State {
	case chunkd.RoundImplementing:
		return "implementing"
	case chunkd.RoundChecking:
		return "reviewing and validating"
	case chunkd.RoundStarted, chunkd.RoundDone, chunkd.RoundFailed:
	}
	return "working"
}

// runHeadline says where a run stands, for the top of its pane: what it is
// doing while it goes, and once it has ended how it ended and what stopped it.
func runHeadline(st watchStyles, s chunkd.Session) string {
	switch s.State {
	case chunkd.SessionRunning:
		if isPlanBuild(s) {
			return st.running(planBuildLabel(s))
		}
		return st.running(roundLabel(s) + " · " + runPhase(s))
	case chunkd.SessionCancelled:
		return st.warning("cancelled")
	case chunkd.SessionFailed:
		return st.err("failed")
	case chunkd.SessionDone:
	}
	note := ""
	if len(s.Stages) > 0 {
		note = strings.Join(strings.Fields(s.Stages[0].Note), " ")
	}
	if note == "" {
		note = runOutcome(s)
	}
	if !loopFailed(s) {
		return st.success(note)
	}
	if b := runBlockers(s); b != "" {
		note += " · blocked by " + b
	}
	return st.err(note)
}

// isPlanBuild is whether s builds a factory plan through factory's tasks
// rather than in rounds of its own.
func isPlanBuild(s chunkd.Session) bool { return s.Factory != nil && s.Factory.PlanID != "" }

// planBuildLabel says where a running plan build stands: the plan's state in
// factory, once the daemon has read it.
func planBuildLabel(s chunkd.Session) string {
	if st := s.Factory.PlanState; st != "" {
		return "Building plan · " + st
	}
	return "Building plan"
}

// planBuildPhase is the plan's phase in factory, short for the run list:
// Building, Integrating, or planning still, until the build session's claim
// has applied the approval.
func planBuildPhase(s chunkd.Session) string {
	phase, _, _ := strings.Cut(s.Factory.PlanState, " - ")
	if phase == "" || phase == "Planning" {
		return "starting"
	}
	return strings.ToLower(phase)
}

// maxBuildLines is how much of a plan build's progress its pane shows
// without opening its details.
const maxBuildLines = 6

// planBuildLines is the latest of what a plan build's session did, for its
// pane: with no rounds, it is how the build is followed.
func planBuildLines(st watchStyles, s chunkd.Session) []string {
	feed := s.Factory.Progress.Lines
	if len(feed) == 0 {
		return nil
	}
	feed = feed[max(len(feed)-maxBuildLines, 0):]
	lines := []string{""}
	for _, l := range feed {
		text := strings.Join(strings.Fields(l.Text), " ")
		if l.Level == chunkd.FeedStep || l.Level == chunkd.FeedDone {
			lines = append(lines, "  "+st.muted(text))
			continue
		}
		lines = append(lines, "  "+st.vdim(text))
	}
	return lines
}

// roundLabel is which round a running run is on, out of the most it will
// run: "Round 2 of up to 3", or "Round 1 of 1" when it runs only one. A run
// that does not say its cap is just "Round 2".
func roundLabel(s chunkd.Session) string {
	n := max(len(s.Rounds), 1)
	f := s.Factory
	switch {
	case f == nil || f.Attempts < 1:
		return fmt.Sprintf("Round %d", n)
	case f.Attempts == 1:
		return fmt.Sprintf("Round %d of 1", n)
	}
	return fmt.Sprintf("Round %d of up to %d", n, f.Attempts)
}

// roundCapNote says what a round is, what the cap on them means and how to
// change it, for a reader who sees "of up to 3" and does not know.
func roundCapNote(attempts int) string {
	rounds := fmt.Sprintf("%d rounds", attempts)
	if attempts == 1 {
		rounds = "1 round"
	}
	return "A round is one implementer pass, then reviews and your checks. Chunk stops after " + rounds +
		" and tells you what is left. Change it with --max-attempts <n>: raise it for large changes, lower it to cap cost."
}

// hasRoundCap reports whether s is a running run that says how many rounds
// it will run, so there is a cap to explain.
func hasRoundCap(s chunkd.Session) bool {
	return s.State == chunkd.SessionRunning && s.Factory != nil && s.Factory.Attempts > 0
}

// capNoteShown reports whether the round cap is explained under s's
// headline: during its first round, when it is new, and after that when the
// user asks, by run ID, as taskOpen does for the task.
func (m Model) capNoteShown(s chunkd.Session) bool {
	if !hasRoundCap(s) {
		return false
	}
	if open, ok := m.capOpen[s.ID]; ok {
		return open
	}
	return len(s.Rounds) <= 1
}

// toggleCapNote shows or hides the round cap's explanation for s.
func (m Model) toggleCapNote(s chunkd.Session) Model {
	open := maps.Clone(m.capOpen)
	if open == nil {
		open = map[string]bool{}
	}
	open[s.ID] = !m.capNoteShown(s)
	m.capOpen = open
	return m
}

// runCost is what the run's implementer turns cost.
func runCost(s chunkd.Session) float64 {
	var total float64
	for _, r := range s.Rounds {
		if r.Implement != nil {
			total += r.Implement.CostUSD
		}
	}
	return total
}

func (m Model) runStatusLine(st watchStyles, s chunkd.Session) string {
	line := "  " + m.runIcon(st, s) + " " + runHeadline(st, s) + "  " + st.dim(ui.FormatDuration(sessionElapsed(s).Truncate(time.Second)))
	if c := runCost(s); c > 0 {
		line += st.dim(fmt.Sprintf(" · $%.2f", c))
	}
	return line
}

// wrapLine word-wraps s to width columns without truncating it: a blank
// line stays blank, and a single word longer than width is kept whole on its
// own line rather than split, since breaking it would trade one overflow for
// another.
func wrapLine(s string, width int) []string {
	if s == "" {
		return []string{""}
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = w
		case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// taskLines is the task row's content: the prompt's lines word-wrapped to
// width, so a line break, heading or list item lands exactly where the prompt
// has it. Collapsed, only the first maxGoalLines wrapped lines are kept, with
// a marker naming how many there are to read in place of the rest. The cap is
// on wrapped lines rather than the prompt's own, since a one-paragraph prompt
// is a single source line and would otherwise fill the pane uncollapsed.
func taskLines(prompt string, width int, expanded bool) []string {
	lines := wrappedTaskLines(prompt, width)
	if len(lines) <= maxGoalLines {
		return lines
	}
	if expanded {
		return append(lines, foldHeader(true, "show less", ""))
	}
	return append(lines[:maxGoalLines:maxGoalLines],
		foldHeader(false, fmt.Sprintf("%d more lines", len(lines)-maxGoalLines), ""))
}

// wrappedTaskLines is the prompt's lines word-wrapped to width, with no fold
// trailer, so a caller can tell whether taskLines would add one.
func wrappedTaskLines(prompt string, width int) []string {
	src := strings.Split(prompt, "\n")
	lines := make([]string, 0, len(src))
	for _, l := range src {
		lines = append(lines, wrapLine(l, width)...)
	}
	return lines
}

// taskWidth is the room the task row's prompt wraps to.
func (m Model) taskWidth() int {
	return max(m.width-leftPaneWidth-8, 20)
}

// renderTaskRows draws the task row at the top of the run pane: the prompt,
// collapsed to its first lines or expanded in full depending on whether the
// run's task was toggled open. first is the run-wide index of the task row
// and sel the run-wide selected index; at and end mark the selected row's
// lines within the returned slice the same way renderRunRound does, so an
// expanded prompt longer than the pane scrolls into view the same way an
// open round does. n is 1 when the run has a task row, 0 when it has none.
func (m Model) renderTaskRows(st watchStyles, s chunkd.Session, first, sel int, focused bool) (lines []string, at, end, n int) {
	at, end = -1, -1
	if !hasTask(s) {
		return nil, at, end, 0
	}
	width := m.taskWidth()
	var rows []string
	if o := s.Factory.SpecOrigin; o != "" {
		rows = append(rows, st.vdim(truncate("from "+o, width)))
	}
	taskRows := taskLines(s.Factory.Prompt, width, m.taskExpanded(s.ID))
	// A fold's trailer — "N more lines" or "show less" — is drawn vdim, not
	// muted like the prompt, so it reads as the fold it is.
	trailer := len(wrappedTaskLines(s.Factory.Prompt, width)) > maxGoalLines
	for i, l := range taskRows {
		if trailer && i == len(taskRows)-1 {
			rows = append(rows, st.vdim(l))
			continue
		}
		rows = append(rows, st.muted(l))
	}
	marker := "  "
	if first == sel {
		at, end = 0, len(rows)
		if focused {
			marker = st.success("› ")
		}
	}
	lines = append(lines, marker+rows[0])
	for _, l := range rows[1:] {
		lines = append(lines, "  "+l)
	}
	return lines, at, end, 1
}

// wrapPane wraps each line to width columns, keeping its colours, with what
// a line wraps to continuing two columns in from where the line starts. at[i]
// is where line i starts in what it returns, and at[len(lines)] is its length.
func wrapPane(lines []string, width int) (out []string, at []int) {
	at = make([]int, 0, len(lines)+1)
	for _, l := range lines {
		at = append(at, len(out))
		if width < 1 || lipgloss.Width(l) <= width {
			out = append(out, l)
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if n > width/2 {
			n = 0
		}
		for i, w := range strings.Split(lipgloss.Wrap(l[n:], width-n-2, ""), "\n") {
			if i == 0 {
				out = append(out, strings.Repeat(" ", n)+w)
				continue
			}
			out = append(out, strings.Repeat(" ", n+2)+w)
		}
	}
	return out, append(at, len(out))
}

// ---- right pane: the selected run ----------------------------------------------

// renderRunPane draws the selected run. Its title and status stay put and its
// outcome stays under the rounds; the rounds between are windowed so the
// selected row stays on screen.
func (m Model) renderRunPane(st watchStyles, run sessionInfo, maxLines int) []string {
	s := run.s
	title := st.vdim("run")
	if m.focusedPane == paneRight {
		title = st.emphasis("run")
	}
	title += "  " + st.muted(sessionTitle(run))
	head := [2]string{title, m.runStatusLine(st, s)}

	var body []string
	focused := m.focusedPane == paneRight
	selStart, selEnd := -1, -1
	item := 0

	if rows, at, end, n := m.renderTaskRows(st, s, item, m.runItem, focused); n > 0 {
		if at >= 0 {
			selStart, selEnd = len(body)+at, len(body)+end
		}
		body = append(body, rows...)
		item += n
	}
	if s.Error != "" {
		body = append(body, "  "+st.err(truncate(strings.Join(strings.Fields(s.Error), " "), 200)))
	}
	if isPlanBuild(s) {
		body = append(body, planBuildLines(st, s)...)
	}

	for ri := range s.Rounds {
		if ri == 0 || m.roundOpen(s, ri) {
			body = append(body, "")
		}
		rows, at, end, n := m.renderRunRound(st, s, ri, item, m.runItem, focused)
		if at >= 0 {
			selStart, selEnd = len(body)+at, len(body)+end
		}
		body = append(body, rows...)
		item += n
	}
	if rows, at, end, n := m.renderDetailRows(st, run, item, m.runItem, focused); n > 0 {
		if at >= 0 {
			selStart, selEnd = len(body)+at, len(body)+end
		}
		body = append(body, rows...)
	}

	// Every line is wrapped to the pane rather than cut at its edge, and the
	// selected row's span is carried over to the lines it wrapped to.
	width := m.width - leftPaneWidth - 5
	// The title goes into the frame's edge, which cuts it to fit, so only
	// the status under it is wrapped.
	status, _ := wrapPane(head[1:], width)
	headLines := append([]string{head[0]}, status...)
	// The round cap's explanation is help, not the run, so it gives way
	// when the pane is too short or narrow to keep the rounds in view too.
	// It wraps between words only, so the flag it names is never split.
	if m.capNoteShown(s) {
		note := wrapLine(roundCapNote(s.Factory.Attempts), width-4)
		fits := true
		for _, l := range note {
			fits = fits && utf8.RuneCountInString(l) <= width-4
		}
		if fits && maxLines-len(headLines)-len(note) >= minBodyLines {
			for _, l := range note {
				headLines = append(headLines, "    "+st.dim(l))
			}
		}
	}
	body, at := wrapPane(body, width)
	if selStart >= 0 {
		selStart, selEnd = at[selStart], at[selEnd]
	}
	outcome, outcomeSel := m.runOutcomeLines(st, s)
	tail, tailAt := wrapPane(outcome, width)
	tailSel := -1
	if outcomeSel >= 0 {
		tailSel = tailAt[outcomeSel]
	}
	room := maxLines - len(headLines)
	if len(tail) > 0 && room-len(tail) >= minBodyLines {
		room -= len(tail)
	} else {
		if tailSel >= 0 {
			selStart, selEnd = len(body)+tailSel, len(body)+tailSel+1
		}
		body = append(body, tail...)
		tail = nil
	}
	if len(body) > room && room > 0 {
		first := 0
		if selEnd > room {
			first = min(selStart, selEnd-room)
		}
		body = body[first:min(first+room, len(body))]
	}
	out := make([]string, 0, len(headLines)+len(body)+len(tail))
	out = append(append(append(out, headLines...), body...), tail...)
	// A finished run's change fills the room left under its outcome, so it
	// is read where the run is shown, without a key to press.
	return append(out, m.runChangeLines(st, run, maxLines-len(out))...)
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
	// waiting dims the row: it has not started.
	waiting bool
	extra   []string // lines under the row, not selectable
}

// renderRunRound draws round ri: one line when closed, or its header, the
// implementer turn and a validate group of checks and reviews when open. first
// is the run-wide index of the round's header; at is the line of the selected
// row within the returned lines, -1 when it is in another round, and end is
// just past the lines under it, so scrolling keeps them in view too; n is how
// many selectable rows the round has.
func (m Model) renderRunRound(st watchStyles, s chunkd.Session, ri, first, sel int, focused bool) (lines []string, at, end, n int) {
	r := s.Rounds[ri]
	open := m.roundOpen(s, ri)
	at, end = -1, -1
	// pad is the indent in front of a selectable row, with the marker in its
	// last two columns when it is the selected one.
	pad := func(idx, indent int) string {
		p := strings.Repeat(" ", indent-2)
		if first+idx == sel {
			at, end = len(lines), len(lines)+1
			if focused {
				return p + st.success("›") + " "
			}
		}
		return p + "  "
	}

	label := "Round " + fmt.Sprint(r.Number)
	if !open {
		line := pad(0, 2) + st.vdim(foldHeader(false, label, "")) + "  " + m.roundIcon(st, r) + "  " + roundRollup(st, r)
		return []string{line}, at, end, 1
	}

	head := pad(0, 2) + st.vdim(foldHeader(true, label, "")) + "  " + roundStateText(st, r)
	if len(r.Reviews) > 0 && (r.Findings > 0 || r.State == chunkd.RoundDone) {
		head += st.dim(fmt.Sprintf("  ·  %d issue%s, %d worth changing", r.Findings, plural(r.Findings), r.Worth))
	}
	lines = []string{head}
	idx := 1

	rows := m.roundRows(st, r)
	items := roundItems(s, ri)
	// The fold marker gets a column only in a round where a row folds, so a
	// round of passed checks lines up as it always has.
	folds := false
	for _, it := range items {
		folds = folds || rowFoldable(s, it)
	}
	nImpl := 0
	if r.Implement != nil {
		nImpl = 1
	}
	width := 0
	for _, row := range rows[nImpl:] {
		width = max(width, utf8.RuneCountInString(row.name))
	}
	if nImpl == 1 {
		width = max(width, utf8.RuneCountInString(rows[0].name)-2)
	}
	draw := func(row runRow, indent, nameWidth int, branch string) {
		name := row.name
		if row.waiting {
			name = st.dim(name)
		}
		// A row with something behind it carries a fold marker, and open, its
		// fold in place of the few lines it shows under itself when shut.
		it := items[idx-1]
		marker, open := "", false
		if folds {
			marker = "  "
		}
		if rowFoldable(s, it) {
			open = m.rowOpen(s.ID, it)
			marker = st.vdim(foldHeader(open, "", ""))
		}
		line := pad(idx, indent) + branch + marker + row.icon + "  " + name + strings.Repeat(" ", max(nameWidth-utf8.RuneCountInString(row.name), 0))
		if row.detail != "" {
			line += "   " + row.detail
		}
		lines = append(lines, line)
		if open {
			lines = append(lines, m.rowFoldLines(st, s, it)...)
		} else {
			lines = append(lines, row.extra...)
		}
		if first+idx == sel {
			end = len(lines)
		}
		idx++
	}
	if nImpl == 1 {
		draw(rows[0], 4, width+2, "")
	}
	// Checks and reviews are two groups, each with its own count, listed from
	// the round's start so the totals do not grow while it runs.
	rest := rows[nImpl:]
	groups := []struct {
		name string
		rows []runRow
		sum  groupCount
	}{
		{"checks", rest[:len(r.Checks)], checkCount(r)},
		{"reviews", rest[len(r.Checks):], reviewCount(r)},
	}
	if awaitingImplementer(r) && len(rest) > 0 {
		lines = append(lines, "      "+st.dim("both begin when the implementer finishes"))
	}
	for _, g := range groups {
		if len(g.rows) == 0 {
			continue
		}
		label := "    " + st.vdim(g.name)
		if text := g.sum.text(awaitingImplementer(r)); text != "" {
			label += st.dim(" · " + text)
		}
		lines = append(lines, label)
		for i, row := range g.rows {
			// A tree down the left, so the group reads as one thing.
			branch := "├─ "
			if i == len(g.rows)-1 {
				branch = "└─ "
			}
			draw(row, 4, width, st.vdim(branch))
		}
	}
	if r.Note != "" {
		lines = append(lines, "      "+st.vdim(r.Note))
	}
	return lines, at, end, idx
}

// awaitingImplementer reports whether a round's checks and reviews are waiting
// for the implementer to finish its turn. It is the phase the Now line calls
// "the implementer is making the change".
func awaitingImplementer(r chunkd.Round) bool {
	return r.State == chunkd.RoundStarted || r.State == chunkd.RoundImplementing
}

// groupCount tallies one group of a round: its checks or its reviews.
type groupCount struct {
	total, pending, passed, failed int
	// failedWord is what a failure is called in this group. While the group
	// still runs, a check counts as passed or not, but a review is done
	// whatever it found, so countPassed says which to report.
	failedWord  string
	countPassed bool
}

// text says how the group stands: that it has not started, how many have
// finished while some are pending, or how they came out once all have.
func (g groupCount) text(notStarted bool) string {
	switch {
	case g.total == 0:
		return ""
	case notStarted && g.pending == g.total:
		return "not started"
	case g.pending > 0 && g.countPassed:
		return fmt.Sprintf("%d of %d passed", g.passed, g.total)
	case g.pending > 0:
		return fmt.Sprintf("%d of %d done", g.total-g.pending, g.total)
	case g.failed > 0:
		return fmt.Sprintf("%d of %d passed, %d %s", g.passed, g.total, g.failed, g.failedWord)
	}
	return fmt.Sprintf("%d of %d passed", g.passed, g.total)
}

// checkCount tallies a round's validation commands.
func checkCount(r chunkd.Round) groupCount {
	g := groupCount{total: len(r.Checks), failedWord: "failed", countPassed: true}
	for _, c := range r.Checks {
		switch c.Status {
		case chunkd.CheckPassed:
			g.passed++
		case chunkd.CheckFailed, chunkd.CheckErrored:
			g.failed++
		default:
			g.pending++
		}
	}
	return g
}

// reviewCount tallies a round's reviews.
func reviewCount(r chunkd.Round) groupCount {
	g := groupCount{total: len(r.Reviews), failedWord: "needs changes"}
	for _, p := range r.Reviews {
		switch {
		case p.State == chunkd.PromptQueued || p.State == chunkd.PromptRunning:
			g.pending++
		case p.State == chunkd.PromptFailed || p.Status == chunkd.CheckFailed || p.Status == chunkd.CheckErrored:
			g.failed++
		default:
			g.passed++
		}
	}
	return g
}

func (m Model) roundIcon(st watchStyles, r chunkd.Round) string {
	switch {
	case roundFailing(r):
		return st.err(ui.IconFail)
	case r.State == chunkd.RoundDone:
		return st.success(ui.IconOK)
	}
	return m.spinner(st)
}

// roundRollup is a closed round in a line: how it came out, then its findings
// and how long it took.
func roundRollup(st watchStyles, r chunkd.Round) string {
	var parts []string
	switch {
	case r.Note != "":
		parts = append(parts, r.Note)
	case len(failedNames(r)) > 0:
		parts = append(parts, "failed: "+strings.Join(failedNames(r), ", "))
	case r.State == chunkd.RoundDone:
		parts = append(parts, "checks passed")
	default:
		parts = append(parts, roundStateText(st, r))
	}
	if len(r.Reviews) > 0 && r.Findings > 0 {
		parts = append(parts, fmt.Sprintf("%d issue%s, %d worth changing", r.Findings, plural(r.Findings), r.Worth))
	}
	if r.EndedAt != nil && !r.StartedAt.IsZero() {
		parts = append(parts, ui.FormatDuration(r.EndedAt.Sub(r.StartedAt).Truncate(time.Second)))
	}
	return st.dim(strings.Join(parts, "  ·  "))
}

// runOutcomeLines say where an ended run's work is and what to do next, or
// while it runs, anything that went wrong setting it up. sel is the line the
// cursor is on, -1 when it is not on one of them.
func (m Model) runOutcomeLines(st watchStyles, s chunkd.Session) (lines []string, sel int) {
	f := s.Factory
	if f == nil {
		return nil, -1
	}
	sel = -1
	// lead is the indent of a selectable row, with the cursor in it when the
	// row has it; it also notes where that row lands.
	lead := func(kind runItemKind) string {
		items := m.runItems(s)
		if m.runItem >= len(items) || items[m.runItem].kind != kind {
			return "  "
		}
		sel = len(lines)
		if m.focusedPane == paneRight {
			return st.success("›") + " "
		}
		return "  "
	}
	// note is what the last action on a row said, on a line under it: beside
	// a long URL it would be clipped away.
	note := func(kind runItemKind) []string {
		if n := m.rowNote; n.runID == s.ID && n.kind == kind && n.text != "" {
			return []string{"    " + st.success(n.text)}
		}
		return nil
	}
	if next := nextStepLine(st, s); next != "" {
		lines = append(lines, "  "+next)
	}
	if !s.State.Finished() {
		// The branch is pushed and the pull request opened after the loop has
		// ended but before the session settles, so it earns a line while the
		// run is still shown as running.
		if pr := f.PullRequest; pr != nil && pr.State != chunkd.PRPending {
			lines = append(lines, lead(itemPR)+st.vdim("Pull request: ")+prProgress(st, pr))
			lines = append(lines, note(itemPR)...)
		}
		if feed := f.Progress.Lines; len(feed) > 0 {
			if l := feed[len(feed)-1]; l.Level == chunkd.FeedWarn || l.Level == chunkd.FeedError {
				lines = append(lines, "  "+feedText(st, l))
			}
		}
	} else {
		if f.Branch != "" {
			what := "nothing committed"
			switch {
			case f.Committed && f.Stat != "":
				what = "committed · " + f.Stat
			case f.Committed:
				what = "committed"
			}
			lines = append(lines, lead(itemBranch)+st.vdim("Branch: ")+st.muted(f.Branch)+"  "+st.dim(what))
			lines = append(lines, note(itemBranch)...)
		}
		if pr := f.PullRequest; pr != nil {
			lines = append(lines, lead(itemPR)+st.vdim("Pull request: ")+prProgress(st, pr))
			lines = append(lines, note(itemPR)...)
		}
		if !f.Committed && f.Worktree != "" && !f.WorktreeRemoved {
			lines = append(lines, "  "+st.vdim("work    ")+st.dim("not committed, in "+f.Worktree))
		}
		if f.Continuable() {
			next := f.ContinueCommand()
			if m.continuer != nil {
				next = "press r to continue it, or " + next
			}
			lines = append(lines, "  "+st.vdim("next    ")+st.muted(next))
		}
	}
	if len(lines) == 0 {
		return nil, -1
	}
	if sel >= 0 {
		sel++ // the blank line in front
	}
	return append([]string{""}, lines...), sel
}

// prProgress is where a run's pull request has got to, for the pr row: the
// pull request itself once it is open, and otherwise what is being done about
// it or why there is none.
func prProgress(st watchStyles, pr *chunkd.PullRequest) string {
	switch pr.State {
	case chunkd.PRPending:
		return st.dim("opens when the checks pass")
	case chunkd.PRPushing:
		return st.running("pushing the branch to origin...")
	case chunkd.PROpening:
		return st.running("opening on GitHub...")
	case chunkd.PROpened:
		// A real terminal hyperlink, so Cmd+click works where the terminal
		// supports it and the plain URL shows where it does not.
		line := st.Muted.Hyperlink(pr.URL).Render(pr.URL)
		if pr.Draft {
			line += "  " + st.dim("draft")
		}
		if l := prLabel(pr); l != "" {
			line += "  " + st.dim("· "+l)
		}
		return line
	case chunkd.PRFailed:
		return st.err("could not be opened: " + pr.Message)
	case chunkd.PRSkipped:
		return st.dim("none opened: " + pr.Message)
	}
	return st.dim(pr.State)
}

// nextStepLine tells a person watching the run what is happening and whether
// there is anything for them to do, in plain terms rather than the run's
// internals.
func nextStepLine(st watchStyles, s chunkd.Session) string {
	if s.Factory == nil || len(s.Rounds) == 0 {
		return ""
	}
	last := s.Rounds[len(s.Rounds)-1]
	if !s.State.Finished() {
		// The pull request is the last thing a passing run does, after the
		// rounds are over, so it is what is happening by then.
		switch pr := s.Factory.PullRequest; {
		case pr == nil:
		case pr.State == chunkd.PRPushing, pr.State == chunkd.PROpening:
			return st.dim("Now: the checks passed and the pull request is being opened. Nothing to do yet.")
		}
		switch last.State {
		case chunkd.RoundStarted, chunkd.RoundImplementing:
			return st.dim("Now: the implementer is making the change. Nothing to do yet.")
		case chunkd.RoundChecking:
			return st.dim("Now: checks and reviewers are looking at the change. Nothing to do yet.")
		case chunkd.RoundDone, chunkd.RoundFailed:
		}
		return ""
	}
	switch s.State {
	case chunkd.SessionFailed:
		return st.err(ui.IconFail + " The run failed before it finished.")
	case chunkd.SessionCancelled:
		return st.dim("Cancelled.")
	case chunkd.SessionDone:
		if s.Factory.Result == chunkd.ResultNoChange {
			return st.dim("The implementer made no change, so there is nothing to look at.")
		}
		if names := failedNames(last); len(names) > 0 {
			return st.err(fmt.Sprintf("%s Still failing after the last round: %s", ui.IconFail, strings.Join(names, ", ")))
		}
		if loopFailed(s) {
			return st.err(ui.IconFail + " The run ended without its checks passing.")
		}
		switch pr := s.Factory.PullRequest; {
		case pr.Opened() && pr.Status == chunkd.PRStatusMerged:
			return st.success(ui.IconOK + " The pull request was merged.")
		case pr.Opened() && pr.Status == chunkd.PRStatusClosed:
			return st.warning(ui.IconWarn + " The pull request was closed without being merged.")
		case pr.Opened() && pr.Status == chunkd.PRStatusFeedback:
			return st.warning(ui.IconWarn + " Reviewers left feedback on the pull request. It is waiting on you.")
		case pr.Opened() && pr.Status == chunkd.PRStatusChecksFailing:
			return st.err(ui.IconFail + " The pull request's checks on GitHub are failing.")
		case pr.Opened() && pr.Status == chunkd.PRStatusInReview:
			return st.success(ui.IconOK + " All checks passed. The pull request is in review.")
		case pr.Opened():
			// The pr row below carries the address; a second copy here only
			// crowds the line out of the pane.
			return st.success(ui.IconOK + " All checks passed. The pull request is open and ready for review.")
		case pr != nil && pr.State == chunkd.PRFailed:
			return st.err(ui.IconFail + " All checks passed, but the pull request could not be opened. The work is on the branch.")
		}
		return st.success(ui.IconOK + " All checks passed. The change is ready for you to look at.")
	case chunkd.SessionRunning:
	}
	return ""
}

// reviewSandboxLines names the sandbox each of s's reviews ran in, by round.
func reviewSandboxLines(st watchStyles, s chunkd.Session) []string {
	var lines []string
	for _, r := range s.Rounds {
		for _, p := range r.Reviews {
			if p.SidecarID == "" {
				continue
			}
			id, _, _ := strings.Cut(p.SidecarID, "-")
			lines = append(lines, fmt.Sprintf("      %s  %s  %s",
				st.dim(fmt.Sprintf("round %d", r.Number)), st.muted(p.Name), st.vdim(id)))
		}
	}
	return lines
}

// countReviewSandboxes is how many of s's reviews ran in a sandbox worth
// naming under details.
func countReviewSandboxes(s chunkd.Session) int {
	n := 0
	for _, r := range s.Rounds {
		for _, p := range r.Reviews {
			if p.SidecarID != "" {
				n++
			}
		}
	}
	return n
}

// detailsSummary is what the details fold holds, in a few words, for its
// collapsed header.
func detailsSummary(s chunkd.Session) string {
	f := s.Factory
	if f == nil {
		return ""
	}
	var parts []string
	if f.Worktree != "" && !f.WorktreeRemoved {
		parts = append(parts, "worktree")
	}
	if f.Log != "" {
		parts = append(parts, "log")
	}
	if len(f.Progress.Lines) > 0 {
		parts = append(parts, "progress")
	}
	if n := countReviewSandboxes(s); n > 0 {
		word := "review sandbox"
		if n != 1 {
			word = "review sandboxes"
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, word))
	}
	return strings.Join(parts, ", ")
}

// hasDetails reports whether s has anything worth a details fold under its
// rounds: its worktree, setup log, progress feed, or a review's sandbox.
func hasDetails(s chunkd.Session) bool {
	return detailsSummary(s) != ""
}

// detailLines are where the run put its work and what it said while setting
// up: the details fold's content, drawn only while it is open.
func (m Model) detailLines(st watchStyles, run sessionInfo) []string {
	f := run.s.Factory
	if f == nil {
		return nil
	}
	var lines []string
	if f.Worktree != "" && !f.WorktreeRemoved {
		lines = append(lines, "    "+st.vdim("worktree ")+st.dim(f.Worktree))
	}
	if f.Log != "" {
		lines = append(lines, "    "+st.vdim("log      ")+st.dim(f.Log))
	}
	if feed := f.Progress.Lines; len(feed) > 0 {
		lines = append(lines, "    "+st.vdim("progress"))
		for _, l := range feed[max(len(feed)-5, 0):] {
			lines = append(lines, "      "+feedText(st, l))
		}
	}
	// A review's row no longer carries its sandbox ID; this is where it stays
	// reachable, for a review whose own logs have to be chased.
	if reviewed := reviewSandboxLines(st, run.s); len(reviewed) > 0 {
		lines = append(lines, "    "+st.vdim("review sandboxes"))
		lines = append(lines, reviewed...)
	}
	return lines
}

// detailsShown reports whether run's details fold is read in full.
func (m Model) detailsShown(runID string) bool {
	return m.detailsOpen[runID]
}

// toggleDetails flips whether runID's details fold is open. Kept per run, as
// taskOpen is, so switching to another run and back leaves it as it was.
func (m Model) toggleDetails(runID string) Model {
	open := maps.Clone(m.detailsOpen)
	if open == nil {
		open = map[string]bool{}
	}
	open[runID] = !m.detailsShown(runID)
	m.detailsOpen = open
	return m
}

// renderDetailRows draws the details fold under the rounds: closed, a line
// naming what it holds; open, the run's worktree, log, setup feed and each
// review's sandbox. first is the run-wide index of the details row and sel
// the run-wide selected index; at and end mark the selected row's lines
// within the returned slice the same way renderRunRound does, so an open
// details block longer than the pane scrolls into view the same way an open
// round does.
func (m Model) renderDetailRows(st watchStyles, run sessionInfo, first, sel int, focused bool) (lines []string, at, end, n int) {
	at, end = -1, -1
	if !hasDetails(run.s) {
		return nil, at, end, 0
	}
	open := m.detailsShown(run.s.ID)
	marker, selected := "  ", first == sel
	if selected && focused {
		marker = st.success("› ")
	}
	lines = append(lines, "", marker+st.vdim(foldHeader(open, "details", detailsSummary(run.s))))
	if open {
		lines = append(lines, m.detailLines(st, run)...)
	}
	if selected {
		at, end = 1, len(lines)
	}
	return lines, at, end, 1
}
