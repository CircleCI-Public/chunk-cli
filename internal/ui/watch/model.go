// Package watch implements the chunk watch TUI dashboard.
package watch

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/browser"
	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/planlist"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
	"github.com/CircleCI-Public/chunk-cli/internal/upgrade"
)

const (
	leftPaneWidth = theme.ListWidth
	pollInterval  = 5 * time.Second
	spinInterval  = 160 * time.Millisecond
)

// listTop is the screen row of the run list's search row: under the header
// and the pane's top edge. Its runs follow, two rows to a run.
const listTop = 2

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const logoBlankRow = "███                   ██████"

var logoLines = []string{
	"        █████████████████",
	"      █████████████████████",
	"    ███████████████████  ███",
	"  ███                ███████",
	logoBlankRow,
	"███       ██ ██       ██████",
	"███   ██         ██   ██████",
	"███     █████████     ██████",
	logoBlankRow,
	"███                   ████",
	"  ███                ██",
	"    █████████████████",
}

// watchStyles is the shared TUI palette with the dashboard's shorthand for
// rendering in each style.
type watchStyles struct{ theme.Styles }

func newWatchStyles(hasDark bool) watchStyles { return watchStyles{theme.New(hasDark)} }

func (s watchStyles) dim(text string) string      { return s.Dim.Render(text) }
func (s watchStyles) emphasis(text string) string { return s.Emphasis.Render(text) }
func (s watchStyles) success(text string) string  { return s.Success.Render(text) }
func (s watchStyles) warning(text string) string  { return s.Warning.Render(text) }
func (s watchStyles) running(text string) string  { return s.Running.Render(text) }
func (s watchStyles) brand(text string) string    { return s.Brand.Render(text) }
func (s watchStyles) err(text string) string      { return s.Error.Render(text) }
func (s watchStyles) muted(text string) string    { return s.Muted.Render(text) }
func (s watchStyles) vdim(text string) string     { return s.VDim.Render(text) }

// ProjectEntry holds everything the watch model needs for one project.
type ProjectEntry struct {
	ProjectRoot string
}

// pane identifies which side of the split layout has keyboard focus.
type pane int

const (
	paneLeft pane = iota
	paneRight
)

type tickMsg struct{}
type spinMsg struct{}
type updateCheckMsg struct{ latest, upgradeCmd string }
type errMsg struct{ err error }

type dataMsg struct {
	projects []ProjectEntry
	branches []string
	headRefs []string
	authErr  string
	// sessions are the daemon's runs, and reviewAuthErr its reason for being
	// unable to start one.
	sessions      []sessionInfo
	reviewAuthErr string
}

