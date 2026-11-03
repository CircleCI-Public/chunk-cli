// Package plan is the planning screen: one view at a time, the Flow view of
// what is under way on the plan (a new plan's request first), or the Plan
// view of the plan factory is writing, rendered as markdown. The developer
// can comment on any part of the plan; comments are queued locally and saved
// to factory when the developer sends. When the developer approves the plan,
// chunk builds it through factory.
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
	"github.com/CircleCI-Public/chunk-cli/internal/ui/chat"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/clipboard"
)

// Approver approves the plan in factory and starts its build, returning the
// build's ID. It reads the plan itself rather than taking the screen's copy,
// which may be behind factory's by a poll. It calls step with each step as
// it starts, as a line for the screen to show while it works.
type Approver func(step func(string)) (string, error)

// ReadyToApprove is whether f waits for the developer to approve it:
// factory waits on the developer and requires an approval of them.
func ReadyToApprove(f Flow) bool {
	return clean(f.WaitingOn) == planstate.Human && approval(f) != ""
}

// questionsOpen is whether f waits on the developer to answer 3. Open
// Questions: factory requires answer-open-questions of them.
func questionsOpen(f Flow) bool {
	return clean(f.WaitingOn) == planstate.Human && slices.ContainsFunc(f.Next, func(s Step) bool {
		return Actor(clean(string(s.Actor))) == ActorHuman && s.Required && clean(s.Event) == answerOpenQuestions
	})
}

// approval is the approval ctrl+s offers for f, as planstate.Approval
// decides from its next; "" for none.
func approval(f Flow) string {
	next := make([]planstate.Move, len(f.Next))
	for i, s := range f.Next {
		next[i] = planstate.Move{Actor: clean(string(s.Actor)), Event: clean(s.Event), Required: s.Required}
	}
	return planstate.Approval(next)
}

// rightTab is which view the body shows, Flow or Plan.
type rightTab int

const (
	tabPlan rightTab = iota
	tabFlow
)

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
		units    []Unit
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
		// notOffered is whether the comments were saved, and factory
		// neither took them nor offered a hand-off.
		notOffered bool
	}
)

// transition is a flow event factory has taken but not yet applied: the
// plan is still in from, and no task for it is out, until factory acts on it.
type transition struct {
	kind  transitionKind
	from  string // m.state when the event was sent
	since time.Time
	// left is whether a refresh since the event was sent found factory
	// waiting on someone other than the developer.
	left bool
}

type transitionKind int

const (
	movingComments transitionKind = iota
	movingDesign
	// movingApprove is the plan approved on a screen that stays open: until
	// factory acts on it, the flow last read may still take comments.
	movingApprove
)

// over is whether factory has acted on t, as a refresh that read state and
// f finds. A return to waiting on the developer counts only after a refresh
// found factory waiting on someone else, for a task that went out and came
// back between two polls.
func (t *transition) over(state string, f Flow) bool {
	if state != t.from || f.Task != "" {
		return true
	}
	switch f.WaitingOn {
	case "":
		return false
	case string(ActorHuman):
		return t.left
	}
	t.left = true
	return false
}

// pollEvery is how often the plan pane polls the store for the plan's text,
// state and comments while the screen is open.
const pollEvery = 5 * time.Second

func (m Model) pollCmd() tea.Cmd {
	return m.after(pollEvery, func(time.Time) tea.Msg { return pollMsg{} })
}

