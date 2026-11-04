// Package onboarding is the onboarding timeline chunk watch opens on in a
// project not yet set up for chunk factory: it signs in, runs the project's
// Setup in the daemon, offers a first change, and follows that change's plan
// to its merge. Its host forwards messages to it, renders Content, and
// handles the messages it emits.
package onboarding

import (
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// LeaveMsg asks the host to show the flow list: the developer left
// onboarding with esc, then Go to flows, and Left is saved.
type LeaveMsg struct{}

// DoneMsg asks the host to show the flow list: the first change's flow ended
// merged, and Finished is saved.
type DoneMsg struct{}

// How often each source is read again.
const (
	setupPoll  = time.Second
	flowPoll   = 2 * time.Second
	checksPoll = 10 * time.Second
)

// outcomeMerged is factory's outcome for a flow whose PR merged.
const outcomeMerged = "merged"

// firstRequest is the first plan's request for change: the change, and a
// line asking the plan to commit the config Setup wrote.
func firstRequest(change string) string {
	return change + "\n\nAlso commit .chunk/config.json, which chunk wrote to set this repo up for chunk factory."
}

type (
	accountMsg struct {
		org  string
		orgs []Org
		err  error
	}
	// setupMsg is a read of the Setup run; epoch is the m.setupEpoch it was
	// asked in, so a read from before a Start is dropped.
	setupMsg struct {
		act     chunkd.SetupActivity
		found   bool
		err     error
		started bool
		epoch   int
	}
	setupTickMsg struct{}
	continuedMsg struct{ err error }
	startedMsg   struct{ opened }
	reopenedMsg  struct{ opened }
	resumedMsg   struct {
		store plan.Store
		err   error
	}
	flowMsg struct {
		store plan.Store
		flow  plan.Flow
		err   error
	}
	flowTickMsg struct{ store plan.Store }
	// structureMsg is a read of the plan: its structure, text and
	// comments, as the cards read them.
	structureMsg struct {
		store     plan.Store
		structure plan.Structure
		text      string
		comments  []plan.Comment
		err       error
	}
	eventMsg struct {
		agent plan.Agent
		plan.Event
	}
	checksMsg struct {
		store  plan.Store
		checks *Checks
		err    error
	}
	checksTickMsg struct{ store plan.Store }
)

// opened is a plan opened, and for a new one created and requested: err is
// why it could not be opened or created, and requestErr why its request
// failed.
type opened struct {
	agent      plan.Agent
	approver   plan.Approver
	store      plan.Store
	root       string
	title      string
	change     string
	suggestion string
	err        error
	requestErr error
}

// change is a change the developer chose: its text, and the suggestion it is
// ("" for their own).
type change struct{ text, suggestion string }

// Model is a Bubble Tea component, not a standalone tea.Model.
type Model struct {
	ctx    context.Context
	deps   Deps
	state  State
	width  int
	height int
	dark   bool
	styles theme.Styles

	// The CircleCI account: accountDone once Org answers, then either org
	// is set, or one of signedOut, accountErr, orgs (to choose from) or
	// noOrgs is.
	accountDone bool
	org         string
	orgs        []Org
	noOrgs      bool
	signedOut   bool
	accountErr  error

	// The Setup run as last read: found is whether the project has one.
	setup      chunkd.SetupActivity
	found      bool
	fetched    bool // whether a read of the run has answered
	setupLine  string
	setupErr   error
	setupWait  bool // a tick or read of the run is outstanding
	setupEpoch int
	// sawRunning is when this model first saw the run running, and
	// setupTracked whether onboarding_setup_finished was tracked.
	sawRunning   time.Time
	setupTracked bool

	input     textinput.Model
	queued    *change
	starting  bool
	changeErr error

	// The started plan: nil store until one is open. root is the project
	// it is planned and built in, "" when it is read-only.
	agent       plan.Agent
	approver    plan.Approver
	store       plan.Store
	root        string
	title       string
	change      string
	flow        plan.Flow
	flowLoaded  bool
	structure   plan.Structure
	activity    string
	planErr     error
	flowErr     error
	needsResume bool
	checks      *Checks
	checksErr   error
	checking    bool
	// ended is how the last plan's flow ended unmerged, for the timeline.
	ended string

	// The plan's text and comments as last read, the cards they make, and
	// Send answers while it runs and how it failed.
	text     string
	comments []plan.Comment
	cards    []card
	sending  bool
	sendErr  error

	// focus is the focused row's ID, needs the ◐ cards drawn at the last
	// update, and marked the ones that appeared while focus stayed in an
	// input with text.
	focus  string
	needs  map[string]bool
	marked map[string]bool
	offset int

	leaving bool
	saveErr error
}

// New creates the onboarding timeline for deps.Root, from deps.State's saved
// state.
func New(ctx context.Context, deps Deps) Model {
	in := textinput.New()
	in.Placeholder = "or describe your own…"
	in.Prompt = ""
	st := in.Styles()
	st.Cursor.Blink = false
	in.SetStyles(st)
	m := Model{
		ctx: ctx, deps: deps, state: deps.State.Load(), width: 80, height: 24, dark: true,
		styles: theme.New(true), input: in, needs: map[string]bool{}, marked: map[string]bool{},
	}
	if len(m.state.Suggestions) == 0 {
		m.state.Suggestions = slices.Clone(Generic)
	}
	m.settle(true)
	return m
}

// Init reads the account, and opens the saved plan when there is one.
func (m Model) Init() tea.Cmd {
	m.track("onboarding_started", nil)
	cmds := []tea.Cmd{m.accountCmd()}
	if id := m.state.PlanID; id != "" {
		cmds = append(cmds, m.reopenCmd(id))
	}
	return tea.Batch(cmds...)
}

// State is onboarding's state as last saved, or as loaded when nothing was
// saved: its host shows the unused suggestions once it closes.
func (m Model) State() State { return m.state }

// Width is the width of the last WindowSizeMsg.
func (m Model) Width() int { return m.width }

// Unsent is any typed text not yet saved: the own change, then each card's
// input, a line apart.
func (m Model) Unsent() string {
	var parts []string
	if t := typedText(m.input); t != "" {
		parts = append(parts, t)
	}
	for _, c := range m.cards {
		if ic, ok := c.(inputCard); ok && ic.typed() != "" {
			parts = append(parts, ic.typed())
		}
	}
	return strings.Join(parts, "\n")
}

// focusedTyped is whether the focused row's input holds unsaved text.
func (m Model) focusedTyped() bool {
	if m.focus == focusInput {
		return typedText(m.input) != ""
	}
	if c, _ := m.focusedCard(); c != nil {
		if ic, ok := c.(inputCard); ok {
			return ic.typed() != ""
		}
	}
	return false
}

// Place names onboarding for the chat: "Set up", and Setup's state, with the
// plan once one is started.
func (m Model) Place() (place, context string) {
	ctx := "[onboarding] setup " + m.setupStateText()
	if m.store != nil && m.store.ID() != "" {
		ctx += "\n[plan] " + m.store.ID() + " " + oneLine(m.title)
	}
	return "Set up", ctx
}

func (m Model) setupStateText() string {
	if !m.found {
		return "not started"
	}
	return string(m.setup.State)
}

// Update advances the component.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if m.ctx.Err() != nil {
		return m, nil
	}
	var cmd tea.Cmd
	moved := false
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 0), max(msg.Height, 0)
		m.input.SetWidth(max(m.width-8, 1))
		m.syncCards()
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.styles = theme.New(m.dark)
		m.syncCards()
	case tea.MouseWheelMsg:
		m.wheel(msg)
	case tea.KeyPressMsg:
		cmd, moved = m.key(msg)
	case tea.PasteMsg:
		if m.focus == focusInput {
			m.input, cmd = m.input.Update(msg)
		} else if _, i := m.focusedCard(); i >= 0 {
			cmd = m.updateCard(i, msg)
		}
	case accountMsg:
		cmd = m.applyAccount(msg)
	case setupMsg:
		cmd = m.applySetup(msg)
	case setupTickMsg:
		cmd = m.fetchCmd(m.setup.Total)
	case continuedMsg:
		m.setupErr = msg.err
		if msg.err == nil {
			cmd = m.pollSetup()
		}
	default:
		cmd = m.updatePlan(msg)
	}
	m.settle(moved)
	return m, cmd
}

