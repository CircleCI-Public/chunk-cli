// Package reviewprogress implements the BubbleTea TUI for chunk review passes.
package reviewprogress

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

const (
	spinInterval  = 160 * time.Millisecond
	maxErrLen     = 50
	sidecarPfxLen = 8
)

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ProgressMsg carries a state-change event from the review pass.
type ProgressMsg review.ProgressEvent

// DoneMsg signals that the review pass has finished.
type DoneMsg struct{ Err error }

type spinMsg struct{}

type promptRow struct {
	name      string
	sidecarID string
	state     review.PromptState
	duration  time.Duration
	errMsg    string
}

// reviewStyles holds computed lipgloss styles.
type reviewStyles struct {
	Dim     lipgloss.Style
	Success lipgloss.Style
	Err     lipgloss.Style
	Running lipgloss.Style
	Muted   lipgloss.Style
	VDim    lipgloss.Style
	Emph    lipgloss.Style
}

func newStyles(hasDark bool) reviewStyles {
	ld := lipgloss.LightDark(hasDark)
	return reviewStyles{
		Dim:     lipgloss.NewStyle().Foreground(ld(lipgloss.Color("248"), lipgloss.Color("246"))),
		Success: lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		Err:     lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		Running: lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
		Muted:   lipgloss.NewStyle().Foreground(ld(lipgloss.Color("252"), lipgloss.Color("250"))),
		VDim:    lipgloss.NewStyle().Foreground(ld(lipgloss.Color("244"), lipgloss.Color("242"))),
		Emph:    lipgloss.NewStyle().Bold(true),
	}
}

// Model is the BubbleTea model for review progress.
type Model struct {
	rows      []promptRow
	byName    map[string]int
	cancelFn  func()
	RunErr    error
	spinIdx   int
	hasDarkBG bool
	width     int
	poolSize  int
}

// New creates a Model ready to run for the given prompts and pool size.
func New(prompts []review.Prompt, poolSize int, cancelFn func()) Model {
	rows := make([]promptRow, len(prompts))
	byName := make(map[string]int, len(prompts))
	for i, p := range prompts {
		rows[i] = promptRow{name: p.Name, state: review.StateQueued}
		byName[p.Name] = i
	}
	return Model{
		rows:      rows,
		byName:    byName,
		cancelFn:  cancelFn,
		hasDarkBG: lipgloss.HasDarkBackground(os.Stdin, os.Stdout),
		poolSize:  poolSize,
	}
}

func (m Model) Init() tea.Cmd {
	return doSpin()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.hasDarkBG = msg.IsDark()

	case tea.WindowSizeMsg:
		m.width = msg.Width

	case tea.KeyPressMsg:
		switch msg.Code {
		case 'q', tea.KeyEscape:
			m.cancelFn()
			return m, tea.Quit
		case 'c':
			if msg.Mod == tea.ModCtrl {
				m.cancelFn()
				return m, tea.Quit
			}
		}

	case ProgressMsg:
		e := review.ProgressEvent(msg)
		if idx, ok := m.byName[e.Prompt]; ok {
			m.rows[idx].state = e.State
			m.rows[idx].sidecarID = e.SidecarID
			m.rows[idx].duration = e.Duration
			m.rows[idx].errMsg = e.Error
		}

	case DoneMsg:
		m.RunErr = msg.Err
		// A pass stopped by a fatal error leaves rows it never reported on;
		// settle them so the final frame doesn't show them still in flight.
		if msg.Err != nil {
			for i := range m.rows {
				if r := &m.rows[i]; r.state == review.StateQueued || r.state == review.StateRunning {
					r.state = review.StateFailed
					r.errMsg = msg.Err.Error()
				}
			}
		}
		return m, tea.Quit

	case spinMsg:
		m.spinIdx++
		if m.anyActive() {
			return m, doSpin()
		}
	}
	return m, nil
}

func (m Model) View() tea.View {
	return tea.NewView(m.render())
}

