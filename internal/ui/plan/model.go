// Package plan is the planning screen: a new plan's request, then its
// planning session's activity, beside the plan factory is writing, rendered
// as markdown. The developer can comment on any part of the plan; comments
// are queued locally and saved to factory when the developer sends. When the
// developer approves the plan, chunk builds it through factory.
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

	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/clipboard"
)

// Approver approves the plan in factory and starts its build, returning the
// build's ID. It reads the plan itself rather than taking the screen's copy,
// which may be behind factory's by a poll. It calls step with each step as
// it starts, as a line for the screen to show while it works.
type Approver func(step func(string)) (string, error)

// ReadyToApprove is whether f waits for the developer to approve it, as
// planstate.ReadyToApprove decides.
func ReadyToApprove(f Flow) bool {
	return planstate.ReadyToApprove(clean(f.State), moves(f.Next))
}

// agentActs is whether next has a transition a session or the daemon
// takes.
func agentActs(next []Step) bool {
	return planstate.AgentActs(moves(next))
}

// moves is next as planstate reads it, stripped of escapes.
func moves(next []Step) []planstate.Move {
	out := make([]planstate.Move, len(next))
	for i, s := range next {
		out[i] = planstate.Move{Actor: clean(string(s.Actor)), Event: clean(s.Event)}
	}
	return out
}

// pane is which side of the screen the keyboard goes to.
type pane int

const (
	paneChat pane = iota
	panePlan
)

// rightTab is what the right pane shows.
type rightTab int

const (
	tabPlan rightTab = iota
	tabFlow
)

// message is one entry in the chat.
type message struct {
	fromUser bool
	text     string
	failed   bool
	// byTask is a reply to a factory task rather than to the developer.
	byTask bool
	// task is the category of the factory task the reply is to, when byTask
	// and the screen knew it.
	task string
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
	// conflictsSentMsg is the end of sending resolve-conflicts.
	conflictsSentMsg struct{ err error }
	// startedMsg is the end of sending prioritize.
	startedMsg struct{ err error }
	// abandonedMsg is the end of sending abandon.
	abandonedMsg struct{ err error }
	// planDeletedMsg is the end of deleting the plan.
	planDeletedMsg struct{ err error }
	// approveStepMsg is a step of the approval starting; next is where the
	// approval's later messages come from.
	approveStepMsg struct {
		text string
		next <-chan tea.Msg
	}
	pollMsg      struct{}
	refreshedMsg struct {
		flow     Flow
		text     string
		comments []Comment
		err      error
	}
	commentSavedMsg struct{ err error }
	// createdMsg is the end of creating the plan in factory, for the
	// developer's first message, text; title is the name factory gave it.
	createdMsg struct {
		text, title string
		err         error
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
	movingConflicts
)

// pollEvery is how often the plan pane polls the store for the plan's text,
// state and comments while the screen is open.
const pollEvery = 5 * time.Second

func pollCmd() tea.Cmd { return tea.Tick(pollEvery, func(time.Time) tea.Msg { return pollMsg{} }) }

// refreshCmd reads the plan's flow, text and comments from the store.
func refreshCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg {
		flow, err := s.Flow(ctx)
		if err != nil {
			return refreshedMsg{err: err}
		}
		text, err := s.Text(ctx)
		if err != nil {
			return refreshedMsg{err: err}
		}
		cs, err := s.Comments(ctx)
		return refreshedMsg{flow: flow, text: text, comments: cs, err: err}
	}
}

// familyCmd reads the plan's relatives from the store. It is a command of its
// own, so a failure to read them never holds up the plan's refresh.
func familyCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg {
		fam, err := s.Family(ctx)
		return familiedMsg{family: fam, err: err}
	}
}

// familiedMsg is the end of reading the plan's relatives.
type familiedMsg struct {
	family Family
	err    error
}

// OpenMsg is the screen asking its host to open plan ID, a relative of its
// plan the developer chose.
type OpenMsg struct{ ID, Title string }

// ClosedMsg is the screen asking its host to close it: the plan was approved
// and is being built as BuildID, or, when BuildID is "", the developer left.
type ClosedMsg struct {
	BuildID string
}

