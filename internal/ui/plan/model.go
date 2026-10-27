// Package plan is the planning screen: a chat with Claude beside the plan
// factory is writing, rendered as markdown. The developer can comment
// on any part of the plan; comments are queued locally and saved to factory
// when the developer sends. When the developer approves the plan, chunk
// builds it through factory.
//
// The screen is a component: its host passes it messages and draws what
// Content returns, and it reports that it is done with a ClosedMsg. Screen
// hosts it as a program of its own.
package plan

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// Approver approves the plan in factory and starts its build, returning the
// build's ID. It reads the plan itself rather than taking the screen's copy,
// which may be behind factory's by a poll. It calls step with each step as
// it starts, as a line for the screen to show while it works.
type Approver func(step func(string)) (string, error)

// ReadyState is the state a plan waits in for the developer once its review
// has passed; it can be approved only from there.
const ReadyState = "Planning - Human"

// The events that approve a plan in ReadyState: its design, so factory
// writes its units, or the plan, so factory builds it.
const (
	approveForUnits    = "approve-for-units"
	approveForBuilding = "approve-for-building"
)

// Stage is what ctrl+s approves for a plan in ReadyState.
type Stage int

const (
	// StageDesign is a plan whose design waits for approval, so factory
	// writes its units.
	StageDesign Stage = iota
	// StagePlan is a plan that waits for approval, so factory builds it.
	StagePlan
	// StageQuestions is a plan factory offers no approval for: it waits for
	// a human to answer 3. Open Questions in comments.
	StageQuestions
)

// StageOf is what ctrl+s approves, given the events the plan's flow applies
// and how many units the plan has. When events lists exactly one approval,
// that one wins. When it lists both, as a flow factory stored before it
// told the two apart does, or is nil, because it has not loaded or there is
// no flow, the plan's units decide: none is StageDesign, any is StagePlan.
// When it is loaded and lists neither, even empty, it is StageQuestions.
func StageOf(events []string, units int) Stage {
	design, build := slices.Contains(events, approveForUnits), slices.Contains(events, approveForBuilding)
	switch {
	case design && !build:
		return StageDesign
	case build && !design:
		return StagePlan
	case !design && events != nil:
		return StageQuestions
	case units == 0:
		return StageDesign
	}
	return StagePlan
}

// pane is which side of the screen the keyboard goes to.
type pane int

const (
	paneChat pane = iota
	panePlan
)

// message is one entry in the chat.
type message struct {
	fromUser bool
	text     string
	failed   bool
	// toBusy is a message of the developer's that reached the agent while it
	// was working, so it reads the message once it is done with that.
	toBusy bool
	// byTask is a reply to a factory task rather than to the developer.
	byTask bool
	// rendered is a reply's markdown as rendered at renderedWidth, kept so
	// the chat is not rendered again on every tick of the spinner.
	rendered      string
	renderedWidth int
}

// renderedBlock is a block of the plan and its lines as glamour renders it.
type renderedBlock struct {
	block
	lines []string
	// at is where source starts in the plan it was split from, or -1 when
	// it cannot be found there as it is.
	at int
}

type (
	approvedMsg struct {
		buildID string
		err     error
	}
	// designApprovedMsg is the end of approving the plan's design.
	designApprovedMsg struct{ err error }
	// approveStepMsg is a step of the approval starting; next is where the
	// approval's later messages come from.
	approveStepMsg struct {
		text string
		next <-chan tea.Msg
	}
	pollMsg      struct{}
	refreshedMsg struct {
		state, text string
		events      []string
		comments    []Comment
		err         error
	}
	commentSavedMsg struct{ err error }
	// createdMsg is the end of creating the plan in factory, for the
	// developer's first message, text.
	createdMsg struct {
		text string
		err  error
	}
	queueSavedMsg struct {
		saved int
		err   error
		// handOff is whether saveQueued had factory take up the comments
		// this round; handedOff, whether factory took the event.
		handOff   bool
		handedOff bool
	}
)

// transition is a flow event factory has taken but not yet applied: the
// plan is still in from until factory's next poll moves it.
type transition struct {
	kind  transitionKind
	from  string // m.state when the event was sent
	since time.Time
}

type transitionKind int

const (
	movingComments transitionKind = iota
	movingDesign
)

// pollEvery is how often the plan pane polls the store for the plan's text,
// state and comments while the screen is open.
const pollEvery = 5 * time.Second

func pollCmd() tea.Cmd { return tea.Tick(pollEvery, func(time.Time) tea.Msg { return pollMsg{} }) }

// refreshCmd reads the plan's state, text and comments from the store.
func refreshCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg {
		state, err := s.State(ctx)
		if err != nil {
			return refreshedMsg{err: err}
		}
		events, err := s.Events(ctx)
		if err != nil {
			return refreshedMsg{err: err}
		}
		text, err := s.Text(ctx)
		if err != nil {
			return refreshedMsg{err: err}
		}
		cs, err := s.Comments(ctx)
		return refreshedMsg{state: state, events: events, text: text, comments: cs, err: err}
	}
}

// ClosedMsg is the screen asking its host to close it: the plan was approved
// and is being built as BuildID, or, when BuildID is "", the developer left.
type ClosedMsg struct {
	BuildID string
}

func closed(buildID string) tea.Cmd {
	return func() tea.Msg { return ClosedMsg{BuildID: buildID} }
}