// anyActive reports whether any prompt is still in progress (queued or running).
// The spinner runs until all prompts reach a terminal state.
func (m Model) anyActive() bool {
	for _, r := range m.rows {
		if r.state == review.StateQueued || r.state == review.StateRunning {
			return true
		}
	}
	return false
}

func (m Model) render() string {
	st := newStyles(m.hasDarkBG)

	w := m.width
	if w < 40 {
		w = 80
	}

	var b strings.Builder

	// Header
	header := st.Emph.Render("chunk review") +
		st.VDim.Render("  ·  ") +
		st.Muted.Render(fmt.Sprintf("%d prompt(s)", len(m.rows))) +
		st.VDim.Render("  ·  ") +
		st.Muted.Render(fmt.Sprintf("pool: review (%d sidecar(s))", m.poolSize))
	b.WriteString(header)
	b.WriteByte('\n')
	b.WriteString(st.VDim.Render(strings.Repeat("─", w)))
	b.WriteByte('\n')

	// Prompt rows
	nameWidth := 0
	for _, r := range m.rows {
		if len(r.name) > nameWidth {
			nameWidth = len(r.name)
		}
	}

	frame := spinFrames[m.spinIdx%len(spinFrames)]

	for _, r := range m.rows {
		paddedName := fmt.Sprintf("%-*s", nameWidth, r.name)
		var icon, nameStr, detail string

		switch r.state {
		case review.StateQueued:
			icon = st.VDim.Render("·")
			nameStr = st.VDim.Render(paddedName)
			detail = ""
		case review.StateRunning:
			icon = st.Running.Render(frame)
			nameStr = paddedName
			pfx := r.sidecarID
			if len(pfx) > sidecarPfxLen {
				pfx = pfx[:sidecarPfxLen]
			}
			detail = st.Running.Render("↪ " + pfx)
		case review.StateDone:
			icon = st.Success.Render(ui.IconOK)
			nameStr = st.Success.Render(paddedName)
			detail = st.Dim.Render(ui.FormatDuration(r.duration))
		case review.StateFailed:
			icon = st.Err.Render(ui.IconFail)
			nameStr = st.Err.Render(paddedName)
			// Errors can carry multi-line stderr; keep the row on one line
			// and cut on a rune boundary.
			errTxt := strings.Join(strings.Fields(r.errMsg), " ")
			if rs := []rune(errTxt); len(rs) > maxErrLen {
				errTxt = string(rs[:maxErrLen-1]) + "…"
			}
			detail = st.Err.Render(errTxt)
		}

		line := "  " + icon + "  " + nameStr
		if detail != "" {
			line += "     " + detail
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}

	b.WriteString(st.VDim.Render(strings.Repeat("─", w)))
	b.WriteByte('\n')

	// Summary bar
	var running, done, queued, failed int
	for _, r := range m.rows {
		switch r.state {
		case review.StateQueued:
			queued++
		case review.StateRunning:
			running++
		case review.StateDone:
			done++
		case review.StateFailed:
			failed++
		}
	}

	var parts []string
	if running > 0 {
		parts = append(parts, st.Running.Render(fmt.Sprintf("%d running", running)))
	}
	if done > 0 {
		parts = append(parts, st.Success.Render(fmt.Sprintf("%d done", done)))
	}
	if failed > 0 {
		parts = append(parts, st.Err.Render(fmt.Sprintf("%d failed", failed)))
	}
	if queued > 0 {
		parts = append(parts, st.VDim.Render(fmt.Sprintf("%d queued", queued)))
	}

	sep := st.VDim.Render("  ·  ")
	bar := strings.Join(parts, sep)
	hint := st.VDim.Render("q") + " " + st.Dim.Render("quit")
	gap := max(w-lipgloss.Width(bar)-lipgloss.Width(hint)-2, 2)
	b.WriteString("  ")
	b.WriteString(bar)
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(hint)
	b.WriteByte('\n')

	return b.String()
}

func doSpin() tea.Cmd {
	return tea.Tick(spinInterval, func(time.Time) tea.Msg { return spinMsg{} })
}
