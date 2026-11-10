package plan

import (
	"fmt"
	"image/color"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/chat"
)

// The Overview is the left pane once the plan exists: where the plan is in
// factory, beside the plan's text. It holds the plan's Outcome, its
// sub-flows, a timeline of the stages the flow goes through, and the plans
// related to it. Each stage keeps a log of what this screen saw happen in it.

const (
	// overviewGutter is the cursor column's width.
	overviewGutter = 2
	// outcomeCut is how many lines of the Outcome show while it is collapsed;
	// a cut hides at least two, so it never hides just one.
	outcomeCut = 6
	// logCut is how many of the newest lines of the current stage's log show
	// while it is collapsed.
	logCut = 6
)

// The stages the Overview treats on their own.
const (
	stageCreated     = "Created"
	stagePlanning    = "Planning"
	stageBuilding    = "Building"
	stageIntegrating = "Integrating"
)

// overviewKind is what a row of the Overview is.
type overviewKind int

const (
	rowOutcome overviewKind = iota
	rowSubFlow
	rowStage
	rowRelated
)

// overviewRow is a row of the Overview the cursor stops on.
type overviewRow struct {
	kind overviewKind
	// stage is a rowStage's stage; rel is a rowSubFlow's or rowRelated's plan.
	stage string
	rel   relatedRow
}

// same is whether r and o are the same row: of the same kind, and for the
// same stage or plan.
func (r overviewRow) same(o overviewRow) bool {
	return r.kind == o.kind && r.stage == o.stage && r.rel.label == o.rel.label && r.rel.ID == o.rel.ID
}

// relatedRow is a row of the Overview's Related section: a relative, and how
// it is related.
type relatedRow struct {
	label string
	Relative
}

// relatedRows is the plan's relatives as Related lists them: its parent, then
// the plans blocking it. Its sub-plans are under Sub-flows.
func (m Model) relatedRows() []relatedRow {
	var rows []relatedRow
	if p := m.family.Parent; p != nil {
		rows = append(rows, relatedRow{"Part of", *p})
	}
	for _, b := range m.family.Blockers {
		rows = append(rows, relatedRow{"Blocked by", b})
	}
	return rows
}

// stageNow is the stage the flow is in: planstate.Stage of its state, Created
// before factory has given the plan one, and Building for a plan whose build
// this screen has begun, by approving the plan (applyApproval's approvedMsg)
// or by starting a build session with R (buildResumed), while factory has yet
// to move it; "" for a state the stages do not know.
func (m Model) stageNow() string {
	state := clean(m.state)
	stage := planstate.Stage(state)
	if state == "" {
		stage = stageCreated
	}
	if m.built && (stage == "" || stage == stageCreated || stage == stagePlanning) {
		return stageBuilding
	}
	return stage
}

// timeline is the stages the Activity section draws: the one the flow is in,
// the one after it, and the ones it has finished, newest first. cur is "" for
// a state the stages do not know, and then there are no others.
func (m Model) timeline() (cur, next string, finished []string) {
	cur = m.stageNow()
	i := slices.Index(planstate.Stages, cur)
	if i < 0 {
		return cur, "", nil
	}
	if cur != done {
		next = planstate.Stages[i+1]
	}
	for j := i - 1; j >= 0; j-- {
		finished = append(finished, planstate.Stages[j])
	}
	return cur, next, finished
}

// overviewRows is the Overview's rows in the order it draws them.
func (m Model) overviewRows() []overviewRow {
	var rows []overviewRow
	if _, ok := m.outcomeSection(); ok {
		rows = append(rows, overviewRow{kind: rowOutcome})
	}
	for _, c := range m.family.Children {
		rows = append(rows, overviewRow{kind: rowSubFlow, rel: relatedRow{"Sub-flow", c}})
	}
	if cur, _, finished := m.timeline(); cur != "" {
		rows = append(rows, overviewRow{kind: rowStage, stage: cur})
		for _, st := range finished {
			rows = append(rows, overviewRow{kind: rowStage, stage: st})
		}
	}
	for _, r := range m.relatedRows() {
		rows = append(rows, overviewRow{kind: rowRelated, rel: r})
	}
	return rows
}

// cursorRow is the row under the Overview's cursor, and whether there is one.
func (m Model) cursorRow() (overviewRow, bool) {
	rows := m.overviewRows()
	if m.overviewCursor < 0 || m.overviewCursor >= len(rows) {
		return overviewRow{}, false
	}
	return rows[m.overviewCursor], true
}

