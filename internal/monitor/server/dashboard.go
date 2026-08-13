package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/ipc"
	"github.com/CircleCI-Public/chunk-cli/internal/monitor/pid"
)

var (
	styleHeader      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	styleColHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	styleActive      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleStale       = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleEnded       = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleErr         = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	styleDim         = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleCursorFg    = lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(lipgloss.Color("236"))
	styleValOK       = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleValFail     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	styleTool        = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	styleGitAhead    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))            // orange: ahead of upstream
	styleGitBehind   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))            // red: behind upstream
	styleGitClean    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))            // dim: clean
	styleGitDirty    = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))            // yellow: dirty
	styleGitConflict = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")) // bold red: conflict
)

type dashboardView int

const (
	viewList dashboardView = iota
	viewDetail
)

// Dashboard is the bubbletea model for the monitor dashboard.
type Dashboard struct {
	sessions    []ipc.Session
	selectedIdx int
	events      []ipc.Event
	activeView  dashboardView
	fetchErr    error
	lastFetch   time.Time
	width       int
	height      int
}

type fetchSessionsMsg struct {
	sessions []ipc.Session
	err      error
}

type fetchEventsMsg struct {
	events []ipc.Event
	err    error
}

type tickMsg struct{}

func (m Dashboard) Init() tea.Cmd {
	return tea.Batch(doFetchSessions, doTick())
}

func (m Dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tickMsg:
		if m.activeView == viewList {
			return m, tea.Batch(doFetchSessions, doTick())
		}
		return m, doTick()

	case fetchSessionsMsg:
		m.lastFetch = time.Now()
		m.fetchErr = msg.err
		if msg.err == nil {
			m.sessions = msg.sessions
			if m.selectedIdx >= len(m.sessions) {
				m.selectedIdx = max(0, len(m.sessions)-1)
			}
		}

	case fetchEventsMsg:
		if msg.err == nil {
			m.events = msg.events
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}
	return m, nil
}

func (m Dashboard) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewList:
		switch msg.Code {
		case 'q', tea.KeyEscape:
			return m, tea.Quit
		case 'r':
			return m, doFetchSessions
		case 'j', tea.KeyDown:
			if m.selectedIdx < len(m.sessions)-1 {
				m.selectedIdx++
			}
		case 'k', tea.KeyUp:
			if m.selectedIdx > 0 {
				m.selectedIdx--
			}
		case tea.KeyEnter:
			if len(m.sessions) > 0 {
				m.activeView = viewDetail
				m.events = nil
				return m, doFetchEvents(m.sessions[m.selectedIdx].ID)
			}
		}

	case viewDetail:
		switch msg.Code {
		case 'q':
			return m, tea.Quit
		case tea.KeyEscape, 'b':
			m.activeView = viewList
			return m, doFetchSessions
		}
	}
	return m, nil
}

func (m Dashboard) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "chunk monitor"
	return v
}

func (m Dashboard) render() string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "%s\n", styleHeader.Render("chunk monitor"))
	fmt.Fprintf(&sb, "%s %s   %s %s\n\n",
		styleDim.Render("server:"), daemonStatusStr("server"),
		styleDim.Render("agent:"), daemonStatusStr("agent"))

	if m.activeView == viewDetail && len(m.sessions) > 0 {
		m.renderDetail(&sb)
	} else {
		m.renderList(&sb)
	}
	return sb.String()
}

const (
	colWProject    = 22
	colWGit        = 8
	colWStatus     = 8
	colWTools      = 6
	colWDuration   = 9
	colWValidation = 10
)

