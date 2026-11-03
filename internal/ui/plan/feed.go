package plan

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
)

// The Flow view's drawing: the strip of facts about the flow, then the
// activity column, its boxes and history, at the view's full width.

const (
	// todoLines is how many units still to do the running box lists.
	todoLines = 4
	// merged is the outcome of a flow whose PR merged.
	merged = "merged"
	// blocked is the status of a box that something holds up.
	blocked = "blocked"
)

// boxKind is what a box of the Flow view is for.
type boxKind int

const (
	boxReview boxKind = iota
	boxSend
	boxStart
	boxMerge
	boxStep
	boxRunning
	boxBlocker
	boxSubPlans
	boxDescribe
	boxFailure
)

// box is one open activity of the Flow view.
type box struct {
	kind              boxKind
	mark, name, actor string
	status            string
	heavy             bool
	// first is the lines over the dim text, styled as they are: the agent's
	// current activity.
	first []string
	lines []string // dim text inside
	// input is the lines of an input the box holds, under its text.
	input []string
	// list is the rows under the text, after a blank line when there is
	// text: the running task's units, or the sub-plans.
	list []listRow
	// tail is the lines under the list: the running task's deviations.
	tail []string
	hint string // " ⏎ send " etc.; "" for none
	id   string // the plan a blocker box opens
}

// listRow is a row of a box's list; id is the plan it opens, "" for a row
// that is not selectable.
type listRow struct {
	text, id string
}

// selectable is something j/k can land on: a box, or a row that opens a
// plan: a sub-plan, or the plan this one is part of.
type selectable struct {
	box *box
	row *Relative
}

// selKey is what a selectable is matched by across refreshes: its kind and
// the plan it names.
type selKey struct {
	row  bool
	kind boxKind
	id   string
}

func (s selectable) key() selKey {
	if s.row != nil {
		return selKey{row: true, id: s.row.ID}
	}
	return selKey{kind: s.box.kind, id: s.box.id}
}

// forwardEvents are the human events the Flow view has no box for: they end
// the plan or send it back, and their keys say so.
var forwardEvents = []string{abandon, "abandon-integrate", "request-revision"}

// boxes are the Flow view's open activities, in order: the last failure,
// those waiting on the developer, the running task, the plans blocking this
// one, then its sub-plans. Before the plan exists they are the failure, then
// the Describe the change box or, once the request is sent, the running
// box. An ended flow has none.
func (m Model) boxes() []box {
	if m.flow.Ended {
		return nil
	}
	var out []box
	heavy := func(kind boxKind, name, hint string, lines ...string) box {
		return box{kind: kind, mark: "◐", name: name, actor: "you", status: "waiting", heavy: true, lines: lines, hint: hint}
	}
	if b, ok := m.failureBox(); ok {
		out = append(out, b)
	}
	if !m.created {
		if !m.creating {
			b := heavy(boxDescribe, "Describe the change", " ⏎ send ", "What do you want to change?")
			b.input = strings.Split(m.input.View(), "\n")
			out = append(out, b)
		}
		if b, ok := m.runningBox(); ok {
			out = append(out, b)
		}
		return out
	}
	reason := func(event string) string {
		for _, s := range m.flow.Next {
			if Actor(clean(string(s.Actor))) == ActorHuman && (clean(s.Event) == event || clean(s.Action) == event) {
				return clean(s.Reason)
			}
		}
		return ""
	}
	withReason := func(b box, event string) box {
		if r := reason(event); r != "" {
			b.lines = append(b.lines, r)
		}
		return b
	}

	switch approval(m.flow) {
	case planstate.ApproveForBuilding:
		n := len(m.units)
		out = append(out, withReason(heavy(boxReview, "Review the plan", " ⏎ review ",
			fmt.Sprintf("%d %s", n, plural(n, "unit", "units"))), planstate.ApproveForBuilding))
	case planstate.ApproveForUnits:
		out = append(out, withReason(heavy(boxReview, "Review the design", " ⏎ review ", "design ready"), planstate.ApproveForUnits))
	}
	if slices.Contains(m.events, addressComments) && len(queuedComments(m.comments)) > 0 {
		out = append(out, withReason(heavy(boxSend, "Send your comments", " ⏎ send "), addressComments))
	}
	if slices.Contains(m.events, prioritize) {
		out = append(out, withReason(heavy(boxStart, "Start work", " ⏎ start "), prioritize))
	}
	for _, s := range m.flow.Next {
		if Actor(clean(string(s.Actor))) != ActorHuman {
			continue
		}
		event, action := clean(s.Event), clean(s.Action)
		switch {
		case action == "merge-pr":
			b := heavy(boxMerge, "Merge the PR", "")
			if pr := clean(m.flow.PR); pr != "" {
				b.lines = append(b.lines, pr)
			}
			out = append(out, withReason(b, action))
		case slices.Contains(forwardEvents, event),
			event == planstate.ApproveForBuilding, event == planstate.ApproveForUnits,
			event == addressComments, event == prioritize:
		default:
			b := heavy(boxStep, stepText(s), "")
			if r := clean(s.Reason); r != "" {
				b.lines = append(b.lines, r)
			}
			out = append(out, b)
		}
	}

	if b, ok := m.runningBox(); ok {
		out = append(out, b)
	}
	if m.familyErr != nil {
		return out
	}
	for _, r := range m.family.Blockers {
		if !r.Active {
			continue
		}
		b := box{kind: boxBlocker, mark: "⚠", name: "Blocked by " + clean(r.Title), actor: clean(r.State), status: blocked, id: r.ID}
		if m.inbox {
			b.hint = " ⏎ open "
		}
		out = append(out, b)
	}
	if b, ok := m.subPlansBox(); ok {
		out = append(out, b)
	}
	return out
}

