// Package watch implements the chunk watch TUI dashboard.
package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/upgrade"
)

const (
	leftPaneWidth  = 28
	divider        = " │ "
	pollInterval   = 5 * time.Second
	spinInterval   = 160 * time.Millisecond
	runningTimeout = 5 * time.Minute
	recentEvents   = 300

	levelDone  = "done"
	levelError = "error"

	// localRunnerName labels the synthesised row for validate runs that happened
	// locally rather than on a sidecar.
	localRunnerName = "local"
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

func newWatchStyles(hasDark bool) watchStyles {
	ld := lipgloss.LightDark(hasDark)
	return watchStyles{
		Dim:      lipgloss.NewStyle().Foreground(ld(lipgloss.Color("248"), lipgloss.Color("246"))),
		Emphasis: lipgloss.NewStyle().Bold(true),
		Success:  lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		Warning:  lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		Running:  lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
		Teal:     lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		Error:    lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		Muted:    lipgloss.NewStyle().Foreground(ld(lipgloss.Color("252"), lipgloss.Color("250"))),
		VDim:     lipgloss.NewStyle().Foreground(ld(lipgloss.Color("244"), lipgloss.Color("242"))),
	}
}

// ProjectEntry holds everything the watch model needs for one project.
type ProjectEntry struct {
	Log         *eventlog.Log
	DataDir     string
	ProjectRoot string
}

// sidecarInfo holds display state for one sidecar or the local runner.
// id == "" identifies the local (non-sidecar) runner for a project.
// sidecarIDs lists every sidecar UUID (and "" for local runs) whose events
// should appear in this entry's activity pane; populated by mergeBranches.
type sidecarInfo struct {
	id         string
	sidecarIDs []string // all IDs to match when filtering events
	name       string
	// sessionID is the agent session owning this sidecar, empty for the local
	// runner and for state written before sidecars were session-scoped. Several
	// sessions can hold sidecars for one worktree, so this is what distinguishes
	// two otherwise identical rows.
	sessionID    string
	projectName  string
	repoName     string    // basename of the main worktree (groups linked worktrees together)
	projectPath  string    // absolute root of this worktree, "" when unknown
	branch       string    // current git branch for this worktree
	projectIdx   int       // index into Model.projects
	snapshotName string    // name of the active snapshot for this project, if any
	fileMtime    time.Time // mtime of the sidecar state file (fallback when no events yet)
	running      bool
	lastActivity time.Time
	lastOp       eventlog.Op
	lastLevel    string // level of the most recent event ("done", "error", etc.)
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
	sidecars []sidecarInfo
	events   [][]eventlog.Event // per-project; index matches Model.projects
	offsets  []int64
	branches []string
	headRefs []string
}

// Model is the BubbleTea model for the watch dashboard.
type Model struct {
	loadFn   func(Model) tea.Msg
	projects []ProjectEntry
	offsets  []int64
	branches []string // current branch per project, refreshed each poll
	headRefs []string // HEAD SHA per project, refreshed each poll

	// watchAll, when set, means the poll loop should keep re-scanning for
	// projects that saved their first sidecar state after this dashboard
	// started, instead of only ever watching the projects it opened with.
	watchAll bool

	sidecars    []sidecarInfo
	selectedIdx int
	selectedID  string             // id of the selected sidecar, so selection survives reordering
	events      [][]eventlog.Event // per-project; index matches projects

	focusedPane      pane
	rightSelectedIdx int
	toggledInvocs    map[time.Time]bool // invocations whose expand/collapse is flipped from default

	width      int
	height     int
	spinIdx    int
	hasSpinner bool
	daemonErr  error // set when the last poll failed; cleared on success

	hasDarkBG bool // updated by tea.BackgroundColorMsg

	updateAvailable string // non-empty tag (e.g. "v1.2.3") when an update is available
	upgradeCmd      string // "chunk upgrade" or "brew upgrade chunk"

	// daemonArgs is the argv that starts the watch daemon, empty when the caller
	// did not supply one. Held so a poll that finds the daemon gone can start
	// another instead of freezing on the last snapshot.
	daemonArgs []string

	// ownSession is the session running this dashboard, empty when a human ran
	// `chunk watch` from a plain shell. When it matches a row's session that row
	// is labelled as the viewer's own, which is the fastest way to answer "which
	// of these is mine" without reading UUIDs.
	ownSession string
}

// noSelection is the initial selectedID sentinel. It can never match a real
// sidecar ID (UUID) or the local runner (id == ""), so the first dataMsg
// always falls back to index 0 — the most recently active sidecar.
const noSelection = "\x00"

// New creates a Model ready to run. When watchAll is true (the default for
// `chunk watch`; `--focus` turns it off), each poll also checks for projects
// that have saved a sidecar since the dashboard started and adds them, so a
// sidecar started after the dashboard launches still shows up without a
// restart.
func New(projects []ProjectEntry, watchAll bool) Model {
	return Model{
		loadFn:        loadFromDaemon,
		projects:      projects,
		offsets:       make([]int64, len(projects)),
		branches:      make([]string, len(projects)),
		headRefs:      make([]string, len(projects)),
		toggledInvocs: make(map[time.Time]bool),
		selectedID:    noSelection,
		watchAll:      watchAll,
		ownSession:    session.IDFromEnv(),
		hasDarkBG:     lipgloss.HasDarkBackground(os.Stdin, os.Stdout),
	}
}

// WithDaemonArgs returns a copy of m that can relaunch the watch daemon when a
// poll finds it gone. subArgs is the argv the daemon is started with, the same
// one passed to watchd.EnsureRunning.
func (m Model) WithDaemonArgs(subArgs []string) Model {
	m.daemonArgs = subArgs
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadData, doSpin(), checkUpdateCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.hasDarkBG = msg.IsDark()
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		switch msg.Code {
		case 'q', tea.KeyEscape:
			return m, tea.Quit
		case tea.KeyRight, 'l':
			m.focusedPane = paneRight
		case tea.KeyLeft, 'h':
			m.focusedPane = paneLeft
		case 's', tea.KeyDown:
			if m.focusedPane == paneLeft {
				if m.selectedIdx < len(m.sidecars)-1 {
					m.selectedIdx++
				}
				m.selectedID = selectedSidecarID(m.sidecars, m.selectedIdx)
				m.rightSelectedIdx = 0
			} else {
				m.rightSelectedIdx++
			}
		case 'w', tea.KeyUp:
			if m.focusedPane == paneLeft {
				if m.selectedIdx > 0 {
					m.selectedIdx--
				}
				m.selectedID = selectedSidecarID(m.sidecars, m.selectedIdx)
				m.rightSelectedIdx = 0
			} else if m.rightSelectedIdx > 0 {
				m.rightSelectedIdx--
			}
		case tea.KeyEnter, tea.KeySpace:
			if m.focusedPane == paneRight {
				m = m.toggleSelectedInvoc()
			}
		case 'c':
			if msg.Mod == tea.ModCtrl {
				return m, tea.Quit
			}
		}
		return m, nil

	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			m = m.withMouseClick(msg.X, msg.Y)
		}
		return m, nil

	case errMsg:
		m.daemonErr = msg.err
		return m, tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })

	case dataMsg:
		m.daemonErr = nil
		m.projects = msg.projects
		m.sidecars = msg.sidecars
		m.events = msg.events
		m.offsets = msg.offsets
		m.branches = msg.branches
		m.headRefs = msg.headRefs
		// Sidecars are re-sorted by recency each poll, so track the selection
		// by id. An unknown id (first poll, or the sidecar aged out) falls back
		// to index 0, the most recently active sidecar.
		m.selectedIdx = indexOfSidecar(m.sidecars, m.selectedID)
		m.selectedID = selectedSidecarID(m.sidecars, m.selectedIdx)
		m.hasSpinner = anyRunning(m.sidecars)
		return m, tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })

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
		return m, nil
	}
	return m, nil
}

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "chunk watch"
	v.MouseMode = tea.MouseModeCellMotion
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
	return m.renderHeader(st) +
		m.renderSeparator(st) +
		m.renderBody(st) +
		m.renderFooter(st)
}

