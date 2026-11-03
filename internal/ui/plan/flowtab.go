package plan

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The Flow view is where the plan is in factory: what is queued, what is
// open and whose turn it is, how it ended, and, in its facts strip, the
// facts about it. This file holds its keys and selection.

// updateFlowKey handles a key for the Flow view: j/k move the selection over
// its boxes and the rows that open a plan, the page keys scroll, and enter does the
// selected item's action. The plan's own keys do nothing here.
func (m Model) updateFlowKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	k := m.keys()
	sels := m.selectables()
	switch {
	case key.Matches(msg, k.Up):
		m.selected = max(m.selected-1, 0)
	case key.Matches(msg, k.Down):
		m.selected = max(min(m.selected+1, len(sels)-1), 0)
	case key.Matches(msg, k.PageUp):
		m.flowView.PageUp()
	case key.Matches(msg, k.PageDown):
		m.flowView.PageDown()
	case key.Matches(msg, k.Act):
		if m.selected >= len(sels) {
			return m, nil
		}
		return m.act(sels[m.selected])
	default:
		return m, nil
	}
	m.renderFlow()
	return m, nil
}

// act does what s, the Flow view's selected item, offers: a box's hint, or
// opening the plan a row or blocker names in a host that can.
func (m Model) act(s selectable) (Model, tea.Cmd) {
	open := func(id, title string) (Model, tea.Cmd) {
		if !m.inbox {
			return m, nil
		}
		return m, func() tea.Msg { return OpenMsg{ID: id, Title: title} }
	}
	if r := s.row; r != nil {
		return open(r.ID, clean(r.Title))
	}
	switch s.box.kind {
	case boxReview:
		return m.showTab(tabPlan)
	case boxSend:
		return m.deliver()
	case boxStart:
		m = m.askToApprove()
		m.renderFlow()
		return m, nil
	case boxBlocker:
		for _, b := range m.family.Blockers {
			if b.ID == s.box.id {
				return open(b.ID, clean(b.Title))
			}
		}
	case boxFailure:
		if m.resumeOffered() {
			return m.resume()
		}
	case boxDescribe:
		return m.send(m.input.Value())
	case boxAnswer:
		if _, _, open := m.openQuestion(); open {
			m.answering = true
			cmd := m.answer.Focus()
			m.renderFlow()
			return m, cmd
		}
		return m.sendAnswers()
	case boxMerge, boxStep, boxRunning, boxSubPlans:
		// These say what is under way and offer nothing to do.
	}
	return m, nil
}

// updateAnswerKey handles a key while the Answer a question box's input has
// the keyboard: enter queues the answer as a comment on the question, as c
// on its block does, and sends every answer once none is left; esc leaves
// the input with what was typed; every other key is typed.
func (m Model) updateAnswerKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	k := m.keys()
	switch {
	case key.Matches(msg, k.LeaveAnswer):
		m.answering = false
		m.answer.Blur()
		m.renderFlow()
		return m, nil
	case key.Matches(msg, k.SaveAnswer):
		text := strings.TrimSpace(m.answer.Value())
		at, _, open := m.openQuestion()
		if text == "" || !open {
			return m, nil
		}
		m.comments = append(m.comments, comment{block: at, span: 1, quote: m.quoteOf(at, at), text: text})
		m.answer.Reset()
		m.drawPlan()
		if _, _, more := m.openQuestion(); more {
			m.renderFlow()
			return m, nil
		}
		m.answering = false
		m.answer.Blur()
		var cmd tea.Cmd
		m, cmd = m.sendAnswers()
		m.renderFlow()
		return m, cmd
	}
	var cmd tea.Cmd
	m.answer, cmd = m.answer.Update(msg)
	m.renderFlow()
	return m, cmd
}

// sendAnswers saves the queued comments and hands them to factory, as s
// does; with no comment queued it still hands off, so factory takes the
// answers already saved, or learns there were no questions to answer.
func (m Model) sendAnswers() (Model, tea.Cmd) {
	if len(queuedComments(m.comments)) > 0 || m.pendingHandOff {
		return m.deliver()
	}
	return m.saveAndHandOff()
}

// renderFlow draws the Flow view into its viewport.
func (m *Model) renderFlow() {
	w := m.flowView.Width()
	if w <= 0 {
		return
	}
	m.setContent(textFlow, m.flowContent(w))
}

// progressHead is the running box's progress line: what factory says is
// under way and the counts behind it; "" when there is nothing to say.
func (m Model) progressHead() string {
	var parts []string
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
	return strings.Join(parts, " · ")
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
