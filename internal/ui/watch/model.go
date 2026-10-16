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

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/upgrade"
)

const (
	leftPaneWidth = 28
	pollInterval  = 5 * time.Second
	spinInterval  = 160 * time.Millisecond
)

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

// watchStyles holds lipgloss styles computed for the current terminal background.
type watchStyles struct {
	Dim      lipgloss.Style
	Emphasis lipgloss.Style
	Success  lipgloss.Style
	Warning  lipgloss.Style
	Running  lipgloss.Style
	Teal     lipgloss.Style
	Error    lipgloss.Style
	Muted    lipgloss.Style
	VDim     lipgloss.Style
	// Chip is the base of a coloured label; the caller sets its background.
	Chip lipgloss.Style
}

func (s watchStyles) dim(text string) string      { return s.Dim.Render(text) }
func (s watchStyles) emphasis(text string) string { return s.Emphasis.Render(text) }
func (s watchStyles) success(text string) string  { return s.Success.Render(text) }
func (s watchStyles) warning(text string) string  { return s.Warning.Render(text) }
func (s watchStyles) running(text string) string  { return s.Running.Render(text) }
func (s watchStyles) teal(text string) string     { return s.Teal.Render(text) }
func (s watchStyles) err(text string) string      { return s.Error.Render(text) }
func (s watchStyles) muted(text string) string    { return s.Muted.Render(text) }
func (s watchStyles) vdim(text string) string     { return s.VDim.Render(text) }

// Chunk brand colours. The accent is navy on a light terminal, where yellow
// would vanish, and yellow on a dark one.
var (
	chunkYellow = lipgloss.Color("#FFF700")
	chunkNavy   = lipgloss.Color("#2E3C52")
	chunkGreen  = lipgloss.Color("#00DB73")
)

func newWatchStyles(hasDark bool) watchStyles {
	ld := lipgloss.LightDark(hasDark)
	return watchStyles{
		Dim:      lipgloss.NewStyle().Foreground(ld(lipgloss.Color("248"), lipgloss.Color("246"))),
		Emphasis: lipgloss.NewStyle().Bold(true),
		Success:  lipgloss.NewStyle().Foreground(chunkGreen),
		Warning:  lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		Running:  lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
		Teal:     lipgloss.NewStyle().Foreground(ld(chunkNavy, chunkYellow)),
		Error:    lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		Muted:    lipgloss.NewStyle().Foreground(ld(lipgloss.Color("252"), lipgloss.Color("250"))),
		VDim:     lipgloss.NewStyle().Foreground(ld(lipgloss.Color("244"), lipgloss.Color("242"))),
		Chip:     lipgloss.NewStyle().Bold(true).Foreground(chunkNavy),
	}
}

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
	sessions      []sessionInfo
	allRuns       []sessionInfo
	query         string
	searching     bool
	reviewAuthErr string

	// selRun is the ID of the selected run, "" when there is none to select.
	// runItem is the selected row inside the run's pane; runConfirm is the run
	// an 'x' has been pressed for once, and runNote what the last action on a
	// run said.
	selRun     string
	runItem    int
	runConfirm string
	runNote    string
	// runOpen is the rounds of each run (by run ID) the user opened or closed,
	// over the default of showing only the round that matters. runDetails shows
	// the detail under a run: its worktree and setup log.
	runOpen    map[string]map[int]bool
	runDetails bool
	// drafter has Claude draft a prompt typed after n, in startDir's project
	// when there is one, and sender hands the draft to the factory. draft is
	// where that has got to: prompt is what is being typed, draftRoot the
	// project Claude is drafting in, and draftResult what it made. draftCancel
	// stops Claude, whose activity arrives on draftActivity until draftWait
	// ends; draftLatest is the last of it. draftNote is what the last step
	// said, and pendingRun the factory run just started, selected once a poll
	// lists it.
	draftCtx      context.Context
	drafter       Drafter
	sender        Sender
	startDir      string
	draft         draftState
	prompt        string
	draftRoot     string
	draftResult   Draft
	draftCancel   context.CancelFunc
	draftWait     context.Context
	draftActivity chan string
	draftLatest   string
	draftNote     string
	pendingRun    string
	// bottomMargin is how many of the terminal's last rows are left blank, for
	// a terminal that reports more rows than it shows.
	bottomMargin int
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

// WithRelaunch returns a copy of m that starts the daemon again when a poll
// finds it gone.
func (m Model) WithRelaunch() Model {
	m.relaunch = true
	return m
}