// failureBox is the box of the latest create, start, resume or agent
// failure, until the next of those succeeds: what failed, and the error
// inside, with R's hint when R resumes the plan.
func (m Model) failureBox() (box, bool) {
	if m.failure == "" {
		return box{}, false
	}
	what, why, _ := strings.Cut(clean(m.failure), ": ")
	b := box{kind: boxFailure, mark: "⚠", name: what, actor: actorName(ActorSession), status: blocked}
	if why != "" {
		b.lines = append(b.lines, why)
	}
	if m.resumeOffered() {
		b.hint = " R resume "
	}
	return b, true
}

// runningBox is the box of the task factory has out for the plan, when it
// has one, or of the agent at work with none, as when it drafts the first
// plan.
func (m Model) runningBox() (box, bool) {
	task := clean(m.flow.Task)
	if task == "" && !m.busy {
		return box{}, false
	}
	name := "Drafting the plan"
	switch {
	case task != "":
		name = taskLabel(task)
		if n := m.currentUnitIndex(); n >= 0 {
			name += fmt.Sprintf(" · Unit %d of %d", n+1, len(m.units))
		} else if u := clean(m.flow.Unit); u != "" {
			name += " · " + u
		}
	case m.byTask && m.task != "":
		name = taskLabel(clean(m.task))
	case m.byTask:
		name = "Working on a factory task"
	}
	actor := ActorSession
	for _, s := range m.flow.Next {
		if task != "" && s.Dispatched && clean(s.Task) == task {
			actor = Actor(clean(string(s.Actor)))
			break
		}
	}
	b := box{kind: boxRunning, mark: "●", name: name, actor: actorName(actor), status: "running"}
	if m.busy {
		b.first = append(b.first, m.activityLine())
	}
	if p := m.progressHead(); p != "" {
		b.lines = append(b.lines, p)
	}
	for _, l := range m.unitChecklist() {
		b.list = append(b.list, listRow{text: l})
	}
	for _, raw := range m.flow.Deviations {
		if d := clean(raw); d != "" {
			b.tail = append(b.tail, "• "+d)
		}
	}
	return b, true
}

// currentUnitIndex is the index in m.units of the unit under way; -1 when
// there is none, or it is not one of the plan's units.
func (m Model) currentUnitIndex() int {
	current := m.currentUnit()
	if current == "" {
		return -1
	}
	return slices.IndexFunc(m.units, func(u Unit) bool { return u.Name == current })
}

var (
	unitPrefixRe = regexp.MustCompile(`^Unit\s+\d+\s*:\s*`)
	unitDoneRe   = regexp.MustCompile(`\s*\(done\)\s*$`)
)

// unitTitle is a unit's heading without its "Unit <n>: " prefix and its
// done suffix.
func unitTitle(name string) string {
	return unitDoneRe.ReplaceAllString(unitPrefixRe.ReplaceAllString(clean(name), ""), "")
}

