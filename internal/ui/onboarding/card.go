package onboarding

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// card is one of the developer's steps on the timeline, with its own
// keys and view. The model builds the cards from the plan's flow and hands
// each its focused keys and the results of its actions.
type card interface {
	// ID is stable across refreshes: "question:<n>", "design", "build",
	// "review" or "merge".
	ID() string
	Update(msg tea.Msg) (card, tea.Cmd)
	View(width int, focused bool) string
	// Needs is whether the card waits on the developer (◐).
	Needs() bool
}

// inputCard is a card with a text input.
type inputCard interface {
	card
	// typed is the input's text not yet saved, "" when only blank.
	typed() string
	// capturing is whether the card takes esc and every key but ↑/↓, as a
	// comment being written does.
	capturing() bool
}

// The sections cards quote.
const (
	outcomeHeading   = "1. Outcome"
	outcomeAfter     = "## 1. Outcome"
	questionsAfter   = "## 3. Open Questions"
	answerQuestions  = "answer-open-questions"
	focusAnswers     = "answers"
	questionIDPrefix = "question:"
)

// cardData is what the cards read of the plan, and drive it with.
type cardData struct {
	ctx       context.Context
	store     plan.Store
	approver  plan.Approver
	browser   Browser
	styles    theme.Styles
	dark      bool
	width     int
	flow      plan.Flow
	structure plan.Structure
	text      string
	comments  []plan.Comment
	checks    *Checks
	checksErr error
}

type (
	// refreshMsg hands a card the plan as last read.
	refreshMsg struct{ cardData }
	// focusMsg tells a card whether it has focus.
	focusMsg struct{ focused bool }
	// cardMsg is a card's action's result, for the card id of store's plan.
	cardMsg struct {
		store plan.Store
		id    string
		msg   tea.Msg
	}
	answerSavedMsg struct{ err error }
	commentedMsg   struct {
		saved bool
		err   error
	}
	approvedMsg     struct{ err error }
	approveStepMsg  struct{ line string }
	approveStepsEnd struct{}
	openedPRMsg     struct{ err error }
)

// wrap runs f, and hands its message to card id.
func (d cardData) wrap(id string, f func() tea.Msg) tea.Cmd {
	s := d.store
	return func() tea.Msg { return cardMsg{store: s, id: id, msg: f()} }
}

func newInput(placeholder string) textinput.Model {
	in := textinput.New()
	in.Placeholder = placeholder
	in.Prompt = ""
	st := in.Styles()
	st.Cursor.Blink = false
	in.SetStyles(st)
	return in
}

// typedText is in's text, "" when only blank.
func typedText(in textinput.Model) string {
	if strings.TrimSpace(in.Value()) == "" {
		return ""
	}
	return in.Value()
}

func mark(st theme.Styles, s actState) string {
	switch s {
	case actRunning:
		return st.Running.Render("●")
	case actYou:
		return st.Warning.Render("◐")
	case actBlocked:
		return st.Warning.Render("⚠")
	case actDone:
		return st.Success.Render("✓")
	case actNext:
	}
	return st.Dim.Render("○")
}

func head(st theme.Styles, s actState, title, owner, detail string) string {
	h := mark(st, s) + " " + st.Emphasis.Render(title) + st.Dim.Render(" · "+owner)
	if detail != "" {
		h += st.Dim.Render(" · " + detail)
	}
	return h
}

// wrapText is s word-wrapped to width, each line indented by two spaces.
func wrapText(s string, width int) []string {
	wrapped := strings.Split(ansi.Wordwrap(strings.TrimSpace(s), max(width-2, 10), ""), "\n")
	out := make([]string, 0, len(wrapped))
	for _, l := range wrapped {
		out = append(out, "  "+l)
	}
	return out
}

func human(s plan.Step) bool {
	return plan.Actor(strings.TrimSpace(string(s.Actor))) == plan.ActorHuman && s.Required
}

func waitsOnHuman(f plan.Flow) bool { return strings.TrimSpace(f.WaitingOn) == planstate.Human }

