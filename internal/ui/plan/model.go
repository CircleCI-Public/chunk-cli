// Package plan is the planning screen: a new plan's title and request, then
// the plan's Overview, with the stages of its flow and what happened in each,
// beside the plan factory is writing, rendered as markdown. The developer can
// comment on any part of the plan; comments are queued locally and saved to
// factory when the developer sends. When the developer approves the plan, the
// worker that claims it builds it.
//
// The screen is a component: its host passes it messages and draws what
// Content returns, and it reports that it is done with a ClosedMsg. Screen
// hosts it as a program of its own.
package plan

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
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

// pane is which side of the screen the keyboard goes to.
type pane int

const (
	paneOverview pane = iota
	panePlan
)

// message is one entry in a stage's log: what the developer asked for, or
// why chunk could not do something.
type message struct {
	request bool
	text    string
	failed  bool
	// stage is the stage stageNow gave when the message was added: Created
	// before factory has given the plan a state, Building once the screen has
	// approved or started a build of the plan, and "" for a state the stages
	// do not know.
	stage string
	// seq is the message's place among the log's entries, as addMessage
	// numbers them.
	seq int
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
	// approvedMsg is the end of sending approve-for-building.
	approvedMsg struct{ err error }
	// claimedMsg is the end of having the default worker claim the plan:
	// host is the worker's hostname.
	claimedMsg struct {
		host string
		err  error
	}
	// releasedMsg is the end of freeing the plan's claim.
	releasedMsg struct{ err error }
	// designApprovedMsg is the end of approving the plan's design.
	designApprovedMsg struct{ err error }
	// startedMsg is the end of sending prioritize.
	startedMsg struct{ err error }
	// endedMsg is the end of sending end.
	endedMsg struct{ err error }
	// planDeletedMsg is the end of deleting the plan.
	planDeletedMsg struct{ err error }
	pollMsg        struct{}
	refreshedMsg   struct {
		flow      Flow
		text      string
		structure Structure
		comments  []Comment
		err       error
	}
	commentSavedMsg struct{ err error }
	// factoriesMsg is the end of listing the factories the new plan can be
	// created in.
	factoriesMsg struct {
		factories []Factory
		err       error
	}
	// createdMsg is the end of creating the plan in factory, with the
	// developer's title and request.
	createdMsg struct {
		title, request string
		err            error
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

// refreshCmd reads the plan's flow, text, structure and comments from the
// store. It fails as a whole when any read fails, so the text and its units
// and open questions always arrive together.
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
		structure, err := s.Structure(ctx)
		if err != nil {
			return refreshedMsg{err: err}
		}
		cs, err := s.Comments(ctx)
		return refreshedMsg{flow: flow, text: text, structure: structure, comments: cs, err: err}
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

// ClosedMsg is the screen asking its host to close it: Approved when the
// developer approved the plan, with Worker the hostname of the worker building
// it, "" when none is.
type ClosedMsg struct {
	Approved bool
	Worker   string
}

// BuildingMsg is the screen telling its host that the plan was approved and
// Worker, "" when none, is building it, while the screen stays open to follow
// it. A host that does not keep the screen gets ClosedMsg instead.
type BuildingMsg struct{ Worker string }

func closed(approved bool, worker string) tea.Cmd {
	return func() tea.Msg { return ClosedMsg{Approved: approved, Worker: worker} }
}

// Model is the planning screen.
type Model struct {
	ctx   context.Context
	store Store

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
	// structure is the plan's sections and units, read with its text by the
	// last good refresh.
	structure Structure
	// familyLoaded is whether a read of the relatives has succeeded, so the
	// plan's blockers are known.
	familyLoaded bool
	messages     []message
	// seq counts the entries added to the stages' logs, so a log can be drawn
	// in the order they happened.
	seq int
	// created is whether the plan has been made in factory with store.Create,
	// and creating whether that is under way.
	created, creating bool
	// claiming is whether a claim of the plan is under way, from the plan's
	// creation or the developer's R; workerName is the hostname of the worker
	// the last claim got, "" before one has.
	claiming   bool
	workerName string
	// releasing is whether the release of the plan's claim is under way.
	releasing bool
	// keepStatus is whether the next refresh leaves the status bar alone even
	// as the flow's state moves: a claim just failed, and the first read of
	// the plan after it is where it moves from nothing to its first state.
	keepStatus bool

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
	// overviewView is the left pane: the title and request before the plan
	// exists, the Overview after.
	overviewView viewport.Model
	// titleInput is the new plan's title, one line above the request input.
	// It is not title: that is the host's title for the plan.
	titleInput textinput.Model
	input      textarea.Model
	spin       spinner.Model
	// field is the box of the new plan's form that has the keyboard, whichever
	// pane has it.
	field field
	// factories are the factories the form offers after "Org default", sorted
	// by name; nil until they are listed, and when listing them fails.
	factories []Factory
	// factory is the chosen entry: 0 for "Org default", else factories[factory-1].
	factory         int
	factoriesFailed bool

	focus pane
	// focusChosen is whether the first refresh of a plan opened as it is, or
	// the developer, has picked the pane the keyboard is in.
	focusChosen bool
	// overviewCursor is the Overview's row under the cursor, an index into
	// overviewRows; expanded holds the rows opened with enter, by "outcome"
	// or by stage. It is made on the first write.
	overviewCursor int
	expanded       map[string]bool
	// rowLines is the line each of the Overview's rows starts on, as it was
	// last drawn, for keeping the cursor in view.
	rowLines []int
	// outcomeFolds is whether the Outcome, as last drawn, has more lines than
	// its cut shows, so enter has something to open.
	outcomeFolds bool
	// outcome keeps the Outcome's rendered markdown between draws.
	outcome *outcomeRender
	width   int
	height  int

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
	// ending; ending is set from the developer's reason until factory
	// has the end event.
	askingReason, ending bool
	// pendingDeletePlan is whether the y being asked for deletes the plan;
	// deleting is set from that y until factory answers.
	pendingDeletePlan, deleting bool
	// approving is set from the developer's y until factory answers, so a
	// second ctrl+s cannot approve the plan twice.
	approving bool
	// pendingStop is whether the y being asked for releases the plan's claim.
	pendingStop bool
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
	// branch is the git branch the plan is for, shown in the header.
	branch string
	// questions is how many open questions the plan has.
	questions int
	// Approved is whether the developer has approved the plan, and Worker the
	// hostname of the live worker that holds it then, "" when none does.
	Approved bool
	Worker   string
	// built is whether the plan was approved while the screen stays open: its
	// build is followed before factory's state moves.
	built bool

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

// titlePlaceholder and requestPlaceholder are the new plan's two inputs'.
const (
	titlePlaceholder   = "Title of the plan…"
	requestPlaceholder = "Describe the change you want planned…"
)

// New returns the planning screen. A new plan asks for its title, then its
// request; a non-empty request fills the request input, and the screen still
// asks for the title. Once the plan exists the left pane, the Overview, takes
// no typing, so a plan that exists opens on the plan pane until its first
// refresh says where the keyboard belongs.
func New(ctx context.Context, store Store, request string) Model {
	ti := textinput.New()
	ti.Placeholder = titlePlaceholder
	ti.Prompt = ""
	ti.Focus()
	tiStyles := ti.Styles()
	tiStyles.Focused.Text = lipgloss.NewStyle()
	tiStyles.Blurred.Text = lipgloss.NewStyle()
	ti.SetStyles(tiStyles)

	in := textarea.New()
	in.Placeholder = requestPlaceholder
	in.ShowLineNumbers = false
	in.Prompt = ""
	// Enter sends; a new line is shift+enter or ctrl+j.
	in.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+j")
	in.SetHeight(inputRows)
	in.SetValue(strings.TrimSpace(request))
	in.Blur()
	inStyles := in.Styles()
	inStyles.Focused.CursorLine = lipgloss.NewStyle()
	in.SetStyles(inStyles)

	ci := textarea.New()
	ci.ShowLineNumbers = false
	ci.Prompt = ""
	ci.Placeholder = "Comment on this part"
	ci.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+j")
	ci.DynamicHeight = true
	// MaxContentHeight bounds the comment itself, at the textarea's own
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
	// The Overview's keys are the screen's own too: its ↑/↓ move its cursor
	// rather than scroll. The request, before the plan exists, has the page
	// keys of its own.
	overviewView := viewport.New()
	overviewView.KeyMap = viewport.KeyMap{}

	created := store.ID() != ""
	focus := paneOverview
	if created {
		focus = panePlan
		ti.Blur()
	}
	return Model{
		ctx:     ctx,
		store:   store,
		created: created,
		focus:   focus,
		// A new plan's keyboard is where the developer typed the request; only
		// a plan that exists is opened where its phase wants the keyboard.
		focusChosen:  !created,
		copyText:     clipboard.Copy,
		planView:     planView,
		overviewView: overviewView,
		outcome:      &outcomeRender{},
		titleInput:   ti,
		field:        fieldTitle,
		input:        in,
		commentInput: ci,
		spin:         spinner.New(spinner.WithSpinner(spinner.Dot)),
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

// WithInbox names the plan and returns to the host's inbox.
func (m Model) WithInbox(title string) Model {
	m.back, m.inbox, m.title = true, true, title
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
	cmds := []tea.Cmd{textarea.Blink, m.Poll(), m.pollCmd()}
	if !m.created {
		cmds = append(cmds, factoriesCmd(m.ctx, m.store))
	}
	return tea.Batch(cmds...)
}

// factoriesCmd lists the factories the new plan can be created in.
func factoriesCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg {
		fs, err := s.Factories(ctx)
		return factoriesMsg{factories: fs, err: err}
	}
}

// field is a box of the new plan's form, in the order the keyboard moves
// through them.
type field int

const (
	fieldTitle field = iota
	// fieldFactory has no text input: it chooses the factory the plan is
	// created in.
	fieldFactory
	fieldRequest
)

// orgDefault is the Factory box's first entry, which names no factory.
const orgDefault = "Org default"

// applyFactories handles the end of listing the factories: the list, sorted by
// name, or the note that it could not be listed. Either way the choice is
// Org default.
func (m Model) applyFactories(msg factoriesMsg) Model {
	m.factory, m.factories = 0, nil
	m.factoriesFailed = msg.err != nil
	if msg.err == nil {
		m.factories = sortedFactories(msg.factories)
	}
	if m.height > 0 {
		m.layout()
	}
	return m
}

// sortedFactories is fs by name, ignoring case, then by org and ID so that the
// order does not depend on the one factory served them in.
func sortedFactories(fs []Factory) []Factory {
	out := slices.Clone(fs)
	slices.SortFunc(out, func(a, b Factory) int {
		return cmp.Or(
			cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.OrgID, b.OrgID),
			cmp.Compare(a.ID, b.ID),
		)
	})
	return out
}

// factoryLabels are the entries the Factory box shows, Org default first and
// then each of m.factories in order. A name that more than one factory has,
// in different orgs, carries the start of its org's ID, so that the one shown
// is the one sent.
func (m Model) factoryLabels() []string {
	count := map[string]int{}
	for _, f := range m.factories {
		count[f.Name]++
	}
	labels := make([]string, 1, 1+len(m.factories))
	labels[0] = orgDefault
	for _, f := range m.factories {
		label := f.Name
		if count[f.Name] > 1 {
			label += " (" + f.OrgID[:min(8, len(f.OrgID))] + ")"
		}
		labels = append(labels, label)
	}
	return labels
}

// chosenFactory is the ID of the chosen entry, "" for Org default.
func (m Model) chosenFactory() string {
	if m.factory == 0 {
		return ""
	}
	return m.factories[m.factory-1].ID
}

// Poll reads the plan's state, text, comments and relatives from the store,
// as the screen's own timer does every few seconds. A host can use it to
// refresh the screen deterministically, without waiting for that timer.
func (m Model) Poll() tea.Cmd {
	return tea.Batch(refreshCmd(m.ctx, m.store), familyCmd(m.ctx, m.store))
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
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

	case pollMsg:
		return m, tea.Batch(m.Poll(), m.pollCmd())

	case refreshedMsg:
		return m.applyRefresh(msg), nil

	case claimedMsg:
		return m.applyClaimed(msg)

	case releasedMsg:
		return m.applyReleased(msg)

	case familiedMsg:
		row, had := m.cursorRow()
		m.family, m.familyErr = msg.family, msg.err
		if msg.err != nil {
			m.family = Family{}
		} else {
			m.familyLoaded = true
		}
		// The cursor stays on a row that is still there.
		m.restoreCursor(row, had)
		m.renderLeft()
		return m, nil

	case factoriesMsg:
		return m.applyFactories(msg), nil

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

	case designApprovedMsg, approvedMsg, startedMsg, endedMsg, planDeletedMsg:
		return m.applyApproval(msg)

	case spinner.TickMsg:
		if !m.spinning() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.renderLeft()
		return m, cmd
	}

	return m.forward(msg)
}

// addMessage adds msg to the log, in the stage the flow is in now.
func (m *Model) addMessage(msg message) {
	m.seq++
	msg.stage, msg.seq = m.stageNow(), m.seq
	m.messages = append(m.messages, msg)
}

// applyCreated handles the end of creating the plan in factory: on success it
// has the default worker claim the plan, and on failure it says so in the
// request pane and keeps what the developer typed.
func (m Model) applyCreated(msg createdMsg) (Model, tea.Cmd) {
	m.creating = false
	if msg.err != nil {
		m.addMessage(message{text: "Could not create the plan: " + msg.err.Error(), failed: true})
		m.renderPlan()
		m.renderLeft()
		return m, nil
	}
	// The inputs are gone for good, and the Overview takes their rows.
	m.created, m.claiming = true, true
	m.addMessage(message{request: true, text: msg.request})
	m.input.Reset()
	m.titleInput.Reset()
	m.input.Blur()
	m.titleInput.Blur()
	// A title the host gave the screen becomes the plan's own once it is
	// created; a screen with no title keeps showing the branch.
	if m.title != "" && msg.title != "" {
		m.title = msg.title
	}
	m.layout()
	return m, tea.Batch(claimCmd(m.ctx, m.store), m.spin.Tick)
}

// noWorkerText is what the screen says when no live worker can claim the plan.
const noWorkerText = "No live worker can claim this plan · start factory daemon, then press R"

// applyClaimed handles the end of having the default worker claim the plan,
// after the plan is created or on the developer's R. A refusal leaves the plan
// as it was, unclaimed, with the reason in the status bar; R claims it again.
// Either way the screen goes on to follow the flow.
func (m Model) applyClaimed(msg claimedMsg) (Model, tea.Cmd) {
	m.claiming = false
	switch {
	case errors.Is(msg.err, ErrNoWorker):
		m.status, m.keepStatus = ui.ErrorStyle.Render(noWorkerText), true
	case msg.err != nil:
		m.status, m.keepStatus = ui.ErrorStyle.Render("Could not claim the plan: "+msg.err.Error()), true
	default:
		m.workerName, m.status = msg.host, ""
	}
	m.renderLeft()
	return m, m.Poll()
}

// applyReleased handles the end of freeing the plan's claim: an error in the
// status bar, with the holder shown as it was, or a refresh that shows the
// freed claim.
func (m Model) applyReleased(msg releasedMsg) (Model, tea.Cmd) {
	m.releasing = false
	if msg.err != nil {
		m.status = ui.ErrorStyle.Render("Could not stop the plan: " + msg.err.Error())
		return m, nil
	}
	m.status, m.workerName = "", ""
	return m, m.Poll()
}

// applyQueueSaved handles the end of saving the queued comments.
func (m Model) applyQueueSaved(msg queueSavedMsg) (Model, tea.Cmd) {
	m.savingComments = false
	// Whatever got saved before the failure, if any, stays saved; only
	// the rest is still queued.
	m.comments = dropQueued(m.comments, msg.saved)
	m.renderPlan()
	m.renderLeft()
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

// spinning is whether something the spinner shows is under way: the creation
// or claim of the plan, an approval or event not yet answered, or the wait for
// factory.
func (m Model) spinning() bool {
	return m.creating || m.claiming || m.approving || m.approvingDesign || m.moving != nil
}

// awaitingStatus is whether the status bar holds something still under way
// or waiting on the developer: a question to answer, the quit prompt, or an
// approval or save factory has not answered yet.
func (m Model) awaitingStatus() bool {
	return m.confirming || m.quitting || m.approving || m.approvingDesign || m.savingComments ||
		m.askingReason || m.ending || m.deleting
}

// applyRefresh is a successful or failed poll of the plan's state, text and
// comments.
func (m Model) applyRefresh(msg refreshedMsg) Model {
	if msg.err != nil {
		m.refreshErr = "could not refresh the plan: " + msg.err.Error()
		return m
	}
	m.refreshErr = ""
	row, had := m.cursorRow()

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

	if msg.flow.State != m.state && !m.awaitingStatus() && !m.keepStatus {
		// A note on what happened is out of date once factory moves the
		// plan, and left up it would hide the bar's buttons.
		m.status = ""
	}
	m.keepStatus = false
	m.state, m.events, m.plan = msg.flow.State, msg.flow.Events, msg.text
	if !reflect.DeepEqual(m.structure, msg.structure) {
		// The pane is drawn from the structure, so it is drawn again even
		// when the text is unchanged.
		m.structure = msg.structure
		m.renderedWidth = 0
	}
	m.questions = len(msg.structure.OpenQuestions)
	m.flow = msg.flow
	m.restoreCursor(row, had)
	// The first refresh of a plan opened as it is puts the keyboard where the
	// plan's phase is about; later moves leave it where it is.
	first := !m.focusChosen
	if first {
		m.focus, m.focusChosen = focusFor(m.state), true
		m.overviewCursor = m.currentRow()
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
	m.renderLeft()
	if first {
		m.scrollToCursor()
	}
	return m
}

// resumable is whether R has the default worker claim the plan, or why not,
// as planstate.ResumeOf decides. Before the plan exists there is nothing to
// say.
func (m Model) resumable() planstate.Resume {
	if !m.created {
		return planstate.Resume{}
	}
	blockers := make([]planstate.Blocker, len(m.family.Blockers))
	for i, b := range m.family.Blockers {
		blockers[i] = planstate.Blocker{Title: clean(b.Title), State: clean(b.State), Outcome: clean(b.Outcome), Active: b.Active}
	}
	return planstate.ResumeOf(planstate.ResumeFacts{
		State:         clean(m.flow.State),
		Ended:         m.flow.Ended,
		PlanID:        m.store.ID(),
		Holder:        clean(m.flow.Holder),
		Worker:        clean(workerID(m.flow.Worker)),
		WorkerLine:    WorkerLine(m.flow.Worker, m.flow.Holder),
		WaitingOn:     clean(m.flow.WaitingOn),
		Blockers:      blockers,
		BlockersKnown: m.familyLoaded,
	})
}

// workerID is w's ID, "" for no worker.
func workerID(w *Worker) string {
	if w == nil {
		return ""
	}
	return w.ID
}

// resume is the developer's R: it has the default worker claim the plan.
func (m Model) resume() (Model, tea.Cmd) {
	if m.resumable().Kind != planstate.ResumeClaim {
		return m, nil
	}
	m.claiming = true
	m.status = "Claiming the plan for a worker…"
	return m, tea.Batch(claimCmd(m.ctx, m.store), m.spin.Tick)
}

// claimCmd has the default worker claim the plan.
func claimCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg {
		host, err := s.Claim(ctx)
		return claimedMsg{host: host, err: err}
	}
}

// releaseCmd frees the plan's claim.
func releaseCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg { return releasedMsg{err: s.Release(ctx)} }
}

// stopOwed is whether S can release the plan's claim: a worker holds a plan
// that has not ended, and no interactive session does.
func (m Model) stopOwed() bool {
	return m.created && m.flow.Worker != nil && !m.flow.Ended && clean(m.flow.Holder) == ""
}

// focusFor is the pane the keyboard opens in for a plan in state: the plan
// while it is being planned, or has not started, and the Overview after.
func focusFor(state string) pane {
	switch strings.TrimSpace(state) {
	case "", planstate.Init, planstate.Ready, planstate.RefineProposal, planstate.RefineUnits:
		return panePlan
	}
	return paneOverview
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
			m.pendingDelete, m.pendingDesign, m.pendingStart = "", false, false
			m.pendingDeletePlan, m.pendingStop = false, false
			return m, nil
		}
		return m.answerYes()
	}
	if m.commenting {
		return m.updateCommentKey(msg)
	}

	if next, cmd, ok := m.updateFieldKey(msg, k); ok {
		return next, cmd
	}

	switch {
	case key.Matches(msg, k.Focus):
		// A narrow screen shows one of the Overview and the plan at a time,
		// so tab goes between them on any width.
		if m.focus == paneOverview {
			return m.focusOn(panePlan)
		}
		return m.focusOn(paneOverview)
	case key.Matches(msg, k.SendPlan):
		return m.askToApprove(), nil
	case key.Matches(msg, k.Resume):
		return m.resume()
	case key.Matches(msg, k.StopBuild):
		m.confirming, m.pendingStop = true, true
		m.status = ui.TitleStyle.Render("Stop this plan? The worker lets go of it. ") + confirmButtons
		return m, nil
	case key.Matches(msg, k.End):
		m.askingReason = true
		m.status = ui.TitleStyle.Render("End plan as: ") + "m complete · c canceled · w won't do · d duplicate · esc keep it"
		return m, nil
	case key.Matches(msg, k.DeletePlan):
		m.confirming, m.pendingDeletePlan = true, true
		m.status = ui.TitleStyle.Render("Delete this plan, its comments and its flow? ") + confirmButtons
		return m, nil
	}

	if m.focus == paneOverview && m.created {
		return m.updateOverviewKey(msg)
	}
	if m.focus == panePlan {
		return m.updatePlanKey(msg)
	}
	switch {
	case key.Matches(msg, k.RequestPageUp):
		m.overviewView.PageUp()
		return m, nil
	case key.Matches(msg, k.RequestPageDown):
		m.overviewView.PageDown()
		return m, nil
	}
	if key.Matches(msg, k.Send) {
		return m.send(m.titleInput.Value(), m.input.Value())
	}
	return m.forward(msg)
}