// restoreCursor puts the cursor back on row, found again among the rows as
// they now are; when it is gone it stays where it was, as far as there are
// rows.
func (m *Model) restoreCursor(row overviewRow, had bool) {
	rows := m.overviewRows()
	if had {
		if i := slices.IndexFunc(rows, row.same); i >= 0 {
			m.overviewCursor = i
			return
		}
	}
	m.overviewCursor = max(min(m.overviewCursor, len(rows)-1), 0)
}

// currentRow is the index of the current stage's row, or 0 when there is none.
func (m Model) currentRow() int {
	cur := m.stageNow()
	i := slices.IndexFunc(m.overviewRows(), func(r overviewRow) bool { return r.kind == rowStage && r.stage == cur })
	return max(i, 0)
}

// scrollToCursor scrolls the Overview, if it must, to have the cursor's row's
// first line in view.
func (m *Model) scrollToCursor() {
	if m.overviewCursor < 0 || m.overviewCursor >= len(m.rowLines) {
		return
	}
	line, top, h := m.rowLines[m.overviewCursor], m.overviewView.YOffset(), m.overviewView.Height()
	switch {
	case line < top:
		m.overviewView.SetYOffset(line)
	case line >= top+h:
		m.overviewView.SetYOffset(line - h + 1)
	}
}

// updateOverviewKey handles a key for the Overview: ↑/↓ move the cursor over
// its rows, the page keys scroll, enter expands or collapses the row under the
// cursor or opens a relative, and s sends the queued comments. The plan's own
// keys do nothing here.
func (m Model) updateOverviewKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	k := m.keys()
	rows := m.overviewRows()
	switch {
	case key.Matches(msg, k.Up):
		m.overviewCursor = max(m.overviewCursor-1, 0)
	case key.Matches(msg, k.Down):
		m.overviewCursor = max(min(m.overviewCursor+1, len(rows)-1), 0)
	case key.Matches(msg, k.PageUp):
		m.overviewView.PageUp()
		return m, nil
	case key.Matches(msg, k.PageDown):
		m.overviewView.PageDown()
		return m, nil
	case key.Matches(msg, k.SendComments):
		return m.deliver()
	case key.Matches(msg, k.Open):
		if m.overviewCursor >= len(rows) {
			return m, nil
		}
		switch r := rows[m.overviewCursor]; r.kind {
		case rowOutcome:
			m.toggle("outcome")
		case rowStage:
			m.toggle(r.stage)
		case rowSubFlow, rowRelated:
			return m, func() tea.Msg { return OpenMsg{ID: r.rel.ID, Title: r.rel.Title} }
		}
	default:
		return m, nil
	}
	m.renderOverview()
	m.scrollToCursor()
	return m, nil
}

// toggle opens id, "outcome" or a stage, or closes it. It writes to a copy of
// the map, as a copy of the screen may still read the one it had.
func (m *Model) toggle(id string) {
	next := maps.Clone(m.expanded)
	if next == nil {
		next = map[string]bool{}
	}
	next[id] = !next[id]
	m.expanded = next
}

// renderOverview draws the Overview into its view. The view keeps its scroll;
// only a move of the cursor, or the first refresh, brings the cursor into
// view.
func (m *Model) renderOverview() {
	w := m.overviewView.Width()
	if w <= 0 {
		return
	}
	content, starts, folds := m.drawOverview(w)
	m.rowLines, m.outcomeFolds = starts, folds
	m.setContent(textOverview, content)
}

// overviewContent is the Overview, w cells wide, and the line each of
// overviewRows starts on, for keeping the cursor in view.
func (m Model) overviewContent(w int) (string, []int) {
	content, starts, _ := m.drawOverview(w)
	return content, starts
}

