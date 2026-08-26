package agentsession

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/daemon"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/tui/sshterm"
)

// SetupFactory starts a setup goroutine for a given sidecar ID/name.
type SetupFactory func(sidecarID, sidecarName string, ch chan<- SetupEvent)

// SetupEvent is sent by the setup goroutine to report progress.
type SetupEvent struct {
	StepIndex int
	Running   bool         // true = step just started
	Err       error        // non-nil = step failed
	Result    *SetupResult // non-nil when all steps complete
}

// SetupResult carries the resources produced by a successful setup.
type SetupResult struct {
	Session      *sidecar.Session
	SidecarID    string
	SidecarName  string
	DaemonClient *daemon.Client
	SSHConn      *sidecar.SSHConn
	EnvVars      map[string]string
}

type stepStatus uint8

const (
	stepPending stepStatus = iota
	stepRunning
	stepDone
	stepFailed
)

type step struct {
	label  string
	status stepStatus
}

type agentPane int

const (
	agentPaneTerminal agentPane = iota
	agentPaneSidecars
)

// localSidecar is a sidecar entry loaded from the local filesystem.
type localSidecar struct {
	id            string
	name          string
	branch        string
	inSync        bool
	lastSyncedRef string
}

type localSidecarsMsg []localSidecar
type localSidecarsTickMsg struct{}

// Model is the top-level agentsession TUI model.
type Model struct {
	terminal     sshterm.Model
	daemonClient *daemon.Client
	sshConn      *sidecar.SSHConn

	sidecarID   string
	sidecarName string

	sidecars    map[string]*daemon.SidecarState
	history     []*daemon.InvocationSummary
	activeSteps []string
	daemonCh    <-chan daemon.SSEEvent

	width    int
	height   int
	lastKey  string
	keyCount int

	// Left sidebar
	projectRoot     string
	dataDir         string
	setupFactory    SetupFactory
	localSidecars   []localSidecar
	leftSelectedIdx int
	focusedPane     agentPane

	// Setup phase
	setupSteps []step
	setupCh    <-chan SetupEvent
	setupDone  bool
	setupErr   error
}

// DefaultSetupStepLabels returns the standard setup step labels.
func DefaultSetupStepLabels() []string {
	return []string{
		"Locating sidecar",
		"Opening SSH session",
		"Connecting via SSH",
		"Copying chunk binary",
		"Installing chunk binary",
		"Starting daemon",
	}
}

// NewWithSetup creates a Model that starts in the setup phase.
func NewWithSetup(stepLabels []string, setupCh <-chan SetupEvent, projectRoot, dataDir string, factory SetupFactory, width, height int) Model {
	steps := make([]step, len(stepLabels))
	for i, label := range stepLabels {
		steps[i] = step{label: label, status: stepPending}
	}
	return Model{
		setupSteps:   steps,
		setupCh:      setupCh,
		projectRoot:  projectRoot,
		dataDir:      dataDir,
		setupFactory: factory,
		width:        width,
		height:       height,
	}
}

const (
	leftSidebarCols  = 22
	rightSidebarCols = 30
)

func calcTermWidth(total int) int {
	const sidebars = leftSidebarCols + 1 + rightSidebarCols + 1
	if total <= sidebars+10 {
		return total / 3
	}
	return total - sidebars
}

type daemonSubMsg struct{ ch <-chan daemon.SSEEvent }
type daemonEventMsg daemon.SSEEvent
type setupEventMsg SetupEvent

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

func nextSetupEvent(ch <-chan SetupEvent) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		return setupEventMsg(e)
	}
}

func (m Model) loadLocalSidecarsCmd() tea.Cmd {
	dataDir := m.dataDir
	projectRoot := m.projectRoot
	return func() tea.Msg {
		if dataDir == "" {
			return localSidecarsMsg(nil)
		}
		matches, _ := filepath.Glob(filepath.Join(dataDir, "sidecar*.json"))

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		head, _ := gitutil.HeadRefCtx(ctx, projectRoot)
		branch := sidecar.CurrentBranch(projectRoot)

		seen := map[string]int{}
		var result []localSidecar
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var as sidecar.ActiveSidecar
			if json.Unmarshal(data, &as) != nil || as.SidecarID == "" {
				continue
			}
			if idx, dup := seen[as.SidecarID]; dup {
				// prefer entry with most recent synced ref
				if as.LastSyncedRef != "" {
					result[idx].lastSyncedRef = as.LastSyncedRef
					result[idx].inSync = head != "" && as.LastSyncedRef != "" && head == as.LastSyncedRef
				}
				continue
			}
			seen[as.SidecarID] = len(result)
			name := as.Name
			result = append(result, localSidecar{
				id:            as.SidecarID,
				name:          name,
				branch:        branch,
				lastSyncedRef: as.LastSyncedRef,
				inSync:        head != "" && as.LastSyncedRef != "" && head == as.LastSyncedRef,
			})
		}
		return localSidecarsMsg(result)
	}
}