// updatePlan handles the messages about the started plan.
func (m *Model) updatePlan(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case startedMsg:
		return m.applyStarted(msg.opened)
	case reopenedMsg:
		return m.applyReopened(msg.opened)
	case resumedMsg:
		if msg.store == m.store {
			m.planErr = msg.err
		}
	case eventMsg:
		return m.applyEvent(msg)
	case flowTickMsg:
		if msg.store == m.store && m.store != nil {
			return flowCmd(m.ctx, m.store)
		}
	case flowMsg:
		return m.applyFlow(msg)
	case structureMsg:
		if msg.store == m.store && msg.err == nil {
			m.structure, m.text, m.comments = msg.structure, msg.text, msg.comments
			m.syncCards()
		}
	case cardMsg:
		return m.applyCardMsg(msg)
	case answersMsg:
		return m.applyAnswers(msg)
	case checksTickMsg:
		if msg.store != m.store {
			return nil
		}
		if m.store != nil && m.flow.PR != "" && !m.flow.Ended {
			return checksCmd(m.ctx, m.deps.Checks, m.store, m.flow.PR)
		}
		m.checking = false
	case checksMsg:
		if msg.store == m.store {
			m.checks, m.checksErr = msg.checks, msg.err
			m.syncCards()
			return tick(m.ctx, checksPoll, checksTickMsg{store: msg.store})
		}
	}
	return nil
}