// drawOverview is overviewContent, and whether the Outcome has more lines than
// its cut shows.
func (m Model) drawOverview(w int) (string, []int, bool) {
	cw := max(w-overviewGutter, 1)
	rows := m.overviewRows()
	starts := make([]int, len(rows))
	var lines []string
	// placed is how many of the rows have a first line yet.
	placed := 0
	place := func(kind overviewKind) {
		if placed < len(rows) && rows[placed].kind == kind {
			starts[placed] = len(lines)
			placed++
		}
	}
	add := func(ls ...string) {
		for _, l := range ls {
			lines = append(lines, strings.Split(l, "\n")...)
		}
	}
	gap := func() {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
	}
	dim := func(s string) string { return m.style(m.dim()).Render(s) }

	if m.inbox {
		add(m.dimLines(m.inboxNote(), cw))
	}
	if line := clean(m.resumable().Line()); line != "" {
		gap()
		add(m.dimLines(line, cw))
	}

	gap()
	add(m.sectionHeading("Outcome", m.dim(), cw))
	_, hasOutcome := m.outcomeSection()
	if hasOutcome {
		place(rowOutcome)
	}
	folds := false
	switch outcome := m.outcomeLines(cw); {
	case len(outcome) == 0:
		add(dim("No outcome yet · → plan"))
	case len(outcome) <= outcomeCut+1:
		add(outcome...)
	case m.expanded["outcome"]:
		folds = true
		add(outcome...)
	default:
		folds = true
		add(outcome[:outcomeCut]...)
		add(dim("… enter to show all"))
	}

	if kids := m.family.Children; len(kids) > 0 {
		gap()
		add(m.sectionHeading("Sub-flows", m.dim(), cw))
		for _, c := range kids {
			place(rowSubFlow)
			marker := dim("○ ")
			if s := clean(c.State); s == planstate.Build || s == planstate.Integrate {
				marker = m.style(m.brand()).Render("▸ ")
			}
			add(marker + lipgloss.NewStyle().Bold(true).Render(clean(c.Title)) + "  " + dim(clean(c.State)))
		}
	}

	gap()
	add(m.sectionHeading("Activity", m.dim(), cw))
	cur, next, finished := m.timeline()
	bw := max(cw-3, 1)
	rail := m.style(m.faint()).Render("│")
	if next != "" {
		add(dim("◌ Up next: "+next), m.style(m.faint()).Render("┆"))
	}
	if cur == "" {
		// A state the stages do not know marks no stage: what there is to
		// say about the flow is all there is.
		add(m.currentBody(cur, cw)...)
	} else {
		place(rowStage)
		add(m.currentHead(cur))
		for _, l := range m.currentBody(cur, bw) {
			add(rail + "  " + l)
		}
		if len(finished) > 0 {
			add(rail)
		}
	}
	for i, st := range finished {
		place(rowStage)
		add(m.finishedHead(st, cw))
		if m.expanded[st] {
			for _, l := range m.stageLog(st, bw) {
				add(rail + "  " + l)
			}
		}
		if i < len(finished)-1 {
			add(rail)
		}
	}

	if rel := m.relatedRows(); len(rel) > 0 {
		gap()
		add(m.sectionHeading("Related", m.dim(), cw))
		for _, r := range rel {
			place(rowRelated)
			label := m.style(m.faint()).Render(fmt.Sprintf("%-10s", r.label))
			add(label + " " + lipgloss.NewStyle().Bold(true).Render(clean(r.Title)) + "  " + dim(clean(r.State)))
		}
	}

	// The cursor column: a marker on the cursor's row while the Overview has
	// the keyboard.
	cursor := -1
	if m.focus == paneOverview && m.overviewCursor >= 0 && m.overviewCursor < len(starts) {
		cursor = starts[m.overviewCursor]
	}
	for i, l := range lines {
		g := strings.Repeat(" ", overviewGutter)
		if i == cursor {
			g = m.style(m.brand()).Render("▶ ")
		}
		lines[i] = ansi.Truncate(g+l, w, "…")
	}
	return strings.Join(lines, "\n"), starts, folds
}

// dimLines is text wrapped to w cells, in the dim colour.
func (m Model) dimLines(text string, w int) string {
	lines := strings.Split(ansi.Wrap(text, max(w, 1), ""), "\n")
	for i, l := range lines {
		lines[i] = m.style(m.dim()).Render(l)
	}
	return strings.Join(lines, "\n")
}

// currentHead is the current stage's row: its mark, and its name, with how
// the flow ended once it has.
func (m Model) currentHead(cur string) string {
	mark := m.style(m.brand()).Render("◉")
	if cur == done {
		mark = m.style(m.green()).Render(ui.IconOK)
	}
	head := mark + " " + lipgloss.NewStyle().Bold(true).Render(cur)
	if o := clean(m.flow.Outcome); m.flow.Ended && o != "" {
		head += " · " + o
	}
	return head
}

