package onboarding

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// actState is where an activity on the timeline stands.
type actState int

const (
	actNext    actState = iota // ○ up next
	actRunning                 // ● running
	actYou                     // ◐ needs the developer
	actBlocked                 // ⚠ blocked
	actDone                    // ✓ done
)

// activity is one row of the timeline: its state, owner ("agent" or "you"),
// title and detail, and any lines under it.
type activity struct {
	state                actState
	owner, title, detail string
	extra                []string
}

const (
	keyEnter = "enter"
	keyEsc   = "esc"
)

// The plan's activities' titles.
const (
	titlePlan   = "Plan"
	titleBuild  = "Build"
	titleReview = "Review"
	titleMerge  = "Merge"
)

const (
	ownerAgent = "agent"
	ownerYou   = "you"
)

// Focusable rows' IDs, besides "org:<i>" and "suggestion:<title>".
const (
	focusInput = "input"
	focusLogin = "login"
	focusSetup = "setup"
)

// row is a block of the timeline's lines. A row with an id takes focus; a
// row with a card is a ◐ card, which takes focus when it appears.
type row struct {
	id    string
	card  string
	lines []string
}

func (m Model) mark(s actState) string { return mark(m.styles, s) }

func (m Model) activityLines(a activity) []string {
	lines := make([]string, 0, 1+len(a.extra))
	lines = append(lines, head(m.styles, a.state, a.title, a.owner, a.detail))
	for _, e := range a.extra {
		lines = append(lines, "  "+e)
	}
	return lines
}

func (m Model) upNextLines(a activity, after string) []string {
	return []string{m.styles.Dim.Render("○ Up next: " + a.title + " · " + a.owner + " · after " + after)}
}

// rows is the timeline from the top.
func (m Model) rows() []row {
	rows := m.accountRows()
	setup := m.setupActivity()
	card := ""
	if setup.state == actYou {
		card = focusSetup
	}
	if m.accountDone && m.org == "" {
		rows = append(rows, row{lines: m.upNextLines(setup, "you sign in")})
	} else {
		id := ""
		if setup.state == actYou || m.setupErr != nil {
			id = focusSetup
		}
		rows = append(rows, row{id: id, card: card, lines: m.activityLines(setup)})
	}
	if m.store == nil {
		rows = append(rows, m.changeRows()...)
		if m.accountDone && m.org != "" {
			rows = append(rows, row{lines: m.upNextLines(activity{title: titlePlan, owner: ownerAgent}, "you choose a change")})
		}
		return rows
	}
	rows = append(rows, row{lines: []string{"", m.styles.Emphasis.Render(cmp.Or(m.change, m.title, "Your change"))}})
	acts := m.planActivities()
	after := "setup"
	for _, a := range acts {
		// Review and Merge are their cards while they wait on the developer.
		if i := m.cardIndex(strings.ToLower(a.title)); i >= 0 && (a.title == titleReview || a.title == titleMerge) {
			rows = append(rows, m.cardRow(m.cards[i]))
			after = a.title
			continue
		}
		if a.state == actNext {
			rows = append(rows, row{lines: m.upNextLines(a, m.upNextAfter(a, after))})
			break
		}
		rows = append(rows, row{lines: m.activityLines(a)})
		after = a.title
		if a.title == titlePlan {
			rows = append(rows, m.planCardRows()...)
		}
	}
	return rows
}

// upNextAfter is what the next activity a waits for, after the activity
// named after: Build waits for the developer's approval, and for Setup while
// it is not done.
func (m Model) upNextAfter(a activity, after string) string {
	if a.title != titleBuild || !m.hasCard("build") {
		return strings.ToLower(after)
	}
	if !m.found || polled(m.setup.State) {
		return "setup and your approval"
	}
	return "your approval"
}