func (m Model) renderHeader(st watchStyles) string {
	n := 0
	for _, sc := range m.sidecars {
		if sc.id != "" {
			n++
		}
	}
	count := fmt.Sprintf("%d sidecar", n)
	if n != 1 {
		count += "s"
	}

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
	title := st.emphasis("chunk watch") + "  " + st.muted(count) + contextTag
	right := st.vdim(clock)
	gap := m.width - lipgloss.Width(title) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return title + strings.Repeat(" ", gap) + right + "\n"
}

func (m Model) renderSeparator(st watchStyles) string {
	return st.vdim(strings.Repeat("─", m.width)) + "\n"
}

func (m Model) renderBody(st watchStyles) string {
	contentHeight := m.height - 4 // header + separator + footer + padding
	// The footer grows by a line when the daemon is unreachable. Without handing
	// that line back the message lands past the last row and is clipped at every
	// terminal size, which is how a daemon that had died came to look like a
	// dashboard that had merely gone quiet.
	if m.daemonErr != nil {
		contentHeight--
	}
	if contentHeight < 1 {
		contentHeight = 1
	}

	leftLines := m.renderSidecarPane(st, contentHeight)
	rightLines := m.renderActivityPane(st, contentHeight)

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
		div := st.vdim("│")
		if m.focusedPane == paneRight {
			div = st.muted("│")
		}
		b.WriteString(" " + lPad + " " + div + " " + r + "\n")
	}
	return b.String()
}