func (m Dashboard) renderList(sb *strings.Builder) {
	fmt.Fprintln(sb, styleDim.Render("  j/k  move    enter  detail    r  refresh    q  quit"))
	fmt.Fprintln(sb)

	if m.fetchErr != nil {
		fmt.Fprintln(sb, styleErr.Render("  error: "+m.fetchErr.Error()))
		return
	}

	if len(m.sessions) == 0 {
		fmt.Fprintln(sb, styleDim.Render("  No sessions recorded."))
		if !m.lastFetch.IsZero() {
			fmt.Fprintf(sb, "\n%s\n", styleDim.Render("  refreshed "+m.lastFetch.Format("15:04:05")))
		}
		return
	}

	header := fmt.Sprintf("  %-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s",
		colWProject, "PROJECT",
		colWGit, "GIT",
		colWStatus, "STATUS",
		colWTools, "TOOLS",
		colWDuration, "DURATION",
		colWValidation, "VALIDATION",
		"SESSION",
	)
	fmt.Fprintln(sb, styleColHeader.Render(header))
	fmt.Fprintln(sb, styleDim.Render("  "+strings.Repeat("─", 88)))

	for i, s := range m.sessions {
		project := fmt.Sprintf("%-*s", colWProject, truncate(shortProject(s.ProjectDir), colWProject))
		rawGit := fmt.Sprintf("%-*s", colWGit, s.GitStatus)
		rawStatus := fmt.Sprintf("%-*s", colWStatus, s.Status)
		tools := fmt.Sprintf("%-*d", colWTools, s.ToolUseCount)
		duration := fmt.Sprintf("%-*s", colWDuration, formatDuration(s.StartedAt, s.LastSeenAt))
		rawVal, valStyle := validationParts(s.ValidationStatus)
		val := fmt.Sprintf("%-*s", colWValidation, rawVal)
		id := shortID(s.ID)

		if i == m.selectedIdx {
			plain := fmt.Sprintf("  %s  %s  %s  %s  %s  %s  %s", project, rawGit, rawStatus, tools, duration, val, id)
			rowWidth := m.width
			if rowWidth < 1 {
				rowWidth = 92
			}
			fmt.Fprintln(sb, styleCursorFg.Width(rowWidth).Render(plain))
		} else {
			styledGit := applyGitStyle(s.GitStatus, rawGit)
			styledStatus := applyStatusStyle(s.Status, rawStatus)
			styledVal := valStyle.Render(val)
			fmt.Fprintf(sb, "  %s  %s  %s  %s  %s  %s  %s\n",
				project, styledGit, styledStatus,
				styleDim.Render(tools), styleDim.Render(duration),
				styledVal, styleDim.Render(id))
		}
	}

	if !m.lastFetch.IsZero() {
		fmt.Fprintf(sb, "\n%s\n", styleDim.Render("  refreshed "+m.lastFetch.Format("15:04:05")))
	}
}