// BuildingMsg is the screen telling its host that the plan was approved and
// is being built as BuildID, while the screen stays open to follow it. A
// host that does not keep the screen gets ClosedMsg instead.
type BuildingMsg struct {
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
	events []string
	// flow is the plan's flow as the last refresh read it.
	flow Flow
	// family is the plan's relatives as the last read of them found them,
	// and familyErr why that read failed, or nil.
	family    Family
	familyErr error
	messages  []message
	// created is whether the plan has been made in factory with store.Create,
	// and creating whether that is under way.
	created, creating bool
	// request is the new plan's request until a session has started on it.
	request string
	// resuming is whether a Request or Resume is under way.
	resuming bool
	// resumedIn is the flow state the screen last called Resume in, so a
	// planner that cannot be started is tried once per state rather than on
	// every poll; "" once a session claims the plan or the state moves.
	resumedIn string

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
	// editing is set from the developer's e until an edit made in $EDITOR
	// has been saved to factory, or has failed to be.
	editing bool
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
	// flowView is the Flow tab, which the right pane shows in place of the
	// plan while tab is tabFlow.
	flowView viewport.Model
	chatView viewport.Model
	input    textarea.Model
	spin     spinner.Model

	focus pane
	// tab is what the right pane shows; tabChosen is whether the first
	// refresh, or the developer, has picked it yet.
	tab       rightTab
	tabChosen bool
	// related is the Flow tab's cursor over its Related rows.
	related int
	width   int
	height  int

	// busy is whether the agent is working: from a send, or from the first
	// thing it does on a factory task, until its reply.
	busy bool
	// byTask is whether what the agent is working on is a factory task's,
	// rather than the developer's message.
	byTask bool
	// task is the category of the factory task the agent's turn is on, as
	// the turn's first refresh found it on the flow; "" until then, and
	// when the flow named none.
	task string
	// log is what the agent has done this turn, oldest first.
	log      []string
	activity string

	confirming bool
	// approvingDesign is set from the developer's y to a design until factory
	// has the event that approves it; pendingDesign is whether the y being
	// asked for approves the design rather than the plan.
	approvingDesign, pendingDesign bool
	// resolvingConflicts is set from the developer's y to resolving the PR's
	// conflicts until factory has the event; pendingConflicts is whether the
	// y being asked for resolves them.
	resolvingConflicts, pendingConflicts bool
	// startingWork is set from the developer's y to starting work on an
	// Unprioritized plan until factory has the event; pendingStart is
	// whether the y being asked for starts it.
	startingWork, pendingStart bool
	// askingReason is whether the status bar asks why the plan is
	// abandoned; abandoning is set from the developer's reason until factory
	// has the abandon event.
	askingReason, abandoning bool
	// pendingDeletePlan is whether the y being asked for deletes the plan;
	// deleting is set from that y until factory answers.
	pendingDeletePlan, deleting bool
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
	// built is whether the plan was approved and is being built while the
	// screen stays open; its comments are no longer sent.
	built bool

	// build reads the plan's build from the daemon; nil when the host has none.
	build Build
	// builds is each build session read, oldest first; reading is whether a
	// read is out or its tick is set, so only one loop runs.
	builds  []buildRun
	reading bool
	// buildMissing is that the last read found no build; buildErr that it failed.
	buildMissing bool
	buildErr     string

	pending string
	// lastClick is the plan block last clicked, to tell a double click.
	lastClick *lastClick
	// text is the text selected with the mouse, to copy.
	text textDrag
	// copyText puts text on the clipboard.
	copyText func(string) tea.Cmd
}

// chatPlaceholder is the request input's, the only input the left pane has.
const chatPlaceholder = "Describe the change you want planned…"

// New returns the planning screen. A non-empty first request is sent as soon
// as the screen opens. Once the plan exists the left pane takes no typing, so
// a plan that exists opens on the plan pane.
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
	// The Flow tab's keys are the screen's own too: its ↑/↓ move the Related
	// cursor rather than scroll.
	flowView := viewport.New()
	flowView.KeyMap = viewport.KeyMap{}

	created := store.ID() != ""
	focus := paneChat
	if created {
		focus = panePlan
		in.Blur()
	}
	return Model{
		ctx:          ctx,
		agent:        agent,
		approve:      approve,
		store:        store,
		created:      created,
		focus:        focus,
		copyText:     clipboard.Copy,
		planView:     planView,
		flowView:     flowView,
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
// No agent starts until a new plan's request is sent, or, with a project,
// factory waits on a planner that is not there. Activity belongs to this
// visit.
func (m Model) WithInbox(title, project string) Model {
	m.back, m.inbox, m.title, m.project = true, true, title, project
	return m
}

// Unsent reports local input that quitting the host would discard.
func (m Model) Unsent() string { return m.unsent() }

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.Poll(), pollCmd(), waitEvent(m.ctx, m.agent))
}