func outcomeBlocks(s plan.Structure) []plan.Block {
	for _, sec := range s.Sections {
		if strings.TrimSpace(sec.Heading) == outcomeHeading {
			return sec.Blocks
		}
	}
	return nil
}

// checksPassed is whether c has every check passed.
func checksPassed(c *Checks) bool { return c != nil && c.Failed == 0 && c.Pending == 0 }

// checksText is c's summary: "checks passed", or what failed or is pending.
func checksText(c *Checks) string {
	switch {
	case c == nil:
		return ""
	case c.Rollup && c.Failed > 0:
		return "checks failed"
	case c.Rollup && c.Pending > 0:
		return "checks pending"
	case c.Failed > 0:
		return fmt.Sprintf("%d failed, %d passed", c.Failed, c.Passed)
	case c.Pending > 0:
		return fmt.Sprintf("%d passed, %d pending", c.Passed, c.Pending)
	}
	return "checks passed"
}

// wantCards is the cards the flow asks for, new, in factory's order: one
// per human step it requires, and Review and Merge in integrate.
func (m Model) wantCards() []card {
	if m.store == nil || m.flow.Ended {
		return nil
	}
	var out []card
	seen := map[string]bool{}
	add := func(c card) {
		if !seen[c.ID()] {
			seen[c.ID()] = true
			out = append(out, c)
		}
	}
	for _, s := range m.flow.Next {
		if !human(s) {
			continue
		}
		switch strings.TrimSpace(s.Event) {
		case answerQuestions:
			for i := range m.structure.OpenQuestions {
				add(newQuestionCard(i))
			}
		case planstate.ApproveForUnits:
			add(newApproveCard(false))
		case planstate.ApproveForBuilding:
			add(newApproveCard(true))
		}
	}
	if strings.TrimSpace(m.flow.State) == planstate.Integrate && waitsOnHuman(m.flow) && m.flow.PR != "" {
		add(newReviewCard())
		if checksPassed(m.checks) {
			add(mergeCard{})
		}
	}
	return out
}

func (m Model) cardData() cardData {
	return cardData{
		ctx: m.ctx, store: m.store, approver: m.approver, browser: m.deps.Browser,
		styles: m.styles, dark: m.dark, width: max(m.width-2, 1),
		flow: m.flow, structure: m.structure, text: m.text, comments: m.comments,
		checks: m.checks, checksErr: m.checksErr,
	}
}

// syncCards keeps the cards the flow still asks for, adds the new ones, and
// hands each the plan as last read.
func (m *Model) syncCards() {
	old := map[string]card{}
	for _, c := range m.cards {
		old[c.ID()] = c
	}
	refresh := refreshMsg{m.cardData()}
	want := m.wantCards()
	out := make([]card, 0, len(want))
	for _, c := range want {
		if o, ok := old[c.ID()]; ok {
			c = o
		}
		c, _ = c.Update(refresh)
		out = append(out, c)
	}
	m.cards = out
}

func (m Model) cardIndex(id string) int {
	return slices.IndexFunc(m.cards, func(c card) bool { return c.ID() == id })
}

func (m Model) hasCard(id string) bool { return m.cardIndex(id) >= 0 }

// focusedCard is the focused card and its index, or nil.
func (m Model) focusedCard() (card, int) {
	i := m.cardIndex(m.focus)
	if i < 0 {
		return nil, -1
	}
	return m.cards[i], i
}

// updateCard hands msg to the card at i.
func (m *Model) updateCard(i int, msg tea.Msg) tea.Cmd {
	cards := slices.Clone(m.cards)
	var cmd tea.Cmd
	cards[i], cmd = cards[i].Update(msg)
	m.cards = cards
	return cmd
}