// Init starts listening for setup events (setup phase) or starts the terminal (running phase).
// Implements tea.Model.
func (m Model) Init() tea.Cmd {
	if !m.setupDone {
		return tea.Batch(nextSetupEvent(m.setupCh), m.loadLocalSidecarsCmd())
	}
	_, termCmd := m.terminal.Init()
	return tea.Batch(termCmd, startDaemonSub(m.daemonClient), m.loadLocalSidecarsCmd())
}

// Update handles messages.
// Implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.setupDone {
			tw := calcTermWidth(m.width)
			th := m.height - 2
			var cmd tea.Cmd
			m.terminal, cmd = m.terminal.Update(tea.WindowSizeMsg{Width: tw, Height: th})
			return m, cmd
		}
		return m, nil

	case tea.KeyPressMsg:
		if !m.setupDone {
			if m.setupErr != nil {
				return m, tea.Quit
			}
			return m, nil
		}

		return m.handleKeyPress(msg)

	case setupEventMsg:
		e := SetupEvent(msg)
		if e.Result != nil {
			// All setup steps complete — transition to running phase.
			m.setupDone = true
			m.sidecarID = e.Result.SidecarID
			m.sidecarName = e.Result.SidecarName
			m.daemonClient = e.Result.DaemonClient
			m.sshConn = e.Result.SSHConn
			m.sidecars = make(map[string]*daemon.SidecarState)
			tw := calcTermWidth(m.width)
			th := m.height - 2
			m.terminal = sshterm.New(e.Result.Session, "bash -lc 'claude; exec bash -l'", e.Result.EnvVars, tw, th)
			m.terminal = m.terminal.SetFocused(true)
			_, termCmd := m.terminal.Init()
			return m, tea.Batch(termCmd, startDaemonSub(m.daemonClient))
		}
		if e.Err != nil {
			if e.StepIndex < len(m.setupSteps) {
				m.setupSteps[e.StepIndex].status = stepFailed
			}
			m.setupErr = e.Err
			return m, nil
		}
		if e.Running {
			if e.StepIndex < len(m.setupSteps) {
				m.setupSteps[e.StepIndex].status = stepRunning
			}
		} else {
			if e.StepIndex < len(m.setupSteps) {
				m.setupSteps[e.StepIndex].status = stepDone
			}
		}
		return m, nextSetupEvent(m.setupCh)

	case localSidecarsMsg:
		m.localSidecars = []localSidecar(msg)
		// Clamp leftSelectedIdx in case the list shrank.
		if m.leftSelectedIdx >= len(m.localSidecars) && len(m.localSidecars) > 0 {
			m.leftSelectedIdx = len(m.localSidecars) - 1
		}
		return m, tea.Tick(5*time.Second, func(time.Time) tea.Msg { return localSidecarsTickMsg{} })

	case localSidecarsTickMsg:
		return m, m.loadLocalSidecarsCmd()

	case daemonSubMsg:
		m.daemonCh = msg.ch
		return m, nextDaemonEvent(m.daemonCh)

	case daemonEventMsg:
		m.applyDaemonEvent(daemon.SSEEvent(msg))
		return m, nextDaemonEvent(m.daemonCh)

	default:
		if !m.setupDone {
			return m, nil
		}
		// Forward other messages (outputMsg, connectedMsg, disconnectedMsg, etc.) to the terminal.
		var cmd tea.Cmd
		m.terminal, cmd = m.terminal.Update(msg)
		if m.terminal.Done() {
			m.terminal.Close()
			if m.sshConn != nil {
				_ = m.sshConn.Close()
			}
			return m, tea.Quit
		}
		return m, cmd
	}
}

func (m Model) handleKeyPress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Ctrl+\ toggles left pane focus.
	if msg.Code == '\\' && msg.Mod == tea.ModCtrl {
		if m.focusedPane == agentPaneSidecars {
			m.focusedPane = agentPaneTerminal
			m.terminal = m.terminal.SetFocused(true)
		} else {
			m.focusedPane = agentPaneSidecars
			m.terminal = m.terminal.SetFocused(false)
		}
		return m, nil
	}

	if m.focusedPane == agentPaneSidecars {
		return m.handleSidecarPaneKey(msg)
	}

	m.lastKey = msg.Keystroke()
	m.keyCount++
	var cmd tea.Cmd
	m.terminal, cmd = m.terminal.Update(msg)
	if m.terminal.Done() {
		m.terminal.Close()
		if m.sshConn != nil {
			_ = m.sshConn.Close()
		}
		return m, tea.Quit
	}
	return m, cmd
}

