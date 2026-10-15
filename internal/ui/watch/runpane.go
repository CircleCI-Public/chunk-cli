package watch

import (
	"fmt"
	"maps"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// The run pane reads top to bottom as the run does: what was asked, where the
// run stands, each round, then where the work ended up. Rounds that are done
// collapse to a line, so the one that matters — the latest, or on a run that
// ended failing the one that failed — has the room. What the run runs on
// (worktree, sidecars, what it said while setting up) is behind "infra".

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

// runItems lists a run's selectable rows in the order the pane draws them:
// each round's header, then, when it is open, its rows.
func (m Model) runItems(s chunkd.Session) []runItem {
	var items []runItem
	for ri := range s.Rounds {
		items = append(items, runItem{kind: itemRound, round: ri})
		if m.roundOpen(s, ri) {
			items = append(items, roundItems(s, ri)...)
		}
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
		round := fmt.Sprintf("Round %d", max(len(s.Rounds), 1))
		if f := s.Factory; f != nil && f.Attempts > 0 {
			round += fmt.Sprintf(" (max %d)", f.Attempts)
		}
		return st.running(round + " · " + runPhase(s))
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

// wrapWords wraps s to width columns and at most maxLines lines, ending the
// last with an ellipsis when there was more.
func wrapWords(s string, width, maxLines int) []string {
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
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = truncate(lines[maxLines-1]+"…", width)
	}
	for i, l := range lines {
		lines[i] = truncate(l, width)
	}
	return lines
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
	if f := s.Factory; f != nil {
		width := max(m.width-leftPaneWidth-8, 20)
		for _, l := range wrapWords(f.Prompt, width, maxGoalLines) {
			body = append(body, "  "+st.muted(l))
		}
	}
	if s.Error != "" {
		body = append(body, "  "+st.err(truncate(strings.Join(strings.Fields(s.Error), " "), 200)))
	}

	focused := m.focusedPane == paneRight
	selStart, selEnd := -1, -1
	item := 0
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
	body = append(body, m.runDetailLines(st, run)...)

	tail := m.runOutcomeLines(st, s)
	room := maxLines - len(head)
	if len(tail) > 0 && room-len(tail) >= minBodyLines {
		room -= len(tail)
	} else {
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
	out := make([]string, 0, len(head)+len(body)+len(tail))
	return append(append(append(out, head[:]...), body...), tail...)
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

	if !open {
		line := pad(0, 2) + st.vdim("▸") + " Round " + fmt.Sprint(r.Number) + "  " + m.roundIcon(st, r) + "  " + roundRollup(st, r)
		return []string{line}, at, end, 1
	}

	head := pad(0, 2) + st.vdim("▾") + " Round " + fmt.Sprint(r.Number) + "  " + roundStateText(st, r)
	if len(r.Reviews) > 0 && (r.Findings > 0 || r.State == chunkd.RoundDone) {
		head += st.dim(fmt.Sprintf("  ·  %d issue%s, %d worth changing", r.Findings, plural(r.Findings), r.Worth))
	}
	lines = []string{head}
	idx := 1

	rows := m.roundRows(st, r)
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
		line := pad(idx, indent) + branch + row.icon + "  " + row.name + strings.Repeat(" ", max(nameWidth-utf8.RuneCountInString(row.name), 0))
		if row.detail != "" {
			line += "   " + row.detail
		}
		lines = append(lines, line)
		lines = append(lines, row.extra...)
		if first+idx == sel {
			end = len(lines)
		}
		idx++
	}
	if nImpl == 1 {
		draw(rows[0], 4, width+2, "")
	}
	if len(rows) > nImpl {
		label := "    " + st.vdim("validate")
		if pending, total := roundPending(r); pending > 0 {
			label += st.dim(fmt.Sprintf(" · %d of %d done", total-pending, total))
		}
		lines = append(lines, label)
		for i, row := range rows[nImpl:] {
			// A tree down the left, so the group reads as one thing.
			branch := "├─ "
			if i == len(rows)-nImpl-1 {
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

// roundPending counts the round's checks and reviews that have not finished,
// and all of them.
func roundPending(r chunkd.Round) (pending, total int) {
	for _, c := range r.Checks {
		total++
		if c.Status != chunkd.CheckPassed && c.Status != chunkd.CheckFailed && c.Status != chunkd.CheckErrored {
			pending++
		}
	}
	for _, p := range r.Reviews {
		total++
		if p.State == chunkd.PromptQueued || p.State == chunkd.PromptRunning {
			pending++
		}
	}
	return pending, total
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
// while it runs, anything that went wrong setting it up.
func (m Model) runOutcomeLines(st watchStyles, s chunkd.Session) []string {
	f := s.Factory
	if f == nil {
		return nil
	}
	var lines []string
	if next := nextStepLine(st, s); next != "" {
		lines = append(lines, "  "+next)
	}
	if !s.State.Finished() {
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
			lines = append(lines, "  "+st.vdim("branch  ")+st.muted(f.Branch)+"  "+st.dim(what))
		}
		if !f.Committed && f.Worktree != "" && !f.WorktreeRemoved {
			lines = append(lines, "  "+st.vdim("work    ")+st.dim("not committed, in "+f.Worktree))
		}
		if f.Continuable() {
			lines = append(lines, "  "+st.vdim("next    ")+st.muted(f.ContinueCommand()))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return append([]string{""}, lines...)
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
		return st.success(ui.IconOK + " All checks passed. The change is ready for you to look at.")
	case chunkd.SessionRunning:
	}
	return ""
}

// runDetailLines are what the run runs on: its worktree, what it said while
// setting up and its sidecars. They are the least of what a person watching a
// run wants, so they stay folded until asked for.
func (m Model) runDetailLines(st watchStyles, run sessionInfo) []string {
	s := run.s
	var lines []string
	if f := s.Factory; f != nil {
		if f.Worktree != "" && !f.WorktreeRemoved {
			lines = append(lines, "    "+st.vdim("worktree ")+st.dim(f.Worktree))
		}
		if f.Log != "" {
			lines = append(lines, "    "+st.vdim("log      ")+st.dim(f.Log))
		}
		if len(f.KeptSidecars) > 0 {
			lines = append(lines, "    "+st.vdim("kept     ")+st.dim(strings.Join(f.KeptSidecars, ", ")))
		}
		if feed := f.Progress.Lines; len(feed) > 0 {
			lines = append(lines, "    "+st.vdim("progress"))
			for _, l := range feed[max(len(feed)-5, 0):] {
				lines = append(lines, "      "+feedText(st, l))
			}
		}
	}
	if scs := m.runSidecars[s.ID]; len(scs) > 0 {
		lines = append(lines, "    "+st.vdim("sidecars"))
		for _, sc := range scs {
			name := sc.name
			if name == "" {
				name = sc.id
			}
			line := "      " + st.muted(name) + "  " + st.vdim(shortUUID(sc.id)) + "  " + m.rowStatus(st, sc)
			if res := renderResources(st, sc.resources); res != "" {
				line += "  " + res
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	if !m.runDetails {
		return []string{"", "  " + st.vdim("▸ infra")}
	}
	return append([]string{"", "  " + st.vdim("▾ infra")}, lines...)
}