func (m Model) renderSidecarPane(st watchStyles, maxLines int) []string {
	lines := make([]string, 0, maxLines)
	add := func(s string) { lines = append(lines, s) }

	if m.focusedPane == paneLeft {
		add(st.emphasis("sidecars"))
	} else {
		add(st.vdim("sidecars"))
	}
	add("")

	if len(m.sidecars) == 0 {
		add(st.muted("nothing recent"))
		add("")
		add(st.dim("no sidecar activity"))
		add(st.dim("in the last day"))
		return lines
	}

	var lastRepo string
	shared := sharedWorktrees(m.sidecars)
	sessions := sessionsPerWorktree(m.sidecars)
	ambiguous := ambiguousBranches(m.sidecars)
	dirLabels := worktreeLabels(m.sidecars, ambiguous)
	lastGroup, haveGroup := groupKey{}, false

	dropped := 0
	for i, sc := range m.sidecars {
		// Cost the row before committing to it. A row is three to six lines and
		// renderBody clips whatever runs past maxLines, so a row begun without the
		// room to finish loses its tail — the sync state and age it exists to
		// report. Building it aside first lets the pane drop it whole instead.
		var row []string
		addRow := func(s string) { row = append(row, s) }

		// Repo separator — groups all worktrees of the same repo under one header.
		if sc.repoName != lastRepo {
			if lastRepo != "" {
				addRow("")
			}
			label := truncate(sc.repoName, leftPaneWidth-4)
			addRow(st.vdim("── ") + st.emphasis(label))
			lastRepo = sc.repoName
			haveGroup = false
		}

		// A worktree driven by more than one session gets the branch printed
		// once as a group header, and each row below it named by its session.
		// Alone, a row keeps the branch as its own label: there is nothing to
		// disambiguate, so it costs the reader nothing to know about sessions.
		group := groupOf(sc)
		multi := shared[group] > 1
		// "" unless this branch lives in more than one directory.
		dirLabel := ""
		if ambiguous[worktreeKey{sc.repoName, sc.branch}] {
			dirLabel = dirLabels[sc.projectIdx]
		}
		if multi && (!haveGroup || group != lastGroup) {
			// A detached HEAD has no branch to name the worktree with, and a
			// branch checked out twice names two of them; either way the
			// directory is what the reader can act on.
			label := sc.branch
			if dirLabel != "" {
				label = dirLabel
			}
			if label == "" {
				label = sc.projectName
			}
			// The branch gets the full width: it is the thing the reader is
			// looking for, and squeezing the count onto the same line would
			// truncate a normal-length branch name to nothing.
			addRow("  " + st.emphasis(truncate(label, leftPaneWidth-2)))
			addRow("  " + st.vdim(fmt.Sprintf("%d sessions", sessions[group])))
		}
		lastGroup, haveGroup = group, true

		selected := i == m.selectedIdx
		nameLine := truncate(m.rowLabel(sc, multi, dirLabel), leftPaneWidth-3)

		if selected {
			if m.focusedPane == paneLeft {
				addRow(st.success("▶ ") + st.emphasis(nameLine))
			} else {
				addRow(st.vdim("▶ ") + st.muted(nameLine))
			}
		} else {
			addRow("  " + nameLine)
		}

		if sc.snapshotName != "" {
			addRow("   " + st.vdim("◈ "+truncate(sc.snapshotName, leftPaneWidth-6)))
		}

		switch {
		case sc.running:
			frame := spinFrames[m.spinIdx%len(spinFrames)]
			addRow("  " + st.running(frame+" "+string(sc.lastOp)+"..."))
		case sc.id == "": // local runner — no sync state
			switch sc.lastLevel {
			case levelDone:
				addRow("  " + st.success(ui.IconOK+" passed"))
			case levelError:
				addRow("  " + st.err(ui.IconFail+" failed"))
			default:
				addRow("  " + st.muted("no runs yet"))
			}
		default:
			addRow("  " + st.muted("synced via rsync"))
		}

		if !sc.lastActivity.IsZero() {
			addRow("  " + st.vdim(ago(sc.lastActivity)))
		} else {
			addRow("")
		}

		// Divider between sidecars in the same repo group.
		if i < len(m.sidecars)-1 && m.sidecars[i+1].repoName == sc.repoName {
			addRow(st.vdim(strings.Repeat("·", leftPaneWidth)))
		}

		// Hold back a line for the hint whenever rows remain after this one.
		budget := maxLines
		if i < len(m.sidecars)-1 {
			budget--
		}
		if len(lines)+len(row) > budget {
			dropped = len(m.sidecars) - i
			break
		}
		lines = append(lines, row...)
	}
	if dropped > 0 {
		// Without this the rows simply stopped: a group's session count was the
		// only clue that any were missing, and a dropped repo had none at all.
		lines = append(lines, "  "+st.vdim(fmt.Sprintf("↓ %d more", dropped)))
	}
	return lines
}

// renderActivityPane renders the right-hand pane for the selected sidecar.
//
// The selection is bounded on both sides before it indexes m.sidecars. Update
// keeps it in range, so a negative value should not arise — but a panic here
// takes the terminal down with it, still in the alternate screen, and the cost
// of the second comparison is nothing next to that.
func (m Model) renderActivityPane(st watchStyles, maxLines int) []string {
	lines := make([]string, 0, maxLines)

	var title string
	if m.focusedPane == paneRight {
		title = st.emphasis("activity")
	} else {
		title = st.vdim("activity")
	}
	if m.selectedIdx >= 0 && m.selectedIdx < len(m.sidecars) {
		sc := m.sidecars[m.selectedIdx]
		branchLabel := sc.branch
		if branchLabel == "" {
			branchLabel = sidecarDisplayName(sc.name, sc.id)
		}
		if sc.repoName != "" {
			title += "  " + st.muted(sc.repoName+"/"+branchLabel)
		} else {
			title += "  " + st.muted(branchLabel)
		}
		// Name the session as well as the sidecar. The left pane can be scrolled
		// away from the selection, and with several sessions in one worktree the
		// repo/branch above is no longer enough to say whose activity this is.
		if sc.sessionID != "" {
			if sc.sessionID == m.ownSession {
				title += "  " + st.teal("● this session")
			} else {
				title += "  " + st.vdim("○ "+shortUUID(sc.sessionID))
			}
		}
		if sc.id != "" && sc.branch != "" {
			title += "  " + st.vdim(sidecarDisplayName(sc.name, sc.id))
		}
	}
	lines = append(lines, title, "")

	var filtered []eventlog.Event
	if m.selectedIdx >= 0 && m.selectedIdx < len(m.sidecars) {
		sc := m.sidecars[m.selectedIdx]
		if sc.projectIdx < len(m.events) {
			for _, e := range m.events[sc.projectIdx] {
				if hasSidecarID(sc.sidecarIDs, e.SidecarID) {
					filtered = append(filtered, e)
				}
			}
		}
	}

	if len(filtered) == 0 {
		// right pane width: total - 1 leading space - leftPaneWidth - 1 space - 1 divider - 1 space
		rightWidth := m.width - leftPaneWidth - 4
		logoMaxWidth := 0
		for _, l := range logoLines {
			if w := lipgloss.Width(l); w > logoMaxWidth {
				logoMaxWidth = w
			}
		}
		pad := (rightWidth - logoMaxWidth) / 2
		if pad < 0 {
			pad = 0
		}
		padStr := strings.Repeat(" ", pad)
		lines = append(lines, st.muted("No activity yet."), "")
		for _, l := range logoLines {
			lines = append(lines, padStr+st.vdim(l))
		}
		return lines
	}

	groups := groupByInvocation(filtered)
	rightSel := m.rightSelectedIdx
	if len(groups) > 0 && rightSel >= len(groups) {
		rightSel = len(groups) - 1
	}
	body, remaining := m.buildCollapsibleLines(st, groups, rightSel, maxLines-2)
	lines = append(lines, body...)
	if remaining > 0 {
		lines = append(lines, "  "+st.vdim(fmt.Sprintf("↓ %d more", remaining)))
	}
	return lines
}