// Poll reads the plan's state, text, comments and relatives from the store,
// as the screen's own timer does every few seconds. A host can use it to
// refresh the screen deterministically, without waiting for that timer.
func (m Model) Poll() tea.Cmd {
	return tea.Batch(refreshCmd(m.ctx, m.store), familyCmd(m.ctx, m.store))
}

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

	case sentMsg, resumedMsg:
		return m.applySent(msg), nil

	case pollMsg:
		return m, tea.Batch(m.Poll(), pollCmd())

	case refreshedMsg:
		m = m.applyRefresh(msg)
		var resume, follow tea.Cmd
		m, resume = m.resumeIfOwed()
		m, follow = m.followBuild()
		return m, tea.Batch(resume, follow)

	case buildTickMsg, buildReadMsg:
		return m.updateBuild(msg)

	case familiedMsg:
		m.family, m.familyErr = msg.family, msg.err
		if msg.err != nil {
			m.family = Family{}
		}
		// The cursor stays on a row that is still there.
		m.related = max(min(m.related, len(m.relatedRows())-1), 0)
		m.renderFlow()
		return m, nil

	case createdMsg:
		return m.applyCreated(msg)

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

	case editedMsg:
		return m.applyEdited(msg)

	case editSavedMsg:
		return m.applyEditSaved(msg)

	case approveStepMsg:
		if m.approving {
			m.status = msg.text
		}
		return m, waitApproval(msg.next)

	case designApprovedMsg, approvedMsg, conflictsSentMsg, startedMsg, abandonedMsg, planDeletedMsg:
		return m.applyApproval(msg)

	case spinner.TickMsg:
		if !m.spinning() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.renderChat()
		return m, cmd
	}

	return m.forward(msg)
}

// applyCreated handles the end of creating the plan in factory: on success
// it starts the planning session on the request, and on failure it says so
// in the Activity pane.
func (m Model) applyCreated(msg createdMsg) (Model, tea.Cmd) {
	m.creating = false
	if msg.err != nil {
		m.busy, m.byTask, m.task, m.activity, m.log = false, false, "", "", nil
		m.messages = append(m.messages, message{text: "Could not create the plan: " + msg.err.Error(), failed: true})
		m.renderPlan()
		m.renderChat()
		return m, nil
	}
	// The input is gone for good, and the activity takes its rows.
	m.created, m.request, m.resuming = true, msg.text, true
	// A title the host gave the screen becomes the plan's own once it is
	// created; a screen with no title keeps showing the branch.
	if m.title != "" && msg.title != "" {
		m.title = msg.title
	}
	m.input.Blur()
	m.layout()
	return m, requestCmd(m.ctx, m.agent, msg.text)
}

// applySent handles the end of a Request (sentMsg) or a Resume (resumedMsg)
// of the screen's agent.
func (m Model) applySent(msg tea.Msg) Model {
	switch msg := msg.(type) {
	case sentMsg:
		if msg.agent != m.agent {
			return m
		}
		m.resuming = false
		if msg.err == nil {
			m.request = ""
			return m
		}
		// The request is kept, and sent again once factory next waits on the
		// planner: it is in no plan yet.
		m.busy, m.byTask, m.task, m.activity, m.log = false, false, "", "", nil
		m.messages = append(m.messages, message{text: "Could not start the planning session: " + msg.err.Error(), failed: true})
		m.renderPlan()
		m.renderChat()
	case resumedMsg:
		if msg.agent != m.agent {
			return m
		}
		m.resuming = false
		if msg.err != nil {
			m.messages = append(m.messages, message{text: "Could not resume the planning session: " + msg.err.Error(), failed: true})
			m.renderChat()
		}
	}
	return m
}

// applyQueueSaved handles the end of saving the queued comments.
func (m Model) applyQueueSaved(msg queueSavedMsg) (Model, tea.Cmd) {
	m.savingComments = false
	// Whatever got saved before the failure, if any, stays saved; only
	// the rest is still queued.
	m.comments = dropQueued(m.comments, msg.saved)
	m.renderPlan()
	m.pendingHandOff = msg.handOff && !msg.handedOff
	m.trackFeedback(msg.saved)
	var tick tea.Cmd
	if msg.handedOff {
		m.moving = &transition{kind: movingComments, from: m.state, since: time.Now()}
		m.status = ""
		tick = m.spin.Tick
	}
	if msg.err != nil {
		m.status = ui.ErrorStyle.Render(msg.err.Error())
	}
	return m, tea.Batch(refreshCmd(m.ctx, m.store), tick)
}