// Model is the planning screen.
type Model struct {
	ctx     context.Context
	agent   Agent
	approve Approver
	store   Store

	plan  string
	state string
	// events are the events factory applies to the plan's flow now, as the
	// last refresh read them.
	events   []string
	messages []message
	// created is whether the plan has been made in factory with store.Create,
	// and creating whether that is under way.
	created, creating bool

	// blocks is renderedFrom, the plan as last rendered, split for
	// commenting and rendered at renderedWidth.
	blocks        []renderedBlock
	renderedFrom  string
	renderedWidth int
	// cursor is the block the plan pane has highlighted.
	cursor int
	// anchor is the block a selection was started from with shift, which
	// runs from there to the cursor, or -1 when there is none.
	anchor int
	// blockTop and blockEnd are each block's first line in the plan pane and
	// the line after its last.
	blockTop, blockEnd []int

	// comments holds the queued comments (id "") beside the open comments
	// factory has (id set); resolved holds the resolved ones.
	comments     []comment
	resolved     []comment
	commenting   bool
	commentInput textarea.Model
	// editingID is the saved comment the editor is changing at once, or ""
	// while it is working on a queued comment.
	editingID string
	// savingEdit is set from opening the editor on a saved comment's enter
	// until the API answers.
	savingEdit bool
	// savingComments is set from the developer's send until the queued
	// comments have been saved (and handed to factory), so a second send cannot
	// start a second save.
	savingComments bool
	// pendingText is the message waiting to go to the agent once the queued
	// comments this send carried have been saved.
	pendingText string
	// pendingDelete is the saved comment a confirming y would delete.
	pendingDelete string
	// pendingHandOff is whether factory is still owed the address-comments
	// event of a send whose comments were saved but whose event failed; the
	// next send retries it.
	pendingHandOff bool
	// moving is what the screen waits on factory for, or nil when it waits
	// on nothing.
	moving *transition

	planView viewport.Model
	chatView viewport.Model
	input    textarea.Model
	spin     spinner.Model

	focus  pane
	width  int
	height int

	// busy is whether the agent is working: from a send, or from the first
	// thing it does on a factory task, until its reply.
	busy bool
	// byTask is whether what the agent is working on is a factory task's,
	// rather than the developer's message.
	byTask bool
	// log is what the agent has done this turn, oldest first.
	log      []string
	activity string

	confirming bool
	// approvingDesign is set from the developer's y to a design until factory
	// has the event that approves it; pendingDesign is whether the y being
	// asked for approves the design rather than the plan.
	approvingDesign, pendingDesign bool
	// approving is set from the developer's y until factory answers, so a
	// second ctrl+s cannot approve the plan twice.
	approving bool
	// quitting is set by a ctrl+c that would drop what the developer has not
	// sent; a second ctrl+c quits.
	quitting bool
	status   string
	// refreshErr is a quiet note that the last poll failed, cleared by the
	// next successful one.
	refreshErr string

	// dark is whether the terminal's background is dark, and disambiguates
	// whether it tells shift+enter from enter.
	dark          bool
	disambiguates bool
	// back is whether the host keeps the screen to reopen once it is closed,
	// so leaving it loses nothing and stops nothing.
	back bool
	// inbox is whether the host offers a plan inbox and Ctrl+P switching.
	inbox   bool
	title   string
	project string
	// branch is the git branch the plan is for, shown in the header.
	branch string
	// units and questions are how many of each the plan has.
	units, questions int
	// BuildID is the build of the plan, once it has been approved.
	BuildID string

	pending string
	// lastClick is the plan block last clicked, to tell a double click.
	lastClick *lastClick
}

const (
	chatPlaceholder     = "Describe the change, or give feedback on the plan…"
	commentsPlaceholder = "Press enter to save your comments, or add a message…"
	// heldPlaceholder is the placeholder while comments are queued but the
	// plan is not waiting on the developer, so they cannot be sent yet.
	heldPlaceholder = "Your comments send once factory is waiting on you; add a message…"
	// reviewPlaceholder is for a plan waiting on the developer's review, which
	// the agent may not change until comments move it back to the agent.
	reviewPlaceholder = "Ask chunk about the plan; comment on it to change it…"
)

// New returns the planning screen. A non-empty first message is sent as soon
// as the screen opens.
func New(ctx context.Context, agent Agent, approve Approver, store Store, first string) Model {
	in := textarea.New()
	in.Placeholder = chatPlaceholder
	in.ShowLineNumbers = false
	in.Prompt = ""
	// Enter sends; a new line is shift+enter or ctrl+j.
	in.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+j")
	in.SetHeight(3)
	in.Focus()
	inStyles := in.Styles()
	inStyles.Focused.CursorLine = lipgloss.NewStyle()
	in.SetStyles(inStyles)

	ci := textarea.New()
	ci.ShowLineNumbers = false
	ci.Prompt = ""
	ci.Placeholder = "Comment on this part"
	ci.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+j")
	ci.DynamicHeight = true
	// MaxContentHeight bounds the comment itself, at the chat input's own
	// limit, so that MaxHeight bounds only the rows it is shown in: without
	// it the editor would take no more lines than it shows.
	ci.MaxContentHeight = ci.MaxHeight
	ci.MaxHeight = maxCommentRows
	ciStyles := ci.Styles()
	ciStyles.Focused.CursorLine = lipgloss.NewStyle()
	ci.SetStyles(ciStyles)

	// The plan's keys are the screen's own, which move the cursor with the
	// view; the viewport's would scroll one without the other, and its d
	// and u would scroll where d deletes a comment.
	planView := viewport.New()
	planView.KeyMap = viewport.KeyMap{}

	return Model{
		ctx:          ctx,
		agent:        agent,
		approve:      approve,
		store:        store,
		created:      store.ID() != "",
		planView:     planView,
		chatView:     viewport.New(),
		input:        in,
		commentInput: ci,
		spin:         spinner.New(spinner.WithSpinner(spinner.Dot)),
		pending:      strings.TrimSpace(first),
		dark:         true,
		anchor:       -1,
	}
}