// Model is the BubbleTea model for the watch dashboard.
type Model struct {
	loadFn func(Model) tea.Msg
	// detailFn fetches a run with what its reviews said, read when a finished
	// review is opened rather than carried in every poll.
	detailFn func(id string) (chunkd.SessionDetail, error)
	projects []ProjectEntry
	branches []string // current branch per project, refreshed each poll
	headRefs []string // HEAD SHA per project, refreshed each poll

	// watchAll, when set, means the poll loop should keep re-scanning for
	// projects that started their first run after this dashboard started,
	// instead of only ever watching the projects it opened with.
	watchAll bool

	focusedPane pane

	width      int
	height     int
	spinIdx    int
	hasSpinner bool
	daemonErr  error // set when the last poll failed; cleared on success

	hasDarkBG bool // updated by tea.BackgroundColorMsg

	updateAvailable string // non-empty tag (e.g. "v1.2.3") when an update is available
	upgradeCmd      string // "chunk upgrade" or "brew upgrade chunk"

	// relaunch is set when a poll that finds the daemon gone should start
	// another instead of freezing on the last snapshot. Only the caller knows
	// whether the daemon is one this machine runs.
	relaunch bool

	// output is the open scrollback pane, nil when none is open.
	output *outputPane
	// outputSeq increments on every pane opening, so a tick chain from a closed
	// pane can be told apart from the current one and dropped.
	outputSeq int

	// authErr explains why command output is unavailable, when it is. An empty
	// logs pane with no explanation sends people hunting the wrong fault.
	authErr string

	// conn is the daemon this dashboard talks to, shown in the header next to
	// whether the last poll reached it.
	conn chunkd.Connection

	// sessions are the runs the left pane lists, across projects. allRuns is
	// every run worth listing before the search filters it down to them; query
	// is what the person typed after /, and searching is true while the
	// keyboard goes to it.
	sessions  []sessionInfo
	allRuns   []sessionInfo
	query     string
	searching bool
	// showEnded lists finished runs alongside the live ones. Off by default so
	// history does not crowd out what is going now.
	showEnded bool
	// attention narrows the list to the runs that need a person, finished or
	// not.
	attention     bool
	reviewAuthErr string

	// selRun is the ID of the selected run, "" when there is none to select.
	// runItem is the selected row inside the run's pane; runConfirm is the run
	// an 'x' or 'D' has been pressed for once, confirmDelete which of the two it
	// was, and runNote what the last action on a run said.
	selRun        string
	runItem       int
	runConfirm    string
	confirmDelete bool
	runNote       string
	// runOpen is the rounds of each run (by run ID) the user opened or closed,
	// over the default of showing only the round that matters. taskOpen is
	// whether each run's task row (by run ID) was expanded, over the default of
	// collapsed. capOpen is whether each run (by run ID) explains its round
	// cap, over the default of during its first round. detailsOpen is whether
	// each run's details fold (by run ID) — its worktree, setup log, progress
	// feed and review sandboxes — is open, over the default of folded.
	// rowsOpen is which rows inside each run's rounds (by run ID) are folded
	// open to show what is behind them, and reviewFolds the findings fetched
	// for the finished reviews among them.
	runOpen     map[string]map[int]bool
	taskOpen    map[string]bool
	capOpen     map[string]bool
	detailsOpen map[string]bool
	rowsOpen    map[string]map[runItem]bool
	reviewFolds map[reviewKey]reviewFold
	// drafter has Claude draft a prompt typed after n, in startDir's project
	// when there is one, and sender hands the draft to the factory; continuer
	// starts a run that picks up a finished one's work. draft is
	// where that has got to: prompt is what is being typed, draftRoot the
	// project Claude is drafting in, and draftResult what it made. draftCancel
	// stops Claude, whose activity arrives on draftActivity until draftWait
	// ends; draftLatest is the last of it. draftNote is what the last step
	// said, and pendingRun the factory run just started, selected once a poll
	// lists it.
	draftCtx      context.Context
	drafter       Drafter
	sender        Sender
	continuer     Continuer
	startDir      string
	draft         draftState
	prompt        string
	draftRoot     string
	draftResult   Draft
	draftDiff     *diffCache
	draftCancel   context.CancelFunc
	draftWait     context.Context
	draftActivity chan string
	draftLatest   string
	draftNote     string
	pendingRun    string
	// draftLog is everything Claude did while drafting, and draftScroll how
	// far the shown draft is scrolled.
	draftLog    []string
	draftScroll int
	// draftFocus is the draft section the section keys act on, draftPlanShut
	// and draftChangesOpen which are folded: the plan starts open and the
	// change closed. They are kept here, not in the lines drawn, so a resize
	// leaves them as they were.
	draftFocus       draftSection
	draftPlanShut    bool
	draftChangesOpen bool
	// draftStarted is when Claude started the draft, and draftTook how long
	// it took, shown so the speed is seen.
	draftStarted time.Time
	draftTook    time.Duration
	// sendingDraft is true from enter until the factory answers.
	sendingDraft bool
	// continuing is true from r until the factory answers.
	continuing bool
	// Each workspace keeps its input and session while another is shown.
	// planScreen points into planWorkspaces, to the last selected workspace.
	planCtx                                   context.Context
	planOpening                               bool
	planner                                   Planner
	planScreen                                *plan.Model
	planOpen                                  bool
	planWorkspaces                            map[uint64]*planWorkspace
	planKey, planSeq                          uint64
	inbox                                     *planlist.Model
	inboxOpen, switchingPlan, confirmPlanQuit bool
	// runChanges is each finished run's committed change, by run ID, read
	// once its run is selected.
	runChanges map[string]runChange
	// bottomMargin is how many of the terminal's last rows are left blank, for
	// a terminal that reports more rows than it shows.
	bottomMargin int
	// openURL opens a link in the default browser; superEnter is whether the
	// terminal can tell Cmd+Enter from Enter. rowNote is what the last open or
	// copy of the run's branch or pull request said, on its row, and noteSeq
	// numbers them so an old one's timer cannot clear a newer one.
	openURL    func(string) error
	superEnter bool
	rowNote    rowNote
	noteSeq    int
	// spinning is true while a spinner tick chain is in flight, so a poll that
	// finds something running after a quiet spell can start one without doubling
	// up on a chain that never stopped.
	spinning bool
}