// spinning is whether something the spinner shows is under way: the agent's
// turn, an approval or event not yet answered, or the wait for factory.
func (m Model) spinning() bool {
	return m.busy || m.approving || m.approvingDesign || m.resolvingConflicts || m.moving != nil || m.buildRunning()
}

// awaitingStatus is whether the status bar holds something still under way
// or waiting on the developer: a question to answer, the quit prompt, or an
// approval or save factory has not answered yet.
func (m Model) awaitingStatus() bool {
	return m.confirming || m.quitting || m.approving || m.approvingDesign || m.resolvingConflicts || m.savingComments ||
		m.askingReason || m.abandoning || m.deleting
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

	if msg.flow.State != m.state && !m.awaitingStatus() {
		// A note on what happened is out of date once factory moves the
		// plan, and left up it would hide the bar's buttons.
		m.status = ""
	}
	m.state, m.events, m.plan = msg.flow.State, msg.flow.Events, msg.text
	m.flow = msg.flow
	if m.flow.Claimed || m.state != m.resumedIn {
		// A flow that leaves the state it was resumed in, or that a session
		// claims, is resumed again once it is next owed one.
		m.resumedIn = ""
	}
	if !m.tabChosen {
		// The first refresh of a visit opens the right pane on what the
		// plan's phase is about; later moves leave the tab where it is.
		m.tab, m.tabChosen = tabFor(m.state), true
	}
	if m.byTask && m.task == "" {
		// The first task the flow names in a turn is the one the turn is on:
		// by its end the agent may have submitted it and the flow moved on.
		m.task = msg.flow.Task
	}
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
	m.renderChat()
	m.renderFlow()
	return m
}

// resumeOwed is whether f is waiting on a planner that is not there, so the
// screen should have its planner claim the plan again. Factory starts a new
// plan's flow in Unprioritized with a task for a session, so that counts as
// planning; a plan deferred there waits on the developer instead.
func resumeOwed(f Flow) bool {
	phase, _ := planstate.Parse(f.State)
	planning := strings.EqualFold(phase, "Planning") || planstate.IsUnprioritized(f.State)
	return !f.Ended && !f.Claimed && planning &&
		slices.ContainsFunc(f.Next, func(s Step) bool { return s.Actor == ActorSession })
}

// resumeIfOwed has the planner claim the plan again when factory waits on a
// planner that is not there, once per flow state. A request that has not
// started a session yet goes in place of the resume, so the planner never
// claims a new plan without it.
func (m Model) resumeIfOwed() (Model, tea.Cmd) {
	if !m.created || m.resuming || !resumeOwed(m.flow) || m.resumedIn == m.state ||
		(m.inbox && m.project == "") {
		return m, nil
	}
	m.resuming, m.resumedIn = true, m.state
	if m.request != "" {
		return m, requestCmd(m.ctx, m.agent, m.request)
	}
	return m, resumeCmd(m.ctx, m.agent)
}

// tabFor is the tab the right pane opens on for a plan in state: the plan
// while it is being planned, or has not started, and its flow after.
func tabFor(state string) rightTab {
	phase, _ := planstate.Parse(state)
	if strings.TrimSpace(state) == "" || planstate.IsUnprioritized(state) || strings.EqualFold(phase, "Planning") {
		return tabPlan
	}
	return tabFlow
}

