// Package watch implements the chunk watch TUI dashboard.
package watch

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
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

// color/style helpers using lipgloss
var (
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleBold   = lipgloss.NewStyle().Bold(true)
	styleGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	styleYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleBlue   = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	stylePurple = lipgloss.NewStyle().Foreground(lipgloss.Color("140"))
	styleTeal   = lipgloss.NewStyle().Foreground(lipgloss.Color("80"))
	styleOrange = lipgloss.NewStyle().Foreground(lipgloss.Color("173"))
	styleAmber  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	styleMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("242"))
	styleVdim   = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)

func dim(s string) string    { return styleDim.Render(s) }
func bold(s string) string   { return styleBold.Render(s) }
func green(s string) string  { return styleGreen.Render(s) }
func yellow(s string) string { return styleYellow.Render(s) }
func blue(s string) string   { return styleBlue.Render(s) }
func purple(s string) string { return stylePurple.Render(s) }
func teal(s string) string   { return styleTeal.Render(s) }
func orange(s string) string { return styleOrange.Render(s) }
func amber(s string) string  { return styleAmber.Render(s) }
func red(s string) string    { return styleRed.Render(s) }
func muted(s string) string  { return styleMuted.Render(s) }
func vdim(s string) string   { return styleVdim.Render(s) }

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
	id            string
	sidecarIDs    []string // all IDs to match when filtering events
	name          string
	projectName   string
	repoName      string    // basename of the main worktree (groups linked worktrees together)
	branch        string    // current git branch for this worktree
	projectIdx    int       // index into Model.projects
	snapshotName  string    // name of the active snapshot for this project, if any
	fileMtime     time.Time // mtime of the sidecar state file (fallback when no events yet)
	lastSyncedRef string
	inSync        bool
	running       bool
	lastActivity  time.Time
	lastOp        eventlog.Op
	lastLevel     string // level of the most recent event ("done", "error", etc.)
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

	updateAvailable string // non-empty tag (e.g. "v1.2.3") when an update is available
	upgradeCmd      string // "chunk upgrade" or "brew upgrade chunk"
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
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadData, doSpin(), checkUpdateCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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

func (m Model) render() string {
	if m.width == 0 {
		return "loading...\n"
	}
	return m.renderHeader() +
		m.renderSeparator() +
		m.renderBody() +
		m.renderFooter()
}

func (m Model) renderHeader() string {
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
		contextTag = "  " + muted(fmt.Sprintf("%d projects", len(m.projects)))
	} else if len(m.projects) == 1 {
		branch := m.branches[0]
		head := m.headRefs[0]
		if branch != "" && head != "" {
			contextTag = "  " + green(branch+"@"+head[:min(7, len(head))])
		}
	}

	clock := time.Now().Format("15:04:05")
	title := bold("chunk watch") + "  " + muted(count) + contextTag
	right := vdim(clock)
	gap := m.width - lipgloss.Width(title) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return title + strings.Repeat(" ", gap) + right + "\n"
}

func (m Model) renderSeparator() string {
	return vdim(strings.Repeat("─", m.width)) + "\n"
}

func (m Model) renderBody() string {
	contentHeight := m.height - 4 // header + separator + footer + padding
	if contentHeight < 1 {
		contentHeight = 1
	}

	leftLines := m.renderSidecarPane(contentHeight)
	rightLines := m.renderActivityPane(contentHeight)

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
		div := vdim("│")
		if m.focusedPane == paneRight {
			div = muted("│")
		}
		b.WriteString(" " + lPad + " " + div + " " + r + "\n")
	}
	return b.String()
}

func (m Model) renderSidecarPane(maxLines int) []string {
	lines := make([]string, 0, maxLines)
	add := func(s string) { lines = append(lines, s) }

	if m.focusedPane == paneLeft {
		add(bold("sidecars"))
	} else {
		add(vdim("sidecars"))
	}
	add("")

	if len(m.sidecars) == 0 {
		// Sidecars may well exist, they just haven't done anything recent
		// enough to be worth showing.
		add(muted("nothing recent"))
		add("")
		add(dim("no sidecar activity"))
		add(dim("in the last day"))
		return lines
	}

	var lastRepo string

	for i, sc := range m.sidecars {
		if len(lines) >= maxLines-2 {
			break
		}

		// Repo separator — groups all worktrees of the same repo under one header.
		if sc.repoName != lastRepo {
			if lastRepo != "" {
				add("")
			}
			label := truncate(sc.repoName, leftPaneWidth-4)
			add(vdim("── ") + bold(label))
			lastRepo = sc.repoName
		}

		selected := i == m.selectedIdx
		branchLabel := sc.branch
		if branchLabel == "" {
			branchLabel = sidecarDisplayName(sc.name, sc.id)
		}
		nameLine := truncate(branchLabel, leftPaneWidth-3)

		if selected {
			if m.focusedPane == paneLeft {
				add(green("▶ ") + bold(nameLine))
			} else {
				add(vdim("▶ ") + muted(nameLine))
			}
		} else {
			add("  " + nameLine)
		}

		if sc.snapshotName != "" {
			add("   " + vdim("◈ "+truncate(sc.snapshotName, leftPaneWidth-6)))
		}
		if sc.id != "" && sc.name != "" {
			add("   " + vdim(truncate(sidecarDisplayName(sc.name, sc.id), leftPaneWidth-6)))
		}

		switch {
		case sc.running:
			frame := spinFrames[m.spinIdx%len(spinFrames)]
			add("  " + blue(frame+" "+string(sc.lastOp)+"..."))
		case sc.id == "": // local runner — no sync state
			switch sc.lastLevel {
			case levelDone:
				add("  " + green("✓ passed"))
			case levelError:
				add("  " + red("✗ failed"))
			default:
				add("  " + muted("no runs yet"))
			}
		case sc.inSync:
			add("  " + green("✓ in sync"))
		case sc.lastSyncedRef == "":
			add("  " + muted("not synced"))
		default:
			add("  " + yellow("↑ needs sync"))
		}

		if !sc.lastActivity.IsZero() {
			add("  " + vdim(ago(sc.lastActivity)))
		} else {
			add("")
		}

		// Divider between sidecars in the same repo group.
		if i < len(m.sidecars)-1 && m.sidecars[i+1].repoName == sc.repoName {
			add(muted(strings.Repeat("─", leftPaneWidth)))
		}
	}
	return lines
}