// New creates a Model ready to run. When watchAll is true (the default for
// `chunk watch`; `--focus` turns it off), each poll also checks for projects
// that have started a run since the dashboard started and adds them, so a run
// started after the dashboard launches still shows up without a restart.
func New(projects []ProjectEntry, watchAll bool) Model {
	return Model{
		loadFn:    loadFromDaemon,
		detailFn:  chunkd.FetchSession,
		projects:  projects,
		branches:  make([]string, len(projects)),
		headRefs:  make([]string, len(projects)),
		watchAll:  watchAll,
		hasDarkBG: lipgloss.HasDarkBackground(os.Stdin, os.Stdout),
		conn:      chunkd.CurrentConnection(),
		openURL:   browser.Open,
		spinning:  true, // Init starts the first chain
	}
}

// WithConnection returns a copy of m that reports conn as the daemon it talks
// to. New reads it from the environment; this is for callers and tests that
// already know.
func (m Model) WithConnection(conn chunkd.Connection) Model {
	m.conn = conn
	return m
}

// WithBottomMargin returns a copy of m that leaves the terminal's last rows
// blank. Editors' built-in terminals can report a row or two more than they
// show, and the rows they hide are the footer's.
func (m Model) WithBottomMargin(rows int) Model {
	m.bottomMargin = max(rows, 0)
	return m
}

// WithOpener returns a copy of m that opens links with open instead of the
// default browser.
func (m Model) WithOpener(open func(string) error) Model {
	m.openURL = open
	return m
}

// WithRelaunch returns a copy of m that starts the daemon again when a poll
// finds it gone.
func (m Model) WithRelaunch() Model {
	m.relaunch = true
	return m
}

func (m Model) Init() tea.Cmd {
	if m.inbox != nil {
		return tea.Batch(m.loadData, doSpin(), checkUpdateCmd(), m.inbox.Init())
	}
	return tea.Batch(m.loadData, doSpin(), checkUpdateCmd())
}

