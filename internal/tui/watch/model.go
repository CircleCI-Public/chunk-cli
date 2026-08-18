// Package watch implements the chunk watch TUI dashboard.
package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
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
type sidecarInfo struct {
	id            string
	name          string
	projectName   string
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

type tickMsg struct{}
type spinMsg struct{}

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

	width      int
	height     int
	spinIdx    int
	hasSpinner bool
}

// New creates a Model ready to run. When watchAll is true, each poll also
// checks for projects that have saved a sidecar since the dashboard started
// and adds them, so a sidecar started after `chunk watch --all` launches
// still shows up without a restart.
func New(projects []ProjectEntry, watchAll bool) Model {
	return Model{
		projects: projects,
		offsets:  make([]int64, len(projects)),
		branches: make([]string, len(projects)),
		headRefs: make([]string, len(projects)),
		watchAll: watchAll,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadData, doSpin())
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
		case 'j', tea.KeyDown:
			if m.selectedIdx < len(m.sidecars)-1 {
				m.selectedIdx++
			}
			m.selectedID = selectedSidecarID(m.sidecars, m.selectedIdx)
		case 'k', tea.KeyUp:
			if m.selectedIdx > 0 {
				m.selectedIdx--
			}
			m.selectedID = selectedSidecarID(m.sidecars, m.selectedIdx)
		case 'c':
			if msg.Mod == tea.ModCtrl {
				return m, tea.Quit
			}
		}
		return m, nil

	case dataMsg:
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
		b.WriteString(" " + lPad + " " + vdim("│") + " " + r + "\n")
	}
	return b.String()
}