// refreshCmd reads the plan's flow, text, units and comments from the store.
// It fails as a whole when any read fails, so the text and its units always
// arrive together.
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
		units, err := s.Units(ctx)
		if err != nil {
			return refreshedMsg{err: err}
		}
		cs, err := s.Comments(ctx)
		return refreshedMsg{flow: flow, text: text, units: units, comments: cs, err: err}
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
	// units are the plan's units, read with its text by the last good refresh.
	units []Unit
	// familyLoaded is whether a read of the relatives has succeeded, so the
	// plan's blockers are known.
	familyLoaded bool
	// failure is the latest create, start, resume or agent failure, until
	// the next one of those succeeds.
	failure string
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
	// claimedIn is the flow state the screen last saw a session claim the
	// plan in, or "" once the state moves.
	claimedIn string

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
	// flowView is the Flow view, which the body shows in place of the plan
	// while tab is tabFlow.
	flowView viewport.Model
	input    textarea.Model
	spin     spinner.Model
	// answer is the Answer a question box's input, which has the keyboard
	// while answering is set.
	answer    textarea.Model
	answering bool
	// helping is whether ? has the body show the keys in place of the view.
	helping bool

	// tab is which view the body shows, Flow or Plan. Every visit opens on
	// the Flow view.
	tab rightTab
	// selected is the Flow view's selection, an index into m.selectables().
	selected int
	width    int
	height   int

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
	// activity is what the agent is doing now, for the running box.
	activity string

	confirming bool
	// approvingDesign is set from the developer's y to a design until factory
	// has the event that approves it; pendingDesign is whether the y being
	// asked for approves the design rather than the plan.
	approvingDesign, pendingDesign bool
	// startingWork is set from the developer's y to starting work on an
	// init plan until factory has the event; pendingStart is
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
	inbox bool
	// chatKey is whether the host opens its chat with ctrl+o.
	chatKey bool
	title   string
	project string
	// branch is the git branch the plan is for.
	branch string
	// questions is how many open questions the plan has.
	questions int
	// BuildID is the build of the plan, once it has been approved.
	BuildID string
	// built is whether the plan was approved, or its build started, while the
	// screen stays open: its build is followed before factory's state moves.
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
	// resumer starts a build session of the plan on R; nil when the host has
	// none. resumingBuild is set from that R until the daemon answers.
	resumer       Resumer
	resumingBuild bool
	// pendingStop is the build session S asked to stop, until the question
	// is answered; stoppingBuild is set from that y until the daemon answers.
	pendingStop   string
	stoppingBuild bool

	pending string
	// lastClick is the plan block last clicked, to tell a double click.
	lastClick *lastClick
	// text is the text selected with the mouse, to copy.
	text textDrag
	// copyText puts text on the clipboard.
	copyText func(string) tea.Cmd
	// tick is the clock the screen's timers run on; nil is tea.Tick.
	tick func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
}

// after is tea.Tick on the screen's clock.
func (m Model) after(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
	if m.tick == nil {
		return tea.Tick(d, fn)
	}
	return m.tick(d, fn)
}

// chatPlaceholder is the request input's, in the Describe the change box.
const chatPlaceholder = "Describe the change you want planned…"

// New returns the planning screen. A non-empty first request is sent as soon
// as the screen opens. Until the plan exists the Flow view's Describe the
// change box has the keyboard.
func New(ctx context.Context, agent Agent, approve Approver, store Store, first string) Model {
	in := textarea.New()
	in.Placeholder = chatPlaceholder
	in.ShowLineNumbers = false
	in.Prompt = ""
	// Enter sends; a new line is shift+enter or ctrl+j.
	in.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+j")
	in.SetHeight(inputRows)
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

	// An answer is one line: enter saves it.
	ai := textarea.New()
	ai.ShowLineNumbers = false
	ai.Prompt = "> "
	ai.Placeholder = "______"
	ai.KeyMap.InsertNewline.SetEnabled(false)
	ai.SetHeight(1)
	aiStyles := ai.Styles()
	aiStyles.Focused.CursorLine = lipgloss.NewStyle()
	ai.SetStyles(aiStyles)

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
	if created {
		in.Blur()
	}
	return Model{
		ctx:          ctx,
		agent:        agent,
		approve:      approve,
		store:        store,
		created:      created,
		tab:          tabFlow,
		copyText:     clipboard.Copy,
		planView:     planView,
		flowView:     flowView,
		input:        in,
		commentInput: ci,
		answer:       ai,
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

// WithBranch returns a copy of m that knows the branch the plan is for.
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

// WithChatKey returns a copy of m whose footer names ctrl+o, for a
// host that opens its chat with it.
func (m Model) WithChatKey() Model {
	m.chatKey = true
	return m
}

// Title is the plan's title as the host named it, or as factory named it
// once created; "" when the host gave none.
func (m Model) Title() string { return m.title }

// ID is the plan's ID, "" until it is created.
func (m Model) ID() string { return m.store.ID() }

// Unsent reports local input that quitting the host would discard.
func (m Model) Unsent() string { return m.unsent() }

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.Poll(), m.pollCmd(), waitEvent(m.ctx, m.agent))
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
			// The request goes in the input as if typed, so a failure to make
			// the plan gives it back there.
			m.input.SetValue(first)
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
		return m, tea.Batch(m.Poll(), m.pollCmd())

	case refreshedMsg:
		m = m.applyRefresh(msg)
		var resume, follow tea.Cmd
		m, resume = m.resumeIfOwed()
		m, follow = m.followBuild()
		return m, tea.Batch(resume, follow)

	case buildTickMsg, buildReadMsg, buildStoppedMsg:
		return m.updateBuild(msg)

	case buildResumedMsg:
		m.resumingBuild = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not start a build session: " + msg.err.Error())
			return m, nil
		}
		// The new session is followed at once, before factory's state moves.
		m.status, m.built, m.failure = "", true, ""
		m.renderFlow()
		return m.followBuild()

	case familiedMsg:
		prev, had := m.selectedKey()
		m.family, m.familyErr = msg.family, msg.err
		if msg.err != nil {
			m.family = Family{}
		} else {
			m.familyLoaded = true
		}
		// The selection stays on the item it was on, or one still there.
		m.reselect(prev, had)
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

	case designApprovedMsg, approvedMsg, startedMsg, abandonedMsg, planDeletedMsg:
		return m.applyApproval(msg)

	case spinner.TickMsg:
		if !m.spinning() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.renderFlow()
		return m, cmd
	}

	return m.forward(msg)
}