// updateDashboardKey handles keys for the two-pane dashboard, when no output
// pane is open. Split out of Update so neither grows past the complexity limit.
//
// Quitting only detaches: runs belong to the daemon and carry on without the
// dashboard. Stopping one takes the deliberate two-press cancel.
func (m Model) updateDashboardKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := m.keys()
	run := m.selectedRun()
	right := m.focusedPane == paneRight
	// A pending cancel is withdrawn by any key but the one that confirms it.
	wasConfirm := m.runConfirm
	m.runConfirm = ""
	if m.draft != draftNone {
		return m.updateDraftKey(msg)
	}
	m.draftNote = ""
	if m.searching {
		return m.updateSearchKey(msg)
	}
	switch {
	case key.Matches(msg, k.Search):
		m.searching = true
		m.focusedPane = paneLeft
	case key.Matches(msg, k.New):
		m.draft, m.prompt = draftPrompting, ""
	case key.Matches(msg, k.Plan):
		return m.openPlan()
	case key.Matches(msg, k.Quit, k.ForceQuit):
		return m.quitWithPlans()
	case key.Matches(msg, k.Back):
		if right {
			m.focusedPane = paneLeft
			return m, nil
		}
		if m.query != "" {
			return m.setQuery(""), nil
		}
		return m.quitWithPlans()
	case key.Matches(msg, k.Cancel):
		return m.requestRunCancel(run, wasConfirm)
	case key.Matches(msg, k.Delete):
		return m.requestRunDelete(run, wasConfirm)
	case key.Matches(msg, k.Continue):
		return m.continueRun(run)
	case key.Matches(msg, k.ToggleEnded):
		return m.toggleEnded(), nil
	case key.Matches(msg, k.Attention):
		return m.toggleAttention(), nil
	case key.Matches(msg, k.Right):
		m.focusedPane = paneRight
	case key.Matches(msg, k.Left):
		m.focusedPane = paneLeft
	case key.Matches(msg, k.Down):
		m = m.moveSelection(run, 1)
	case key.Matches(msg, k.Up):
		m = m.moveSelection(run, -1)
	case key.Matches(msg, k.Open):
		if run != nil {
			return m.openRunItem(run, m.runItems(run.s))
		}
	case key.Matches(msg, k.OpenPR):
		return m.openPullRequest(run)
	case key.Matches(msg, k.Copy):
		return m.copyRunItem(run)
	case key.Matches(msg, k.CopyBranch):
		return m.copyRunRow(run, itemBranch)
	case key.Matches(msg, k.CopyPR):
		return m.copyRunRow(run, itemPR)
	case key.Matches(msg, k.Toggle):
		if run != nil {
			return m.toggleRunRow(run)
		}
	case key.Matches(msg, k.Rounds):
		if run != nil {
			m = m.toggleCapNote(run.s)
		}
	}
	return m, nil
}

// moveSelection moves whatever the focused pane selects: the left pane's run,
// or the selected run's row.
func (m Model) moveSelection(run *sessionInfo, delta int) Model {
	if m.focusedPane == paneLeft {
		return m.moveRunSelection(delta)
	}
	if run != nil {
		m.runItem = min(max(m.runItem+delta, 0), max(len(m.runItems(run.s))-1, 0))
	}
	return m
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m.updateWithPlan(msg)
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.hasDarkBG = msg.IsDark()
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = max(msg.Height-m.bottomMargin, 1)
		if m.output != nil {
			m.output.wrap(m.width - 1)
		}
		return m, nil

	case tea.KeyPressMsg:
		// An open output pane owns the keyboard: it is a full-height reading view,
		// and leaving the underlying list navigable would scroll two things at
		// once. Esc closes it rather than quitting the dashboard.
		if m.output != nil {
			return m.updateOutputKey(msg)
		}
		return withSelectedChange(m.updateDashboardKey(msg))

	case runChangeMsg:
		return m.withRunChange(msg), nil

	case tea.PasteMsg, draftActivityMsg, draftDoneMsg, runStartedMsg, draftTickMsg:
		return m.updateDraftMsg(msg)

	case planReadyMsg:
		return m.withPlanReady(msg)

	case errMsg:
		m.daemonErr = msg.err
		return m, tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })

	case outputMsg:
		return m.withOutputChunk(msg)

	case reviewDetailMsg:
		return m.withReviewDetail(msg)

	case reviewFoldMsg:
		return m.withReviewFold(msg), nil

	case outputTickMsg:
		// Only the current pane's tick chain may continue. Without the sequence
		// check, closing and reopening the pane leaves the old chain alive to
		// find a non-nil pane and schedule alongside the new one, so every
		// reopen would add another poller.
		if m.output == nil || msg.seq != m.outputSeq {
			return m, nil
		}
		// A finished command's buffer is final: the daemon appends everything the
		// stream produced before marking it done, and serves data and Running
		// under one lock, so a chunk that reported Running:false was already
		// complete. Polling past it is a socket round trip every 200ms for output
		// that cannot change — and the pane can sit open on a finished command
		// indefinitely.
		if !m.output.running {
			return m, nil
		}
		return m, tea.Batch(fetchOutput(m.output.commandID, m.output.offset), outputTick(msg.seq))

	case dataMsg:
		m.daemonErr = nil
		m.projects = msg.projects
		m.branches = msg.branches
		m.headRefs = msg.headRefs
		m.authErr = msg.authErr
		// A new round, or the run ending, can change which round is open by
		// default and so add or drop rows above the selection: note which row
		// it is before the poll and find that row again after.
		prevRun, prevItem := m.selRun, m.selectedRunItem()
		m.allRuns = visibleRuns(msg.sessions, time.Now())
		m.sessions = m.listedRuns()
		m.reviewAuthErr = msg.reviewAuthErr
		m = m.reselectRun().selectPendingRun()
		if run := m.selectedRun(); run != nil {
			if run.s.ID == prevRun && prevItem != nil {
				m.runItem = m.findRunItem(run.s, *prevItem)
			} else {
				m.runItem = min(m.runItem, max(len(m.runItems(run.s))-1, 0))
			}
		}
		m.hasSpinner = anySessionLive(m.allRuns)
		next := tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
		if m.hasSpinner && !m.spinning {
			m.spinning = true
			next = tea.Batch(next, doSpin())
		}
		return withSelectedChange(m, next)

	case sessionActionMsg:
		return m.withSessionAction(msg), nil

	case runContinuedMsg:
		return m.withRunContinued(msg), nil

	case tea.KeyboardEnhancementsMsg, prOpenedMsg, rowNoteExpiredMsg:
		return m.updateRowNote(msg)

	case updateCheckMsg:
		m.updateAvailable = msg.latest
		m.upgradeCmd = msg.upgradeCmd
		return m, nil

	case tickMsg:
		return m, m.loadData

	case spinMsg:
		m.spinIdx++
		if m.hasSpinner {
			return m, doSpin()
		}
		m.spinning = false
		return m, nil
	}
	return m, nil
}