func (m Model) renderActivityPane(maxLines int) []string {
	lines := make([]string, 0, maxLines)

	var title string
	if m.focusedPane == paneRight {
		title = bold("activity")
	} else {
		title = vdim("activity")
	}
	if m.selectedIdx < len(m.sidecars) {
		sc := m.sidecars[m.selectedIdx]
		branchLabel := sc.branch
		if branchLabel == "" {
			branchLabel = sidecarDisplayName(sc.name, sc.id)
		}
		if sc.repoName != "" {
			title += "  " + muted(sc.repoName+"/"+branchLabel)
		} else {
			title += "  " + muted(branchLabel)
		}
		if sc.id != "" {
			title += "  " + vdim(sidecarDisplayName(sc.name, sc.id))
		}
	}
	lines = append(lines, title, "")

	var filtered []eventlog.Event
	if m.selectedIdx < len(m.sidecars) {
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
		lines = append(lines, muted("No activity yet."), "")
		for _, l := range logoLines {
			lines = append(lines, padStr+vdim(l))
		}
		return lines
	}

	groups := groupByInvocation(filtered)
	rightSel := m.rightSelectedIdx
	if len(groups) > 0 && rightSel >= len(groups) {
		rightSel = len(groups) - 1
	}
	body, remaining := m.buildCollapsibleLines(groups, rightSel, maxLines-2)
	lines = append(lines, body...)
	if remaining > 0 {
		lines = append(lines, "  "+vdim(fmt.Sprintf("↓ %d more", remaining)))
	}
	return lines
}