// unitChecklist is the running box's list of the plan's units: the done
// ones collapsed into a line per run, the unit under way, then up to
// todoLines of the units still to do and how many more there are.
func (m Model) unitChecklist() []string {
	if len(m.units) == 0 {
		return nil
	}
	current := m.currentUnitIndex()
	var out []string
	todo, more := 0, 0
	for i := 0; i < len(m.units); i++ {
		u := m.units[i]
		if u.Done {
			j := i
			for j+1 < len(m.units) && m.units[j+1].Done {
				j++
			}
			if todo < todoLines {
				if j > i {
					out = append(out, fmt.Sprintf("✓ %d–%d done", i+1, j+1))
				} else {
					out = append(out, fmt.Sprintf("✓ %d  %s", i+1, unitTitle(u.Name)))
				}
			}
			i = j
			continue
		}
		if todo == todoLines {
			more++
			continue
		}
		mark := "○"
		if i == current {
			mark = "●"
		}
		out = append(out, fmt.Sprintf("%s %d  %s", mark, i+1, unitTitle(u.Name)))
		todo++
	}
	if more > 0 {
		out = append(out, fmt.Sprintf("○ +%d more", more))
	}
	return out
}

// subPlansBox is the box of the plan's sub-plans, when it has any: blocked
// while factory waits on them, done once they have all ended. It lists each
// sub-plan, a row that opens it in a host that can.
func (m Model) subPlansBox() (box, bool) {
	kids := m.family.Children
	if len(kids) == 0 {
		return box{}, false
	}
	landed, ended := 0, 0
	rows := make([]listRow, len(kids))
	for i, c := range kids {
		if relativeEnded(c) {
			ended++
		}
		if clean(c.Outcome) == merged {
			landed++
		}
		rows[i] = listRow{text: subPlanMark(c) + " " + clean(c.Title)}
		if m.inbox {
			rows[i].id = c.ID
		}
	}
	b := box{kind: boxSubPlans, mark: "●", status: "running", name: fmt.Sprintf("Sub-plans · %d/%d", landed, len(kids)), list: rows}
	switch {
	case ended == len(kids):
		b.mark, b.status = "✓", "done"
	case slices.ContainsFunc(m.flow.Next, func(s Step) bool { return clean(string(s.Actor)) == planstate.SubPlans }):
		b.mark, b.status = "⚠", blocked
	}
	return b, true
}

// relativeEnded reports whether r has an outcome or its state is ended.
func relativeEnded(r Relative) bool {
	return clean(r.Outcome) != "" || clean(r.State) == planstate.Ended
}

// upNext is the Flow view's up-next line: the first task factory has
// queued and not yet handed out, who takes it, and why it waits.
func (m Model) upNext() (string, bool) {
	if m.flow.Ended {
		return "", false
	}
	for _, s := range m.flow.Next {
		task := clean(s.Task)
		if task == "" || s.Dispatched {
			continue
		}
		parts := []string{task, actorName(Actor(clean(string(s.Actor))))}
		if r := clean(s.Reason); r != "" {
			parts = append(parts, r)
		}
		return "┄ up next: " + strings.Join(parts, " · "), true
	}
	return "", false
}

// selectables are what j/k move over: the boxes, then the Sub-plans box's
// rows, then the plan this one is part of. A host that cannot open plans
// has none of those rows.
func (m Model) selectables() []selectable {
	bs := m.boxes()
	var out []selectable
	for i := range bs {
		if bs[i].kind == boxBlocker && !m.inbox {
			continue
		}
		out = append(out, selectable{box: &bs[i]})
	}
	if !m.inbox || m.familyErr != nil {
		return out
	}
	for i := range m.family.Children {
		out = append(out, selectable{row: &m.family.Children[i]})
	}
	if p := m.family.Parent; p != nil {
		out = append(out, selectable{row: p})
	}
	return out
}

// chosen is the selected item's key, when the Flow view shows a selection:
// once the plan exists, and the request input no longer has the keyboard.
func (m Model) chosen() (selKey, bool) {
	if !m.created {
		return selKey{}, false
	}
	return m.selectedKey()
}