func (m Model) View() tea.View {
	if m.inboxOpen {
		inbox := m.inbox.WithNote(m.draftNote)
		content := inbox.Content()
		if m.width > 0 {
			content = m.renderHeader(m.styles()) + content
		}
		v := tea.NewView(content)
		v.AltScreen = true
		v.WindowTitle = "chunk watch — plans"
		return v
	}
	if m.planOpen {
		v := tea.NewView(m.planScreen.Content())
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		v.WindowTitle = "chunk watch"
		return v
	}
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "chunk watch"
	return v
}

func (m Model) styles() watchStyles {
	return newWatchStyles(m.hasDarkBG)
}

func (m Model) render() string {
	if m.width == 0 {
		return "loading...\n"
	}
	st := m.styles()
	if m.output != nil {
		return m.renderHeader(st) + m.renderOutputPane(st)
	}
	if m.welcoming() {
		return m.renderHeader(st) + m.renderWelcome(st) + m.renderFooter(st)
	}
	return m.renderHeader(st) + m.renderBody(st) + m.renderFooter(st)
}

func (m Model) renderHeader(st watchStyles) string {
	n := len(m.sessions)
	count := fmt.Sprintf("%d run%s", n, plural(n))

	var contextTag string
	if len(m.projects) > 1 {
		contextTag = "  " + st.muted(fmt.Sprintf("%d projects", len(m.projects)))
	} else if len(m.projects) == 1 {
		branch := m.branches[0]
		head := m.headRefs[0]
		if branch != "" && head != "" {
			contextTag = "  " + st.success(branch+"@"+head[:min(7, len(head))])
		}
	}

	title := st.brand("◆ ") + st.emphasis("chunk watch") + "  "
	if m.inbox != nil {
		// The count is the showing tab's, so it never reads as the other's.
		tab := 1
		if m.inboxOpen {
			tab = 0
			n = m.inbox.Len()
			count = fmt.Sprintf("%d plan%s", n, plural(n))
		}
		title += st.Tabs(tab, "Plans", "Runs") + "  "
	}
	title += st.muted(count) + contextTag
	right := m.draftReadyTag(st) + m.attentionTag(st) + m.runTag(st) + m.connectionTag(st)
	gap := m.width - lipgloss.Width(title) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	// Clipped like every other line: one that wraps costs a row the fixed-height
	// layout never budgeted for.
	return clip(title+strings.Repeat(" ", gap)+right, m.width) + "\n"
}

