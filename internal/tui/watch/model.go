// Package watch implements the chunk watch TUI dashboard.
package watch

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

const (
	leftPaneWidth = 28
	spinInterval  = 160 * time.Millisecond

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

// ProjectEntry identifies a project to include in the watch dashboard.
type ProjectEntry struct {
	ProjectRoot string
}

// sidecarInfo holds display state for one sidecar.
type sidecarInfo struct {
	id            string
	name          string
	projectName   string
	snapshotName  string
	fileMtime     time.Time
	lastSyncedRef string
	inSync        bool
	running       bool
	lastActivity  time.Time
	lastOp        eventlog.Op
}

type tickMsg struct{}
type spinMsg struct{}

type dataMsg struct {
	sidecars []sidecarInfo
	events   [][]eventlog.Event
	branches []string
	headRefs []string
}

// Model is the BubbleTea model for the watch dashboard.
type Model struct {
	projects []ProjectEntry
	branches []string // current branch per project, refreshed each poll
	headRefs []string // HEAD SHA per project, refreshed each poll

	sidecars    []sidecarInfo
	selectedIdx int
	selectedID  string             // id of the selected sidecar, so selection survives reordering
	events      [][]eventlog.Event // per project, capped independently

	width      int
	height     int
	spinIdx    int
	hasSpinner bool
}

// New creates a Model ready to run.
func New(projects []ProjectEntry) Model {
	return Model{
		projects: projects,
		branches: make([]string, len(projects)),
		headRefs: make([]string, len(projects)),
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
		m.sidecars = msg.sidecars
		m.events = msg.events
		m.branches = msg.branches
		m.headRefs = msg.headRefs
		// Sidecars are re-sorted by recency each poll, so track the selection
		// by id. An unknown id (first poll, or the sidecar aged out) falls back
		// to index 0, the most recently active sidecar.
		m.selectedIdx = indexOfSidecar(m.sidecars, m.selectedID)
		m.selectedID = selectedSidecarID(m.sidecars, m.selectedIdx)
		m.hasSpinner = anyRunning(m.sidecars)
		return m, tea.Tick(watchd.PollInterval, func(time.Time) tea.Msg { return tickMsg{} })

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
	count := fmt.Sprintf("%d sidecar", len(m.sidecars))
	if len(m.sidecars) != 1 {
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
		add(muted("nothing active"))
		add("")
		add(dim("no sidecar activity"))
		add(dim("in the last hour"))
		return lines
	}

	var lastProject string

	for i, sc := range m.sidecars {
		if len(lines) >= maxLines-2 {
			break
		}

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
		id := m.sidecars[m.selectedIdx].id
		for _, evs := range m.events {
			for _, e := range evs {
				if e.SidecarID == id {
					filtered = append(filtered, e)
				}
			}
		}
	}

	if len(filtered) == 0 {
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

// loadData fetches the current snapshot from the watch daemon and returns a
// dataMsg with the display-ready state.
func (m Model) loadData() tea.Msg {
	roots := make([]string, len(m.projects))
	for i, p := range m.projects {
		roots[i] = p.ProjectRoot
	}

	snap, err := watchd.FetchSnapshot(roots)
	if err != nil {
		// Daemon not available; return an empty update so the TUI retries next tick.
		return dataMsg{}
	}

	return buildDataMsg(snap, m.projects)
}

// buildDataMsg maps a Snapshot onto a dataMsg, preserving the project ordering
// from entries.
func buildDataMsg(snap watchd.Snapshot, entries []ProjectEntry) dataMsg {
	byRoot := make(map[string]watchd.ProjectSnapshot, len(snap.Projects))
	for _, p := range snap.Projects {
		byRoot[p.Root] = p
	}

	events := make([][]eventlog.Event, len(entries))
	branches := make([]string, len(entries))
	headRefs := make([]string, len(entries))
	var allSidecars []sidecarInfo

	for i, entry := range entries {
		p, ok := byRoot[entry.ProjectRoot]
		if !ok {
			continue
		}
		branches[i] = p.Branch
		headRefs[i] = p.HeadRef
		events[i] = p.Events
		for _, ss := range p.Sidecars {
			allSidecars = append(allSidecars, sidecarFromState(ss))
		}
	}

	return dataMsg{
		sidecars: allSidecars,
		events:   events,
		branches: branches,
		headRefs: headRefs,
	}
}

// sidecarFromState maps a daemon SidecarState to the TUI's internal sidecarInfo.
func sidecarFromState(ss watchd.SidecarState) sidecarInfo {
	return sidecarInfo{
		id:            ss.ID,
		name:          ss.Name,
		projectName:   ss.ProjectName,
		snapshotName:  ss.SnapshotName,
		fileMtime:     ss.FileMtime,
		lastSyncedRef: ss.LastSyncedRef,
		inSync:        ss.InSync,
		running:       ss.Running,
		lastActivity:  ss.LastActivity,
		lastOp:        ss.LastOp,
	}
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