// fail says what failed on the status line, and keeps it as the Flow view's
// failure box until the next create, start, resume or agent turn succeeds.
func (m *Model) fail(text string) {
	m.failure = text
	m.status = ui.ErrorStyle.Render(text)
	m.renderFlow()
}

// applyCreated handles the end of creating the plan in factory: on success
// it starts the planning session on the request, and on failure it says so,
// with the request still in the Describe the change box.
func (m Model) applyCreated(msg createdMsg) (Model, tea.Cmd) {
	m.creating = false
	if msg.err != nil {
		m.busy, m.byTask, m.task, m.activity = false, false, "", ""
		m.renderPlan()
		m.fail("Could not create the plan: " + msg.err.Error())
		return m, m.input.Focus()
	}
	// The input is gone for good.
	m.created, m.request, m.resuming = true, msg.text, true
	m.failure = ""
	m.input.Reset()
	// A title the host gave the screen becomes the plan's own once it is
	// created; a screen with no title keeps showing New flow.
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
			m.request, m.failure = "", ""
			m.renderFlow()
			return m
		}
		// The request is kept, and sent again once factory next waits on the
		// planner: it is in no plan yet.
		m.busy, m.byTask, m.task, m.activity = false, false, "", ""
		m.renderPlan()
		m.fail("Could not start the planning session: " + msg.err.Error())
	case resumedMsg:
		if msg.agent != m.agent {
			return m
		}
		m.resuming = false
		if msg.err != nil {
			m.fail("Could not resume the planning session: " + msg.err.Error())
			return m
		}
		m.failure = ""
		m.renderFlow()
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
	if msg.notOffered {
		m.status = "Comments saved."
	}
	if msg.err != nil {
		m.status = ui.ErrorStyle.Render(msg.err.Error())
	}
	return m, tea.Batch(refreshCmd(m.ctx, m.store), tick)
}

// spinning is whether something the spinner shows is under way: the agent's
// turn, an approval or event not yet answered, or the wait for factory.
func (m Model) spinning() bool {
	return m.busy || m.approving || m.approvingDesign || m.moving != nil || m.buildRunning()
}