// draftReadyTag flags a draft ready to send in the header too, so the next
// action stays visible while the diff below it is scrolled out of view.
func (m Model) draftReadyTag(st watchStyles) string {
	if m.draft != draftReady || m.draftResult.Stat == "" {
		return ""
	}
	return st.Accent.Padding(0, 1).Render("⏎ ready to send") + "  "
}

// connectionTag says which daemon the dashboard is showing and whether it is
// answering. An unreachable daemon is named in the header as well as the footer
// because the rows below it are the last thing that daemon said, and a header
// that stayed green would let stale rows pass for live ones.
func (m Model) connectionTag(st watchStyles) string {
	label := m.conn.Label()
	if m.daemonErr != nil {
		return st.err(ui.IconFail + " " + label + " unreachable")
	}
	return st.success(ui.IconOK) + " " + st.muted(label)
}

// contentHeight is how many lines the two panes get between the header and
// the footer.
func (m Model) contentHeight() int {
	h := m.height - 4 // header + the boxes' top and bottom edges (2) + the help bar
	// The footer grows by a line when the daemon is unreachable. Without handing
	// that line back the message lands past the last row and is clipped at every
	// terminal size, which is how a daemon that had died came to look like a
	// dashboard that had merely gone quiet.
	if m.daemonErr != nil {
		h--
	}
	// So does a run's confirmation or note, and a new run takes draftLines.
	if m.runLine() != "" {
		h--
	}
	if m.draft != draftNone {
		h -= draftLines - 1
	}
	if h < 1 {
		h = 1
	}
	return h
}

// renderBody is the two panes, each its own rounded box with its title set
// into the box's top edge, so a pane is drawn one line taller than the rows
// it fills.
func (m Model) renderBody(st watchStyles) string {
	contentHeight := m.contentHeight()
	paneHeight := contentHeight + 1

	leftLines := m.renderLeftPane(st, paneHeight)
	var rightLines []string
	if m.draft == draftPrompting {
		rightLines = m.renderNewRunPane(st)
	} else if m.draft == draftWorking || m.draft == draftReady {
		rightLines = m.renderDraftPane(st, paneHeight)
	} else if m.handingOff() {
		rightLines = m.renderSentPane(st, paneHeight)
	} else if run := m.selectedRun(); run != nil {
		rightLines = m.renderRunPane(st, *run, paneHeight)
	} else if m.pendingRun != "" {
		rightLines = m.renderPendingPane(st)
	} else {
		rightLines = m.renderEmptyPane(st)
	}
	leftTitle, leftLines := paneTitle(leftLines)
	rightTitle, rightLines := paneTitle(rightLines)

	rw := m.width - leftPaneWidth - 5
	left := make([]string, contentHeight)
	right := make([]string, contentHeight)
	for i := 0; i < contentHeight; i++ {
		l := ""
		if i < len(leftLines) {
			l = leftLines[i]
		}
		r := ""
		if i < len(rightLines) {
			r = rightLines[i]
		}
		left[i] = padLine(l, leftPaneWidth)
		// Clip rather than let a long line wrap: the two panes are drawn as one
		// fixed-height block, so a line that wraps shifts everything below it and
		// the layout no longer matches the height it was built for.
		right[i] = " " + padLine(clip(r, rw), rw)
	}
	h := contentHeight + 2
	return lipgloss.JoinHorizontal(lipgloss.Top,
		st.Pane(m.focusedPane == paneLeft, leftPaneWidth+2, h, leftTitle, strings.Join(left, "\n")),
		st.Pane(m.focusedPane == paneRight, m.width-leftPaneWidth-2, h, rightTitle, strings.Join(right, "\n")),
	) + "\n"
}

// paneTitle splits a pane's lines into its title and the lines under it.
func paneTitle(lines []string) (string, []string) {
	if len(lines) == 0 {
		return "", nil
	}
	return lines[0], lines[1:]
}

// renderLeftPane is the run list, windowed so the selected run stays on screen
// when there are more runs than room.
func (m Model) renderLeftPane(st watchStyles, maxLines int) []string {
	runs := m.renderRunList(st)
	if len(runs) > maxLines {
		return runListWindow(runs, maxLines, m.runIndex())
	}
	return runs
}