// buildCollapsibleLines renders invocation groups newest-first with expand/collapse state.
// It returns the rendered lines and the count of groups that did not fit.
func (m Model) buildCollapsibleLines(st watchStyles, groups []invocationGroup, rightSel, maxLines int) ([]string, int) {
	rendered := make([]string, 0, maxLines)
	add := func(s string) { rendered = append(rendered, s) }

	lastGI := -1
	for gi := len(groups) - 1; gi >= 0 && len(rendered) < maxLines; gi-- {
		ri := len(groups) - 1 - gi // right-pane index: 0 = most recent
		isMostRecent := gi == len(groups)-1
		expanded := m.invocExpanded(groups[gi], isMostRecent)
		selected := m.focusedPane == paneRight && ri == rightSel

		if ri > 0 {
			add("")
		}
		add(renderInvocationHeader(st, groups[gi], expanded, selected))
		if expanded {
			g := groups[gi]
			for ei := len(g.events) - 1; ei >= 0 && len(rendered) < maxLines; ei-- {
				e := g.events[ei]
				if isSummaryEvent(e) {
					continue // already shown in header
				}
				add("  " + renderEvent(st, e))
			}
		}
		lastGI = gi
	}
	remaining := lastGI // groups at indices 0..lastGI-1 were not rendered
	if remaining < 0 {
		remaining = 0
	}
	return rendered, remaining
}

// invocExpanded reports whether an invocation should be shown expanded.
// Most recent defaults to expanded; older default to collapsed. User toggles flip these.
func (m Model) invocExpanded(g invocationGroup, isMostRecent bool) bool {
	toggled := m.toggledInvocs[invocEndTime(g)]
	if isMostRecent {
		return !toggled
	}
	return toggled
}

// invocEndTime is the stable key for an invocation: timestamp of its last event.
func invocEndTime(g invocationGroup) time.Time {
	if len(g.events) == 0 {
		return time.Time{}
	}
	return g.events[len(g.events)-1].Ts
}

// currentInvocGroups returns the invocation groups for the currently selected sidecar.
func (m Model) currentInvocGroups() []invocationGroup {
	if m.selectedIdx < 0 || m.selectedIdx >= len(m.sidecars) {
		return nil
	}
	sc := m.sidecars[m.selectedIdx]
	var filtered []eventlog.Event
	if sc.projectIdx < len(m.events) {
		for _, e := range m.events[sc.projectIdx] {
			if hasSidecarID(sc.sidecarIDs, e.SidecarID) {
				filtered = append(filtered, e)
			}
		}
	}
	return groupByInvocation(filtered)
}

// toggleSelectedInvoc flips the expand/collapse state of the right-pane selected invocation.
func (m Model) toggleSelectedInvoc() Model {
	groups := m.currentInvocGroups()
	ri := len(groups) - 1 - m.rightSelectedIdx
	if ri < 0 || ri >= len(groups) {
		return m
	}
	key := invocEndTime(groups[ri])
	if m.toggledInvocs[key] {
		delete(m.toggledInvocs, key)
	} else {
		m.toggledInvocs[key] = true
	}
	return m
}

// withMouseClick processes a left-click, toggling an invocation header if one was hit.
// The right pane content starts at terminal column leftPaneWidth+4 and terminal row 4
// (2 rows for header+separator, 2 rows for the activity pane title+blank).
func (m Model) withMouseClick(x, y int) Model {
	if x <= leftPaneWidth+2 {
		return m // click is in left pane or on divider
	}
	contentLine := y - 4 // 2 header rows + 2 activity title/blank rows
	if contentLine < 0 {
		return m
	}
	groups := m.currentInvocGroups()
	ri := m.invocRiAtLine(groups, contentLine)
	if ri < 0 {
		return m
	}
	m.focusedPane = paneRight
	m.rightSelectedIdx = ri
	gi := len(groups) - 1 - ri
	key := invocEndTime(groups[gi])
	if m.toggledInvocs[key] {
		delete(m.toggledInvocs, key)
	} else {
		m.toggledInvocs[key] = true
	}
	return m
}