// awaitingStatus is whether the status bar holds something still under way
// or waiting on the developer: a question to answer, the quit prompt, or an
// approval or save factory has not answered yet.
func (m Model) awaitingStatus() bool {
	return m.confirming || m.quitting || m.approving || m.approvingDesign || m.savingComments ||
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
	prevSel, hadSel := m.selectedKey()

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
	if !slices.Equal(m.units, msg.units) {
		// The steps come from the units, so the pane is drawn again even
		// when the text is unchanged.
		m.units = msg.units
		m.renderedWidth = 0
	}
	m.flow = msg.flow
	if m.flow.Claimed || m.state != m.resumedIn {
		// A flow that leaves the state it was resumed in, or that a session
		// claims, is resumed again once it is next owed one.
		m.resumedIn = ""
	}
	switch {
	case m.flow.Claimed:
		m.claimedIn = m.state
	case m.state != m.claimedIn:
		m.claimedIn = ""
	}
	if m.byTask && m.task == "" {
		// The first task the flow names in a turn is the one the turn is on:
		// by its end the agent may have submitted it and the flow moved on.
		m.task = msg.flow.Task
	}
	if m.moving != nil && m.moving.over(m.state, m.flow) {
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
	m.reselect(prevSel, hadSel)
	if _, _, open := m.openQuestion(); m.answering &&
		(!open || m.moving != nil || !slices.Contains(m.events, answerOpenQuestions)) {
		// The box has no question left to answer; what was typed is kept.
		m.answering = false
		m.answer.Blur()
	}
	m.renderFlow()
	return m
}

// resumeOwed is whether f is waiting on a planner that is not there, so the
// screen should have its planner claim the plan again: it is being planned,
// or not started, and factory waits on a session. Factory starts a new
// plan's flow in init with a task for a session, so that counts as
// planning; a plan deferred there waits on the developer instead.
func resumeOwed(f Flow) bool {
	switch clean(f.State) {
	case planstate.Init, planstate.Ready, planstate.RefineProposal, planstate.RefineUnits:
	default:
		return false
	}
	return !f.Ended && !f.Claimed && clean(f.WaitingOn) == planstate.Session
}

// resumeIfOwed has the planner claim the plan again when factory waits on a
// planner that is not there, once per flow state. A request that has not
// started a session yet goes in place of the resume, so the planner never
// claims a new plan without it. In init it never resumes a state it has
// seen a session claim, until the state moves.
func (m Model) resumeIfOwed() (Model, tea.Cmd) {
	if !m.created || m.resuming || !resumeOwed(m.flow) || m.resumedIn == m.state ||
		(m.state == planstate.Init && m.claimedIn == m.state) ||
		(m.inbox && m.project == "") {
		return m, nil
	}
	m.resuming, m.resumedIn = true, m.state
	if m.request != "" {
		return m, requestCmd(m.ctx, m.agent, m.request)
	}
	return m, resumeCmd(m.ctx, m.agent)
}

// resumable is whether R resumes the plan, and how, or why not, as
// planstate.ResumeOf decides. Before the plan exists there is nothing to say.
// A screen with no Resumer starts no build: it says where one can be.
func (m Model) resumable() planstate.Resume {
	if !m.created {
		return planstate.Resume{}
	}
	blockers := make([]planstate.Blocker, len(m.family.Blockers))
	for i, b := range m.family.Blockers {
		blockers[i] = planstate.Blocker{Title: clean(b.Title), State: clean(b.State), Outcome: clean(b.Outcome), Active: b.Active}
	}
	r := planstate.ResumeOf(planstate.ResumeFacts{
		State:         clean(m.flow.State),
		Ended:         m.flow.Ended,
		PlanID:        m.store.ID(),
		Holder:        clean(m.flow.Holder),
		WaitingOn:     clean(m.flow.WaitingOn),
		Blockers:      blockers,
		BlockersKnown: m.familyLoaded,
	})
	if r.Kind == planstate.ResumeBuild && m.resumer == nil {
		return planstate.Resume{Why: "Resume this build from chunk watch"}
	}
	return r
}

// resume is the developer's R: it has the planner claim the plan again, as
// resumeIfOwed does, or starts a build session of it.
func (m Model) resume() (Model, tea.Cmd) {
	switch m.resumable().Kind {
	case planstate.ResumePlanner:
		m.resuming, m.resumedIn = true, m.state
		return m, resumeCmd(m.ctx, m.agent)
	case planstate.ResumeBuild:
		m.resumingBuild = true
		m.status = "starting a build session…"
		return m, resumeBuildCmd(m.ctx, m.resumer, m.project, m.store.ID())
	case planstate.ResumeNone:
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	m.clearText()
	k := m.keys()
	if m.askingReason {
		// Before Quit, so esc keeps the plan rather than leaving.
		return m.answerReason(msg, k)
	}
	if m.helping && (key.Matches(msg, k.Help) || msg.String() == "esc") {
		// Before Quit, so esc closes the help rather than leaving.
		return m.toggleHelp()
	}
	if key.Matches(msg, k.Quit) {
		return m.quit(msg.String())
	}
	if m.helping {
		// The arrows and page keys scroll the help; any other key closes it
		// and does what it does, so nothing changes behind it unseen.
		switch msg.String() {
		case "up", "k":
			m.flowView.ScrollUp(1)
			return m, nil
		case "down", "j":
			m.flowView.ScrollDown(1)
			return m, nil
		case "pgup":
			m.flowView.PageUp()
			return m, nil
		case "pgdown":
			m.flowView.PageDown()
			return m, nil
		}
		m.helping = false
		m.renderFlow()
		k = m.keys()
	}
	if m.quitting {
		m.quitting, m.status = false, ""
	}
	if m.confirming {
		m.confirming, m.status = false, ""
		if !key.Matches(msg, k.Confirm) {
			m.pendingDelete, m.pendingDesign, m.pendingStart = "", false, false
			m.pendingDeletePlan, m.pendingStop = false, ""
			return m, nil
		}
		return m.answerYes()
	}
	if m.commenting {
		return m.updateCommentKey(msg)
	}
	if m.answering {
		return m.updateAnswerKey(msg)
	}

	switch {
	case key.Matches(msg, k.Help):
		return m.toggleHelp()
	case key.Matches(msg, k.SendPlan):
		return m.askToApprove(), nil
	case key.Matches(msg, k.Resume):
		return m.resume()
	case key.Matches(msg, k.StopBuild):
		m.confirming, m.pendingStop = true, m.latestBuildID()
		m.status = ui.TitleStyle.Render("Stop this build session? ") + confirmButtons
		return m, nil
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

	if !m.created {
		// The Describe the change box's input has the keyboard.
		if key.Matches(msg, k.Send) {
			return m.send(m.input.Value())
		}
		return m.forward(msg)
	}
	if m.tab == tabFlow {
		return m.updateFlowKey(msg)
	}
	return m.updatePlanKey(msg)
}

// heldStatus says why a send left the queued comments unsent.
const heldStatus = "Comments not sent: factory is working on the plan. Send them once it is waiting on you."

// deliver is a send of the queued comments from the developer: while factory
// waits on the developer or asks for comments it saves them to factory and
// hands them off as factory's flow then shows; at any other time they stay
// queued. Nothing goes to the planning session.
func (m Model) deliver() (Model, tea.Cmd) {
	if m.savingComments {
		return m, nil
	}
	queued := queuedComments(m.comments)
	// A hand-off owed by an earlier send is retried even with nothing queued.
	if len(queued) == 0 && !m.pendingHandOff {
		return m, nil
	}
	return m.saveAndHandOff()
}

// saveAndHandOff saves the queued comments, none or more, and hands them to
// factory, while it takes comments; at any other time they stay queued.
func (m Model) saveAndHandOff() (Model, tea.Cmd) {
	if m.savingComments {
		return m, nil
	}
	queued := queuedComments(m.comments)
	if !m.takesComments() {
		if m.moving == nil {
			m.status = heldStatus
		}
		return m, nil
	}
	m.savingComments = true
	m.status = ""
	return m, saveQueued(m.ctx, m.store, queued)
}

// answerYes does what the question in the status bar asked, now it has been
// answered yes.
func (m Model) answerYes() (Model, tea.Cmd) {
	if id := m.pendingStop; id != "" {
		m.pendingStop, m.stoppingBuild = "", true
		m.status = "Stopping the build session…"
		return m, stopBuildCmd(m.ctx, m.build, id)
	}
	if m.pendingDeletePlan {
		m.pendingDeletePlan, m.deleting = false, true
		m.status = "Deleting the plan…"
		return m, deletePlanCmd(m.ctx, m.store)
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

// showTab shows view t in the body, dropping any selection of the plan.
// Before the plan exists the body stays on the Flow view.
func (m Model) showTab(t rightTab) (Model, tea.Cmd) {
	if !m.created {
		return m, nil
	}
	m.tab = t
	m.anchor = -1
	m.drawPlan()
	m.renderFlow()
	return m, nil
}

// toggleHelp opens the help, which lists the keys in the Flow view's
// viewport, or closes it.
func (m Model) toggleHelp() (Model, tea.Cmd) {
	m.helping = !m.helping
	m.clearText()
	m.renderFlow()
	m.flowView.GotoTop()
	return m, nil
}

// helpContent is the help's body: every key the screen answers to now that
// has a hint, one per row, under a KEYS label.
func (m Model) helpContent() string {
	dim, brand := m.style(m.dim()), m.style(m.brand())
	lines := []string{dim.Render("KEYS"), ""}
	for _, b := range m.keys().ShortHelp() {
		h := b.Help()
		if !b.Enabled() || h.Key == "" {
			continue
		}
		lines = append(lines, brand.Render(fmt.Sprintf("%-12s", h.Key))+" "+dim.Render(h.Desc))
	}
	return strings.Join(lines, "\n")
}

// askToApprove starts approving the plan, or its design: it asks the
// developer to confirm, or says why it cannot go yet.
func (m Model) askToApprove() Model {
	queued := len(queuedComments(m.comments))
	switch {
	case m.built, m.approving, m.approvingDesign, m.startingWork, m.abandoning, m.deleting, m.moving != nil:
	case m.busy:
		m.status = warn("Wait for chunk to finish.")
	case slices.Contains(m.events, prioritize):
		m.confirming, m.pendingStart = true, true
		m.status = ui.TitleStyle.Render("Start work on the plan? ") + confirmButtons
	case strings.TrimSpace(m.plan) == "":
		m.status = warn("There is no plan to approve yet.")
	case !ReadyToApprove(m.flow) && m.inReview():
		m.status = warn("The plan is not ready yet: wait for its review to pass.")
	case !ReadyToApprove(m.flow) && questionsOpen(m.flow):
		m.status = warn("Answer 3. Open Questions in comments first; factory offers no approval yet.")
	case !ReadyToApprove(m.flow):
		m.status = warn("The plan cannot be approved now: " + m.notReadyWhy() + ".")
	case approval(m.flow) == planstate.ApproveForUnits && (queued > 0 || m.openComments() > 0):
		m.status = warn("Send or resolve your comments first; then ctrl+s approves the design.")
	case approval(m.flow) == planstate.ApproveForUnits:
		m.confirming, m.pendingDesign = true, true
		m.status = ui.TitleStyle.Render("Approve the design, so chunk writes the units? ") + confirmButtons
	// Only a screen that closes on approval loses its queued comments: one
	// that stays open keeps them for the next time factory takes comments.
	case queued > 0 && !m.back:
		m.confirming = true
		m.status = ui.TitleStyle.Render(fmt.Sprintf("Your %d unsent %s will be lost when this screen closes. Approve this plan and build it? ",
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
	if !m.created && !m.creating && strings.TrimSpace(m.input.Value()) != "" {
		parts = append(parts, "unsent request")
	}
	return strings.Join(parts, " and ")
}

func warn(s string) string { return ui.WarningStyle.Render(s) }

// forward hands msg to the component that has the keyboard: the comment
// editor, the request input before the plan exists, or the view shown.
func (m Model) forward(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	switch {
	case m.commenting:
		if m.savingEdit {
			return m, nil
		}
		m.commentInput, cmd = m.commentInput.Update(msg)
		m.moveCursor(m.cursor)
	case !m.created:
		if m.creating {
			return m, nil
		}
		m.input, cmd = m.input.Update(msg)
		m.renderFlow()
	case m.showsFlow():
		m.flowView, cmd = m.flowView.Update(msg)
	default:
		m.planView, cmd = m.planView.Update(msg)
	}
	return m, cmd
}

// send is a new plan's request: it creates the plan in factory, then starts
// the planning session on text once it is made. Once the plan exists, or
// while it is being created, it does nothing, as it does for an empty text.
// The request stays in the input until the plan is made, so a failure to
// make it gives it back.
func (m Model) send(text string) (Model, tea.Cmd) {
	text = strings.TrimSpace(text)
	if m.created || m.creating || text == "" {
		return m, nil
	}
	m.status = ""
	var tick tea.Cmd
	if !m.busy {
		m.busy, m.byTask, m.task = true, false, ""
		tick = m.spin.Tick
	}
	m.creating = true
	m.input.Blur()
	m.renderPlan()
	m.renderFlow()
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
			m.busy, m.byTask = true, true
			// A task for this screen's planner means factory has acted.
			m.moving = nil
			tick = m.spin.Tick
			// The flow names the task; read it now rather than at the next poll.
			refresh = refreshCmd(m.ctx, m.store)
		}
		// The turn is under way, so the last failure is behind it.
		m.failure = ""
		m.activity = ev.Activity
		m.renderPlan()
		m.renderFlow()
		return m, tea.Batch(next, tick, refresh)
	case ev.Reply != nil:
		// The reply is chunk's own, not the flow's: the running box showed
		// what the agent was doing, and the refresh shows what came of it.
		m.busy, m.byTask, m.task, m.activity = false, false, "", ""
	case ev.Problem != nil:
		m.busy, m.byTask, m.task, m.activity = false, false, "", ""
		m.fail("chunk hit a problem: " + ev.Problem.Error())
	}
	m.renderPlan()
	m.renderFlow()
	return m, tea.Batch(next, refreshCmd(m.ctx, m.store))
}

// applyApproval is the end of approving the plan's design, or the plan, of
// sending prioritize or abandon, or of deleting the plan.
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
		m.built, m.tab = true, tabFlow
		m.moving = &transition{kind: movingApprove, from: m.state, since: time.Now()}
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

// startWorkCmd has factory start work on the plan nobody has started.
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

// Layout: the frame's top line and its rule, then one full-width view, the
// Flow view or the Plan view (the strip, the plan, and its row for comments),
// then the footer's rule and its row of hints.
const (
	headerRows = 1
	// ruleRows is the rule under the frame's top line.
	ruleRows   = 1
	footerRows = 2
	inputRows  = 6
	// flowMargin is the cells left of the Flow view.
	flowMargin     = 1
	planFooterRows = 1
)

// topRows is what sits above the view: the top line and its rule, and the
// strip when the Plan view shows.
func (m *Model) topRows() int {
	if !m.showsFlow() {
		return headerRows + ruleRows + stripRows
	}
	return headerRows + ruleRows
}

// showsFlow is whether the body draws the Flow view's viewport: on the Flow
// view, and on either view while the help is shown, which is drawn there.
func (m *Model) showsFlow() bool {
	return m.tab == tabFlow || m.helping
}

// bodyRows is the rows between the top line's rule and the footer.
func (m *Model) bodyRows() int {
	return max(m.height-headerRows-ruleRows-footerRows, 1)
}

func (m *Model) layout() {
	w := max(m.width-flowMargin, 1)
	m.planView.SetWidth(w)
	m.planView.SetHeight(max(m.bodyRows()-stripRows-planFooterRows, 1))
	m.flowView.SetWidth(w)
	m.flowView.SetHeight(m.bodyRows())
	// The editor is the text of a comment's card, which sits past the gutter
	// on a rail, indented, with a cell either side.
	m.commentInput.SetWidth(m.planView.Width() - gutterWidth - 6)
	// The request is written inside the Describe the change box's border.
	m.input.SetWidth(max(w-4, 1))
	m.answer.SetWidth(max(w-4, 1))
	m.renderPlan()
	m.renderFlow()
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
		m.questions = 0
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
	r, err := chat.NewRenderer(m.dark, w-1)
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
	steps := unitSteps(blocks, m.units)
	var st planStats
	from := 0
	for i, b := range blocks {
		st.step = steps[i]
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
	m.questions = st.questions
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

// activityLine is the running box's first line while the agent works: what
// it is doing now, with the spinner.
func (m Model) activityLine() string {
	a := m.activity
	if a == "" {
		a = "chunk is thinking…"
	}
	// The spinner's frames carry their own space after the dot.
	return m.style(m.purple()).Render(m.spin.View() + a)
}

// taskLabel is a factory task's category as the screen shows it: in
// sentence case, with spaces for hyphens, so "early-plan" reads "Early plan".
func taskLabel(category string) string {
	if category == "" {
		return ""
	}
	s := strings.ReplaceAll(category, "-", " ")
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// restyle renders the plan and the Flow view again for a change of
// background.
func (m *Model) restyle() {
	m.renderedWidth = 0
	m.renderPlan()
	m.renderFlow()
}

// Content is the screen as its host draws it: the top line and its rule, the
// view shown, and the footer, filling the size it was last sent.
func (m Model) Content() string {
	if m.width == 0 {
		return ""
	}
	var body string
	if m.showsFlow() {
		body = lipgloss.NewStyle().PaddingLeft(flowMargin).Render(m.shown(textFlow))
	} else {
		body = m.strip() + "\n" + m.shown(textPlan) + "\n" + m.planFooter()
	}
	rows := m.bodyRows()
	body = lipgloss.NewStyle().Height(rows).MaxHeight(rows).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), m.topRule(), body, m.actionBar())
}