// firstListedRun is the first run runListWindow shows, of a list lines tall,
// so that a click can be traced back to the run it landed on.
func firstListedRun(lines, maxLines, selected int) int {
	if lines <= maxLines || maxLines <= 2 || selected < 0 {
		return 0
	}
	visibleRuns := max((maxLines-2)/2, 1)
	return min(max(selected-visibleRuns+1, 0), selected)
}

// runListWindow keeps the selected run visible when the complete run list is
// taller than the pane. Each run occupies two lines after the two-line header.
func runListWindow(lines []string, maxLines, selected int) []string {
	if maxLines <= 0 {
		return nil
	}
	if maxLines <= 2 || selected < 0 {
		return lines[:min(maxLines, len(lines))]
	}
	room := maxLines - 2
	firstRun := firstListedRun(len(lines), maxLines, selected)
	firstLine := 2 + firstRun*2
	lastLine := min(firstLine+room, len(lines)-1) // leave out the trailing blank
	out := append([]string(nil), lines[:2]...)
	return append(out, lines[firstLine:lastLine]...)
}

// renderPendingPane is the right pane between a run being sent to the factory
// and a poll listing it: the start hints would read as if nothing had happened.
func (m Model) renderPendingPane(st watchStyles) []string {
	title := st.vdim("run")
	if m.focusedPane == paneRight {
		title = st.emphasis("run")
	}
	return []string{
		title,
		"",
		st.success(ui.IconOK + " Sent to the factory"),
		st.muted("  Its run appears in the list in a moment."),
	}
}

// renderEmptyPane is the right-hand pane when there is no run to show. The
// dashboard is about factory runs, so with none to draw it says so rather than
// leaving the pane blank.
func (m Model) renderEmptyPane(st watchStyles) []string {
	title := st.vdim("run")
	if m.focusedPane == paneRight {
		title = st.emphasis("run")
	}
	lines := make([]string, 0, 5+len(logoLines))
	lines = append(lines, title, "")
	switch {
	case m.attention && m.query == "":
		lines = append(lines, st.muted("No run needs you."), st.dim("Press ")+st.Brand.Bold(true).Render("a")+st.dim(" to show every run."), "")
	case len(m.allRuns) > 0 && m.query == "" && !m.showEnded:
		lines = append(lines, st.muted("No active runs."), st.dim("Press ")+st.Brand.Bold(true).Render("e")+st.dim(" to show finished runs."), "")
	case len(m.allRuns) > 0:
		lines = append(lines, st.muted("No run matches the search."), "")
	default:
		lines = append(lines, m.renderStartHints(st)...)
	}

	// right pane content width: the right box's border, then one leading space.
	rightWidth := m.width - leftPaneWidth - 4
	logoMaxWidth := 0
	for _, l := range logoLines {
		if w := lipgloss.Width(l); w > logoMaxWidth {
			logoMaxWidth = w
		}
	}
	pad := max((rightWidth-logoMaxWidth)/2, 0)
	padStr := strings.Repeat(" ", pad)
	for _, l := range logoLines {
		lines = append(lines, padStr+st.vdim(l))
	}
	return lines
}

// renderStartHints is what the empty pane offers when there is nothing to
// watch and nothing has ever run: the two ways to begin, each with the key that
// starts it, so a new user sees what to do on arriving. A key that is not on
// offer is left out, and with neither the command line is all that is left.
func (m Model) renderStartHints(st watchStyles) []string {
	k := m.keys()
	var actions []string
	for _, b := range []key.Binding{k.New, k.Plan} {
		if !b.Enabled() {
			continue
		}
		h := b.Help()
		actions = append(actions, st.Brand.Bold(true).Render("["+h.Key+"]")+" "+st.muted(h.Desc))
	}
	cmd := st.dim("chunk factory build \"<what to change>\"")
	if len(actions) == 0 {
		return []string{st.muted("No runs yet."), st.dim("Start one with: ") + cmd, ""}
	}
	return []string{
		st.muted("No runs yet."),
		st.emphasis("Let's build"),
		strings.Join(actions, "   "),
		st.dim("or run: ") + cmd,
		"",
	}
}

