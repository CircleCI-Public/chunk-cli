package statusview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/daemon"
)

// Model is a full-screen BubbleTea status view for the chunk agent status pane.
type Model struct {
	daemonClient *daemon.Client
	sidecarID    string
	sidecarName  string

	sidecars    map[string]*daemon.SidecarState
	history     []*daemon.InvocationSummary
	activeSteps []string
	daemonCh    <-chan daemon.SSEEvent

	width  int
	height int
}

// New creates a statusview Model.
func New(sidecarID, sidecarName string, dc *daemon.Client) Model {
	return Model{
		daemonClient: dc,
		sidecarID:    sidecarID,
		sidecarName:  sidecarName,
		sidecars:     make(map[string]*daemon.SidecarState),
	}
}

type daemonSubMsg struct{ ch <-chan daemon.SSEEvent }
type daemonEventMsg daemon.SSEEvent

func startDaemonSub(c *daemon.Client) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan daemon.SSEEvent, 64)
		go func() {
			defer close(ch)
			_ = c.Subscribe(context.Background(), func(e daemon.SSEEvent) {
				ch <- e
			})
		}()
		return daemonSubMsg{ch: ch}
	}
}

func nextDaemonEvent(ch <-chan daemon.SSEEvent) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		return daemonEventMsg(e)
	}
}

// Init starts the daemon subscription.
func (m Model) Init() tea.Cmd {
	return startDaemonSub(m.daemonClient)
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case daemonSubMsg:
		m.daemonCh = msg.ch
		return m, nextDaemonEvent(m.daemonCh)

	case daemonEventMsg:
		m.applyDaemonEvent(daemon.SSEEvent(msg))
		return m, nextDaemonEvent(m.daemonCh)
	}
	return m, nil
}

func (m *Model) applyDaemonEvent(e daemon.SSEEvent) {
	switch e.Type {
	case "snapshot":
		var snap daemon.Snapshot
		if err := json.Unmarshal(e.Data, &snap); err == nil {
			m.sidecars = snap.Sidecars
			m.history = snap.History
		}
	case "sidecar_updated":
		var sc daemon.SidecarState
		if err := json.Unmarshal(e.Data, &sc); err == nil {
			m.sidecars[sc.ID] = &sc
		}
	case "invocation_started":
		m.activeSteps = nil
	case "invocation_step":
		var step struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
		}
		if err := json.Unmarshal(e.Data, &step); err == nil {
			m.activeSteps = append(m.activeSteps, fmt.Sprintf("%s  %s", step.Level, step.Msg))
		}
	case "invocation_finished":
		var sum daemon.InvocationSummary
		if err := json.Unmarshal(e.Data, &sum); err == nil {
			m.history = append([]*daemon.InvocationSummary{&sum}, m.history...)
			if len(m.history) > 10 {
				m.history = m.history[:10]
			}
			m.activeSteps = nil
		}
	}
}

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	labelStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("240"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// View renders the status panel.
func (m Model) View() tea.View {
	var sb strings.Builder

	name := m.sidecarName
	if name == "" {
		name = m.sidecarID
	}
	sb.WriteString(headerStyle.Render("chunk agent") + "  " + dimStyle.Render(name) + "\n\n")

	sb.WriteString(labelStyle.Render("VALIDATION") + "\n")
	sc := m.sidecars[m.sidecarID]
	switch {
	case sc != nil && sc.ActiveInvocationID != "":
		sb.WriteString(dimStyle.Render("  running...") + "\n")
		const limit = 5
		start := len(m.activeSteps) - limit
		if start < 0 {
			start = 0
		}
		for _, step := range m.activeSteps[start:] {
			sb.WriteString(dimStyle.Render("  "+truncate(step, m.width-3)) + "\n")
		}
	case len(m.history) > 0:
		last := m.history[0]
		if last.SidecarID == m.sidecarID {
			status := okStyle.Render("✓")
			if !last.OK {
				status = errStyle.Render("✗")
			}
			sb.WriteString("  " + status + "  " + truncate(last.Msg, m.width-5) + "\n")
		}
	default:
		sb.WriteString(dimStyle.Render("  no runs yet") + "\n")
	}
	sb.WriteString("\n")

	sb.WriteString(labelStyle.Render("HISTORY") + "\n")
	shown := 0
	for _, inv := range m.history {
		if shown >= 8 {
			break
		}
		status := okStyle.Render("✓")
		if !inv.OK {
			status = errStyle.Render("✗")
		}
		ts := inv.FinishedAt.Format("15:04")
		sb.WriteString("  " + status + " " + ts + "  " + truncate(inv.Msg, m.width-10) + "\n")
		shown++
	}
	if len(m.history) == 0 {
		sb.WriteString(dimStyle.Render("  none") + "\n")
	}

	v := tea.NewView(sb.String())
	v.AltScreen = true
	v.WindowTitle = "chunk status"
	return v
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