func (m Model) accountRows() []row {
	you := func(title, detail string, extra ...string) []string {
		return m.activityLines(activity{state: actYou, owner: ownerYou, title: title, detail: detail, extra: extra})
	}
	switch {
	case !m.accountDone:
		return []row{{lines: m.activityLines(activity{state: actRunning, owner: ownerAgent, title: "Sign in to CircleCI", detail: "checking"})}}
	case m.signedOut:
		return []row{{id: focusLogin, card: focusLogin, lines: you("Sign in to CircleCI", "Run `chunk auth login` in another terminal", "[r] Retry")}}
	case m.accountErr != nil:
		return []row{{id: focusLogin, card: focusLogin, lines: you("Sign in to CircleCI", oneLine(m.accountErr.Error()), "[r] Retry")}}
	case m.noOrgs:
		return []row{{id: focusLogin, card: focusLogin, lines: you("Create an organization", "You have no CircleCI organization: create one at app.circleci.com", "[r] Retry")}}
	case len(m.orgs) > 0:
		rows := []row{{lines: you("Choose an organization", "↑/↓ and enter")}}
		for i, o := range m.orgs {
			r := row{id: "org:" + strconv.Itoa(i), lines: []string{"  " + oneLine(cmp.Or(o.Name, o.ID))}}
			if i == 0 {
				r.card = "orgs" // The card takes focus on its first choice.
			}
			rows = append(rows, r)
		}
		return rows
	}
	return []row{{lines: m.activityLines(activity{state: actDone, owner: ownerYou, title: "Sign in to CircleCI"})}}
}

func (m Model) setupActivity() activity {
	a := activity{state: actRunning, owner: ownerAgent, title: "Setup"}
	switch {
	case !m.found && m.setupErr == nil:
		a.detail = "starting"
	case !m.found:
		a.state = actBlocked
	case m.setup.State == chunkd.SetupRunning:
		a.detail = cmp.Or(m.setupLine, m.setup.Step)
	case m.setup.State == chunkd.SetupOK:
		a.state, a.detail = actDone, "recorded "+m.setup.Image
	case m.setup.State == chunkd.SetupContinued:
		a.state, a.detail = actDone, "continued without an image"
	case m.setup.State == chunkd.SetupStuck:
		a.state, a.owner, a.title = actYou, ownerYou, "Fix the environment"
		a.extra = append(a.extra, oneLine(m.setup.Summary), "[r] Retry  [c] Continue without an image")
	case m.setup.State == chunkd.SetupFailed:
		a.state, a.owner = actYou, ownerYou
		a.extra = append(a.extra, oneLine(m.setup.Summary), "[r] Retry")
	default:
		a.detail = string(m.setup.State)
	}
	if m.setupErr != nil {
		a.extra = append(a.extra, m.styles.Error.Render(oneLine(m.setupErr.Error())))
		if !m.found {
			a.extra = append(a.extra, "[r] Retry")
		}
	}
	return a
}

// waitsFor is why a queued change has not started.
func (m Model) waitsFor() string {
	if m.found && m.setup.State == chunkd.SetupFailed && m.setup.Checks <= 0 {
		return "Start · waits for a validation command"
	}
	return "Start · waits for setup to detect commands"
}

func (m Model) changeRows() []row {
	rows := []row{{lines: []string{""}}}
	if m.ended != "" {
		rows = append(rows, row{lines: []string{m.mark(actBlocked) + " " + m.styles.Emphasis.Render(cmp.Or(m.change, m.title, "Your change")) + m.styles.Dim.Render(" · "+m.ended), ""}})
	}
	rows = append(rows, row{lines: []string{m.styles.Emphasis.Render("What should we change first?")}})
	for _, s := range m.state.Suggestions {
		if s.Used {
			continue
		}
		line := "  " + s.Title
		if m.queued != nil && m.queued.suggestion == s.Title {
			line += m.styles.Dim.Render(" · " + m.waitsFor())
		}
		rows = append(rows, row{id: "suggestion:" + s.Title, lines: []string{line}})
	}
	rows = append(rows, row{id: focusInput, lines: []string{"  " + m.styles.Field.Render(m.input.View())}})
	if m.queued != nil && m.queued.suggestion == "" {
		rows = append(rows, row{lines: []string{"  " + oneLine(m.queued.text) + m.styles.Dim.Render(" · "+m.waitsFor())}})
	}
	if m.starting {
		rows = append(rows, row{lines: []string{m.styles.Dim.Render("  starting…")}})
	}
	if m.changeErr != nil {
		rows = append(rows, row{lines: []string{"  " + m.styles.Error.Render(oneLine(m.changeErr.Error()))}})
	}
	return rows
}