// WithTerminal returns a copy of m for a terminal with a dark background or
// not, that tells shift+enter from enter or not. The screen learns of a
// change to either from the messages its host passes on.
func (m Model) WithTerminal(dark, disambiguates bool) Model {
	m.dark, m.disambiguates = dark, disambiguates
	return m
}

// WithBranch returns a copy of m that names the branch the plan is for in its
// header.
func (m Model) WithBranch(branch string) Model {
	m.branch = branch
	return m
}

// WithBack returns a copy of m for a host that keeps the screen when it is
// closed and can open it again as it was: esc, when there is nothing for it
// to stop or cancel, and ctrl+c close it without asking or stopping Claude.
func (m Model) WithBack() Model {
	m.back = true
	return m
}

// WithInbox names the plan and local project and returns to the host's inbox.
// No agent starts until a message is sent. Chat history belongs to this visit.
func (m Model) WithInbox(title, project string) Model {
	m.back, m.inbox, m.title, m.project = true, true, title, project
	return m
}

// Unsent reports local input that quitting the host would discard.
func (m Model) Unsent() string { return m.unsent() }

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.Poll(), pollCmd(), waitEvent(m.ctx, m.agent))
}

// Poll reads the plan's state, text and comments from the store, as the
// screen's own timer does every few seconds. A host can use it to refresh
// the screen deterministically, without waiting for that timer.
func (m Model) Poll() tea.Cmd { return refreshCmd(m.ctx, m.store) }

// Next waits for the agent's next event, as the screen itself does from Init
// on. A host can use it to deliver one deterministically.
func (m Model) Next() tea.Cmd { return waitEvent(m.ctx, m.agent) }

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		if m.pending != "" {
			first := m.pending
			m.pending = ""
			return m.send(first)
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.updateKey(msg)

	case tea.MouseMsg:
		return m.updateMouse(msg)

	case tea.KeyboardEnhancementsMsg:
		m.disambiguates = msg.SupportsKeyDisambiguation()
		return m, nil

	case tea.BackgroundColorMsg:
		if m.dark != msg.IsDark() {
			m.dark = msg.IsDark()
			m.restyle()
		}
		return m, nil

	case eventMsg:
		if msg.agent != m.agent {
			return m, nil
		}
		return m.applyEvent(msg.Event)

	case sentMsg:
		if msg.agent == m.agent && msg.err != nil {
			m.busy, m.byTask, m.activity, m.log = false, false, "", nil
			m.messages = append(m.messages, message{text: "Could not reach chunk: " + msg.err.Error(), failed: true})
			m.renderPlan()
			m.renderChat()
		}
		return m, nil

	case pollMsg:
		return m, tea.Batch(refreshCmd(m.ctx, m.store), pollCmd())

	case refreshedMsg:
		return m.applyRefresh(msg), nil

	case createdMsg:
		m.creating = false
		if msg.err != nil {
			m.busy, m.byTask, m.activity, m.log = false, false, "", nil
			m.messages = append(m.messages, message{text: "Could not create the plan: " + msg.err.Error(), failed: true})
			m.renderPlan()
			m.renderChat()
			return m, nil
		}
		m.created = true
		return m, sendCmd(m.ctx, m.agent, msg.text)

	case commentSavedMsg:
		m.savingEdit = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render(msg.err.Error())
			return m, nil
		}
		m.commenting, m.anchor, m.editingID = false, -1, ""
		m.commentInput.Blur()
		return m, refreshCmd(m.ctx, m.store)

	case queueSavedMsg:
		return m.applyQueueSaved(msg)

	case approveStepMsg:
		if m.approving {
			m.status = msg.text
		}
		return m, waitApproval(msg.next)

	case designApprovedMsg, approvedMsg:
		return m.applyApproval(msg)

	case spinner.TickMsg:
		if !m.busy && !m.approving && !m.approvingDesign && m.moving == nil {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.renderChat()
		return m, cmd
	}

	return m.forward(msg)
}

// applyQueueSaved handles the end of saving the queued comments.
func (m Model) applyQueueSaved(msg queueSavedMsg) (Model, tea.Cmd) {
	m.savingComments = false
	// Whatever got saved before the failure, if any, stays saved; only
	// the rest is still queued.
	m.comments = dropQueued(m.comments, msg.saved)
	m.renderPlan()
	m.syncPlaceholder()
	m.pendingHandOff = msg.handOff && !msg.handedOff
	var tick tea.Cmd
	if msg.handedOff {
		m.moving = &transition{kind: movingComments, from: m.state, since: time.Now()}
		m.status = ""
		tick = m.spin.Tick
	}
	if msg.err != nil {
		m.status = ui.ErrorStyle.Render(msg.err.Error())
		return m, tea.Batch(refreshCmd(m.ctx, m.store), tick)
	}
	text := m.pendingText
	m.pendingText = ""
	if text != "" {
		next, cmd := m.send(text)
		return next, tea.Batch(cmd, refreshCmd(next.ctx, next.store), tick)
	}
	return m, tea.Batch(refreshCmd(m.ctx, m.store), tick)
}