// View is Content, for a host that draws onboarding alone.
func (m Model) View() string { return m.Content() }

func (m Model) track(name string, props map[string]any) {
	if props == nil {
		props = map[string]any{}
	}
	if m.store != nil && m.store.ID() != "" {
		props["plan_id"] = m.store.ID()
	}
	telemetry.Track(m.ctx, name, props)
}

func (m *Model) save() {
	m.saveErr = m.deps.State.Save(m.state)
}

// tick sends msg after d, unless ctx ends first.
func tick(ctx context.Context, d time.Duration, msg tea.Msg) tea.Cmd {
	return func() tea.Msg {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			return msg
		}
	}
}

// The account.

func (m Model) accountCmd() tea.Cmd {
	ctx, a := m.ctx, m.deps.Account
	return func() tea.Msg {
		org, orgs, err := a.Org(ctx)
		return accountMsg{org: org, orgs: orgs, err: err}
	}
}

func (m *Model) applyAccount(msg accountMsg) tea.Cmd {
	m.accountDone = true
	m.org, m.orgs, m.noOrgs, m.signedOut, m.accountErr = "", nil, false, false, nil
	switch {
	case errors.Is(msg.err, ErrSignedOut):
		m.signedOut = true
	case msg.err != nil:
		m.accountErr = msg.err
	case msg.org != "":
		m.org = msg.org
	case len(msg.orgs) == 1:
		m.org = msg.orgs[0].ID
	case len(msg.orgs) == 0:
		m.noOrgs = true
	default:
		m.orgs = slices.Clone(msg.orgs)
	}
	if m.org == "" {
		return nil
	}
	return m.fetchCmd(-1)
}

// chooseOrg sets the project's org to the developer's choice and reads its
// Setup run.
func (m *Model) chooseOrg(i int) tea.Cmd {
	if i < 0 || i >= len(m.orgs) {
		return nil
	}
	m.org, m.orgs = m.orgs[i].ID, nil
	return m.fetchCmd(-1)
}

// The Setup run.

func (m *Model) fetchCmd(after int) tea.Cmd {
	m.setupWait = true
	ctx, s, epoch := m.ctx, m.deps.Setup, m.setupEpoch
	return func() tea.Msg {
		act, found, err := s.Fetch(ctx, after)
		return setupMsg{act: act, found: found, err: err, epoch: epoch}
	}
}

func (m *Model) startSetupCmd() tea.Cmd {
	m.setupEpoch++
	m.setupErr = nil
	ctx, s, org, epoch := m.ctx, m.deps.Setup, m.org, m.setupEpoch
	return func() tea.Msg {
		act, err := s.Start(ctx, org)
		return setupMsg{act: act, found: err == nil, err: err, started: true, epoch: epoch}
	}
}

func (m Model) continueCmd() tea.Cmd {
	ctx, s := m.ctx, m.deps.Setup
	return func() tea.Msg { return continuedMsg{err: s.Continue(ctx)} }
}

// polled is whether the run is read again: while it runs, or is stuck and
// waits on Retry or Continue.
func polled(s chunkd.SetupState) bool { return s == chunkd.SetupRunning || s == chunkd.SetupStuck }

