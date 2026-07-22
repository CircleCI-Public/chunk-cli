// Package watch implements the chunk watch TUI dashboard.
package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

const (
	leftPaneWidth  = 28
	divider        = " │ "
	pollInterval   = 2 * time.Second
	spinInterval   = 160 * time.Millisecond
	runningTimeout = 5 * time.Minute
	recentEvents   = 300

	levelDone  = "done"
	levelError = "error"
)

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// color helpers — always enabled (watch requires a TTY)
func fg(code, s string) string { return "\x1b[" + code + "m" + s + "\x1b[0m" }

func dim(s string) string    { return fg("2", s) }
func bold(s string) string   { return fg("1", s) }
func green(s string) string  { return fg("38;5;78", s) }
func yellow(s string) string { return fg("38;5;179", s) }
func blue(s string) string   { return fg("38;5;110", s) }
func purple(s string) string { return fg("38;5;140", s) }
func teal(s string) string   { return fg("38;5;80", s) }
func orange(s string) string { return fg("38;5;173", s) }
func red(s string) string    { return fg("38;5;167", s) }
func muted(s string) string  { return fg("38;5;242", s) }
func vdim(s string) string   { return fg("38;5;238", s) }

// sidecarInfo holds display state for one sidecar.
type sidecarInfo struct {
	id            string
	name          string
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
	events   []eventlog.Event
	offset   int64
}

// Model is the BubbleTea model for the watch dashboard.
type Model struct {
	log         *eventlog.Log
	dataDir     string
	projectRoot string

	sidecars    []sidecarInfo
	selectedIdx int
	events      []eventlog.Event
	logOffset   int64

	width      int
	height     int
	spinIdx    int
	hasSpinner bool
}

// New creates a Model ready to run.
func New(log *eventlog.Log, dataDir, projectRoot string) Model {
	return Model{log: log, dataDir: dataDir, projectRoot: projectRoot}
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
		case 'k', tea.KeyUp:
			if m.selectedIdx > 0 {
				m.selectedIdx--
			}
		case 'c':
			if msg.Mod == tea.ModCtrl {
				return m, tea.Quit
			}
		}
		return m, nil

	case dataMsg:
		m.sidecars = msg.sidecars
		m.events = msg.events
		m.logOffset = msg.offset
		if m.selectedIdx >= len(m.sidecars) && len(m.sidecars) > 0 {
			m.selectedIdx = len(m.sidecars) - 1
		}
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
	count := fmt.Sprintf("%d sidecar", len(m.sidecars))
	if len(m.sidecars) != 1 {
		count += "s"
	}
	branch := sidecar.CurrentBranch(m.projectRoot)
	head := headRef(m.projectRoot)
	branchTag := ""
	if branch != "" && head != "" {
		branchTag = "  " + green(branch+"@"+head[:min(7, len(head))])
	}
	clock := time.Now().Format("15:04:05")
	title := bold("chunk watch") + "  " + muted(count) + branchTag
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
		add(muted("no sidecars yet"))
		add("")
		add(dim("chunk sidecar create"))
		return lines
	}

	for i, sc := range m.sidecars {
		if len(lines) >= maxLines-2 {
			break
		}
		selected := i == m.selectedIdx
		name := truncate(sc.name, leftPaneWidth-3)
		if name == "" {
			name = truncate(sc.id, leftPaneWidth-3)
		}
		if selected {
			add(muted("▶ ") + bold(name))
		} else {
			add("  " + name)
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

		if i < len(m.sidecars)-1 {
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
		name := sc.name
		if name == "" {
			name = sc.id
		}
		title += "  " + muted(name)
	}
	lines = append(lines, title, "")

	var filtered []eventlog.Event
	if m.selectedIdx < len(m.sidecars) {
		id := m.sidecars[m.selectedIdx].id
		for _, e := range m.events {
			if e.SidecarID == id {
				filtered = append(filtered, e)
			}
		}
	}

	if len(filtered) == 0 {
		return append(lines, vdim("no activity yet"))
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
			cur = make(eventGroup, 0, len(events)-len(groups))
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
		return vdim("›"), muted(e.Msg)
	}
}

func (m Model) renderFooter() string {
	keys := []struct{ key, action string }{
		{"j/k", "select"},
		{"q", "quit"},
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, vdim(k.key)+" "+dim(k.action))
	}
	bar := strings.Join(parts, "  "+vdim("·")+"  ")
	return vdim(strings.Repeat("─", m.width)) + "\n" + "  " + bar + "\n"
}

// loadData reads sidecar state files and new event log entries.
func (m Model) loadData() tea.Msg {
	sidecars := loadSidecars(m.dataDir, m.projectRoot)

	events := m.events
	offset := m.logOffset
	if m.log != nil {
		if offset == 0 {
			recent, _ := m.log.Recent(recentEvents)
			events = recent
			_, newOffset, _ := m.log.TailFrom(0)
			offset = newOffset
		} else {
			newEvents, newOffset, _ := m.log.TailFrom(offset)
			events = append(events, newEvents...)
			offset = newOffset
			if len(events) > recentEvents {
				events = events[len(events)-recentEvents:]
			}
		}
	}

	for i := range sidecars {
		sc := &sidecars[i]
		for j := len(events) - 1; j >= 0; j-- {
			e := events[j]
			if e.SidecarID != sc.id {
				continue
			}
			if sc.lastActivity.IsZero() {
				sc.lastActivity = e.Ts
				sc.lastOp = e.Op
			}
			if e.Level != levelDone && e.Level != levelError && time.Since(e.Ts) < runningTimeout {
				sc.running = true
			}
			break
		}
	}

	return dataMsg{sidecars: sidecars, events: events, offset: offset}
}

// loadSidecars reads all sidecar*.json files from dataDir, deduplicates by ID.
func loadSidecars(dataDir, projectRoot string) []sidecarInfo {
	matches, _ := filepath.Glob(filepath.Join(dataDir, "sidecar*.json"))
	head := headRef(projectRoot)
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
		result = append(result, sidecarInfo{
			id:            as.SidecarID,
			name:          as.Name,
			lastSyncedRef: as.LastSyncedRef,
			inSync:        inSync,
		})
	}
	return result
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

// headRef returns the full HEAD SHA for the git repo at dir.
func headRef(dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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