func (m Model) renderFooter(st watchStyles) string {
	// The help view drops hints from the end until the bar fits. The same
	// fixed-height reasoning as the notice below: a wrapped footer costs a line
	// the layout did not budget for, which is worse than a hint the reader can
	// find by pressing the key.
	help := theme.Help(st.Styles, m.width-2)
	if m.draft != draftNone {
		// A new run's keys are the ones a new user is told to press, so they
		// stand out from the actions they name.
		help.Styles.ShortKey = st.Brand.Bold(true)
	}
	var bar string
	if m.welcoming() {
		bar = help.View(quitOnly{m.keys().Quit})
	} else {
		bar = help.View(m.keys())
	}
	// help adds a hint that overflows when there is no room left for its
	// ellipsis, so the clip below is still needed.
	bar = clip(bar, m.width-2)

	// Right-align one notice, dropping it entirely when it does not fit: padding
	// it onto an over-long bar would wrap the footer and break the fixed-height
	// layout. An auth failure wins the slot over the update hint — a logs pane
	// that will never fill is more immediate than a version.
	notice := ""
	switch {
	case m.authErr != "":
		notice = st.warning(ui.IconWarn + " " + m.authErr)
	case m.updateAvailable != "":
		notice = "↑ " + m.updateAvailable + "  " + st.dim(m.upgradeCmd)
	}
	if notice != "" {
		if gap := m.width - 2 - lipgloss.Width(bar) - lipgloss.Width(notice); gap >= 2 {
			bar += strings.Repeat(" ", gap) + notice
		}
	}

	footer := "  " + bar + "\n"
	if m.draft != draftNone {
		// A new run's keys go last, under the step they are for.
		footer = m.draftFooter(st, bar)
	}
	switch {
	case m.draft != draftNone:
	case m.draftNote != "":
		footer += "  " + st.muted(clip(m.draftNote, m.width-2)) + "\n"
	case m.runConfirm != "" && m.runLine() != "":
		footer += "  " + st.warning(clip(m.runLine(), m.width-2)) + "\n"
	case m.runLine() != "":
		footer += "  " + st.muted(clip(m.runLine(), m.width-2)) + "\n"
	}
	if m.daemonErr != nil {
		footer += "  " + st.err("daemon unavailable: "+m.daemonErr.Error()) + "\n"
	}
	return footer
}

// runLine is the footer's extra line for the selected run: the cancel
// confirmation, or what the last action on it said. "" when there is none.
func (m Model) runLine() string {
	run := m.selectedRun()
	switch {
	case m.draft != draftNone:
		return "draft"
	case m.draftNote != "":
		return m.draftNote
	case run == nil:
		return ""
	case m.runConfirm == run.s.ID && m.confirmDelete:
		return "press D again to delete this run's record"
	case m.runConfirm == run.s.ID:
		return "press x again to cancel this run — q only detaches"
	}
	return m.runNote
}

// loadData delegates all disk and subprocess I/O to loadFn.
func (m Model) loadData() tea.Msg {
	return m.loadFn(m)
}

func doSpin() tea.Cmd {
	return tea.Tick(spinInterval, func(time.Time) tea.Msg { return spinMsg{} })
}

// checkUpdateCmd checks for a newer release for the footer notice. Root's
// PersistentPreRunE skips watch (see noUpdateCheckCommands) so this is the
// only check on this path — two would race on the cache file and print the
// notice twice.
func checkUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		latest := upgrade.Check()
		if latest == "" {
			return updateCheckMsg{}
		}
		return updateCheckMsg{latest: latest, upgradeCmd: upgrade.SelfUpgradeCommand()}
	}
}

// padLine pads s to exactly width visible characters, accounting for ANSI codes.
func padLine(s string, width int) string {
	vis := lipgloss.Width(s)
	if vis >= width {
		return lipgloss.NewStyle().MaxWidth(width).Render(s)
	}
	return s + strings.Repeat(" ", width-vis)
}

// truncate shortens s to at most n runes.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n > 1 {
		return string(runes[:n-1]) + "…"
	}
	return string(runes[:n])
}

// ago returns a human-readable duration since t.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}