// awaitingStatus is whether the status bar holds something still under way
// or waiting on the developer: a question to answer, the quit prompt, or an
// approval or save factory has not answered yet.
func (m Model) awaitingStatus() bool {
	return m.confirming || m.quitting || m.approving || m.approvingDesign || m.savingComments
}

// applyRefresh is a successful or failed poll of the plan's state, text and
// comments.
func (m Model) applyRefresh(msg refreshedMsg) Model {
	if msg.err != nil {
		m.refreshErr = "could not refresh the plan: " + msg.err.Error()
		return m
	}
	m.refreshErr = ""

	// The cursor stays on a comment on a rewritten part, wherever it now
	// sits, rather than drifting to whatever ends up at its old stop.
	held := false
	var heldID, heldQuote, heldText string
	if m.cursor >= len(m.blocks) && !m.onResolved(m.cursor) {
		if i := m.commentAt(m.cursor); i >= 0 {
			held = true
			heldID, heldQuote, heldText = m.comments[i].id, m.comments[i].quote, m.comments[i].text
		}
	}

	if msg.state != m.state && !m.awaitingStatus() {
		// A note on what happened is out of date once factory moves the
		// plan, and left up it would hide the bar's buttons.
		m.status = ""
	}
	m.state, m.events, m.plan = msg.state, msg.events, msg.text
	if m.moving != nil && m.state != m.moving.from {
		m.moving = nil
	}
	m.applyComments(msg.comments)
	m.renderPlan()
	if held {
		for k, j := range m.orphans() {
			c := m.comments[j]
			if (heldID != "" && c.id == heldID) || (heldID == "" && c.quote == heldQuote && c.text == heldText) {
				m.moveCursor(len(m.blocks) + k)
				break
			}
		}
	}
	m.syncPlaceholder()
	m.renderChat()
	return m
}

func (m Model) updateKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	k := m.keys()
	if key.Matches(msg, k.Quit) {
		return m.quit(msg.String())
	}
	if m.quitting {
		m.quitting, m.status = false, ""
	}
	if m.confirming {
		m.confirming, m.status = false, ""
		if !key.Matches(msg, k.Confirm) {
			m.pendingDelete, m.pendingDesign = "", false
			return m, nil
		}
		return m.answerYes()
	}
	if m.commenting {
		return m.updateCommentKey(msg)
	}

	switch {
	case key.Matches(msg, k.Focus):
		if m.focus == paneChat {
			return m.focusOn(panePlan)
		}
		return m.focusOn(paneChat)
	case key.Matches(msg, k.SendPlan):
		return m.askToApprove(), nil
	}

	if m.focus == panePlan {
		return m.updatePlanKey(msg)
	}
	switch {
	case key.Matches(msg, k.ChatPageUp):
		m.chatView.PageUp()
		return m, nil
	case key.Matches(msg, k.ChatPageDown):
		m.chatView.PageDown()
		return m, nil
	}
	if key.Matches(msg, k.Send) {
		return m.deliver(strings.TrimSpace(m.input.Value()))
	}
	return m.forward(msg)
}

// heldStatus says why a send left the queued comments unsent.
const heldStatus = "Comments not sent: factory is working on the plan. Send them once it is waiting on you."

// deliver is a send from the developer: from a Human state it saves the
// queued comments to factory and hands them to it, then sends text to the
// agent, if there is any; from any other state the comments stay queued.
func (m Model) deliver(text string) (Model, tea.Cmd) {
	if m.savingComments {
		return m, nil
	}
	queued := queuedComments(m.comments)
	if text == "" && len(queued) == 0 && !m.pendingHandOff {
		return m, nil
	}
	// A hand-off owed by an earlier send is retried even with nothing queued.
	if len(queued) == 0 && !m.pendingHandOff {
		return m.send(text)
	}
	if !m.takesComments() {
		next, cmd := m, tea.Cmd(nil)
		if text != "" {
			next, cmd = m.send(text)
		}
		if next.moving == nil {
			next.status = heldStatus
		}
		return next, cmd
	}
	m.savingComments = true
	m.pendingText = text
	m.status = ""
	return m, saveQueued(m.ctx, m.store, queued, m.takesComments())
}

// answerYes does what the question in the status bar asked, now it has been
// answered yes.
func (m Model) answerYes() (Model, tea.Cmd) {
	if m.pendingDesign {
		m.pendingDesign, m.approvingDesign = false, true
		m.status = "Approving the design…"
		return m, tea.Batch(approveDesignCmd(m.ctx, m.store), m.spin.Tick)
	}
	if id := m.pendingDelete; id != "" {
		m.pendingDelete = ""
		return m, deleteCmd(m.ctx, m.store, id)
	}
	m.approving = true
	m.status = "Approving the plan…"
	return m, tea.Batch(approveCmd(m.approve), m.spin.Tick)
}

// focusOn gives the keyboard to pane p, dropping any selection.
func (m Model) focusOn(p pane) (Model, tea.Cmd) {
	m.anchor = -1
	if p == panePlan {
		m.focus = panePlan
		m.input.Blur()
		m.drawPlan()
		return m, nil
	}
	m.focus = paneChat
	m.drawPlan()
	return m, m.input.Focus()
}