func (m Model) updateKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	m.clearText()
	k := m.keys()
	if m.askingReason {
		// Before Quit, so esc keeps the plan rather than leaving.
		return m.answerReason(msg, k)
	}
	if key.Matches(msg, k.Quit) {
		return m.quit(msg.String())
	}
	if m.quitting {
		m.quitting, m.status = false, ""
	}
	if m.confirming {
		m.confirming, m.status = false, ""
		if !key.Matches(msg, k.Confirm) {
			m.pendingDelete, m.pendingDesign, m.pendingConflicts, m.pendingStart = "", false, false, false
			m.pendingDeletePlan = false
			return m, nil
		}
		return m.answerYes()
	}
	if m.commenting {
		return m.updateCommentKey(msg)
	}

	switch {
	case key.Matches(msg, k.Focus):
		// A narrow screen shows one of chat, Flow and Plan at a time, so tab
		// goes round all three.
		switch {
		case m.focus == paneChat && m.narrow():
			return m.showTab(tabFlow)
		case m.focus == paneChat:
			return m.focusOn(panePlan)
		case m.narrow() && m.tab == tabFlow:
			return m.showTab(tabPlan)
		}
		return m.focusOn(paneChat)
	case key.Matches(msg, k.SendPlan):
		return m.askToApprove(), nil
	case key.Matches(msg, k.Abandon):
		m.askingReason = true
		m.status = ui.TitleStyle.Render("Abandon plan as: ") + "c canceled · w won't do · d duplicate · esc keep it"
		return m, nil
	case key.Matches(msg, k.DeletePlan):
		m.confirming, m.pendingDeletePlan = true, true
		m.status = ui.TitleStyle.Render("Delete this plan, its comments and its flow? ") + confirmButtons
		return m, nil
	case key.Matches(msg, k.FlowTab):
		return m.showTab(tabFlow)
	case key.Matches(msg, k.PlanTab):
		return m.showTab(tabPlan)
	}

	if m.focus == panePlan && m.tab == tabFlow {
		return m.updateFlowKey(msg)
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
		if !m.created {
			return m.send(m.input.Value())
		}
		return m.deliver()
	}
	return m.forward(msg)
}

// heldStatus says why a send left the queued comments unsent.
const heldStatus = "Comments not sent: factory is working on the plan. Send them once it is waiting on you."

// deliver is a send of the queued comments from the developer: from a Human
// state it saves them to factory and hands them to it; from any other state
// they stay queued. Nothing goes to the planning session.
func (m Model) deliver() (Model, tea.Cmd) {
	if m.savingComments || m.built {
		return m, nil
	}
	queued := queuedComments(m.comments)
	// A hand-off owed by an earlier send is retried even with nothing queued.
	if len(queued) == 0 && !m.pendingHandOff {
		return m, nil
	}
	if !m.takesComments() {
		if m.moving == nil {
			m.status = heldStatus
		}
		return m, nil
	}
	m.savingComments = true
	m.status = ""
	return m, saveQueued(m.ctx, m.store, queued, m.handOffEvent())
}