// applyCardMsg hands an action's result to its card, and reads the plan
// again after one that may have changed it.
func (m *Model) applyCardMsg(msg cardMsg) tea.Cmd {
	if msg.store != m.store || m.store == nil || msg.msg == nil {
		return nil
	}
	var cmds []tea.Cmd
	if i := m.cardIndex(msg.id); i >= 0 {
		cmds = append(cmds, m.updateCard(i, msg.msg))
	}
	switch r := msg.msg.(type) {
	case approveStepMsg, approveStepsEnd, openedPRMsg:
	case answerSavedMsg:
		if r.err == nil {
			m.focusNextQuestion(msg.id)
		}
		cmds = append(cmds, structureCmd(m.ctx, m.store))
	default:
		cmds = append(cmds, structureCmd(m.ctx, m.store))
	}
	return tea.Batch(cmds...)
}

// focusNextQuestion moves focus from the question just answered to the next
// unanswered one, or Send answers once there is none.
func (m *Model) focusNextQuestion(from string) {
	if m.focus != from {
		return
	}
	for _, c := range m.cards {
		if strings.HasPrefix(c.ID(), questionIDPrefix) && c.ID() != from && c.Needs() {
			m.focus = c.ID()
			return
		}
	}
}

func (m Model) questionCards() []questionCard {
	var out []questionCard
	for _, c := range m.cards {
		if q, ok := c.(questionCard); ok {
			out = append(out, q)
		}
	}
	return out
}

// allAnswered is whether there are questions and every one is answered.
func (m Model) allAnswered() bool {
	qs := m.questionCards()
	return len(qs) > 0 && !slices.ContainsFunc(qs, func(q questionCard) bool { return !q.answered })
}

// sendAnswers saves each question's unsaved text as its answer, then has
// factory take the answers up.
func (m *Model) sendAnswers() tea.Cmd {
	qs := m.questionCards()
	if m.sending || len(qs) == 0 || m.store == nil {
		return nil
	}
	type pending struct {
		id     string
		blocks []plan.Block
		body   string
	}
	var ps []pending
	for _, q := range qs {
		if t := strings.TrimSpace(q.input.Value()); t != "" && !q.answered {
			ps = append(ps, pending{id: q.ID(), blocks: q.q.Blocks, body: t})
		}
	}
	m.sending, m.sendErr = true, nil
	ctx, s := m.ctx, m.store
	return func() tea.Msg {
		msg := answersMsg{store: s}
		if len(ps) > 0 {
			text, err := s.Text(ctx)
			if err != nil {
				msg.err = fmt.Errorf("read the plan: %w", err)
				return msg
			}
			for _, p := range ps {
				if _, err := s.AddComment(ctx, plan.Quote(text, questionsAfter, p.blocks), p.body); err != nil {
					msg.err = fmt.Errorf("save an answer: %w", err)
					return msg
				}
				msg.saved = append(msg.saved, p.id)
			}
		}
		if err := s.AnswerOpenQuestions(ctx); err != nil {
			msg.err = fmt.Errorf("send answers: %w", err)
		}
		return msg
	}
}

// answersMsg is Send answers' result: the questions whose text it saved,
// and why it stopped.
type answersMsg struct {
	store plan.Store
	saved []string
	err   error
}

func (m *Model) applyAnswers(msg answersMsg) tea.Cmd {
	if msg.store != m.store || m.store == nil {
		return nil
	}
	m.sending, m.sendErr = false, msg.err
	for _, id := range msg.saved {
		if i := m.cardIndex(id); i >= 0 {
			_ = m.updateCard(i, answerSavedMsg{})
		}
	}
	return structureCmd(m.ctx, m.store)
}

// answersRows is the Send answers row: once every question is answered, or
// while answers are sent or failed to send.
func (m Model) answersRows() []row {
	if len(m.questionCards()) == 0 {
		return nil
	}
	var lines []string
	switch {
	case m.sending:
		lines = append(lines, m.styles.Dim.Render("sending answers…"))
	case m.allAnswered():
		lines = append(lines, m.styles.Emphasis.Render("[enter] Send answers"))
	}
	if m.sendErr != nil {
		lines = append(lines, m.styles.Error.Render(oneLine(m.sendErr.Error())))
	}
	if len(lines) == 0 {
		return nil
	}
	r := row{lines: lines}
	if m.allAnswered() && !m.sending {
		r.id, r.card = focusAnswers, focusAnswers
	}
	return []row{r}
}