// askToApprove starts approving the plan, or its design: it asks the
// developer to confirm, or says why it cannot go yet.
func (m Model) askToApprove() Model {
	queued := len(queuedComments(m.comments))
	switch {
	case m.approving, m.approvingDesign, m.moving != nil:
	case m.busy:
		m.status = warn("Wait for chunk to finish.")
	case strings.TrimSpace(m.plan) == "":
		m.status = warn("There is no plan to approve yet.")
	case m.state != ReadyState && m.inReview():
		m.status = warn("The plan is not ready yet: wait for its review to pass.")
	case m.state != ReadyState:
		m.status = warn("The plan cannot be approved while it is " + m.stateText() + ".")
	case StageOf(m.events, m.units) == StageQuestions:
		m.status = warn("Answer 3. Open Questions in comments first; factory offers no approval yet.")
	case StageOf(m.events, m.units) == StageDesign && (queued > 0 || m.openComments() > 0):
		m.status = warn("Send or resolve your comments first; then ctrl+s approves the design.")
	case StageOf(m.events, m.units) == StageDesign:
		m.confirming, m.pendingDesign = true, true
		m.status = ui.TitleStyle.Render("Approve the design, so chunk writes the units? ") + confirmButtons
	case queued > 0:
		m.confirming = true
		m.status = ui.TitleStyle.Render(fmt.Sprintf("Your %d queued %s will not be saved. Approve the plan and build it anyway? ",
			queued, plural(queued, "comment", "comments"))) + confirmButtons
	default:
		m.confirming = true
		m.status = ui.TitleStyle.Render("Approve this plan and build it? ") + confirmButtons
	}
	return m
}

// quit closes the screen, unless that would drop comments or a message the
// developer has not sent, when it asks for a second press of the same key
// first. A screen its host keeps loses nothing by closing, so it closes at
// once.
func (m Model) quit(pressed string) (Model, tea.Cmd) {
	if m.back {
		return m, closed("")
	}
	if unsent := m.unsent(); unsent != "" && !m.quitting {
		m.quitting, m.confirming = true, false
		m.status = warn(fmt.Sprintf("Your %s will be lost. Press %s again to quit.", unsent, pressed))
		return m, nil
	}
	return m, closed("")
}

// unsent names what the developer has written that has not gone to Claude,
// or is "" when there is nothing. Saved comments do not count: they are
// already in factory.
func (m Model) unsent() string {
	n := len(queuedComments(m.comments))
	if m.commenting && m.editingID == "" && strings.TrimSpace(m.commentInput.Value()) != "" && m.cursor < len(m.blocks) {
		// The comment being written replaces the queued ones it takes in.
		_, _, in := m.widen(m.selection())
		queuedIn := 0
		for _, j := range in {
			if m.comments[j].id == "" {
				queuedIn++
			}
		}
		n += 1 - queuedIn
	}
	var parts []string
	if n > 0 {
		parts = append(parts, fmt.Sprintf("%d unsent %s", n, plural(n, "comment", "comments")))
	}
	if strings.TrimSpace(m.input.Value()) != "" {
		parts = append(parts, "unsent message")
	}
	return strings.Join(parts, " and ")
}

func warn(s string) string { return ui.WarningStyle.Render(s) }

// forward hands msg to the focused pane's component.
func (m Model) forward(msg tea.Msg) (Model, tea.Cmd) {
	if m.commenting {
		if m.savingEdit {
			return m, nil
		}
		var cmd tea.Cmd
		m.commentInput, cmd = m.commentInput.Update(msg)
		m.moveCursor(m.cursor)
		return m, cmd
	}
	var cmd tea.Cmd
	if m.focus == panePlan {
		m.planView, cmd = m.planView.Update(msg)
		return m, cmd
	}
	var cmds []tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	// The chat scrolls with the mouse wheel while typing.
	if _, ok := msg.(tea.KeyPressMsg); !ok {
		m.chatView, cmd = m.chatView.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// send creates the plan in factory on the first message, then sends text to
// the agent as the developer's message. The agent may be working already, on
// an earlier message or a factory task; it reads text once it is done. While
// the plan is being created a message waits in the input.
func (m Model) send(text string) (Model, tea.Cmd) {
	if m.creating {
		return m, nil
	}
	m.input.Reset()
	m.messages = append(m.messages, message{fromUser: true, text: text, toBusy: m.busy})
	m.status = ""
	var tick tea.Cmd
	if !m.busy {
		m.busy, m.byTask, m.log = true, false, nil
		tick = m.spin.Tick
	}
	m.syncPlaceholder()
	m.renderPlan()
	m.renderChat()
	m.chatView.GotoBottom()
	if !m.created {
		m.creating = true
		return m, tea.Batch(createCmd(m.ctx, m.store, text), tick)
	}
	return m, tea.Batch(sendCmd(m.ctx, m.agent, text), tick)
}

// createCmd creates the plan in factory, titled for text, the developer's
// first message.
func createCmd(ctx context.Context, s Store, text string) tea.Cmd {
	return func() tea.Msg { return createdMsg{text: text, err: s.Create(ctx, titleOf(text))} }
}

// applyEvent shows one thing the agent reported, then waits for the next.
func (m Model) applyEvent(ev Event) (Model, tea.Cmd) {
	next := waitEvent(m.ctx, m.agent)
	switch {
	case ev.Activity != "":
		var tick tea.Cmd
		if !m.busy {
			// Nothing was sent, so a factory task started this turn.
			m.busy, m.byTask, m.log = true, true, nil
			// Factory hands out a task only once it has moved the plan.
			m.moving = nil
			tick = m.spin.Tick
		}
		m.activity = ev.Activity
		m.log = append(m.log, m.activity)
		if len(m.log) > 5 {
			m.log = m.log[len(m.log)-5:]
		}
		m.renderPlan()
		m.renderChat()
		return m, tea.Batch(next, tick)
	case ev.Reply != nil:
		m.busy, m.byTask, m.activity, m.log = false, false, "", nil
		m.messages = append(m.messages, message{text: ev.Reply.Text, byTask: ev.Reply.ByTask})
	case ev.Problem != nil:
		m.busy, m.byTask, m.activity, m.log = false, false, "", nil
		m.messages = append(m.messages, message{text: ev.Problem.Error(), failed: true})
	}
	m.renderPlan()
	m.renderChat()
	return m, tea.Batch(next, refreshCmd(m.ctx, m.store))
}

// maxTitleLen keeps a plan's title short enough to read in the plan list.
const maxTitleLen = 40

// titleOf is the plan's title from the developer's message: a slug of its
// first line, cut at a whole word.
func titleOf(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	words := strings.FieldsFunc(strings.ToLower(line), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 {
		return "plan"
	}
	first := []rune(words[0])
	slug := string(first[:min(len(first), maxTitleLen)])
	for _, w := range words[1:] {
		if utf8.RuneCountInString(slug)+1+utf8.RuneCountInString(w) > maxTitleLen {
			break
		}
		slug += "-" + w
	}
	return slug
}

// syncPlaceholder tells the developer that enter saves their comments when
// there are some queued, and that only comments change a plan waiting on
// their review.
func (m *Model) syncPlaceholder() {
	switch {
	case len(queuedComments(m.comments)) > 0 && !m.takesComments():
		m.input.Placeholder = heldPlaceholder
	case len(queuedComments(m.comments)) > 0:
		m.input.Placeholder = commentsPlaceholder
	case m.takesComments():
		m.input.Placeholder = reviewPlaceholder
	default:
		m.input.Placeholder = chatPlaceholder
	}
}

// applyApproval is the end of approving the plan's design, or the plan.
func (m Model) applyApproval(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case designApprovedMsg:
		m.approvingDesign = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not approve the design: " + msg.err.Error())
			return m, nil
		}
		m.moving = &transition{kind: movingDesign, from: m.state, since: time.Now()}
		m.status = ""
		return m, tea.Batch(refreshCmd(m.ctx, m.store), m.spin.Tick)
	case approvedMsg:
		m.approving = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not approve the plan: " + msg.err.Error())
			return m, nil
		}
		m.BuildID = msg.buildID
		return m, closed(msg.buildID)
	}
	return m, nil
}