// invocRiAtLine returns the right-pane index of the invocation whose header falls on
// contentLine (0-indexed within buildCollapsibleLines output), or -1 if the line is
// a blank separator or an event line.
func (m Model) invocRiAtLine(groups []invocationGroup, contentLine int) int {
	line := 0
	for ri := 0; ri < len(groups); ri++ {
		gi := len(groups) - 1 - ri
		isMostRecent := gi == len(groups)-1
		if ri > 0 {
			if line == contentLine {
				return -1 // blank separator
			}
			line++
		}
		if line == contentLine {
			return ri // header line
		}
		line++
		if m.invocExpanded(groups[gi], isMostRecent) {
			nEvents := len(groups[gi].events)
			if contentLine >= line && contentLine < line+nEvents {
				return -1 // event line
			}
			line += nEvents
		}
	}
	return -1
}

// invocationGroup holds all events belonging to one validate invocation.
type invocationGroup struct {
	events []eventlog.Event
}

// outcomeOf returns the icon, label, and terminal level of the invocation.
// The label is the N/M count extracted from the summary event (e.g. "3/3").
// Returns "" level when the invocation is still running.
func outcomeOf(g invocationGroup) (icon, label, level string) {
	for i := len(g.events) - 1; i >= 0; i-- {
		e := g.events[i]
		if !isSummaryEvent(e) {
			continue
		}
		count := extractCount(e.Msg)
		if count == "" {
			count = "passed"
		}
		if e.Level == levelDone {
			return ui.IconOK, count, levelDone
		}
		return ui.IconFail, count, levelError
	}
	return "●", "running", ""
}

// extractCount pulls the "N/M" fraction from a summary message like "3/3 passed  5.2s".
func extractCount(msg string) string {
	i := strings.IndexByte(msg, ' ')
	if i <= 0 {
		return ""
	}
	part := msg[:i]
	if strings.Contains(part, "/") {
		return part
	}
	return ""
}

// renderInvocationHeader renders a one-line summary for an invocation group.
func renderInvocationHeader(st watchStyles, g invocationGroup, expanded, selected bool) string {
	var arrow string
	if expanded {
		arrow = "▼ "
	} else {
		arrow = "▶ "
	}
	if selected {
		arrow = st.emphasis(arrow)
	} else {
		arrow = st.vdim(arrow)
	}

	icon, label, level := outcomeOf(g)
	var outcomeStr string
	switch level {
	case levelDone:
		outcomeStr = st.success(icon + " " + label)
	case levelError:
		outcomeStr = st.err(icon + " " + label)
	default:
		outcomeStr = st.running(icon + " " + label)
	}

	var ts time.Time
	if n := len(g.events); n > 0 {
		ts = g.events[n-1].Ts
	}
	tsStr := st.vdim(ts.Format("15:04:05"))

	durStr := ""
	if len(g.events) > 1 {
		dur := g.events[len(g.events)-1].Ts.Sub(g.events[0].Ts).Round(time.Second)
		if dur >= time.Second {
			durStr = "  " + st.vdim("("+dur.String()+")")
		}
	}

	label2 := "validate" + "  " + outcomeStr + "  " + tsStr + durStr
	if selected {
		label2 = st.emphasis("validate") + "  " + outcomeStr + "  " + tsStr + durStr
	}
	return arrow + label2
}

// groupByInvocation collects events into invocation groups. An invocation ends
// on the overall validate summary event ("N/M passed  Xs"), not on each
// per-command done event. A trailing group is created for any events without a
// terminal summary (the current in-progress invocation).
func groupByInvocation(events []eventlog.Event) []invocationGroup {
	var groups []invocationGroup
	cur := make([]eventlog.Event, 0, len(events))
	for _, e := range events {
		cur = append(cur, e)
		if isSummaryEvent(e) {
			groups = append(groups, invocationGroup{events: cur})
			cur = nil
		}
	}
	if len(cur) > 0 {
		groups = append(groups, invocationGroup{events: cur})
	}
	return groups
}