func (m *Model) applySetup(msg setupMsg) tea.Cmd {
	if !msg.started {
		m.setupWait = false
	}
	if msg.epoch != m.setupEpoch {
		return m.pollSetup()
	}
	m.fetched = true
	if msg.err != nil {
		m.setupErr = msg.err
		return m.pollSetup()
	}
	m.setupErr = nil
	if !msg.found {
		if msg.started {
			return nil
		}
		return m.startSetupCmd()
	}
	if m.found && msg.act.Total < m.setup.Total {
		// A new attempt: its feed starts again.
		m.setupLine = ""
	}
	m.setup, m.found = msg.act, true
	if n := len(msg.act.Lines); n > 0 {
		m.setupLine = oneLine(msg.act.Lines[n-1].Text)
	}
	now := time.Now()
	switch {
	case m.setup.State == chunkd.SetupRunning && m.sawRunning.IsZero():
		m.sawRunning = now
	case m.setup.State != chunkd.SetupRunning && !m.sawRunning.IsZero() && !m.setupTracked:
		m.setupTracked = true
		m.track("onboarding_setup_finished", map[string]any{
			"outcome":    string(m.setup.State),
			"duration_s": int(math.Round(now.Sub(m.sawRunning).Seconds())),
		})
	}
	return tea.Batch(m.pollSetup(), m.startQueued())
}

// pollSetup reads the run again after setupPoll while it is polled and no
// read is outstanding.
func (m *Model) pollSetup() tea.Cmd {
	if m.setupWait || !m.found || !polled(m.setup.State) {
		return nil
	}
	m.setupWait = true
	return tick(m.ctx, setupPoll, setupTickMsg{})
}

// retrySetup is r on Setup: Start again on a stuck or failed run, or read it
// again after a read failed.
func (m *Model) retrySetup() tea.Cmd {
	switch {
	case m.org == "":
		return nil
	case m.found && (m.setup.State == chunkd.SetupStuck || m.setup.State == chunkd.SetupFailed):
		return m.startSetupCmd()
	case m.setupErr != nil && !m.found:
		m.setupErr = nil
		m.setupEpoch++
		return m.fetchCmd(-1)
	}
	return nil
}

// Starting the change.

// choose starts c, or queues it until Setup's config step finds a check.
func (m *Model) choose(c change) tea.Cmd {
	if m.store != nil || m.starting {
		return nil
	}
	m.changeErr = nil
	m.queued = &c
	return m.startQueued()
}

// startQueued starts the queued change once the config has a check.
func (m *Model) startQueued() tea.Cmd {
	if m.queued == nil || m.starting || !m.found || m.setup.Checks <= 0 {
		return nil
	}
	c := *m.queued
	m.queued, m.starting = nil, true
	ctx, open, root := m.ctx, m.deps.Open, m.deps.Root
	return func() tea.Msg {
		o := opened{change: c.text, suggestion: c.suggestion}
		p, err := open(ctx, root, "")
		if err != nil {
			o.err = err
			return startedMsg{o}
		}
		req := firstRequest(c.text)
		title, err := p.Store.Create(ctx, req)
		if err != nil {
			o.err = err
			return startedMsg{o}
		}
		o.agent, o.approver, o.store, o.root, o.title = p.Agent, p.Approve, p.Store, p.Root, title
		o.requestErr = p.Agent.Request(ctx, req)
		return startedMsg{o}
	}
}

func (m *Model) applyStarted(o opened) tea.Cmd {
	m.starting = false
	if o.err != nil {
		m.changeErr = o.err
		return nil
	}
	m.state.PlanID, m.state.Suggestion = o.store.ID(), o.suggestion
	m.setUsed(o.suggestion, true)
	m.save()
	m.input.Reset()
	m.ended = ""
	cmd := m.follow(o)
	m.planErr = o.requestErr
	source := "own"
	if o.suggestion != "" {
		source = "suggested"
	}
	m.track("onboarding_change_started", map[string]any{"source": source})
	return cmd
}

func (m *Model) setUsed(title string, used bool) {
	for i := range m.state.Suggestions {
		if m.state.Suggestions[i].Title == title {
			m.state.Suggestions[i].Used = used
		}
	}
}

// The plan.

func (m Model) reopenCmd(id string) tea.Cmd {
	ctx, open, root := m.ctx, m.deps.Open, m.deps.Root
	return func() tea.Msg {
		p, err := open(ctx, root, id)
		return reopenedMsg{opened{agent: p.Agent, approver: p.Approve, store: p.Store, root: p.Root, err: err}}
	}
}

func (m *Model) applyReopened(o opened) tea.Cmd {
	if o.err != nil {
		m.planErr = o.err
		return nil
	}
	o.change = m.state.Suggestion
	cmd := m.follow(o)
	m.needsResume = true
	return cmd
}