func (m Dashboard) renderDetail(sb *strings.Builder) {
	s := m.sessions[m.selectedIdx]
	fmt.Fprintln(sb, styleDim.Render("esc/b: back   q: quit"))
	fmt.Fprintln(sb)
	fmt.Fprintf(sb, "Session:    %s\n", s.ID)
	fmt.Fprintf(sb, "Project:    %s\n", s.ProjectDir)
	fmt.Fprintf(sb, "Status:     %s\n", statusStyled(s.Status))
	fmt.Fprintf(sb, "Git:        %s\n", applyGitStyle(s.GitStatus, gitStatusLabel(s.GitStatus)))
	fmt.Fprintf(sb, "Validation: %s\n", validationDisplay(s.ValidationStatus))
	fmt.Fprintf(sb, "Started:    %s\n", s.StartedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(sb, "Duration:   %s\n", formatDuration(s.StartedAt, s.LastSeenAt))
	fmt.Fprintf(sb, "Tool uses:  %d\n", s.ToolUseCount)
	fmt.Fprintln(sb)

	if len(m.events) == 0 {
		fmt.Fprintln(sb, styleDim.Render("loading events..."))
		return
	}

	fmt.Fprintln(sb, styleHeader.Render("Events"))
	fmt.Fprintln(sb, strings.Repeat("─", 50))
	for _, e := range m.events {
		ts := e.OccurredAt.Local().Format("15:04:05")
		tool := ""
		if e.ToolName != "" {
			tool = "  " + styleTool.Render(e.ToolName)
		}
		fmt.Fprintf(sb, "%s  %-14s%s\n", styleDim.Render(ts), e.EventType, tool)
	}
}

func daemonStatusStr(name string) string {
	pidPath, err := monitor.PIDPath(name)
	if err != nil {
		return styleErr.Render("?")
	}
	running, p, _ := pid.Running(pidPath)
	if running {
		return styleActive.Render(fmt.Sprintf("running (pid %d)", p))
	}
	return styleEnded.Render("stopped")
}

func applyGitStyle(gitStatus, padded string) string {
	switch {
	case gitStatus == gitStatusClean || gitStatus == "":
		return styleGitClean.Render(padded)
	case gitStatus == gitStatusConflict:
		return styleGitConflict.Render(padded)
	case gitStatus == "dirty":
		return styleGitDirty.Render(padded)
	case strings.HasPrefix(gitStatus, "↓"):
		return styleGitBehind.Render(padded)
	default:
		return styleGitAhead.Render(padded)
	}
}

// gitStatusLabel returns a human-readable label for display in the detail view.
func gitStatusLabel(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func applyStatusStyle(status, padded string) string {
	switch status {
	case statusActive:
		return styleActive.Render(padded)
	case "stale": //nolint:goconst
		return styleStale.Render(padded)
	default:
		return styleEnded.Render(padded)
	}
}

func statusStyled(s string) string {
	return applyStatusStyle(s, s)
}

// validationParts returns the display string and the style to apply to it.
func validationParts(s string) (string, lipgloss.Style) {
	switch s {
	case "passed":
		return "✓ passed", styleValOK
	case "failed":
		return "✗ failed", styleValFail
	default:
		return "—", styleDim
	}
}

func validationDisplay(s string) string {
	text, style := validationParts(s)
	return style.Render(text)
}

func shortProject(dir string) string {
	if dir == "" {
		return "—"
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if rel, err := filepath.Rel(home, dir); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + rel
		}
	}
	return filepath.Base(dir)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8] + "..."
	}
	return id
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func formatDuration(start, last time.Time) string {
	d := last.Sub(start)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func doTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func doFetchSessions() tea.Msg {
	sockPath, err := monitor.SocketPath("server")
	if err != nil {
		return fetchSessionsMsg{err: err}
	}
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return fetchSessionsMsg{err: fmt.Errorf("connect to server: %w", err)}
	}
	defer func() { _ = conn.Close() }()
	if err := ipc.Send(conn, ipc.Request{Cmd: ipc.CmdListSessions}); err != nil {
		return fetchSessionsMsg{err: err}
	}
	resp, err := ipc.ReceiveResponse(conn)
	if err != nil {
		return fetchSessionsMsg{err: err}
	}
	if !resp.OK {
		return fetchSessionsMsg{err: fmt.Errorf("server: %s", resp.Error)}
	}
	return fetchSessionsMsg{sessions: resp.Sessions}
}

func doFetchEvents(sessionID string) tea.Cmd {
	return func() tea.Msg {
		sockPath, err := monitor.SocketPath("server")
		if err != nil {
			return fetchEventsMsg{err: err}
		}
		conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
		if err != nil {
			return fetchEventsMsg{err: fmt.Errorf("connect to server: %w", err)}
		}
		defer func() { _ = conn.Close() }()
		if err := ipc.Send(conn, ipc.Request{Cmd: ipc.CmdGetEvents, SessionID: sessionID}); err != nil {
			return fetchEventsMsg{err: err}
		}
		resp, err := ipc.ReceiveResponse(conn)
		if err != nil {
			return fetchEventsMsg{err: err}
		}
		if !resp.OK {
			return fetchEventsMsg{err: fmt.Errorf("server: %s", resp.Error)}
		}
		return fetchEventsMsg{events: resp.Events}
	}
}