// selectedKey is the selected item's key; false when nothing is selectable.
func (m Model) selectedKey() (selKey, bool) {
	sels := m.selectables()
	if m.selected >= len(sels) {
		return selKey{}, false
	}
	return sels[m.selected].key(), true
}

// reselect puts the selection back on the item prev names, or keeps it in
// range when that item is gone.
func (m *Model) reselect(prev selKey, had bool) {
	sels := m.selectables()
	if had {
		if i := slices.IndexFunc(sels, func(s selectable) bool { return s.key() == prev }); i >= 0 {
			m.selected = i
			return
		}
	}
	m.selected = max(min(m.selected, len(sels)-1), 0)
}

// flowContent is the Flow view, w cells wide: the facts strip, the
// resumable line, then the activity column.
func (m Model) flowContent(w int) string {
	var lines []string
	if strip := m.factsStrip(w); strip != "" {
		lines = append(lines, strip)
	}
	if line := clean(m.resumable().Line()); line != "" {
		lines = append(lines, ansi.Truncate(m.style(m.dim()).Render(line), w, "…"), "")
	}
	lines = append(lines, m.feedLines(w)...)
	return strings.Join(lines, "\n")
}

// feedLines is the activity column, w cells wide: the label line, what is
// up next, the open boxes, then history, or, with none of those, who the
// flow waits on.
func (m Model) feedLines(w int) []string {
	dim := m.style(m.dim())
	bs := m.boxes()
	label := dim.Render("ACTIVITY")
	count := ""
	if len(bs) > 0 {
		count = dim.Render(fmt.Sprintf("%d open", len(bs)))
	}
	lines := []string{label + strings.Repeat(" ", max(w-lipgloss.Width(label)-lipgloss.Width(count), 1)) + count}

	next, hasNext := m.upNext()
	if hasNext {
		lines = append(lines, dim.Render(" "+next))
	}
	sel, showing := m.chosen()
	row := ""
	if showing && sel.row {
		row = sel.id
	}
	for _, b := range bs {
		on := showing && !sel.row && sel.kind == b.kind && sel.id == b.id
		lines = append(lines, m.drawBox(b, w, on, row)...)
	}
	history := m.historyRow(w)
	if history != "" {
		lines = append(lines, history)
	}
	if !hasNext && len(bs) == 0 && history == "" {
		text := "Nothing under way"
		if wo := clean(m.flow.WaitingOn); wo != "" {
			text = "Waiting on " + actorName(Actor(wo))
			if why := clean(m.flow.WaitingOnWhy); why != "" {
				text += ": " + why
			}
		}
		lines = append(lines, dim.Render(text))
	}
	if m.showsBuild() {
		// The build's own feed, which the daemon serves, under the activity.
		lines = append(lines, strings.Split(strings.TrimRight(m.buildSection(w), "\n"), "\n")...)
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return lines
}

// historyRow is the end of an ended flow, as a history row: what, who and
// when, the last against the column's end; "" for a flow that has not ended.
func (m Model) historyRow(w int) string {
	if !m.flow.Ended {
		return ""
	}
	what := "✓ Ended"
	if o := clean(m.flow.Outcome); o != "" {
		if o != merged {
			what = "✕ Ended"
		}
		what += " · " + o
	}
	when := ""
	if !m.flow.UpdatedAt.IsZero() {
		when = m.style(m.dim()).Render(age(time.Since(m.flow.UpdatedAt)))
	}
	return what + strings.Repeat(" ", max(w-lipgloss.Width(what)-lipgloss.Width(when), 1)) + when
}

// drawBox draws b w cells wide, heavy or light, in the brand colour when
// selected, with ▶ on the list row that opens selRow, the selected plan.
func (m Model) drawBox(b box, w int, selected bool, selRow string) []string {
	tl, h, tr, v, bl, br := "┌", "─", "┐", "│", "└", "┘"
	edge := m.style(m.faint())
	if b.heavy {
		tl, h, tr, v, bl, br = "┏", "━", "┓", "┃", "┗", "┛"
		edge = lipgloss.NewStyle().Bold(true)
	}
	if selected {
		edge = m.style(m.brand())
	}
	var right string
	if b.heavy {
		right = " " + lipgloss.NewStyle().Reverse(true).Render(" "+b.actor+" ") + " " + b.status
	} else {
		who := b.status
		if b.actor != "" {
			who = b.actor + " · " + b.status
		}
		right = " " + who
	}
	fixed := 3 + lipgloss.Width(b.mark) + 1 + 1 + lipgloss.Width(right) + 3
	name := ansi.Truncate(b.name, max(w-fixed-1, 1), "…")
	fill := max(w-fixed-lipgloss.Width(name), 0)
	top := edge.Render(tl+h+" ") + b.mark + " " + name + " " + edge.Render(strings.Repeat(h, fill)) + right + edge.Render(" "+h+tr)
	out := []string{ansi.Truncate(top, w, "…")}

	inner := max(w-4, 0)
	row := func(s string) {
		s = ansi.Truncate(s, inner, "…")
		pad := strings.Repeat(" ", max(inner-lipgloss.Width(s), 0))
		out = append(out, edge.Render(v+" ")+s+pad+edge.Render(" "+v))
	}
	dim := m.style(m.dim())
	for _, l := range b.first {
		row(l)
	}
	for _, l := range b.lines {
		row(dim.Render(l))
	}
	for _, l := range b.input {
		row(l)
	}
	if len(b.list) > 0 && len(out) > 1 {
		row("")
	}
	for _, r := range b.list {
		switch r.id {
		case "":
			row(r.text)
		case selRow:
			row(m.style(m.brand()).Render("▶") + " " + r.text)
		default:
			row("  " + r.text)
		}
	}
	for _, l := range b.tail {
		row(dim.Render(l))
	}
	if b.hint != "" {
		row(lipgloss.NewStyle().Reverse(true).Render(b.hint))
	}
	out = append(out, edge.Render(bl+strings.Repeat(h, max(w-2, 0))+br))
	return out
}

// factsStrip is the one dim line of facts about the flow, w cells wide: who
// it waits on, how far the plan is approved, its pull request and the plan
// it is part of, each when it has a value, with ▶ on Part of while it is
// selected; "" before the plan exists.
func (m Model) factsStrip(w int) string {
	if !m.created {
		return ""
	}
	dim := m.style(m.dim())
	var fields []string
	if wo := clean(m.flow.WaitingOn); wo != "" {
		fields = append(fields, dim.Render("Waiting on "+actorName(Actor(wo))))
	}
	state := "in planning"
	switch {
	case m.flow.ApprovedForBuilding:
		state = "approved"
	case m.flow.ApprovedForUnits:
		state = "design approved"
	}
	fields = append(fields, dim.Render("Plan "+state))
	if pr := clean(m.flow.PR); pr != "" {
		st := lipgloss.NewStyle().Underline(true).Foreground(m.brand())
		if linkable(pr) {
			st = st.Hyperlink(pr)
		}
		fields = append(fields, dim.Render("PR ")+st.Render(prNumber(pr)))
	}
	if p := m.family.Parent; p != nil && m.familyErr == nil {
		f := dim.Render("Part of " + clean(p.Title))
		if sel, showing := m.chosen(); showing && sel.row && sel.id == p.ID {
			f = m.style(m.brand()).Render("▶") + " " + f
		}
		fields = append(fields, f)
	}
	return ansi.Truncate(strings.Join(fields, dim.Render(" · ")), w, "…")
}

// subPlanMark is the mark a sub-plan gets in the Sub-plans box.
func subPlanMark(r Relative) string {
	switch s := clean(r.State); {
	case clean(r.Outcome) == merged:
		return "✓"
	case relativeEnded(r):
		return "✕"
	case r.Blocked:
		return "⚠"
	case s == planstate.Build || s == planstate.Integrate:
		return "●"
	case r.Waiting:
		return "◐"
	}
	return "○"
}

// actorName is who a, as factory names them, is to the developer.
func actorName(a Actor) string {
	switch a {
	case ActorHuman:
		return "you"
	case ActorSession:
		return "chunk"
	case ActorDaemon:
		return "factory"
	}
	return string(a)
}

var prPathRe = regexp.MustCompile(`/pull/(\d+)$`)

// prNumber is the PR's number as #n, or url as it is when it does not end in one.
func prNumber(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return raw
	}
	sub := prPathRe.FindStringSubmatch(u.Path)
	if sub == nil {
		return raw
	}
	return "#" + sub[1]
}

// age is d as the history column shows it: 5m, 2h, 1d.
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