// updateFieldKey moves the new plan's keyboard between the boxes of its form
// by tab, shift+tab and enter. ok is false for any other key.
func (m Model) updateFieldKey(msg tea.KeyPressMsg, k keyMap) (Model, tea.Cmd, bool) {
	if m.created || m.focus != paneOverview {
		return m, nil, false
	}
	switch {
	case m.field != fieldRequest && (key.Matches(msg, k.NextField) || key.Matches(msg, k.Send)):
		return m, m.focusField(m.field + 1), true
	case m.field != fieldTitle && key.Matches(msg, k.PrevField):
		return m, m.focusField(m.field - 1), true
	case m.field == fieldFactory && key.Matches(msg, k.Up):
		m.factory = max(m.factory-1, 0)
		return m, nil, true
	case m.field == fieldFactory && key.Matches(msg, k.Down):
		m.factory = min(m.factory+1, len(m.factories))
		return m, nil, true
	}
	return m, nil, false
}

// focusField gives the new plan's keyboard to box f: its text input, or none
// for Factory. The Factory box opens and closes with it, so the log above the
// boxes is laid out again.
func (m *Model) focusField(f field) tea.Cmd {
	m.field = f
	m.titleInput.Blur()
	m.input.Blur()
	if m.height > 0 {
		m.layout()
	}
	switch f {
	case fieldFactory:
		return nil
	case fieldTitle:
		return m.titleInput.Focus()
	case fieldRequest:
		return m.input.Focus()
	}
	return nil
}