// isSummaryEvent reports whether e is the overall validate summary that closes
// an invocation. Summary messages are formatted "N/M passed  Xs" — only digits
// precede the first "/", distinguishing them from per-command done events.
func isSummaryEvent(e eventlog.Event) bool {
	if e.Op != eventlog.OpValidate || (e.Level != levelDone && e.Level != levelError) {
		return false
	}
	i := strings.IndexByte(e.Msg, '/')
	if i <= 0 {
		return false
	}
	for _, c := range e.Msg[:i] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// eventGroup is a slice of events from a single op-run.
type eventGroup []eventlog.Event

// groupEvents partitions events into op-runs. A run ends when a "done" or
// "error" event arrives, or when the op changes.
func groupEvents(events []eventlog.Event) []eventGroup {
	var groups []eventGroup
	cur := make(eventGroup, 0, len(events))
	for _, e := range events {
		if len(cur) > 0 && e.Op != cur[0].Op {
			groups = append(groups, cur)
			cur = make(eventGroup, 0)
		}
		cur = append(cur, e)
		if e.Level == levelDone || e.Level == levelError {
			groups = append(groups, cur)
			cur = make(eventGroup, 0)
		}
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

func renderEvent(st watchStyles, e eventlog.Event) string {
	icon, msg := iconAndMsg(st, e)
	if e.Op == eventlog.OpValidate {
		// Op tag omitted — already implied by the invocation header.
		return icon + "  " + msg
	}
	return opTag(st, e.Op) + "  " + icon + "  " + msg
}

func opTag(st watchStyles, op eventlog.Op) string {
	switch op {
	case eventlog.OpSync:
		return st.running("sync    ")
	case eventlog.OpValidate:
		return "validate"
	case eventlog.OpExec:
		return st.teal("exec    ")
	case eventlog.OpSetup:
		return "setup   "
	case eventlog.OpHook:
		return "hook    "
	default:
		return st.muted(fmt.Sprintf("%-8s", string(op)))
	}
}

func iconAndMsg(st watchStyles, e eventlog.Event) (string, string) {
	switch e.Level {
	case levelDone:
		return st.success(ui.IconOK), st.success(e.Msg)
	case "warn":
		return st.warning(ui.IconWarn), st.warning(e.Msg)
	case levelError:
		return st.err(ui.IconFail), st.err(e.Msg)
	default:
		if strings.HasPrefix(e.Msg, "$ ") {
			return st.muted("$"), st.muted(strings.TrimPrefix(e.Msg, "$ "))
		}
		return st.vdim("›"), st.muted(e.Msg)
	}
}

func (m Model) renderFooter(st watchStyles) string {
	var keys []struct{ key, action string }
	if m.focusedPane == paneLeft {
		keys = []struct{ key, action string }{
			{"↑/↓ w/s", "select"},
			{"→", "runs"},
			{"q", "quit"},
		}
	} else {
		keys = []struct{ key, action string }{
			{"↑/↓ w/s", "navigate"},
			{"Enter/Space", "toggle"},
			{"←", "sidecars"},
			{"q", "quit"},
		}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, st.vdim(k.key)+" "+st.dim(k.action))
	}
	bar := strings.Join(parts, "  "+st.vdim("·")+"  ")

	// Right-align the update notice, but drop it entirely when it does not
	// fit: padding it onto an over-long bar would wrap the footer and break
	// the fixed-height layout.
	if m.updateAvailable != "" {
		notice := "↑ " + m.updateAvailable + "  " + st.dim(m.upgradeCmd)
		if gap := m.width - 2 - lipgloss.Width(bar) - lipgloss.Width(notice); gap >= 2 {
			bar += strings.Repeat(" ", gap) + notice
		}
	}

	footer := st.vdim(strings.Repeat("─", m.width)) + "\n" + "  " + bar + "\n"
	if m.daemonErr != nil {
		footer += "  " + st.err("daemon unavailable: "+m.daemonErr.Error()) + "\n"
	}
	return footer
}

// loadData delegates all disk and subprocess I/O to loadFn.
func (m Model) loadData() tea.Msg {
	return m.loadFn(m)
}

// sortByActivity puts the most recently active project first, and within a
// project the most recently active sidecar first. Projects stay grouped so the
// sidecar pane still renders one header per project.
//
// Worktree groups are the same story one level down: every row for a given
// (repo, branch, project) sits together, ordered by the group's own most recent
// activity. Without that tier the rows of a branch with two sessions could be
// split apart by a busier branch in the same repo, and the pane could not label
// the group once.
func sortByActivity(sidecars []sidecarInfo, ownSession string) {
	latestRepo := map[string]time.Time{}
	latestGroup := map[groupKey]time.Time{}
	for _, sc := range sidecars {
		eff := effectiveActivity(sc)
		if eff.After(latestRepo[sc.repoName]) {
			latestRepo[sc.repoName] = eff
		}
		if k := groupOf(sc); eff.After(latestGroup[k]) {
			latestGroup[k] = eff
		}
	}
	sort.SliceStable(sidecars, func(i, j int) bool {
		a, b := sidecars[i], sidecars[j]
		if a.repoName != b.repoName {
			return latestRepo[a.repoName].After(latestRepo[b.repoName])
		}
		ka, kb := groupOf(a), groupOf(b)
		if ka != kb {
			return latestGroup[ka].After(latestGroup[kb])
		}
		// Inside a group the viewer's own row leads. "Which of these is mine" is
		// the first question a shared worktree raises, and a row that reshuffles
		// by recency every poll makes the reader hunt for the answer the label
		// was added to give. Ordering between the other rows is untouched.
		if ownSession != "" {
			aMine, bMine := a.sessionID == ownSession, b.sessionID == ownSession
			if aMine != bMine {
				return aMine
			}
		}
		return effectiveActivity(a).After(effectiveActivity(b))
	})
}

// groupKey identifies one worktree: every row sharing it is a different session
// (or the local runner) working in the same directory on the same branch.
type groupKey struct {
	repoName   string
	branch     string
	projectIdx int
}

func groupOf(sc sidecarInfo) groupKey {
	return groupKey{sc.repoName, sc.branch, sc.projectIdx}
}

// sharedWorktrees counts the rows in each worktree group, so the pane can tell
// a branch owned by one session from one several sessions are working in.
//
// mergeBranches has already folded the local runner into a real sidecar row
// wherever both exist, so a group holding more than one row means two sidecars
// claim the same worktree — which, now that sidecars are session-scoped, means
// two sessions.
func sharedWorktrees(sidecars []sidecarInfo) map[groupKey]int {
	counts := make(map[groupKey]int, len(sidecars))
	for _, sc := range sidecars {
		counts[groupOf(sc)]++
	}
	return counts
}

// sessionsPerWorktree counts the sessions in each group — one per sidecar. The
// local runner occupies a row but is not a session, so a worktree with two
// sessions and a local run reads "2 sessions" over three rows rather than three.
func sessionsPerWorktree(sidecars []sidecarInfo) map[groupKey]int {
	counts := make(map[groupKey]int, len(sidecars))
	for _, sc := range sidecars {
		if sc.id != "" {
			counts[groupOf(sc)]++
		}
	}
	return counts
}

// worktreeKey identifies a branch within a repo, with no directory attached. Two
// rows sharing one in different directories are two checkouts of the same
// branch — a second clone, or a worktree that happens to be on the same branch —
// and the branch name alone cannot tell them apart.
type worktreeKey struct {
	repoName string
	branch   string
}

// ambiguousBranches reports which (repo, branch) pairs are checked out in more
// than one directory. Those rows cannot be named by their branch: repoName is the
// basename of the main worktree, so two clones collapse under one repo header and
// would render as two identical branch rows, separable only by a truncated
// sidecar UUID — the very confusion per-session labels were added to remove.
//
// A detached HEAD has no branch to be ambiguous about, and those rows are already
// named by their directory, so they are left alone.
func ambiguousBranches(sidecars []sidecarInfo) map[worktreeKey]bool {
	dirs := map[worktreeKey]map[int]bool{}
	for _, sc := range sidecars {
		if sc.branch == "" {
			continue
		}
		k := worktreeKey{sc.repoName, sc.branch}
		if dirs[k] == nil {
			dirs[k] = map[int]bool{}
		}
		dirs[k][sc.projectIdx] = true
	}
	out := make(map[worktreeKey]bool, len(dirs))
	for k, seen := range dirs {
		if len(seen) > 1 {
			out[k] = true
		}
	}
	return out
}

// worktreeLabels names each directory involved in an ambiguous branch. A row's
// projectIdx fixes its repo and branch — a directory is only ever on one branch
// at a time — so one label per index is unambiguous.
func worktreeLabels(sidecars []sidecarInfo, ambiguous map[worktreeKey]bool) map[int]string {
	paths := map[worktreeKey]map[int]string{}
	for _, sc := range sidecars {
		k := worktreeKey{sc.repoName, sc.branch}
		if !ambiguous[k] {
			continue
		}
		path := sc.projectPath
		if path == "" {
			path = sc.projectName
		}
		if paths[k] == nil {
			paths[k] = map[int]string{}
		}
		paths[k][sc.projectIdx] = path
	}

	labels := map[int]string{}
	for _, byIdx := range paths {
		for idx, label := range shortestUniqueSuffixes(byIdx) {
			labels[idx] = label
		}
	}
	return labels
}

// shortestUniqueSuffixes labels each path with the fewest trailing segments that
// tell them all apart, so the pane spends its 28 columns on the part that
// differs. One segment is usually not enough: two clones of a repo share a
// basename, which is what made the branch ambiguous in the first place.
func shortestUniqueSuffixes(paths map[int]string) map[int]string {
	segs := make(map[int][]string, len(paths))
	longest := 1
	for idx, path := range paths {
		parts := strings.Split(strings.Trim(filepath.ToSlash(path), "/"), "/")
		segs[idx] = parts
		if len(parts) > longest {
			longest = len(parts)
		}
	}

	out := make(map[int]string, len(paths))
	for n := 1; n <= longest; n++ {
		counts := map[string]int{}
		for idx, parts := range segs {
			out[idx] = pathSuffix(parts, n)
			counts[out[idx]]++
		}
		collision := false
		for _, c := range counts {
			if c > 1 {
				collision = true
				break
			}
		}
		if !collision {
			return out
		}
	}
	// Identical paths cannot be told apart; the full path is the best available.
	return out
}

func pathSuffix(parts []string, n int) string {
	if n >= len(parts) {
		return strings.Join(parts, "/")
	}
	return strings.Join(parts[len(parts)-n:], "/")
}

// rowLabel names a row in the left pane. Alone in its worktree a row is its
// branch, as it has always been. Sharing one, the branch has moved to the group
// header and the row names its session instead — the viewer's own session by
// name, since "which of these is mine" is the first thing they need to know.
func (m Model) rowLabel(sc sidecarInfo, multi bool, dirLabel string) string {
	if !multi {
		// dirLabel is set only where the branch is checked out in more than one
		// directory, and there the directory is the only thing that distinguishes
		// this row from its twin.
		if dirLabel != "" {
			return dirLabel
		}
		if sc.branch != "" {
			return sc.branch
		}
		return sidecarDisplayName(sc.name, sc.id)
	}
	// The empty case is listed first deliberately: when the dashboard runs
	// outside a session ownSession is empty too, and an unattributed row must
	// not be mistaken for the viewer's own.
	switch sc.sessionID {
	case "":
		// The local runner, or sidecar state written outside a session or before
		// sessions existed. sidecarDisplayName prefers the full UUID, which does
		// not fit beside the short session labels this row sits next to, so
		// abbreviate it the same way; the full value stays on the detail line.
		if sc.id != "" {
			return "○ " + shortUUID(sc.id)
		}
		return "○ " + sidecarDisplayName(sc.name, sc.id)
	case m.ownSession:
		return "● this session"
	default:
		return "○ " + shortUUID(sc.sessionID)
	}
}

// shortUUID abbreviates a session ID or sidecar UUID for display. Both are
// UUIDs, and the first block is already enough to tell two of them apart on
// screen — the pane is 28 columns wide, so a full one only truncates to noise.
func shortUUID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// mergeBranches collapses entries with the same (repoName, branch, projectIdx) into
// one. The first-seen entry (most recently active after sortByActivity) is the
// primary; subsequent entries contribute their sidecarIDs to the merged set.
// When the primary is a local-only entry (id == "") and a real sidecar is seen
// later, the sidecar's identity and sync state are promoted onto the primary so
// the left pane shows sync status rather than pass/fail.
func mergeBranches(sidecars []sidecarInfo) []sidecarInfo {
	// A local validate run belongs to the worktree, not to any session in it. With
	// one session holding the worktree that distinction is invisible and folding
	// the local row in buys a cleaner pane. With several, the fold would credit
	// one session with runs another developer — or no agent at all — made, so
	// those groups keep the local row separate.
	realPerGroup := map[groupKey]int{}
	for _, sc := range sidecars {
		if sc.id != "" {
			realPerGroup[groupOf(sc)]++
		}
	}

	seen := map[groupKey]int{}
	result := make([]sidecarInfo, 0, len(sidecars))
	for _, sc := range sidecars {
		k := groupOf(sc)
		if idx, ok := seen[k]; !ok {
			result = append(result, sc)
			seen[k] = len(result) - 1
		} else {
			// Only merge when one entry is a local runner (id == ""). Two real
			// sidecars on the same branch belong to different sessions and must
			// stay separate so they are distinguishable in the left pane.
			//
			// This guard, not the key, is what keeps sessions apart. Adding
			// sessionID to the key instead would stop the local runner — which
			// has no session — from ever merging into the sidecar row it belongs
			// to, leaving a duplicate "local" row under every branch.
			if result[idx].id != "" && sc.id != "" {
				result = append(result, sc)
				seen[k] = len(result) - 1
				continue
			}
			// One of the pair is the local runner. Fold it in only where a single
			// session holds the worktree; see realPerGroup above.
			if realPerGroup[k] > 1 {
				result = append(result, sc)
				seen[k] = len(result) - 1
				continue
			}
			result[idx].sidecarIDs = append(result[idx].sidecarIDs, sc.sidecarIDs...)
			// Promote a real sidecar over a local-only primary so the left pane
			// shows sync state rather than a pass/fail badge. The session comes
			// with it: the row now stands for that sidecar, and leaving the
			// session behind would strip the "this session" label off the
			// viewer's own row whenever local activity happened to sort first.
			if result[idx].id == "" && sc.id != "" {
				result[idx].id = sc.id
				result[idx].name = sc.name
				result[idx].sessionID = sc.sessionID
				result[idx].snapshotName = sc.snapshotName
				result[idx].fileMtime = sc.fileMtime
			}
		}
	}
	return result
}

// hasSidecarID reports whether id is in the ids slice.
func hasSidecarID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// effectiveActivity is the sidecar's last event time, falling back to the mtime
// of its state file when no events survive in the window.
func effectiveActivity(sc sidecarInfo) time.Time {
	if !sc.lastActivity.IsZero() {
		return sc.lastActivity
	}
	return sc.fileMtime
}

// indexOfSidecar returns the position of id, or 0 when it is absent.
func indexOfSidecar(sidecars []sidecarInfo, id string) int {
	for i, sc := range sidecars {
		if sc.id == id {
			return i
		}
	}
	return 0
}

// selectedSidecarID returns the id at idx, or "" when idx is out of range.
func selectedSidecarID(sidecars []sidecarInfo, idx int) string {
	if idx < 0 || idx >= len(sidecars) {
		return ""
	}
	return sidecars[idx].id
}

const (
	// activeWindow is how recently a sidecar must have been active to be shown.
	activeWindow = time.Hour
	// fallbackWindow bounds how far back the pane reaches when nothing has been
	// active within activeWindow.
	fallbackWindow = 24 * time.Hour
	// linesPerSidecar is the worst-case row cost of one sidecar in the sidecar
	// pane: name, snapshot, sidecar id, status, age, and the divider to the next
	// row. A shared worktree adds two more for its group header, so this is a
	// floor rather than an exact figure; renderSidecarPane drops whatever does
	// not fit and says so, so an optimistic count costs a "more" hint, not a
	// half-drawn row.
	linesPerSidecar = 6
	// defaultCapacity is used until the first WindowSizeMsg gives a real height.
	defaultCapacity = 5
)

// filterSidecars keeps every sidecar active within activeWindow, with no
// per-project cap, however many that is. When nothing has been active that
// recently it falls back to the most recent sidecars, enough to fill the pane
// and reaching back no further than fallbackWindow, so an idle dashboard still
// shows you where you left off. sidecars must already be sorted by recency.
func filterSidecars(sidecars []sidecarInfo, capacity int) []sidecarInfo {
	now := time.Now()

	var active []sidecarInfo
	for _, sc := range sidecars {
		if within(now, sc, activeWindow) {
			active = append(active, sc)
		}
	}
	if len(active) > 0 {
		return active
	}

	if capacity < 1 {
		capacity = 1
	}
	var recent []sidecarInfo
	for _, sc := range sidecars {
		if len(recent) >= capacity {
			break
		}
		if within(now, sc, fallbackWindow) {
			recent = append(recent, sc)
		}
	}
	return recent
}

// within reports whether the sidecar was active no longer than window ago.
func within(now time.Time, sc sidecarInfo, window time.Duration) bool {
	eff := effectiveActivity(sc)
	return !eff.IsZero() && now.Sub(eff) <= window
}

// sidecarCapacity is how many sidecars the left pane can render at the current
// terminal height. It mirrors renderBody's content height, less the pane title
// and the blank line under it.
func (m Model) sidecarCapacity() int {
	if m.height <= 0 {
		return defaultCapacity
	}
	paneHeight := m.height - 4 - 2
	if paneHeight < linesPerSidecar {
		return 1
	}
	return paneHeight / linesPerSidecar
}

func anyRunning(sidecars []sidecarInfo) bool {
	for _, sc := range sidecars {
		if sc.running {
			return true
		}
	}
	return false
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

// sidecarDisplayName returns the best identifier for a sidecar.
// Prefers the UUID (id) for precise identification; falls back to name when id is empty.
func sidecarDisplayName(name, id string) string {
	if id != "" {
		return id
	}
	if name != "" {
		return name
	}
	return ""
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