// buildCollapsibleLines renders invocation groups newest-first with expand/collapse state.
// It returns the rendered lines and the count of groups that did not fit.
func (m Model) buildCollapsibleLines(groups []invocationGroup, rightSel, maxLines int) ([]string, int) {
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
		add(renderInvocationHeader(groups[gi], expanded, selected))
		if expanded {
			g := groups[gi]
			for ei := len(g.events) - 1; ei >= 0 && len(rendered) < maxLines; ei-- {
				e := g.events[ei]
				if isSummaryEvent(e) {
					continue // already shown in header
				}
				add("  " + renderEvent(e))
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
	if m.selectedIdx >= len(m.sidecars) {
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
			return "✓", count, levelDone
		}
		return "✗", count, levelError
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
func renderInvocationHeader(g invocationGroup, expanded, selected bool) string {
	var arrow string
	if expanded {
		arrow = "▼ "
	} else {
		arrow = "▶ "
	}
	if selected {
		arrow = bold(arrow)
	} else {
		arrow = vdim(arrow)
	}

	icon, label, level := outcomeOf(g)
	var outcomeStr string
	switch level {
	case levelDone:
		outcomeStr = green(icon + " " + label)
	case levelError:
		outcomeStr = red(icon + " " + label)
	default:
		outcomeStr = blue(icon + " " + label)
	}

	var ts time.Time
	if n := len(g.events); n > 0 {
		ts = g.events[n-1].Ts
	}
	tsStr := vdim(ts.Format("15:04:05"))

	durStr := ""
	if len(g.events) > 1 {
		dur := g.events[len(g.events)-1].Ts.Sub(g.events[0].Ts).Round(time.Second)
		if dur >= time.Second {
			durStr = "  " + vdim("("+dur.String()+")")
		}
	}

	label2 := purple("validate") + "  " + outcomeStr + "  " + tsStr + durStr
	if selected {
		label2 = bold(purple("validate")) + "  " + outcomeStr + "  " + tsStr + durStr
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

func renderEvent(e eventlog.Event) string {
	icon, msg := iconAndMsg(e)
	if e.Op == eventlog.OpValidate {
		// Op tag omitted — already implied by the invocation header.
		return icon + "  " + msg
	}
	return opTag(e.Op) + "  " + icon + "  " + msg
}

func opTag(op eventlog.Op) string {
	switch op {
	case eventlog.OpSync:
		return blue("sync    ")
	case eventlog.OpValidate:
		return purple("validate")
	case eventlog.OpExec:
		return teal("exec    ")
	case eventlog.OpSetup:
		return orange("setup   ")
	case eventlog.OpHook:
		return amber("hook    ")
	default:
		return muted(fmt.Sprintf("%-8s", string(op)))
	}
}

func iconAndMsg(e eventlog.Event) (string, string) {
	switch e.Level {
	case levelDone:
		return green("✓"), green(e.Msg)
	case "warn":
		return yellow("⚠"), yellow(e.Msg)
	case levelError:
		return red("✗"), red(e.Msg)
	default:
		if strings.HasPrefix(e.Msg, "$ ") {
			return muted("$"), muted(strings.TrimPrefix(e.Msg, "$ "))
		}
		return vdim("›"), muted(e.Msg)
	}
}

func (m Model) renderFooter() string {
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
		parts = append(parts, vdim(k.key)+" "+dim(k.action))
	}
	bar := strings.Join(parts, "  "+vdim("·")+"  ")

	// Right-align the update notice, but drop it entirely when it does not
	// fit: padding it onto an over-long bar would wrap the footer and break
	// the fixed-height layout.
	if m.updateAvailable != "" {
		notice := amber("↑ "+m.updateAvailable) + "  " + dim(m.upgradeCmd)
		if gap := m.width - 2 - lipgloss.Width(bar) - lipgloss.Width(notice); gap >= 2 {
			bar += strings.Repeat(" ", gap) + notice
		}
	}

	footer := vdim(strings.Repeat("─", m.width)) + "\n" + "  " + bar + "\n"
	if m.daemonErr != nil {
		footer += "  " + red("daemon unavailable: "+m.daemonErr.Error()) + "\n"
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
func sortByActivity(sidecars []sidecarInfo) {
	latest := map[string]time.Time{}
	for _, sc := range sidecars {
		if eff := effectiveActivity(sc); eff.After(latest[sc.repoName]) {
			latest[sc.repoName] = eff
		}
	}
	sort.SliceStable(sidecars, func(i, j int) bool {
		a, b := sidecars[i], sidecars[j]
		if a.repoName != b.repoName {
			return latest[a.repoName].After(latest[b.repoName])
		}
		return effectiveActivity(a).After(effectiveActivity(b))
	})
}

// mergeBranches collapses entries with the same (repoName, branch, projectIdx) into
// one. The first-seen entry (most recently active after sortByActivity) is the
// primary; subsequent entries contribute their sidecarIDs to the merged set.
// When the primary is a local-only entry (id == "") and a real sidecar is seen
// later, the sidecar's identity and sync state are promoted onto the primary so
// the left pane shows sync status rather than pass/fail.
func mergeBranches(sidecars []sidecarInfo) []sidecarInfo {
	type key struct {
		repoName   string
		branch     string
		projectIdx int
	}
	seen := map[key]int{}
	result := make([]sidecarInfo, 0, len(sidecars))
	for _, sc := range sidecars {
		k := key{sc.repoName, sc.branch, sc.projectIdx}
		if idx, ok := seen[k]; !ok {
			result = append(result, sc)
			seen[k] = len(result) - 1
		} else {
			// Only merge when one entry is a local runner (id == ""). Two real
			// sidecars on the same branch belong to different sessions and must
			// stay separate so they are distinguishable in the left pane.
			if result[idx].id != "" && sc.id != "" {
				result = append(result, sc)
				seen[k] = len(result) - 1
				continue
			}
			result[idx].sidecarIDs = append(result[idx].sidecarIDs, sc.sidecarIDs...)
			// Promote a real sidecar over a local-only primary so the left pane
			// shows sync state rather than a pass/fail badge.
			if result[idx].id == "" && sc.id != "" {
				result[idx].id = sc.id
				result[idx].name = sc.name
				result[idx].inSync = sc.inSync
				result[idx].lastSyncedRef = sc.lastSyncedRef
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
	// pane: project header, name, status, age, and the gap between projects.
	linesPerSidecar = 5
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

// sidecarDisplayName returns the best human-readable name for a sidecar.
// Uses Name if set, otherwise strips a UUID suffix from the ID.
func sidecarDisplayName(name, id string) string {
	if name != "" {
		return name
	}
	if clean := stripUUIDSuffix(id); clean != "" && clean != id {
		return clean
	}
	return id
}

// stripUUIDSuffix removes a "-<8hex>-<4hex>-..." UUID portion from s.
// "chunk-cli-2d66488f-e67f-4c3a-9abc-112233445566" → "chunk-cli"
func stripUUIDSuffix(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			continue
		}
		rest := s[i+1:]
		if len(rest) >= 9 && isHex8(rest[:8]) && rest[8] == '-' && i > 0 {
			return s[:i]
		}
	}
	return s
}

// isHex8 reports whether s is exactly 8 lowercase hex characters.
func isHex8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
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