// approveDesignCmd approves the plan's design, for factory to write its
// units.
func approveDesignCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg { return designApprovedMsg{err: s.ApproveDesign(ctx)} }
}

// approveCmd runs approve in the background, and reads what it reports: an
// approveStepMsg as each step starts, then its approvedMsg.
func approveCmd(approve Approver) tea.Cmd {
	// Buffered past the steps an approval takes, so it never waits on a
	// screen that has closed.
	ch := make(chan tea.Msg, 16)
	go func() {
		defer close(ch)
		id, err := approve(func(text string) {
			select {
			case ch <- approveStepMsg{text: text}:
			default:
			}
		})
		ch <- approvedMsg{buildID: id, err: err}
	}()
	return waitApproval(ch)
}

// waitApproval reads the approval's next message from ch.
func waitApproval(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		if step, isStep := msg.(approveStepMsg); isStep {
			step.next = ch
			return step
		}
		return msg
	}
}

// Layout: the chat is on the left and the plan, which needs the room, takes
// the right 60% of the screen. Each pane has a one-cell border with its title
// set into the top edge; the plan has a row under it for comments, and the
// screen a header row above the panes and a footer row below them.
const (
	borderSize   = 2
	bubbleChrome = borderSize + 2
	// chatGutter is the cells of room left and right of the messages.
	chatGutter = 1
	headerRows = 1
	tabRows    = 1
	footerRows = 3
	inputRows  = 3
	// inputBoxRows is the input and its border, which has the input's title
	// set into the top edge.
	inputBoxRows = inputRows + borderSize
	// padX and padY are the room between a pane's border and its content.
	padX           = 1
	padY           = 1
	planFooterRows = 1
	planPercent    = 60
)

func (m *Model) planWidth() int {
	if m.narrow() {
		return m.width
	}
	return m.width * planPercent / 100
}

func (m *Model) chatWidth() int {
	if m.narrow() {
		return m.width
	}
	return m.width - m.planWidth()
}

// topRows is what sits above the panes.
func (m *Model) topRows() int {
	if m.narrow() {
		return headerRows + tabRows
	}
	return headerRows
}

func (m *Model) layout() {
	inner := m.height - m.topRows() - footerRows - borderSize
	m.planView.SetWidth(m.planWidth() - borderSize - 2*padX)
	m.planView.SetHeight(inner - planFooterRows - padY)
	// The editor is the text of a comment's card, which sits past the gutter
	// on a rail, indented, with a cell either side.
	m.commentInput.SetWidth(m.planView.Width() - gutterWidth - 6)
	m.chatView.SetWidth(m.chatWidth() - borderSize - 2*padX)
	m.chatView.SetHeight(inner - inputBoxRows - padY)
	m.input.SetWidth(m.chatWidth() - 2*borderSize - 2*padX)
	m.renderPlan()
	m.renderChat()
}

// newRenderer is a glamour renderer for the terminal's background that wraps
// at width.
func newRenderer(dark bool, width int) (*glamour.TermRenderer, error) {
	style := "light"
	if dark {
		style = "dark"
	}
	return glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width))
}