// finishedHead is a finished stage's row, w cells wide: its name as a past
// stage, and what its log holds.
func (m Model) finishedHead(stage string, w int) string {
	head := ui.IconOK + " " + planstate.FinishedStage(stage)
	switch replies, sessions := m.logCounts(stage); {
	case stage == stageCreated:
		if req := m.requestExcerpt(w - ansi.StringWidth(head) - len(` · ""`)); req != "" {
			head += ` · "` + req + `"`
		}
	case replies > 0 || sessions > 0:
		var parts []string
		if replies > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", replies, plural(replies, "reply", "replies")))
		}
		if sessions > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", sessions, plural(sessions, "build session", "build sessions")))
		}
		head += " · " + strings.Join(parts, " · ")
	}
	return m.style(m.dim()).Render(ansi.Truncate(head, w, "…"))
}

// requestExcerpt is the developer's request, as far as it is known, on one
// line of at most w cells; "" when it is not known, or w is too small.
func (m Model) requestExcerpt(w int) string {
	if w < 2 {
		return ""
	}
	for _, msg := range m.messages {
		if msg.fromUser && msg.stage == stageCreated {
			return ansi.Truncate(clean(msg.text), w, "…")
		}
	}
	return ""
}

// currentBody is the current stage's details and log, w cells wide, drawn under
// its row. cur is "" for a state the stages do not know.
func (m Model) currentBody(cur string, w int) []string {
	var out []string
	out = append(out, m.progressHead()...)
	if cur == stageBuilding || cur == stageIntegrating {
		out = append(out, m.unitsLines(w)...)
	}

	if pr := clean(m.flow.PR); pr != "" {
		st := lipgloss.NewStyle().Underline(true).Foreground(m.brand())
		if linkable(pr) {
			st = st.Hyperlink(pr)
		}
		out = append(out, m.sectionHeading("Pull request", m.dim(), w), st.Render(pr))
		for _, d := range m.flow.Deviations {
			out = append(out, m.style(m.dim()).Render("• ")+clean(d))
		}
	}

	if q := queuedComments(m.comments); len(q) > 0 && !m.savingComments {
		out = append(out, m.queuedParts(q, w)...)
		out = append(out, m.style(m.faint()).Render(fmt.Sprintf("%d queued · s to send", len(q))))
	}
	if m.savingComments {
		out = append(out, m.style(m.purple()).Render(m.spin.View()+"Saving your comments…"))
	}

	log := m.stageLog(cur, w)
	if cur != "" && !m.expanded[cur] && len(log) > logCut {
		log = append(log[:logCut:logCut], m.style(m.faint()).Render("… enter for the full log"))
	}
	out = append(out, log...)
	for i, l := range out {
		out[i] = ansi.Truncate(l, w, "…")
	}
	return out
}

// unitsLines is the plan's units, each marked done, under way or to do, as
// lines w cells wide with no unit split across two; none while the plan has
// sub-plans, which are under Sub-flows.
func (m Model) unitsLines(w int) []string {
	if len(m.family.Children) > 0 {
		return nil
	}
	marks := m.unitMarks()
	if len(marks) == 0 {
		return []string{m.style(m.dim()).Render("No units")}
	}
	const sep = "   "
	var out []string
	line := ""
	for _, mark := range marks {
		switch {
		case line == "":
			line = mark
		case ansi.StringWidth(line)+len(sep)+ansi.StringWidth(mark) <= w:
			line += sep + mark
		default:
			out = append(out, line)
			line = mark
		}
	}
	return append(out, line)
}

// unitMarks is the plan's units as factory reads them, each marked done,
// under way or to do.
func (m Model) unitMarks() []string {
	current := m.currentUnit()
	marks := make([]string, 0, len(m.structure.Units))
	for i, u := range m.structure.Units {
		marker := m.style(m.dim()).Render("○ ")
		switch {
		case u.Done:
			marker = m.style(m.green()).Render(ui.IconOK + " ")
		case strings.TrimPrefix(unitHeading(Unit{Number: u.Number, Name: u.Name}), "### ") == current:
			marker = m.style(m.brand()).Render("▸ ")
		}
		num := m.style(m.faint()).Render(strconv.Itoa(i + 1))
		marks = append(marks, marker+num+"  "+lipgloss.NewStyle().Bold(true).Render(u.Name))
	}
	return marks
}