func (m Model) handleSidecarPaneKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyUp, 'k':
		if m.leftSelectedIdx > 0 {
			m.leftSelectedIdx--
		}
	case tea.KeyDown, 'j':
		if m.leftSelectedIdx < len(m.localSidecars)-1 {
			m.leftSelectedIdx++
		}
	case tea.KeyEnter, tea.KeySpace:
		if m.leftSelectedIdx < len(m.localSidecars) {
			sc := m.localSidecars[m.leftSelectedIdx]
			if sc.id != m.sidecarID {
				return m.startSidecarSwitch(sc.id, sc.name)
			}
		}
	case tea.KeyEscape:
		m.focusedPane = agentPaneTerminal
		m.terminal = m.terminal.SetFocused(true)
	}
	return m, nil
}

func (m Model) startSidecarSwitch(id, name string) (tea.Model, tea.Cmd) {
	m.terminal.Close()
	if m.sshConn != nil {
		_ = m.sshConn.Close()
		m.sshConn = nil
	}

	m.setupDone = false
	m.setupErr = nil
	m.sidecarID = id
	m.sidecarName = name
	m.focusedPane = agentPaneTerminal

	labels := DefaultSetupStepLabels()
	steps := make([]step, len(labels))
	for i, l := range labels {
		steps[i] = step{label: l, status: stepPending}
	}
	m.setupSteps = steps

	setupCh := make(chan SetupEvent, 16)
	m.setupCh = setupCh
	go m.setupFactory(id, name, setupCh)
	return m, nextSetupEvent(setupCh)
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

// View renders the full TUI layout.
// Implements tea.Model.
func (m Model) View() tea.View {
	var content string
	if !m.setupDone {
		content = m.renderSetup()
	} else {
		content = m.renderHeader() + "\n" + m.renderBody() + "\n" + m.renderFooter()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "chunk agent"
	if m.setupDone && m.terminal.Connected() {
		cx, cy := m.terminal.CursorPosition()
		v.Cursor = tea.NewCursor(leftSidebarCols+1+cx, 1+cy)
	}
	return v
}

var (
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	dividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	labelStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("240"))
	boldStyle    = lipgloss.NewStyle().Bold(true)
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	runningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

func (m Model) renderSetup() string {
	var sb strings.Builder
	sb.WriteString(headerStyle.Render("chunk agent") + "\n\n")

	if m.setupErr != nil {
		sb.WriteString(errStyle.Render("  Setup failed: "+m.setupErr.Error()) + "\n\n")
		sb.WriteString(dimStyle.Render("  Press any key to exit.") + "\n")
		return sb.String()
	}

	sb.WriteString(dimStyle.Render("  Setting up...") + "\n\n")
	for _, s := range m.setupSteps {
		var icon, label string
		switch s.status {
		case stepDone:
			icon = okStyle.Render("✓")
			label = s.label
		case stepRunning:
			icon = runningStyle.Render("→")
			label = runningStyle.Render(s.label)
		case stepFailed:
			icon = errStyle.Render("✗")
			label = errStyle.Render(s.label)
		case stepPending:
			icon = dimStyle.Render("·")
			label = dimStyle.Render(s.label)
		}
		sb.WriteString("  " + icon + "  " + label + "\n")
	}
	return sb.String()
}

func (m Model) renderHeader() string {
	name := m.sidecarName
	if name == "" {
		name = m.sidecarID
	}
	return headerStyle.Render("chunk agent") + "  " + dimStyle.Render(name)
}

func (m Model) renderFooter() string {
	if m.focusedPane == agentPaneSidecars {
		return dimStyle.Render("↑↓ navigate  enter switch  esc terminal")
	}
	return dimStyle.Render("ctrl+\\ sidecars  exit claude to quit")
}

func (m Model) renderBody() string {
	tw := calcTermWidth(m.width)
	sh := m.height - 2
	rightWidth := m.width - leftSidebarCols - 1 - tw - 1
	if rightWidth < 1 {
		rightWidth = rightSidebarCols
	}

	leftLines := strings.Split(m.renderLeftSidebar(leftSidebarCols, sh), "\n")
	termLines := strings.Split(m.terminal.View(), "\n")
	rightLines := strings.Split(m.renderRightSidebar(rightWidth, sh), "\n")

	for len(leftLines) < sh {
		leftLines = append(leftLines, "")
	}
	for len(termLines) < sh {
		termLines = append(termLines, "")
	}
	for len(rightLines) < sh {
		rightLines = append(rightLines, "")
	}

	rows := make([]string, sh)
	for i := range rows {
		ll := ""
		if i < len(leftLines) {
			ll = leftLines[i]
		}
		tl := ""
		if i < len(termLines) {
			tl = termLines[i]
		}
		rl := ""
		if i < len(rightLines) {
			rl = rightLines[i]
		}
		rows[i] = padToWidth(ll, leftSidebarCols) + dividerStyle.Render("│") +
			padToWidth(tl, tw) + dividerStyle.Render("│") +
			padToWidth(rl, rightWidth)
	}
	return strings.Join(rows, "\n")
}

// padToWidth pads s with spaces until its visible (ANSI-stripped) width reaches w.
func padToWidth(s string, w int) string {
	n := ansi.StringWidth(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

func (m Model) renderLeftSidebar(width, _ int) string {
	var sb strings.Builder

	if m.focusedPane == agentPaneSidecars {
		sb.WriteString(labelStyle.Render("SIDECARS") + "\n")
	} else {
		sb.WriteString(dimStyle.Render("SIDECARS") + "\n")
	}

	if m.dataDir == "" {
		sb.WriteString(dimStyle.Render("  (unavailable)") + "\n")
		return sb.String()
	}

	if len(m.localSidecars) == 0 {
		sb.WriteString(dimStyle.Render("  none") + "\n")
		return sb.String()
	}

	for i, sc := range m.localSidecars {
		selected := i == m.leftSelectedIdx && m.focusedPane == agentPaneSidecars
		isActive := sc.id == m.sidecarID

		name := sc.name
		if name == "" {
			name = sc.id
		}
		nameLine := truncate(name, width-3)

		switch {
		case isActive && selected:
			sb.WriteString(okStyle.Render("▶ ") + boldStyle.Render(nameLine) + "\n")
		case isActive:
			sb.WriteString(dimStyle.Render("▶ ") + nameLine + "\n")
		case selected:
			sb.WriteString(runningStyle.Render("▶ ") + nameLine + "\n")
		default:
			sb.WriteString("  " + dimStyle.Render(nameLine) + "\n")
		}

		if sc.branch != "" {
			sb.WriteString("  " + dimStyle.Render(truncate(sc.branch, width-3)) + "\n")
		}

		switch {
		case sc.inSync:
			sb.WriteString("  " + okStyle.Render("✓ in sync") + "\n")
		case sc.lastSyncedRef == "":
			sb.WriteString("  " + dimStyle.Render("not synced") + "\n")
		default:
			sb.WriteString("  " + runningStyle.Render("↑ needs sync") + "\n")
		}
	}

	return sb.String()
}

func (m Model) renderRightSidebar(width, _ int) string {
	var sb strings.Builder

	sb.WriteString(labelStyle.Render("VALIDATION") + "\n")
	sc := m.sidecars[m.sidecarID]
	switch {
	case sc != nil && sc.ActiveInvocationID != "":
		sb.WriteString(dimStyle.Render("  running...") + "\n")
		const limit = 3
		start := len(m.activeSteps) - limit
		if start < 0 {
			start = 0
		}
		for _, step := range m.activeSteps[start:] {
			sb.WriteString(dimStyle.Render("  "+truncate(step, width-3)) + "\n")
		}
	case len(m.history) > 0:
		last := m.history[0]
		if last.SidecarID == m.sidecarID {
			status := okStyle.Render("✓")
			if !last.OK {
				status = errStyle.Render("✗")
			}
			sb.WriteString("  " + status + "  " + truncate(last.Msg, width-5) + "\n")
		}
	default:
		sb.WriteString(dimStyle.Render("  no runs yet") + "\n")
	}
	sb.WriteString("\n")

	sb.WriteString(labelStyle.Render("HISTORY") + "\n")
	shown := 0
	for _, inv := range m.history {
		if shown >= 5 {
			break
		}
		status := okStyle.Render("✓")
		if !inv.OK {
			status = errStyle.Render("✗")
		}
		ts := inv.FinishedAt.Format("15:04")
		sb.WriteString("  " + status + " " + ts + "  " + truncate(inv.Msg, width-10) + "\n")
		shown++
	}
	if len(m.history) == 0 {
		sb.WriteString(dimStyle.Render("  none") + "\n")
	}

	return sb.String()
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