func (m Model) renderSidecarPane(maxLines int) []string {
	lines := make([]string, 0, maxLines)
	add := func(s string) { lines = append(lines, s) }

	add(vdim("sidecars"))
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

	var lastProject string

	for i, sc := range m.sidecars {
		if len(lines) >= maxLines-2 {
			break
		}

		// Project separator — always shown so each sidecar has a project label.
		if sc.projectName != lastProject {
			if lastProject != "" {
				add("")
			}
			label := truncate(sc.projectName, leftPaneWidth-4)
			add(vdim("── " + label))
			if sc.snapshotName != "" {
				add("   " + vdim("◈ "+truncate(sc.snapshotName, leftPaneWidth-6)))
			}
			lastProject = sc.projectName
		}

		selected := i == m.selectedIdx
		nameLine := truncate(sidecarDisplayName(sc.name, sc.id), leftPaneWidth-3)

		if selected {
			add(muted("▶ ") + bold(nameLine))
		} else {
			add("  " + nameLine)
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

		// Suppress dotted divider when next sidecar starts a new project group.
		if i < len(m.sidecars)-1 && m.sidecars[i+1].projectName == sc.projectName {
			add(vdim(strings.Repeat("·", leftPaneWidth)))
		}
	}
	return lines
}

func (m Model) renderActivityPane(maxLines int) []string {
	lines := make([]string, 0, maxLines)

	title := vdim("activity")
	if m.selectedIdx < len(m.sidecars) {
		sc := m.sidecars[m.selectedIdx]
		displayName := sidecarDisplayName(sc.name, sc.id)
		if sc.projectName != "" {
			title += "  " + muted(sc.projectName+"/"+displayName)
		} else {
			title += "  " + muted(displayName)
		}
	}
	lines = append(lines, title, "")

	var filtered []eventlog.Event
	if m.selectedIdx < len(m.sidecars) {
		sc := m.sidecars[m.selectedIdx]
		if sc.projectIdx < len(m.events) {
			for _, e := range m.events[sc.projectIdx] {
				if e.SidecarID == sc.id {
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

	return append(lines, buildActivityLines(filtered, maxLines-2)...)
}

// buildActivityLines renders events newest-first, grouped by op-run.
func buildActivityLines(events []eventlog.Event, maxLines int) []string {
	groups := groupEvents(events)
	rendered := make([]string, 0, maxLines)
	add := func(s string) { rendered = append(rendered, s) }

	for gi := len(groups) - 1; gi >= 0 && len(rendered) < maxLines; gi-- {
		if gi < len(groups)-1 {
			add(vdim(strings.Repeat("─", 44)))
		}
		g := groups[gi]
		for ei := len(g) - 1; ei >= 0 && len(rendered) < maxLines; ei-- {
			add(renderEvent(g[ei]))
		}
	}
	return rendered
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
	ts := vdim(e.Ts.Format("15:04:05"))
	op := opTag(e.Op)
	icon, msg := iconAndMsg(e)
	return ts + "  " + op + "  " + icon + "  " + msg
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
	keys := []struct{ key, action string }{
		{"↑/↓ j/k", "select"},
		{"q", "quit"},
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, vdim(k.key)+" "+dim(k.action))
	}
	bar := strings.Join(parts, "  "+vdim("·")+"  ")
	return vdim(strings.Repeat("─", m.width)) + "\n" + "  " + bar + "\n"
}

// loadData reads sidecar state files and new event log entries from all projects.
func (m Model) loadData() tea.Msg {
	projects := m.projects
	if m.watchAll {
		projects = discoverNewProjects(projects)
	}

	var allSidecars []sidecarInfo

	// Build per-project event slices, preserving existing events across polls.
	allEventsByProject := make([][]eventlog.Event, len(projects))
	for i := range allEventsByProject {
		if i < len(m.events) {
			allEventsByProject[i] = m.events[i]
		}
	}

	newOffsets := make([]int64, len(projects))
	copy(newOffsets, m.offsets)
	newBranches := make([]string, len(projects))
	newHeadRefs := make([]string, len(projects))

	for i, p := range projects {
		newBranches[i] = sidecar.CurrentBranch(p.ProjectRoot)
		newHeadRefs[i] = headRef(p.ProjectRoot)
		snapName := loadSnapshotName(p.DataDir)
		sidecars := loadSidecars(p.DataDir, p.ProjectRoot, snapName, newHeadRefs[i], i)
		allSidecars = append(allSidecars, sidecars...)

		// Synthesize a local entry for this project; filtered out later if no activity.
		allSidecars = append(allSidecars, sidecarInfo{
			id:          "",
			name:        "local",
			projectName: filepath.Base(p.ProjectRoot),
			projectIdx:  i,
		})

		if p.Log == nil {
			continue
		}
		// i can exceed the prior slice length for a project discovered this poll.
		var priorOffset int64
		if i < len(m.offsets) {
			priorOffset = m.offsets[i]
		}
		fresh, newOff, _ := p.Log.TailFrom(priorOffset)
		allEventsByProject[i] = capEvents(allEventsByProject[i], fresh)
		newOffsets[i] = newOff
	}

	annotateActivity(allSidecars, allEventsByProject)
	sortByActivity(allSidecars)
	allSidecars = filterSidecars(allSidecars, m.sidecarCapacity())

	return dataMsg{projects: projects, sidecars: allSidecars, events: allEventsByProject, offsets: newOffsets, branches: newBranches, headRefs: newHeadRefs}
}

// discoverNewProjects returns known plus any project whose data directory
// exists (per sidecar.AllProjectRoots) but isn't in known yet, each opened as
// a new ProjectEntry. Roots that fail to open are skipped rather than
// aborting the whole poll.
func discoverNewProjects(known []ProjectEntry) []ProjectEntry {
	roots, err := sidecar.AllProjectRoots()
	if err != nil {
		return known
	}
	seen := make(map[string]bool, len(known))
	for _, p := range known {
		seen[p.ProjectRoot] = true
	}
	for _, root := range roots {
		if seen[root] {
			continue
		}
		dataDir, err := config.ProjectDataDir(root)
		if err != nil {
			continue
		}
		el, err := eventlog.Open(dataDir)
		if err != nil {
			continue
		}
		known = append(known, ProjectEntry{Log: el, DataDir: dataDir, ProjectRoot: root})
		seen[root] = true
	}
	return known
}

// capEvents appends fresh to prior, keeping at most recentEvents. The cap is
// applied per project so a project with a long history cannot evict another
// project's recent activity.
func capEvents(prior, fresh []eventlog.Event) []eventlog.Event {
	merged := make([]eventlog.Event, 0, len(prior)+len(fresh))
	merged = append(merged, prior...)
	merged = append(merged, fresh...)
	if len(merged) > recentEvents {
		merged = merged[len(merged)-recentEvents:]
	}
	return merged
}

// annotateActivity fills lastActivity, lastOp, lastLevel and running from the
// newest event belonging to each sidecar.
func annotateActivity(sidecars []sidecarInfo, eventsByProject [][]eventlog.Event) {
	for i := range sidecars {
		sc := &sidecars[i]
		if sc.projectIdx >= len(eventsByProject) {
			continue
		}
		events := eventsByProject[sc.projectIdx]
		for j := len(events) - 1; j >= 0; j-- {
			e := events[j]
			if e.SidecarID != sc.id {
				continue
			}
			sc.lastActivity = e.Ts
			sc.lastOp = e.Op
			sc.lastLevel = e.Level
			if e.Level != levelDone && e.Level != levelError && time.Since(e.Ts) < runningTimeout {
				sc.running = true
			}
			break
		}
	}
}

// sortByActivity puts the most recently active project first, and within a
// project the most recently active sidecar first. Projects stay grouped so the
// sidecar pane still renders one header per project.
func sortByActivity(sidecars []sidecarInfo) {
	latest := map[string]time.Time{}
	for _, sc := range sidecars {
		if eff := effectiveActivity(sc); eff.After(latest[sc.projectName]) {
			latest[sc.projectName] = eff
		}
	}
	sort.SliceStable(sidecars, func(i, j int) bool {
		a, b := sidecars[i], sidecars[j]
		if a.projectName != b.projectName {
			return latest[a.projectName].After(latest[b.projectName])
		}
		return effectiveActivity(a).After(effectiveActivity(b))
	})
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

// loadSidecars reads all sidecar*.json files from dataDir, deduplicates by ID.
func loadSidecars(dataDir, projectRoot string, snapshotName string, head string, projectIdx int) []sidecarInfo {
	matches, _ := filepath.Glob(filepath.Join(dataDir, "sidecar*.json"))
	projectName := filepath.Base(projectRoot)
	seen := map[string]bool{}
	var result []sidecarInfo
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var as sidecar.ActiveSidecar
		if json.Unmarshal(data, &as) != nil || as.SidecarID == "" {
			continue
		}
		if seen[as.SidecarID] {
			continue
		}
		seen[as.SidecarID] = true
		inSync := head != "" && as.LastSyncedRef != "" && head == as.LastSyncedRef
		var mtime time.Time
		if fi, err := os.Stat(path); err == nil {
			mtime = fi.ModTime()
		}
		result = append(result, sidecarInfo{
			id:            as.SidecarID,
			name:          as.Name,
			projectName:   projectName,
			projectIdx:    projectIdx,
			snapshotName:  snapshotName,
			fileMtime:     mtime,
			lastSyncedRef: as.LastSyncedRef,
			inSync:        inSync,
		})
	}
	return result
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

// loadSnapshotName returns the Name field from any snapshot*.json in dataDir,
// or "" if none is found or the name is not set.
func loadSnapshotName(dataDir string) string {
	matches, _ := filepath.Glob(filepath.Join(dataDir, "snapshot*.json"))
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var snap struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &snap) == nil && snap.Name != "" {
			return snap.Name
		}
	}
	return ""
}

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

// headRef returns the full HEAD SHA for the git repo at dir.
func headRef(dir string) string {
	if dir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sha, _ := gitutil.HeadRefCtx(ctx, dir)
	return sha
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