// progressHead is the dim lines above the units: what factory says is under
// way and the counts behind it, then who the flow waits on; each only when
// there is something to say.
func (m Model) progressHead() []string {
	var rows, parts []string
	if p := m.flow.Progress; p != nil {
		if s := clean(p.Summary); s != "" {
			parts = append(parts, s)
		}
		if p.Findings > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.Findings, plural(p.Findings, "review finding", "review findings")))
		}
		if p.Deviations != nil && *p.Deviations > 0 {
			d := fmt.Sprintf("%d %s", *p.Deviations, plural(*p.Deviations, "deviation", "deviations"))
			if p.BindingDeviations != nil && *p.BindingDeviations > 0 {
				d += fmt.Sprintf(" (%d binding)", *p.BindingDeviations)
			}
			parts = append(parts, d)
		}
		if p.Fixups > 0 {
			parts = append(parts, fmt.Sprintf("%d %s left", p.Fixups, plural(p.Fixups, "fixup", "fixups")))
		}
	}
	if len(parts) > 0 {
		rows = append(rows, m.style(m.dim()).Render(strings.Join(parts, " · ")))
	}
	if w := clean(m.flow.WaitingOn); w != "" {
		line := "Waiting on " + w
		if why := clean(m.flow.WaitingOnWhy); why != "" {
			line += ": " + why
		}
		rows = append(rows, m.style(m.dim()).Render(line))
	}
	return rows
}

// currentUnit is the unit under way: factory's progress.current_unit when it
// serves one, else the unit the flow's task builds.
func (m Model) currentUnit() string {
	if p := m.flow.Progress; p != nil && p.CurrentUnit != "" {
		return p.CurrentUnit
	}
	return m.flow.Unit
}

// sectionHeading is name as a section of the plan is headed: in capitals, in
// c, with a rule to w.
func (m Model) sectionHeading(name string, c color.Color, w int) string {
	label := strings.ToUpper(name)
	rule := max(w-ansi.StringWidth(label)-2, 0)
	return m.style(c).Bold(true).Render(label) + " " + m.style(m.faint()).Render(strings.Repeat("─", rule))
}

// holds is whether stage's log holds an entry made while the flow was in
// entryStage: the entries of the stage itself and, in the current stage's,
// those of a state the stages do not know and those of a stage after it, which
// the flow has moved back from and which has no row of its own.
func (m Model) holds(stage, entryStage string) bool {
	if stage == entryStage {
		return true
	}
	cur := m.stageNow()
	if stage != cur {
		return false
	}
	return entryStage == "" || cur == "" ||
		slices.Index(planstate.Stages, entryStage) > slices.Index(planstate.Stages, cur)
}

// notesStage is the stage whose log has the notes on reading the build: the
// Building stage when it has a session, otherwise the current stage.
func (m Model) notesStage() string {
	cur := m.stageNow()
	if i := slices.Index(planstate.Stages, cur); i >= slices.Index(planstate.Stages, stageBuilding) &&
		slices.ContainsFunc(m.builds, func(r buildRun) bool { return r.stage == stageBuilding }) {
		return stageBuilding
	}
	return cur
}

// stageHasLog is whether stage's log has anything in it.
func (m Model) stageHasLog(stage string) bool {
	switch {
	case slices.ContainsFunc(m.messages, func(msg message) bool { return m.holds(stage, msg.stage) }),
		slices.ContainsFunc(m.builds, func(r buildRun) bool { return m.holds(stage, r.stage) }),
		stage == m.stageNow() && m.busy:
		return true
	}
	return stage == m.notesStage() && (m.buildMissing || m.buildErr != "")
}

// logCounts is how many planner replies and build sessions stage's log holds.
func (m Model) logCounts(stage string) (replies, sessions int) {
	for _, msg := range m.messages {
		if !msg.fromUser && !msg.failed && m.holds(stage, msg.stage) {
			replies++
		}
	}
	for _, r := range m.builds {
		if m.holds(stage, r.stage) {
			sessions++
		}
	}
	return replies, sessions
}