// heldStatus says why a send left the queued comments unsent.
const heldStatus = "Comments not sent: factory is working on the plan. Send them once it is waiting on you."

// deliver is a send of the queued comments from the developer: while factory
// waits on the developer or asks for comments it saves them to factory and
// hands them off as factory's flow then shows; at any other time they stay
// queued.
func (m Model) deliver() (Model, tea.Cmd) {
	if m.savingComments {
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
	m.renderLeft()
	return m, saveQueued(m.ctx, m.store, queued)
}

// answerYes does what the question in the status bar asked, now it has been
// answered yes.
func (m Model) answerYes() (Model, tea.Cmd) {
	if m.pendingStop {
		m.pendingStop, m.releasing = false, true
		m.status = "Stopping the plan…"
		return m, releaseCmd(m.ctx, m.store)
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
	return m, tea.Batch(approveCmd(m.ctx, m.store), m.spin.Tick)
}

// answerReason answers the end prompt: a reason key sends the end event
// for that reason, and any other key keeps the plan.
func (m Model) answerReason(msg tea.KeyPressMsg, k keyMap) (Model, tea.Cmd) {
	m.askingReason, m.status = false, ""
	var reason string
	switch {
	case key.Matches(msg, k.EndComplete):
		reason = "completed"
	case key.Matches(msg, k.EndCanceled):
		reason = "canceled"
	case key.Matches(msg, k.EndWontDo):
		reason = "wont-do"
	case key.Matches(msg, k.EndDuplicate):
		reason = "duplicate"
	default:
		return m, nil
	}
	m.ending = true
	m.status = "Ending: " + reasonText(reason) + "…"
	return m, endCmd(m.ctx, m.store, reason)
}

// focusOn gives the keyboard to pane p, dropping any selection.
func (m Model) focusOn(p pane) (Model, tea.Cmd) {
	m.anchor = -1
	if p == panePlan {
		m.focus = panePlan
		m.input.Blur()
		m.titleInput.Blur()
		m.drawPlan()
		m.renderLeft()
		return m, nil
	}
	m.focus = paneOverview
	m.drawPlan()
	m.renderLeft()
	if m.created {
		return m, nil
	}
	return m, m.focusField(m.field)
}

// askToApprove starts approving the plan, or its design: it asks the
// developer to confirm, or says why it cannot go yet.
func (m Model) askToApprove() Model {
	queued := len(queuedComments(m.comments))
	switch {
	case m.built, m.approving, m.approvingDesign, m.startingWork, m.ending, m.deleting, m.moving != nil:
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
		return m, closed(m.Approved, m.Worker)
	}
	if unsent := m.unsent(); unsent != "" && !m.quitting {
		m.quitting, m.confirming = true, false
		m.status = warn(fmt.Sprintf("Your %s will be lost. Press %s again to quit.", unsent, pressed))
		return m, nil
	}
	return m, closed(m.Approved, m.Worker)
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
	if !m.created && (strings.TrimSpace(m.input.Value()) != "" || strings.TrimSpace(m.titleInput.Value()) != "") {
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
	if m.focus == panePlan {
		m.planView, cmd = m.planView.Update(msg)
		return m, cmd
	}
	var cmds []tea.Cmd
	if !m.created {
		switch m.field {
		case fieldFactory:
		case fieldTitle:
			m.titleInput, cmd = m.titleInput.Update(msg)
			cmds = append(cmds, cmd)
		case fieldRequest:
			m.input, cmd = m.input.Update(msg)
			cmds = append(cmds, cmd)
		}
	}
	// The left pane scrolls with the mouse wheel while typing.
	if _, ok := msg.(tea.KeyPressMsg); !ok {
		m.overviewView, cmd = m.overviewView.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// sendEmpty is what the screen says when the new plan's title or request is
// empty, by which it is.
func sendEmpty(title, request string) string {
	switch {
	case title == "" && request == "":
		return "Add a title and a request."
	case title == "":
		return "Add a title."
	case request == "":
		return "Add a request."
	}
	return ""
}

// send is a new plan's title and request: it creates the plan, in the factory
// chosen in the Factory box, and has a worker claim it once it is made. Once
// the plan exists, or while it is being created, it does nothing, and with an
// empty title or request it says which is missing and sends nothing.
func (m Model) send(title, request string) (Model, tea.Cmd) {
	if m.created || m.creating {
		return m, nil
	}
	title, request = strings.TrimSpace(title), strings.TrimSpace(request)
	if why := sendEmpty(title, request); why != "" {
		m.status = why
		return m, nil
	}
	m.status = ""
	m.creating = true
	m.renderPlan()
	m.renderLeft()
	return m, tea.Batch(createCmd(m.ctx, m.store, title, request, m.chosenFactory()), m.spin.Tick)
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

// createCmd creates the plan in factory, titled title, with request as its
// Outcome.
func createCmd(ctx context.Context, s Store, title, request, factoryID string) tea.Cmd {
	return func() tea.Msg {
		return createdMsg{title: title, request: request, err: s.Create(ctx, title, request, factoryID)}
	}
}

// applyApproval is the end of approving the plan's design, or the plan, of
// sending prioritize or end, or of deleting the plan.
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
	case endedMsg:
		m.ending = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not end the plan: " + msg.err.Error())
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
		return m, closed(false, "")
	case approvedMsg:
		m.approving = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not approve the plan: " + msg.err.Error())
			return m, nil
		}
		m.Approved = true
		m.Worker = liveHostname(m.flow.Worker)
		// Counted once factory has the plan.
		m.track("plan_approved", nil)
		if !m.back {
			return m, closed(true, m.Worker)
		}
		// The keyboard goes to the Overview, to follow the build.
		m.built, m.focusChosen = true, true
		m, _ = m.focusOn(paneOverview)
		m.overviewCursor = m.currentRow()
		m.moving = &transition{kind: movingApprove, from: m.state, since: time.Now()}
		m.status = "Approved"
		if m.Worker != "" {
			m.status += " · " + m.Worker + " is building it"
		}
		worker := m.Worker
		return m, tea.Batch(
			func() tea.Msg { return BuildingMsg{Worker: worker} },
			refreshCmd(m.ctx, m.store),
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

// endCmd has factory end the plan without its PR merging, for reason.
func endCmd(ctx context.Context, s Store, reason string) tea.Cmd {
	return func() tea.Msg { return endedMsg{err: s.End(ctx, reason)} }
}

// deletePlanCmd deletes the plan in factory.
func deletePlanCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg { return planDeletedMsg{err: s.Delete(ctx)} }
}

// reasonText is how the screen names an end reason.
func reasonText(reason string) string {
	switch reason {
	case "completed":
		return "complete"
	case "wont-do":
		return "won't do"
	}
	return reason
}

// approveCmd has factory take the approval: approve-for-building, sent when
// factory waits for it.
func approveCmd(ctx context.Context, s Store) tea.Cmd {
	return func() tea.Msg { return approvedMsg{err: s.Approve(ctx)} }
}

// liveHostname is the hostname of w when it is a live worker, "" otherwise: a
// worker that is not live builds nothing.
func liveHostname(w *Worker) string {
	if w == nil || !w.Live || w.Unread != "" {
		return ""
	}
	return clean(w.Hostname)
}

// Layout: the Overview is on the left and the plan, which needs the room, takes
// the right 60% of the screen. Each pane has a one-cell border with its title
// set into the top edge; the plan has a row under it for comments, and the
// screen a header row above the panes and a footer row below them.
const (
	borderSize = 2
	// requestGutter is the cells of room left and right of the messages.
	requestGutter = 1
	headerRows    = 1
	tabRows       = 1
	footerRows    = 3
	inputRows     = 6
	// inputBoxRows is the request input and its border, which has the input's
	// title set into the top edge; titleBoxRows is the title's, of one row.
	inputBoxRows = inputRows + borderSize
	titleBoxRows = 1 + borderSize
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

// leftWidth is the left pane's width.
func (m *Model) leftWidth() int {
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
	// The editor is the text of a comment's card, which sits past the gutter
	// on a rail, indented, with a cell either side.
	m.commentInput.SetWidth(m.planView.Width() - gutterWidth - 6)
	m.overviewView.SetWidth(m.leftWidth() - borderSize - 2*padX)
	if m.created {
		m.overviewView.SetHeight(inner - padY)
	} else {
		m.overviewView.SetHeight(max(1, inner-m.factoryBoxRows()-titleBoxRows-inputBoxRows-padY))
	}
	m.titleInput.SetWidth(m.leftWidth() - 2*borderSize - 2*padX)
	m.input.SetWidth(m.leftWidth() - 2*borderSize - 2*padX)
	m.renderPlan()
	m.renderLeft()
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
	blocks, steps := blocksOf(m.structure)
	if len(blocks) == 0 {
		m.blocks = nil
		if m.creating {
			m.setContent(textPlan, lipgloss.NewStyle().Padding(1, 2).Render(m.skeleton(m.planView.Width()-4)))
			return
		}
		m.setContent(textPlan, ui.HelperStyle.Padding(1, 2).Width(m.planView.Width()).
			Render("No plan yet. Give the plan a title and say what you want changed, and the plan appears here."))
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
	m.blocks = make([]renderedBlock, len(blocks))
	var st planStats
	from := 0
	for i, b := range blocks {
		at := -1
		if j := strings.Index(m.plan[from:], b.source); j >= 0 {
			at, from = from+j, from+j+len(b.source)
		}
		m.blocks[i] = renderedBlock{block: b, lines: m.drawBlock(r, b, steps[i], &st, w), at: at}
	}
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

// queuedParts is the queued comments waiting to be saved, as lines w cells
// wide: each one's quote, then its text, on a bar.
func (m Model) queuedParts(cs []comment, w int) []string {
	parts := make([]string, 0, 2*len(cs))
	for _, c := range cs {
		parts = append(parts, m.style(m.faint()).Render("┃ "+excerpt(c.quote, w-2)))
		for _, l := range strings.Split(ansi.Wrap(c.text, max(w-2, 1), ""), "\n") {
			parts = append(parts, m.style(m.amber()).Render("┃ "+l))
		}
	}
	return parts
}

// renderLeft draws the left pane: the request before the plan exists, the
// Overview after.
func (m *Model) renderLeft() {
	if m.created {
		m.renderOverview()
		return
	}
	m.renderRequest()
}

// renderRequest draws the left pane before the plan exists: what went wrong in
// making the plan, and the wait while it is made. There are no queued comments
// or worker yet.
func (m *Model) renderRequest() {
	// A cell of room each side keeps the messages off the pane's border.
	w := m.overviewView.Width() - 2*requestGutter
	if w <= 0 {
		return
	}
	var b strings.Builder
	for _, msg := range m.messages {
		if !msg.failed {
			continue
		}
		b.WriteString(chunkStyle.Render("chunk") + "\n")
		b.WriteString(ui.ErrorStyle.Width(w).Render(msg.text) + "\n\n")
	}
	if m.creating {
		b.WriteString(m.style(m.purple()).Render(m.spin.View() + "Creating the plan…"))
	}
	// The pane follows what is added to it unless the developer has scrolled
	// up to read, when it stays where they are.
	follow := m.overviewView.AtBottom()
	m.setContent(textOverview, lipgloss.NewStyle().PaddingLeft(requestGutter).Render(b.String()))
	if follow {
		m.overviewView.GotoBottom()
	}
}

// taskLabel is a factory task's category as the Overview shows it: in sentence
// case, with spaces for hyphens, so "early-plan" reads "Early plan".
func taskLabel(category string) string {
	if category == "" {
		return ""
	}
	s := strings.ReplaceAll(category, "-", " ")
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// restyle renders the plan and the left pane again for a change of
// background.
func (m *Model) restyle() {
	m.renderedWidth = 0
	m.renderPlan()
	m.renderLeft()
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

	planTitle := title(m.focus == panePlan, "Plan")
	if m.moving != nil {
		planTitle += title(m.focus == panePlan, " · "+m.movingText())
	}
	body := m.shown(textPlan) + "\n" + m.planFooter()
	leftTitle, leftBody := "New plan", m.shown(textOverview)+"\n"+m.inputBoxes()
	if m.created {
		leftTitle, leftBody = "Overview", m.shown(textOverview)
	}
	left := pane(m.focus == paneOverview, m.leftWidth(), title(m.focus == paneOverview, leftTitle), leftBody)
	plan := pane(m.focus == panePlan, m.planWidth(), planTitle, body)

	panes := lipgloss.JoinHorizontal(lipgloss.Top, left, plan)
	top := m.header() + "\n" + m.strip()
	if m.narrow() {
		// One pane at a time, the other a tab away.
		panes = left
		if m.focus == panePlan {
			panes = plan
		}
		top += "\n" + m.tabs()
	}
	return lipgloss.JoinVertical(lipgloss.Left, top, panes, m.actionBar())
}

// inputBoxes are the new plan's boxes, each under the pane's log. A box's
// border stays a step below the pane's accent, so the accent shows once per
// pane.
func (m Model) inputBoxes() string {
	return m.box("Title", m.field == fieldTitle, m.titleInput.View()) + "\n" +
		m.factoryBoxView() + "\n" +
		m.box("Request", m.field == fieldRequest, m.input.View())
}

// box is a titled box as wide as the pane's content.
func (m Model) box(name string, focused bool, content string) string {
	w := m.leftWidth() - borderSize - 2*padX
	edge := m.quiet()
	if focused && m.focus == paneOverview && !m.commenting {
		edge = m.dim()
	}
	b := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(edge).
		Width(w).Render(content)
	return titled(b, ui.TitleStyle.Render(name), lipgloss.NewStyle().Foreground(edge).Render)
}

func (m Model) factoryBoxView() string {
	return m.box("Factory", m.field == fieldFactory, m.factoryList())
}

// factoryBoxRows is the Factory box's rows as drawn: its border and the
// entries shown, a name too long for the box taking more than one row each.
func (m Model) factoryBoxRows() int { return lipgloss.Height(m.factoryBoxView()) }

// factoryList is the Factory box's content: the chosen entry while the
// keyboard is elsewhere, and every entry while it has it, with > on the
// chosen one. A list that could not be read says so, dim, beside Org default.
func (m Model) factoryList() string {
	labels := m.factoryLabels()
	entry := func(i int) string {
		if i == 0 && m.factoriesFailed {
			return labels[0] + "  " + ui.HelperStyle.Faint(true).Render("could not list factories")
		}
		return labels[i]
	}
	if m.field != fieldFactory {
		return entry(m.factory)
	}
	rows := make([]string, len(labels))
	for i := range labels {
		mark := "  "
		if i == m.factory {
			mark = "> "
		}
		rows[i] = mark + entry(i)
	}
	return strings.Join(rows, "\n")
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