// planActivities are Plan, Build, CI checks, Review and Merge, from the
// plan's flow.
func (m Model) planActivities() []activity {
	f := m.flow
	merged := f.Ended && f.Outcome == outcomeMerged
	integrating := f.State == planstate.Integrate || merged

	r := activity{state: actNext, owner: ownerYou, title: titleReview}
	switch {
	case merged:
		r.state = actDone
	case integrating && f.WaitingOn == planstate.Human:
		r.state, r.detail = actYou, f.PR
	}
	mg := activity{state: actNext, owner: ownerYou, title: titleMerge}
	if merged {
		mg.state = actDone
	}
	acts := []activity{m.planActivity(), m.buildActivity(), m.checksActivity(), r, mg}
	if f.Ended && !merged {
		acts = append(acts, activity{state: actBlocked, owner: ownerAgent, title: "Flow", detail: "ended: " + f.Outcome})
	}
	return acts
}

func (m Model) merged() bool { return m.flow.Ended && m.flow.Outcome == outcomeMerged }

func (m Model) planActivity() activity {
	f := m.flow
	p := activity{state: actRunning, owner: ownerAgent, title: titlePlan, detail: cmp.Or(m.activity, "starting")}
	switch {
	case f.State == planstate.Build || f.State == planstate.Integrate || m.merged():
		p.state, p.detail = actDone, ""
		return p
	case len(m.questionCards()) > 0 || (len(m.structure.OpenQuestions) > 0 && f.WaitingOn == planstate.Human):
		p.state, p.detail = actBlocked, "waits for your answers"
	case m.hasCard("design") || m.hasCard("build"):
		p.state, p.detail = actBlocked, "waits for your approval"
	case f.WaitingOn == planstate.Human:
		p.state, p.detail = actBlocked, "waits for you"
	}
	p.extra = m.errLines(m.planErr, m.flowErr)
	return p
}

func (m Model) buildActivity() activity {
	f := m.flow
	b := activity{state: actNext, owner: ownerAgent, title: titleBuild}
	switch {
	case f.State == planstate.Integrate || m.merged():
		b.state = actDone
		return b
	case f.State != planstate.Build:
		return b
	case !m.found || polled(m.setup.State):
		b.state, b.detail = actBlocked, "waits for setup"
	default:
		b.state, b.detail = actRunning, m.unitText()
	}
	b.extra = m.errLines(m.planErr, m.flowErr)
	return b
}

func (m Model) checksActivity() activity {
	f := m.flow
	ci := activity{state: actNext, owner: ownerAgent, title: "CI checks"}
	switch {
	case m.merged():
		ci.state = actDone
		return ci
	case f.PR == "":
		return ci
	case m.checks != nil && m.checks.Failed > 0:
		ci.state, ci.detail = actRunning, fmt.Sprintf("%d failed, %d passed", m.checks.Failed, m.checks.Passed)
	case m.checks != nil && m.checks.Pending > 0:
		ci.state, ci.detail = actRunning, fmt.Sprintf("%d passed, %d pending", m.checks.Passed, m.checks.Pending)
	case m.checks != nil:
		ci.state, ci.detail = actDone, "checks passed"
	default:
		ci.state, ci.detail = actRunning, f.State
		ci.extra = m.errLines(m.checksErr)
	}
	for _, d := range f.Deviations {
		ci.extra = append(ci.extra, oneLine(d))
	}
	return ci
}