// stageLog is the entries of stage's log as lines w cells wide, newest first,
// as the timeline is: for the current stage the live turn, then what the
// reads of the build said of it, then its messages and its build sessions in
// the reverse of the order they were added.
func (m Model) stageLog(stage string, w int) []string {
	type entry struct {
		seq   int
		lines []string
	}
	var entries []entry
	for i := range m.messages {
		msg := &m.messages[i]
		if m.holds(stage, msg.stage) {
			entries = append(entries, entry{msg.seq, m.messageLines(msg, w)})
		}
	}
	var runs []buildRun
	for _, r := range m.builds {
		if m.holds(stage, r.stage) {
			runs = append(runs, r)
		}
	}
	for _, r := range runs {
		sec := strings.TrimRight(m.buildSection([]buildRun{r}, false, w), "\n")
		entries = append(entries, entry{r.seq, strings.Split(sec, "\n")})
	}
	if stage == m.notesStage() {
		var notes []string
		if len(runs) == 0 {
			// With no session to head them, the notes have a divider of their own.
			notes = strings.Split(strings.TrimRight(m.buildSection(nil, true, w), "\n"), "\n")
		} else {
			notes = m.buildNotes(w)
		}
		if len(notes) > 0 && notes[0] != "" {
			entries = append(entries, entry{m.seq + 1, notes})
		}
	}
	slices.SortStableFunc(entries, func(a, b entry) int { return b.seq - a.seq })
	if stage == m.stageNow() && m.busy {
		entries = append([]entry{{lines: strings.Split(m.activityLog(w), "\n")}}, entries...)
	}
	var out []string
	for i, e := range entries {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, e.lines...)
	}
	return out
}

// messageLines is msg as a log draws it, w cells wide: the developer's request
// as "you" and its text, a planner's reply headed by chunk, and a failure in
// the error style.
func (m Model) messageLines(msg *message, w int) []string {
	switch {
	case msg.fromUser:
		const lead = 5
		lines := strings.Split(ansi.Wrap(msg.text, max(w-lead, 1), ""), "\n")
		for i := range lines {
			if i == 0 {
				lines[i] = m.style(m.faint()).Bold(true).Render("you") + "  " + lines[i]
				continue
			}
			lines[i] = strings.Repeat(" ", lead) + lines[i]
		}
		return lines
	case msg.failed:
		lines := strings.Split(ansi.Wrap(msg.text, max(w, 1), ""), "\n")
		for i, l := range lines {
			lines[i] = ui.ErrorStyle.Render(l)
		}
		return append([]string{chunkStyle.Render("chunk")}, lines...)
	}
	return m.replyLines(msg, w)
}

// outcomeRender is the Outcome's markdown as last rendered, so a redraw of the
// Overview on every tick of the spinner does not render it again.
type outcomeRender struct {
	src   string
	width int
	dark  bool
	lines []string
	ok    bool
}

// outcomeSection is the plan's 1. Outcome section, and whether it has one.
func (m Model) outcomeSection() (Section, bool) {
	for _, s := range m.structure.Sections {
		if strings.EqualFold(sectionNumRe.ReplaceAllString(strings.TrimSpace(s.Heading), ""), "Outcome") {
			return s, true
		}
	}
	return Section{}, false
}

// outcomeText is the Outcome's blocks as markdown, a list as its items, each
// set apart by a blank line.
func (m Model) outcomeText() string {
	sec, ok := m.outcomeSection()
	if !ok {
		return ""
	}
	var parts []string
	var walk func(bs []Block)
	walk = func(bs []Block) {
		for _, b := range bs {
			if b.Type == "list" {
				walk(b.Blocks)
				continue
			}
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, t)
			}
		}
	}
	walk(sec.Blocks)
	return strings.Join(parts, "\n\n")
}

// outcomeLines is the Outcome rendered as markdown to w cells; none when there
// is nothing to say.
func (m Model) outcomeLines(w int) []string {
	text := m.outcomeText()
	if text == "" {
		return nil
	}
	c := m.outcome
	if c != nil && c.ok && c.src == text && c.width == w && c.dark == m.dark {
		return c.lines
	}
	var lines []string
	if r, err := chat.NewRenderer(m.dark, w); err == nil {
		lines = renderLines(r, text)
	} else {
		lines = strings.Split(text, "\n")
	}
	for i, l := range lines {
		lines[i] = onlySafeLinks(l)
	}
	if c != nil {
		*c = outcomeRender{src: text, width: w, dark: m.dark, lines: lines, ok: true}
	}
	return lines
}

// hyperlinkRe is a terminal hyperlink's opening: its parameters and its URL.
var hyperlinkRe = regexp.MustCompile(`\x1b\]8;([^;\x07\x1b]*);([^\x07\x1b]*)(?:\x07|\x1b\\)`)

// onlySafeLinks is a line glamour rendered without the hyperlinks the plan pane
// would not draw: only an http or https URL is sent to the terminal as a link,
// as in the plan. The link's text stays, as plain text.
func onlySafeLinks(line string) string {
	return hyperlinkRe.ReplaceAllStringFunc(line, func(open string) string {
		if url := hyperlinkRe.FindStringSubmatch(open)[2]; url == "" || linkable(url) {
			return open
		}
		return ""
	})
}