// renderMarkdown renders md with glamour at width, or returns it as typed
// when it cannot be rendered.
func renderMarkdown(md string, dark bool, width int) string {
	r, err := newRenderer(dark, width)
	if err != nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return strings.Trim(out, "\n")
}

// renderLines renders one block with r, without the blank lines glamour puts
// around a document.
func renderLines(r *glamour.TermRenderer, md string) []string {
	out, err := r.Render(md)
	if err != nil {
		return strings.Split(md, "\n")
	}
	lines := strings.Split(out, "\n")
	blank := func(s string) bool { return strings.TrimSpace(ansi.Strip(s)) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// renderPlan splits the plan into blocks and renders each, when the plan or
// the pane's width has changed since it last did.
func (m *Model) renderPlan() {
	w := m.planView.Width() - gutterWidth
	if w <= 0 {
		return
	}
	if strings.TrimSpace(m.plan) == "" {
		m.blocks = nil
		m.units, m.questions = 0, 0
		if m.busy {
			m.planView.SetContent(lipgloss.NewStyle().Padding(1, 2).Render(m.skeleton(m.planView.Width() - 4)))
			return
		}
		m.planView.SetContent(ui.HelperStyle.Padding(1, 2).Width(m.planView.Width()).
			Render("No plan yet. Tell chunk what you want to change, and the plan it writes appears here."))
		return
	}
	if w == m.renderedWidth && m.blocks != nil && m.renderedFrom == m.plan {
		// The blocks themselves need no work, but the comments can have
		// changed (one saved, one resolved, …) since this plan text was last
		// rendered, so they are re-anchored even on this fast path.
		m.reanchor()
		m.drawPlan()
		return
	}
	r, err := newRenderer(m.dark, w-1)
	if err != nil {
		m.planView.SetContent(m.plan)
		return
	}
	var cursorSource string
	if m.cursor < len(m.blocks) {
		cursorSource = m.blocks[m.cursor].source
	}
	blocks := splitBlocks(m.plan)
	m.blocks = make([]renderedBlock, len(blocks))
	var st planStats
	from := 0
	for i, b := range blocks {
		at := -1
		if j := strings.Index(m.plan[from:], b.source); j >= 0 {
			at, from = from+j, from+j+len(b.source)
		}
		lines, ok := m.styled(b, &st, w)
		if !ok {
			lines, ok = m.blockLines(b, w-1)
		}
		if !ok {
			lines = renderLines(r, b.source)
		}
		m.blocks[i] = renderedBlock{block: b, lines: lines, at: at}
	}
	m.units, m.questions = st.units, st.questions
	m.renderedFrom, m.renderedWidth = m.plan, w
	m.reanchor()
	// A selection is of blocks as they were split, so it goes with them.
	m.anchor = -1
	// The cursor stays on its block when Claude left that block as it was,
	// and otherwise on the same place in the plan as near as there is one.
	// On a comment on a part Claude has since rewritten it has no block to
	// follow; applyRefresh puts it back on that comment.
	m.cursor = min(m.cursor, m.stops()-1)
	for i, b := range m.blocks {
		if cursorSource != "" && b.source == cursorSource {
			m.cursor = i
			break
		}
	}
	m.moveCursor(m.cursor)
}

// userBubble is text as a bubble against the right edge of the chat, so it
// reads as the developer's side of the conversation.
func (m *Model) userBubble(text string, w int) string {
	// The bubble leaves room on its left, so that it reads as a side of the
	// conversation, and has a border and a cell of padding each side.
	inner := max(w*85/100-bubbleChrome, 1)
	fit := min(lipgloss.Width(text), inner)
	bubble := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.faint()).
		Padding(0, 1).Width(fit + bubbleChrome).Render(text)
	return lipgloss.PlaceHorizontal(w, lipgloss.Right, bubble)
}

// queuedBubble is the queued comments waiting to be saved, shown like the
// message they will go with when the developer sends.
func (m *Model) queuedBubble(cs []comment, w int) string {
	inner := max(w*85/100-bubbleChrome, 1)
	parts := make([]string, 0, 2*len(cs))
	for _, c := range cs {
		parts = append(parts, m.style(m.faint()).Render("┃ "+excerpt(c.quote, inner-2)), m.style(m.amber()).Render("┃ "+c.text))
	}
	text := strings.Join(parts, "\n")
	fit := min(lipgloss.Width(text), inner)
	bubble := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.faint()).
		Padding(0, 1).Width(fit + bubbleChrome).Render(text)
	return lipgloss.PlaceHorizontal(w, lipgloss.Right, bubble)
}