// answerYes does what the question in the status bar asked, now it has been
// answered yes.
func (m Model) answerYes() (Model, tea.Cmd) {
	if m.pendingDeletePlan {
		m.pendingDeletePlan, m.deleting = false, true
		m.status = "Deleting the plan…"
		return m, deletePlanCmd(m.ctx, m.store)
	}
	if m.pendingConflicts {
		m.pendingConflicts, m.resolvingConflicts = false, true
		m.status = "Sending resolve-conflicts…"
		return m, tea.Batch(resolveConflictsCmd(m.ctx, m.store), m.spin.Tick)
	}
	if m.pendingStart {
		m.pendingStart, m.startingWork = false, true
		m.status = "Starting work…"
		return m, startWorkCmd(m.ctx, m.store)
	}
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

// answerReason answers the abandon prompt: a reason key sends the abandon
// for that reason, and any other key keeps the plan.
func (m Model) answerReason(msg tea.KeyPressMsg, k keyMap) (Model, tea.Cmd) {
	m.askingReason, m.status = false, ""
	var reason string
	switch {
	case key.Matches(msg, k.AbandonCanceled):
		reason = "canceled"
	case key.Matches(msg, k.AbandonWontDo):
		reason = "wont-do"
	case key.Matches(msg, k.AbandonDuplicate):
		reason = "duplicate"
	default:
		return m, nil
	}
	m.abandoning = true
	m.status = "Abandoning: " + reasonText(reason) + "…"
	return m, abandonCmd(m.ctx, m.store, reason)
}

// focusOn gives the keyboard to pane p, dropping any selection.
func (m Model) focusOn(p pane) (Model, tea.Cmd) {
	m.anchor = -1
	if p == panePlan {
		m.focus = panePlan
		m.input.Blur()
		m.drawPlan()
		m.renderFlow()
		return m, nil
	}
	m.focus = paneChat
	m.drawPlan()
	m.renderFlow()
	if m.created {
		return m, nil
	}
	return m, m.input.Focus()
}

// showTab gives the keyboard to the right pane, showing tab t there.
func (m Model) showTab(t rightTab) (Model, tea.Cmd) {
	m.tab, m.tabChosen = t, true
	return m.focusOn(panePlan)
}

// askToApprove starts approving the plan, or its design: it asks the
// developer to confirm, or says why it cannot go yet.
func (m Model) askToApprove() Model {
	queued := len(queuedComments(m.comments))
	switch {
	case m.built, m.approving, m.approvingDesign, m.resolvingConflicts, m.startingWork, m.abandoning, m.deleting, m.moving != nil:
	case m.busy:
		m.status = warn("Wait for chunk to finish.")
	case m.conflicting() && queued > 0:
		m.status = warn("Send your comments first: they hand the plan to the agent, which rebases it too.")
	case m.conflicting():
		m.confirming, m.pendingConflicts = true, true
		m.status = ui.TitleStyle.Render("Hand the plan back to the agent to rebase its PR onto its base? ") + confirmButtons
	case planstate.IsUnprioritized(m.state) && slices.Contains(m.events, prioritize):
		m.confirming, m.pendingStart = true, true
		m.status = ui.TitleStyle.Render("Start work on the plan? ") + confirmButtons
	case strings.TrimSpace(m.plan) == "":
		m.status = warn("There is no plan to approve yet.")
	case !ReadyToApprove(m.flow) && m.inReview():
		m.status = warn("The plan is not ready yet: wait for its review to pass.")
	case !ReadyToApprove(m.flow):
		m.status = warn("The plan cannot be approved while it is " + m.stateText() + ".")
	case planstate.StageOf(m.events, m.units) == planstate.StageQuestions:
		m.status = warn("Answer 3. Open Questions in comments first; factory offers no approval yet.")
	case planstate.StageOf(m.events, m.units) == planstate.StageDesign && (queued > 0 || m.openComments() > 0):
		m.status = warn("Send or resolve your comments first; then ctrl+s approves the design.")
	case planstate.StageOf(m.events, m.units) == planstate.StageDesign:
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
	if !m.created && strings.TrimSpace(m.input.Value()) != "" {
		parts = append(parts, "unsent request")
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
	if m.focus == panePlan && m.tab == tabFlow {
		m.flowView, cmd = m.flowView.Update(msg)
		return m, cmd
	}
	if m.focus == panePlan {
		m.planView, cmd = m.planView.Update(msg)
		return m, cmd
	}
	var cmds []tea.Cmd
	if !m.created {
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}
	// The chat scrolls with the mouse wheel while typing.
	if _, ok := msg.(tea.KeyPressMsg); !ok {
		m.chatView, cmd = m.chatView.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// send is a new plan's request: it creates the plan in factory, then starts
// the planning session on text once it is made. Once the plan exists, or
// while it is being created, it does nothing, as it does for an empty text.
func (m Model) send(text string) (Model, tea.Cmd) {
	text = strings.TrimSpace(text)
	if m.created || m.creating || text == "" {
		return m, nil
	}
	m.input.Reset()
	m.messages = append(m.messages, message{fromUser: true, text: text})
	m.status = ""
	var tick tea.Cmd
	if !m.busy {
		m.busy, m.byTask, m.task, m.log = true, false, "", nil
		tick = m.spin.Tick
	}
	m.renderPlan()
	m.renderChat()
	m.chatView.GotoBottom()
	m.creating = true
	return m, tea.Batch(createCmd(m.ctx, m.store, text), tick)
}

// track reports eventName from the screen, with the plan's ID once factory
// has given it one, so a funnel can join the plan's events.
func (m Model) track(eventName string, props map[string]any) {
	if props == nil {
		props = map[string]any{}
	}
	if m.store != nil && m.store.ID() != "" {
		props["plan_id"] = m.store.ID()
	}
	telemetry.Track(m.ctx, eventName, props)
}

// trackFeedback reports a round of feedback on the plan: comments saved
// together, so a funnel can count the rounds before a plan is approved.
// comments is how many were saved; a save that saved none is not a round.
func (m Model) trackFeedback(comments int) {
	if comments == 0 {
		return
	}
	m.track("plan_feedback_sent", map[string]any{"feedback_type": "comments", "comment_count": comments})
}

// createCmd creates the plan in factory for text, the developer's request,
// which the store names it for.
func createCmd(ctx context.Context, s Store, text string) tea.Cmd {
	return func() tea.Msg {
		title, err := s.Create(ctx, text)
		return createdMsg{text: text, title: title, err: err}
	}
}

// applyEvent shows one thing the agent reported, then waits for the next.
func (m Model) applyEvent(ev Event) (Model, tea.Cmd) {
	next := waitEvent(m.ctx, m.agent)
	switch {
	case ev.Activity != "":
		var tick tea.Cmd
		var refresh tea.Cmd
		if !m.busy {
			// Nothing was sent, so a factory task started this turn.
			m.busy, m.byTask, m.log = true, true, nil
			// Factory hands out a task only once it has moved the plan.
			m.moving = nil
			tick = m.spin.Tick
			// The flow names the task; read it now rather than at the next poll.
			refresh = refreshCmd(m.ctx, m.store)
		}
		m.activity = ev.Activity
		m.log = append(m.log, m.activity)
		if len(m.log) > 5 {
			m.log = m.log[len(m.log)-5:]
		}
		m.renderPlan()
		m.renderChat()
		return m, tea.Batch(next, tick, refresh)
	case ev.Reply != nil:
		task := m.task
		m.busy, m.byTask, m.task, m.activity, m.log = false, false, "", "", nil
		m.messages = append(m.messages, message{text: ev.Reply.Text, byTask: ev.Reply.ByTask, task: task})
	case ev.Problem != nil:
		m.busy, m.byTask, m.task, m.activity, m.log = false, false, "", "", nil
		m.messages = append(m.messages, message{text: ev.Problem.Error(), failed: true})
	}
	m.renderPlan()
	m.renderChat()
	return m, tea.Batch(next, refreshCmd(m.ctx, m.store))
}

// applyApproval is the end of approving the plan's design, or the plan, of
// sending resolve-conflicts, prioritize or abandon, or of deleting the plan.
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
	case conflictsSentMsg:
		m.resolvingConflicts = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not send resolve-conflicts: " + msg.err.Error())
			return m, nil
		}
		m.moving = &transition{kind: movingConflicts, from: m.state, since: time.Now()}
		m.status = ""
		return m, tea.Batch(refreshCmd(m.ctx, m.store), m.spin.Tick)
	case startedMsg:
		m.startingWork = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not start work: " + msg.err.Error())
			return m, nil
		}
		return m, refreshCmd(m.ctx, m.store)
	case abandonedMsg:
		m.abandoning = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not abandon the plan: " + msg.err.Error())
			return m, nil
		}
		// Factory has the event; the refresh shows the flow end once its
		// next poll applies it.
		m.status = ""
		return m, refreshCmd(m.ctx, m.store)
	case planDeletedMsg:
		m.deleting = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not delete the plan: " + msg.err.Error())
			return m, nil
		}
		return m, closed("")
	case approvedMsg:
		m.approving = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not approve the plan: " + msg.err.Error())
			return m, nil
		}
		m.BuildID = msg.buildID
		// Counted once factory has the plan, with the run it started.
		m.track("plan_approved", map[string]any{"run_id": msg.buildID})
		if !m.back {
			return m, closed(msg.buildID)
		}
		m.built, m.tab, m.tabChosen = true, tabFlow, true
		m.status = "Approved · building as run " + msg.buildID
		var follow tea.Cmd
		m, follow = m.followBuild()
		return m, tea.Batch(
			func() tea.Msg { return BuildingMsg{BuildID: msg.buildID} },
			refreshCmd(m.ctx, m.store),
			follow,
		)
	}
	return m, nil
}