func (m Model) errLines(errs ...error) []string {
	var out []string
	for _, err := range errs {
		if err != nil {
			out = append(out, m.styles.Error.Render(oneLine(err.Error())))
		}
	}
	return out
}

// unitText is the unit the build is on, as "unit N of M: <name>".
func (m Model) unitText() string {
	units := m.structure.Units
	if len(units) == 0 {
		return cmp.Or(oneLine(m.flow.Unit), "building")
	}
	i := slices.IndexFunc(units, func(u plan.Unit) bool {
		return m.flow.Unit != "" && (u.Name == m.flow.Unit || strings.Contains(m.flow.Unit, u.Name))
	})
	if i < 0 {
		i = slices.IndexFunc(units, func(u plan.Unit) bool { return !u.Done })
	}
	if i < 0 {
		return fmt.Sprintf("%d units built", len(units))
	}
	return fmt.Sprintf("unit %d of %d: %s", units[i].Number, len(units), units[i].Name)
}

// lines is the timeline's lines, and the index of the focused row's first.
func (m Model) lines() ([]string, int) {
	var out []string
	at := -1
	for _, r := range m.rows() {
		for i, l := range r.lines {
			prefix := "  "
			if i == 0 && r.id != "" && r.id == m.focus {
				prefix = m.styles.Brand.Render("▶ ")
				at = len(out)
			}
			if i == 0 && r.card != "" && m.marked[r.card] {
				l += m.styles.Brand.Render(" •")
			}
			out = append(out, prefix+l)
		}
	}
	return out, at
}

func (m Model) footer() []string {
	if m.leaving {
		return []string{m.styles.Warning.Render("Go to flows? Onboarding won't open again in this repository.") +
			m.styles.Dim.Render("  enter go to flows · esc stay")}
	}
	out := []string{m.styles.Dim.Render("↑/↓ move · enter start · esc go to flows")}
	if m.saveErr != nil {
		out = append(out, m.styles.Error.Render("save onboarding: "+oneLine(m.saveErr.Error())))
	}
	return out
}

func (m Model) bodyHeight() int { return max(m.height-len(m.footer())-1, 1) }

// Content draws the timeline, then its footer, within the last
// WindowSizeMsg. Every row is clipped to the width.
func (m Model) Content() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	lines, _ := m.lines()
	h := m.bodyHeight()
	off := min(m.offset, max(len(lines)-h, 0))
	lines = lines[off:min(off+h, len(lines))]
	for len(lines) < h {
		lines = append(lines, "")
	}
	lines = append(lines, "")
	lines = append(lines, m.footer()...)
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "…")
	}
	return strings.Join(lines[:min(len(lines), m.height)], "\n")
}

// focusable is the IDs of the rows that take focus, in order.
func focusable(rows []row) []string {
	var ids []string
	for _, r := range rows {
		if r.id != "" {
			ids = append(ids, r.id)
		}
	}
	return ids
}

// settle moves focus to a ◐ card that appeared, keeps it on a row that is
// there, and, when moved, scrolls the focused row into view.
func (m *Model) settle(moved bool) {
	rows := m.rows()
	ids := focusable(rows)
	needs := map[string]bool{}
	jumped := false
	for _, r := range rows {
		if r.card == "" {
			continue
		}
		needs[r.card] = true
		if m.needs[r.card] {
			continue
		}
		// Focus moves to the first new card only, and not off an input
		// holding text: then the card is only marked.
		if jumped || m.focusedTyped() {
			if !jumped {
				m.marked[r.card] = true
			}
			continue
		}
		m.focus, moved, jumped = r.id, true, true
	}
	m.needs = needs
	for c := range m.marked {
		if !needs[c] {
			delete(m.marked, c)
		}
	}
	for _, r := range rows {
		if r.card != "" && r.id == m.focus {
			delete(m.marked, r.card)
		}
	}
	if !slices.Contains(ids, m.focus) {
		m.focus, moved = "", true
		if len(ids) > 0 {
			m.focus = ids[0]
		}
	}
	if m.focus == focusInput {
		_ = m.input.Focus()
	} else {
		m.input.Blur()
	}
	cards := slices.Clone(m.cards)
	for i, c := range cards {
		cards[i], _ = c.Update(focusMsg{focused: c.ID() == m.focus})
	}
	m.cards = cards
	if moved {
		m.scrollToFocus()
	}
}