func (m *Model) renderChat() {
	// A cell of room each side keeps the messages off the pane's border.
	w := m.chatView.Width() - 2*chatGutter
	if w <= 0 {
		return
	}
	var b strings.Builder
	if m.inbox {
		note := "Reading this plan does not start an agent."
		if m.project == "" {
			note += " Open chunk watch in its repository to chat or build."
		} else {
			note += " Sending a message starts or resumes it in " + m.project + "."
		}
		b.WriteString(m.style(m.dim()).Width(w).Render(note) + "\n\n")
	}
	for i := range m.messages {
		msg := &m.messages[i]
		switch {
		case msg.fromUser:
			b.WriteString(m.userBubble(msg.text, w))
			if msg.toBusy {
				b.WriteString("\n" + lipgloss.PlaceHorizontal(w, lipgloss.Right,
					m.style(m.faint()).Render("sent while chunk was working; it reads this next")))
			}
		case msg.failed:
			b.WriteString(chunkStyle.Render("chunk") + "\n")
			b.WriteString(ui.ErrorStyle.Width(w).Render(msg.text))
		default:
			b.WriteString(chunkStyle.Render("chunk"))
			if msg.byTask {
				b.WriteString(m.style(m.faint()).Render(" · factory task"))
			}
			b.WriteString("\n")
			if msg.renderedWidth != w {
				msg.rendered, msg.renderedWidth = renderMarkdown(msg.text, m.dark, w-2), w
			}
			b.WriteString(msg.rendered)
		}
		b.WriteString("\n\n")
	}
	if q := queuedComments(m.comments); len(q) > 0 && !m.savingComments {
		// The comments wait in a bubble like the message they will go in.
		b.WriteString(m.queuedBubble(q, w) + "\n")
		b.WriteString(m.style(m.faint()).Render("queued · press enter to save") + "\n\n")
	}
	if m.savingComments {
		b.WriteString(m.style(m.purple()).Render(m.spin.View()+"Saving your comments…") + "\n\n")
	}
	if m.busy {
		b.WriteString(m.activityLog(w))
	}
	// The chat follows what is added to it unless the developer has
	// scrolled up to read, when it stays where they are.
	follow := m.chatView.AtBottom()
	m.chatView.SetContent(lipgloss.NewStyle().PaddingLeft(chatGutter).Render(b.String()))
	if follow {
		m.chatView.GotoBottom()
	}
}

// activityLog is what the agent has been doing this turn: each thing it has
// finished with a tick, and the one in hand with the spinner.
func (m Model) activityLog(w int) string {
	if len(m.log) == 0 {
		// The spinner's frames carry their own space after the dot.
		return m.style(m.purple()).Render(m.spin.View() + "chunk is thinking…")
	}
	var lines []string
	if m.byTask {
		lines = append(lines, m.style(m.faint()).Render("working on a factory task"))
	}
	for i, a := range m.log {
		a = ansi.Truncate(a, w-2, "…")
		if i == len(m.log)-1 {
			lines = append(lines, m.style(m.purple()).Render(m.spin.View()+a))
			continue
		}
		lines = append(lines, m.style(m.green()).Render("✓ ")+m.style(m.dim()).Render(a))
	}
	return strings.Join(lines, "\n")
}

// restyle renders the plan and the chat again for a change of background.
func (m *Model) restyle() {
	m.renderedWidth = 0
	for i := range m.messages {
		m.messages[i].renderedWidth = 0
	}
	m.renderPlan()
	m.renderChat()
}

var chunkStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))

// Content is the screen as its host draws it: the panes and the footer,
// filling the size it was last sent.
func (m Model) Content() string {
	if m.width == 0 {
		return ""
	}
	pane := func(focused bool, w int, title, content string) string {
		edge := m.quiet()
		if focused {
			edge = m.brand()
		}
		s := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(edge).
			Padding(padY, padX, 0, padX).
			Width(w).Height(m.height - m.topRows() - footerRows)
		return titled(s.Render(content), title, lipgloss.NewStyle().Foreground(edge).Render)
	}
	// The pane without the keyboard has a dim title as well as a dim border.
	title := func(focused bool, name string) string {
		if focused {
			return ui.TitleStyle.Render(name)
		}
		return ui.HelperStyle.Faint(true).Render(name)
	}

	planName := "Plan"
	if m.moving != nil {
		planName = "Plan · " + m.movingText()
	} else if m.state != "" {
		planName = "Plan · " + m.stateText()
	}
	planTitle := title(m.focus == panePlan, planName)
	if m.focus == panePlan {
		planTitle += ui.HelperStyle.Render(fmt.Sprintf("  %3.f%%", m.planView.ScrollPercent()*100))
	}
	chat := pane(m.focus == paneChat, m.chatWidth(), title(m.focus == paneChat, "Chat"),
		m.chatView.View()+"\n"+m.inputBox())
	plan := pane(m.focus == panePlan, m.planWidth(), planTitle,
		m.planView.View()+"\n"+m.planFooter())

	panes := lipgloss.JoinHorizontal(lipgloss.Top, chat, plan)
	top := m.header()
	if m.narrow() {
		// One pane at a time, the other a tab away.
		panes = chat
		if m.focus == panePlan {
			panes = plan
		}
		top += "\n" + m.tabs()
	}
	return lipgloss.JoinVertical(lipgloss.Left, top, panes, m.actionBar())
}

// inputBox is the message being written, in a box of its own under the chat.
// Its border stays a step below the pane's accent, so the accent shows once
// per pane.
func (m Model) inputBox() string {
	edge := m.quiet()
	if m.focus == paneChat && !m.commenting {
		edge = m.dim()
	}
	w := m.chatWidth() - borderSize - 2*padX
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(edge).
		Width(w).Render(m.input.View())
	return titled(box, ui.TitleStyle.Render("Message"), lipgloss.NewStyle().Foreground(edge).Render)
}

// titled sets title into the top edge of box, a pane drawn with a rounded
// border, as in "╭─ Plan ─────╮", cut to leave at least one cell of edge
// after it. line draws the edge in the border's colour.
func titled(box, title string, line func(...string) string) string {
	top, rest, _ := strings.Cut(box, "\n")
	n := lipgloss.Width(top) - 2
	if n < 5 {
		return box
	}
	title = ansi.Truncate(title, n-4, "…")
	edge := line("╭─ ") + title + line(" "+strings.Repeat("─", max(n-3-lipgloss.Width(title), 0))+"╮")
	return edge + "\n" + rest
}