func (m Model) Init() tea.Cmd {
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
	case key.Matches(msg, k.Quit, k.ForceQuit):
		return m, tea.Quit
	case key.Matches(msg, k.Back):
		if right {
			m.focusedPane = paneLeft
			return m, nil
		}
		if m.query != "" {
			return m.setQuery(""), nil
		}
		return m, tea.Quit
	case key.Matches(msg, k.Cancel):
		return m.requestRunCancel(run, wasConfirm)
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
	case key.Matches(msg, k.Copy):
		return m.copyRunFailure(run)
	case key.Matches(msg, k.Toggle):
		if run != nil {
			m = m.toggleRound(run)
		}
	case key.Matches(msg, k.Details):
		m.runDetails = !m.runDetails
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
		return m.updateDashboardKey(msg)

	case tea.PasteMsg, draftActivityMsg, draftDoneMsg, runStartedMsg:
		return m.updateDraftMsg(msg)

	case errMsg:
		m.daemonErr = msg.err
		return m, tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })

	case outputMsg:
		return m.withOutputChunk(msg)

	case reviewDetailMsg:
		return m.withReviewDetail(msg)

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
		m.sessions = filterRuns(m.allRuns, m.query)
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
			return m, tea.Batch(next, doSpin())
		}
		return m, next

	case sessionActionMsg:
		return m.withSessionAction(msg), nil

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
	v := tea.NewView(m.render())
	v.AltScreen = true
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
		return m.renderHeader(st) + m.renderSeparator(st) + m.renderOutputPane(st)
	}
	return m.renderHeader(st) +
		m.renderFrameEdge(st, '╭', '┬', '╮') +
		m.renderBody(st) +
		m.renderFooter(st)
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

	clock := time.Now().Format("15:04:05")
	title := st.teal("◆ ") + st.emphasis("chunk watch") + "  " + st.muted(count) + contextTag
	right := m.runTag(st) + m.connectionTag(st) + "  " + st.vdim(clock)
	gap := m.width - lipgloss.Width(title) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	// Clipped like every other line: one that wraps costs a row the fixed-height
	// layout never budgeted for.
	return clip(title+strings.Repeat(" ", gap)+right, m.width) + "\n"
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

// frameStyle colours a part of the frame around the two panes: the focused
// pane's edges are drawn in the accent colour and the other pane's stay quiet.
// The divider is the focused pane's edge whichever pane that is.
func (m Model) frameStyle(st watchStyles, rightPane bool) func(string) string {
	if (m.focusedPane == paneRight) == rightPane {
		return st.teal
	}
	return st.vdim
}

// renderFrameEdge is the top or bottom of the frame around the two panes, with
// a junction over the divider.
func (m Model) renderFrameEdge(st watchStyles, left, junction, right rune) string {
	leftW := leftPaneWidth + 1 // the pane and the space before the divider
	rightW := max(m.width-leftW-3, 0)
	l, r := m.frameStyle(st, false), m.frameStyle(st, true)
	return l(string(left)+strings.Repeat("─", leftW)) + st.teal(string(junction)) +
		r(strings.Repeat("─", rightW)+string(right)) + "\n"
}

func (m Model) renderSeparator(st watchStyles) string {
	return st.vdim(strings.Repeat("─", m.width)) + "\n"
}

// contentHeight is how many lines the two panes get between the header and
// the footer.
func (m Model) contentHeight() int {
	h := m.height - 4 // header + separator + footer + padding
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

func (m Model) renderBody(st watchStyles) string {
	contentHeight := m.contentHeight()

	leftLines := m.renderLeftPane(st, contentHeight)
	var rightLines []string
	if run := m.selectedRun(); run != nil {
		rightLines = m.renderRunPane(st, *run, contentHeight)
	} else {
		rightLines = m.renderEmptyPane(st)
	}

	var b strings.Builder
	for i := 0; i < contentHeight; i++ {
		l := ""
		if i < len(leftLines) {
			l = leftLines[i]
		}
		r := ""
		if i < len(rightLines) {
			r = rightLines[i]
		}
		lPad := padLine(l, leftPaneWidth)
		// Clip rather than let a long line wrap: the two panes are drawn as one
		// fixed-height block, so a line that wraps shifts everything below it and
		// the layout no longer matches the height it was built for.
		rPad := padLine(clip(r, m.width-leftPaneWidth-5), m.width-leftPaneWidth-5)
		b.WriteString(m.frameStyle(st, false)("│") + lPad + " " + st.teal("│") + " " + rPad + m.frameStyle(st, true)("│") + "\n")
	}
	return b.String()
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
	visibleRuns := max(room/2, 1)
	firstRun := min(max(selected-visibleRuns+1, 0), selected)
	firstLine := 2 + firstRun*2
	lastLine := min(firstLine+room, len(lines)-1) // leave out the trailing blank
	out := append([]string(nil), lines[:2]...)
	return append(out, lines[firstLine:lastLine]...)
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
	if len(m.allRuns) > 0 {
		lines = append(lines, st.muted("No run matches the search."), "")
	} else {
		lines = append(lines, st.muted("No runs yet."), st.dim("Start one with: chunk factory build \"<what to change>\""), "")
	}

	// right pane width: total - 1 leading space - leftPaneWidth - 1 space - 1 divider - 1 space
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

func (m Model) renderFooter(st watchStyles) string {
	// The help view drops hints from the end until the bar fits. The same
	// fixed-height reasoning as the notice below: a wrapped footer costs a line
	// the layout did not budget for, which is worse than a hint the reader can
	// find by pressing the key.
	help := newHelp(st, m.width-2)
	if m.draft != draftNone {
		// A new run's keys are the ones a new user is told to press, so they
		// stand out from the actions they name.
		help.Styles.ShortKey = st.Teal.Bold(true)
	}
	bar := help.View(m.keys())
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

	footer := m.renderFrameEdge(st, '╰', '┴', '╯') + "  " + bar + "\n"
	if m.draft != draftNone {
		// A new run's keys go last, under the step they are for.
		footer = m.renderFrameEdge(st, '╰', '┴', '╯') + m.draftFooter(st, bar)
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