// approveDesignCmd approves the plan's design, for factory to write its
// units.
func approveDesignCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg { return designApprovedMsg{err: s.ApproveDesign(ctx)} }
}

// resolveConflictsCmd has factory rebase the plan's conflicting PR.
func resolveConflictsCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg { return conflictsSentMsg{err: s.ResolveConflicts(ctx)} }
}

// startWorkCmd has factory start work on the Unprioritized plan.
func startWorkCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg { return startedMsg{err: s.StartWork(ctx)} }
}

// abandonCmd has factory end the plan without its PR merging, for reason.
func abandonCmd(ctx context.Context, s Store, reason string) tea.Cmd {
	return func() tea.Msg { return abandonedMsg{err: s.Abandon(ctx, reason)} }
}

// deletePlanCmd deletes the plan in factory.
func deletePlanCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg { return planDeletedMsg{err: s.Delete(ctx)} }
}

// reasonText is how the screen names an abandon reason.
func reasonText(reason string) string {
	if reason == "wont-do" {
		return "won't do"
	}
	return reason
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
		return headerRows + stripRows + tabRows
	}
	return headerRows + stripRows
}

func (m *Model) layout() {
	inner := m.height - m.topRows() - footerRows - borderSize
	m.planView.SetWidth(m.planWidth() - borderSize - 2*padX)
	m.planView.SetHeight(inner - planFooterRows - padY)
	m.flowView.SetWidth(m.planView.Width())
	m.flowView.SetHeight(m.planView.Height())
	// The editor is the text of a comment's card, which sits past the gutter
	// on a rail, indented, with a cell either side.
	m.commentInput.SetWidth(m.planView.Width() - gutterWidth - 6)
	m.chatView.SetWidth(m.chatWidth() - borderSize - 2*padX)
	if m.created {
		m.chatView.SetHeight(inner - padY)
	} else {
		m.chatView.SetHeight(inner - inputBoxRows - padY)
	}
	m.input.SetWidth(m.chatWidth() - 2*borderSize - 2*padX)
	m.renderPlan()
	m.renderChat()
	m.renderFlow()
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
			m.setContent(textPlan, lipgloss.NewStyle().Padding(1, 2).Render(m.skeleton(m.planView.Width()-4)))
			return
		}
		m.setContent(textPlan, ui.HelperStyle.Padding(1, 2).Width(m.planView.Width()).
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
		m.setContent(textPlan, m.plan)
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
			note += " Open chunk watch in its repository to plan it."
		} else {
			note += " Its planning session resumes here in " + m.project + " when factory is waiting on it."
		}
		b.WriteString(m.style(m.dim()).Width(w).Render(note) + "\n\n")
	}
	for i := range m.messages {
		msg := &m.messages[i]
		switch {
		case msg.fromUser:
			b.WriteString(m.userBubble(msg.text, w))
		case msg.failed:
			b.WriteString(chunkStyle.Render("chunk") + "\n")
			b.WriteString(ui.ErrorStyle.Width(w).Render(msg.text))
		default:
			b.WriteString(chunkStyle.Render("chunk"))
			if msg.byTask {
				mark := " · factory task"
				if msg.task != "" {
					mark = " · " + taskLabel(msg.task)
				}
				b.WriteString(m.style(m.faint()).Render(mark))
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
		b.WriteString(m.style(m.faint()).Render("queued · press enter or s to send") + "\n\n")
	}
	if m.savingComments {
		b.WriteString(m.style(m.purple()).Render(m.spin.View()+"Saving your comments…") + "\n\n")
	}
	if m.busy {
		b.WriteString(m.activityLog(w))
	}
	if m.showsBuild() {
		b.WriteString(m.buildSection(w))
	}
	// The chat follows what is added to it unless the developer has
	// scrolled up to read, when it stays where they are.
	follow := m.chatView.AtBottom()
	m.setContent(textChat, lipgloss.NewStyle().PaddingLeft(chatGutter).Render(b.String()))
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
		label := "working on a factory task"
		if m.task != "" {
			label = taskLabel(m.task)
		}
		lines = append(lines, m.style(m.faint()).Render(label))
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

// taskLabel is a factory task's category as the chat shows it: in sentence
// case, with spaces for hyphens, so "early-plan" reads "Early plan".
func taskLabel(category string) string {
	if category == "" {
		return ""
	}
	s := strings.ReplaceAll(category, "-", " ")
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// restyle renders the plan and the chat again for a change of background.
func (m *Model) restyle() {
	m.renderedWidth = 0
	for i := range m.messages {
		m.messages[i].renderedWidth = 0
	}
	m.renderPlan()
	m.renderChat()
	m.renderFlow()
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

	// The right pane's title is its tabs, the one showing drawn as a title.
	tabWord := func(t rightTab, name string) string {
		if m.tab == t {
			return title(m.focus == panePlan, name)
		}
		return ui.HelperStyle.Faint(true).Render(name)
	}
	planTitle := tabWord(tabFlow, "Flow") + "  " + tabWord(tabPlan, "Plan")
	if m.moving != nil {
		planTitle += title(m.focus == panePlan, " · "+m.movingText())
	}
	body := m.shown(textPlan) + "\n" + m.planFooter()
	if m.tab == tabFlow {
		body = m.shown(textFlow)
	}
	chatBody := m.shown(textChat) + "\n" + m.inputBox()
	if m.created {
		chatBody = m.shown(textChat)
	}
	chat := pane(m.focus == paneChat, m.chatWidth(), title(m.focus == paneChat, activityTitle), chatBody)
	plan := pane(m.focus == panePlan, m.planWidth(), planTitle, body)

	panes := lipgloss.JoinHorizontal(lipgloss.Top, chat, plan)
	top := m.header() + "\n" + m.strip()
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

// activityTitle is the left pane's title, before the plan exists, when the
// pane holds the request input, as after.
const activityTitle = "Activity"

// inputBox is the request being written, in a box of its own under the chat.
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
