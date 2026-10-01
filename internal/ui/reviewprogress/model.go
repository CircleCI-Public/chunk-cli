// Package reviewprogress implements the BubbleTea TUI for chunk review passes,
// and the row rendering that other views (the watch dashboard's session view)
// share with it.
package reviewprogress

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

const spinInterval = 160 * time.Millisecond

// ProgressMsg carries a state-change event from the review pass.
type ProgressMsg review.ProgressEvent

// DoneMsg signals that the review pass has finished.
type DoneMsg struct{ Err error }

type spinMsg struct{}

// Model is the BubbleTea model for review progress.
type Model struct {
	rows      []Row
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
	rows := make([]Row, len(prompts))
	byName := make(map[string]int, len(prompts))
	for i, p := range prompts {
		rows[i] = Row{Name: p.Name, State: review.StateQueued}
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
			m.rows[idx].State = e.State
			m.rows[idx].SidecarID = e.SidecarID
			m.rows[idx].Duration = e.Duration
			m.rows[idx].Err = e.Error
		}

	case DoneMsg:
		m.RunErr = msg.Err
		// A pass stopped by a fatal error leaves rows it never reported on;
		// settle them so the final frame doesn't show them still in flight.
		if msg.Err != nil {
			for i := range m.rows {
				if r := &m.rows[i]; r.State == review.StateQueued || r.State == review.StateRunning {
					r.State = review.StateFailed
					r.Err = msg.Err.Error()
				}
			}
		}
		return m, tea.Quit

	case spinMsg:
		m.spinIdx++
		if Active(m.rows) {
			return m, doSpin()
		}
	}
	return m, nil
}

func (m Model) View() tea.View {
	return tea.NewView(m.render())
}

func (m Model) render() string {
	st := NewStyles(m.hasDarkBG)

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

	nameWidth := NameWidth(m.rows)
	for _, r := range m.rows {
		b.WriteString(RenderRow(st, r, nameWidth, m.spinIdx))
		b.WriteByte('\n')
	}

	b.WriteString(st.VDim.Render(strings.Repeat("─", w)))
	b.WriteByte('\n')

	bar := RenderSummary(st, m.rows)
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