// follow starts reading o's plan: its agent's events and its flow.
func (m *Model) follow(o opened) tea.Cmd {
	m.agent, m.approver, m.store, m.root = o.agent, o.approver, o.store, o.root
	m.title, m.change = o.title, o.change
	m.flow, m.flowLoaded, m.structure = plan.Flow{}, false, plan.Structure{}
	m.activity, m.planErr, m.flowErr = "", nil, nil
	m.checks, m.checksErr, m.checking = nil, nil, false
	m.text, m.comments, m.cards, m.sending, m.sendErr = "", nil, nil, false, nil
	return tea.Batch(waitEvent(m.ctx, m.agent), flowCmd(m.ctx, m.store))
}

func waitEvent(ctx context.Context, a plan.Agent) tea.Cmd {
	if a == nil {
		return nil
	}
	events := a.Events()
	return func() tea.Msg {
		select {
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			return eventMsg{agent: a, Event: ev}
		case <-ctx.Done():
			return nil
		}
	}
}

func (m *Model) applyEvent(msg eventMsg) tea.Cmd {
	if msg.agent != m.agent {
		return nil
	}
	switch {
	case msg.Activity != "":
		m.activity = oneLine(msg.Activity)
	case msg.Problem != nil:
		m.planErr = msg.Problem
	}
	return waitEvent(m.ctx, m.agent)
}

func flowCmd(ctx context.Context, s plan.Store) tea.Cmd {
	return func() tea.Msg {
		f, err := s.Flow(ctx)
		return flowMsg{store: s, flow: f, err: err}
	}
}

func structureCmd(ctx context.Context, s plan.Store) tea.Cmd {
	return func() tea.Msg {
		msg := structureMsg{store: s}
		if msg.structure, msg.err = s.Structure(ctx); msg.err != nil {
			return msg
		}
		if msg.text, msg.err = s.Text(ctx); msg.err != nil {
			return msg
		}
		msg.comments, msg.err = s.Comments(ctx)
		return msg
	}
}

func resumeCmd(ctx context.Context, a plan.Agent, s plan.Store) tea.Cmd {
	return func() tea.Msg { return resumedMsg{store: s, err: a.Resume(ctx)} }
}

func checksCmd(ctx context.Context, checks PRChecks, s plan.Store, pr string) tea.Cmd {
	return func() tea.Msg {
		if checks == nil {
			return checksMsg{store: s}
		}
		c, err := checks(ctx, pr)
		return checksMsg{store: s, checks: c, err: err}
	}
}

func (m *Model) applyFlow(msg flowMsg) tea.Cmd {
	if msg.store != m.store || m.store == nil {
		return nil
	}
	if msg.err != nil {
		m.flowErr = msg.err
		return tick(m.ctx, flowPoll, flowTickMsg{store: m.store})
	}
	m.flowErr = nil
	changed := !m.flowLoaded || msg.flow.State != m.flow.State
	m.flow, m.flowLoaded = msg.flow, true
	m.syncCards()
	var cmds []tea.Cmd
	if m.needsResume {
		m.needsResume = false
		if s := m.flow.State; s == planstate.RefineProposal || s == planstate.RefineUnits {
			cmds = append(cmds, resumeCmd(m.ctx, m.agent, m.store))
		}
	}
	// While the developer has a step, the plan is read at every poll, so
	// the cards see a reworded question or an answer made elsewhere.
	if changed || waitsOnHuman(m.flow) || len(m.cards) > 0 {
		cmds = append(cmds, structureCmd(m.ctx, m.store))
	}
	if m.flow.PR != "" && !m.checking && !m.flow.Ended {
		m.checking = true
		cmds = append(cmds, checksCmd(m.ctx, m.deps.Checks, m.store, m.flow.PR))
	}
	if m.flow.Ended {
		cmds = append(cmds, m.end())
		return tea.Batch(cmds...)
	}
	cmds = append(cmds, tick(m.ctx, flowPoll, flowTickMsg{store: m.store}))
	return tea.Batch(cmds...)
}

// end handles the flow's end: merged finishes onboarding; otherwise the
// change can be started again, or another one.
func (m *Model) end() tea.Cmd {
	if m.flow.Outcome == outcomeMerged {
		m.state.Finished = true
		m.save()
		m.track("onboarding_merged", nil)
		return func() tea.Msg { return DoneMsg{} }
	}
	m.setUsed(m.state.Suggestion, false)
	m.state.PlanID, m.state.Suggestion = "", ""
	m.save()
	m.ended = "ended: " + cmp.Or(m.flow.Outcome, "unknown")
	m.agent, m.approver, m.store, m.root = nil, nil, nil, ""
	m.cards = nil
	return nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