func (m *Model) scrollToFocus() {
	lines, at := m.lines()
	h := m.bodyHeight()
	m.offset = min(m.offset, max(len(lines)-h, 0))
	if at < 0 {
		return
	}
	if at < m.offset {
		m.offset = at
	}
	if at >= m.offset+h {
		m.offset = at - h + 1
	}
}

func (m *Model) move(step int) {
	ids := focusable(m.rows())
	i := slices.Index(ids, m.focus)
	if i < 0 || len(ids) == 0 {
		return
	}
	m.focus = ids[min(max(i+step, 0), len(ids)-1)]
}

func (m *Model) wheel(msg tea.MouseWheelMsg) {
	lines, _ := m.lines()
	steps := map[tea.MouseButton]int{tea.MouseWheelDown: 3, tea.MouseWheelUp: -3}
	m.offset += steps[msg.Button]
	m.offset = min(max(m.offset, 0), max(len(lines)-m.bodyHeight(), 0))
}

// keyCapture hands a key to the focused card while it captures keys, as a
// comment being written does: all but ↑/↓.
func (m *Model) keyCapture(k string, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	c, i := m.focusedCard()
	if ic, ok := c.(inputCard); !ok || !ic.capturing() || k == "up" || k == "down" {
		return nil, false
	}
	return m.updateCard(i, msg), true
}

// keyCard handles a key on a card or Send answers: ctrl+enter sends the
// answers from a question, and the focused card takes the rest.
func (m *Model) keyCard(k string, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if k == "ctrl+enter" && len(m.questionCards()) > 0 && (m.focus == focusAnswers || strings.HasPrefix(m.focus, questionIDPrefix)) {
		return m.sendAnswers(), true
	}
	if m.focus == focusAnswers {
		if k == keyEnter {
			return m.sendAnswers(), true
		}
		return nil, true
	}
	if _, i := m.focusedCard(); i >= 0 {
		return m.updateCard(i, msg), true
	}
	return nil, false
}

// key handles a key press, and reports whether it moved focus.
func (m *Model) key(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k := msg.String()
	if m.leaving {
		switch k {
		case keyEnter, "y":
			m.leaving = false
			m.state.Left = true
			m.save()
			return func() tea.Msg { return LeaveMsg{} }, false
		case keyEsc, "n":
			m.leaving = false
		}
		return nil, false
	}
	if cmd, ok := m.keyCapture(k, msg); ok {
		return cmd, false
	}
	switch k {
	case keyEsc, "ctrl+p":
		m.leaving = true
		return nil, false
	case "up":
		m.move(-1)
		return nil, true
	case "down":
		m.move(1)
		return nil, true
	}
	if cmd, ok := m.keyCard(k, msg); ok {
		return cmd, false
	}
	if m.focus == focusInput {
		if k != keyEnter {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return cmd, false
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return nil, false
		}
		return m.choose(change{text: text}), false
	}
	switch k {
	case keyEnter:
		if t, ok := strings.CutPrefix(m.focus, "suggestion:"); ok {
			return m.choose(change{text: t, suggestion: t}), false
		}
		if s, ok := strings.CutPrefix(m.focus, "org:"); ok {
			i, _ := strconv.Atoi(s)
			return m.chooseOrg(i), false
		}
	case "r":
		if m.signedOut || m.accountErr != nil || m.noOrgs {
			return m.accountCmd(), false
		}
		return m.retrySetup(), false
	case "c":
		if m.found && m.setup.State == chunkd.SetupStuck {
			return m.continueCmd(), false
		}
	}
	return nil, false
}