// cardRow is c's row.
func (m Model) cardRow(c card) row {
	r := row{id: c.ID(), lines: strings.Split(c.View(max(m.width-2, 1), m.focus == c.ID()), "\n")}
	if c.Needs() {
		r.card = c.ID()
	}
	return r
}

// planCardRows is the cards of planning, under Plan: the questions with
// Send answers, then the approvals.
func (m Model) planCardRows() []row {
	var rows []row
	for _, c := range m.cards {
		if strings.HasPrefix(c.ID(), questionIDPrefix) {
			rows = append(rows, m.cardRow(c))
		}
	}
	rows = append(rows, m.answersRows()...)
	for _, c := range m.cards {
		if id := c.ID(); id == "design" || id == "build" {
			rows = append(rows, m.cardRow(c))
		}
	}
	return rows
}

// commentBox is a card's Comment: free text saved as a comment quoting
// 1. Outcome, then address-comments. resend is a comment saved whose
// address-comments failed, which enter on an empty box sends again.
type commentBox struct {
	on, busy, resend bool
	err              error
	input            textinput.Model
}

func newCommentBox() commentBox { return commentBox{input: newInput("your comment…")} }

func (b *commentBox) open() {
	b.on = true
	_ = b.input.Focus()
}

// key is a key press while the box is open.
func (b *commentBox) key(msg tea.KeyPressMsg, d cardData, id string) tea.Cmd {
	switch msg.String() {
	case keyEsc:
		b.on = false
		b.input.Blur()
		return nil
	case keyEnter:
	default:
		var cmd tea.Cmd
		b.input, cmd = b.input.Update(msg)
		return cmd
	}
	body := strings.TrimSpace(b.input.Value())
	if b.busy || (body == "" && !b.resend) {
		return nil
	}
	b.busy, b.err = true, nil
	ctx, s, blocks := d.ctx, d.store, outcomeBlocks(d.structure)
	return d.wrap(id, func() tea.Msg {
		if body != "" {
			text, err := s.Text(ctx)
			if err != nil {
				return commentedMsg{err: fmt.Errorf("read the plan: %w", err)}
			}
			if _, err := s.AddComment(ctx, plan.Quote(text, outcomeAfter, blocks), body); err != nil {
				return commentedMsg{err: fmt.Errorf("save the comment: %w", err)}
			}
		}
		if err := s.AddressComments(ctx); err != nil {
			return commentedMsg{saved: true, err: fmt.Errorf("send the comment: %w", err)}
		}
		return commentedMsg{saved: true}
	})
}

func (b *commentBox) done(msg commentedMsg) {
	b.busy, b.err = false, msg.err
	b.resend = msg.saved && msg.err != nil
	if msg.saved {
		b.input.Reset()
	}
	if msg.err == nil {
		b.on = false
		b.input.Blur()
	}
}

func (b commentBox) lines(st theme.Styles) []string {
	var out []string
	if b.on {
		out = append(out, "  "+st.Field.Render(b.input.View()), "  "+st.Dim.Render("enter send · esc cancel"))
	}
	if b.busy {
		out = append(out, "  "+st.Dim.Render("sending…"))
	}
	if b.err != nil {
		out = append(out, "  "+st.Error.Render(oneLine(b.err.Error())))
	}
	return out
}

// openPR opens url with the browser.
func openPR(d cardData, id, url string) tea.Cmd {
	b := d.browser
	return d.wrap(id, func() tea.Msg {
		if b == nil {
			return openedPRMsg{err: errors.New("no browser")}
		}
		return openedPRMsg{err: b(url)}
	})
}

// prLine is the URL to open by hand, once the browser could not.
func prLine(st theme.Styles, failed bool, url string) []string {
	if !failed {
		return nil
	}
	return []string{"  " + st.Warning.Render("Open "+url+" in your browser")}
}
