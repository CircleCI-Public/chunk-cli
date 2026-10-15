// Package factorystart is the TUI for firing a prompt at a local agent and
// handing its first pass to the factory: the developer types a prompt, watches
// the agent work, looks over the diff and hands it off or keeps it.
package factorystart

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// Pass is a first pass at the prompt: the run it was made in, what the agent
// said it did, and the diff it left, summarized per file ("" for none).
type Pass struct {
	RunID    string
	Worktree string
	Summary  string
	Stat     string
}

// Work starts a run for prompt and has the agent make its pass, reporting
// what it does to activity. It returns the run as far as it got, with an
// error, when the pass fails or ctx is cancelled.
type Work func(ctx context.Context, prompt string, activity func(string)) (Pass, error)

// Choice is how the developer left the TUI.
type Choice int

const (
	// Cancelled means no prompt was sent, so there is no run.
	Cancelled Choice = iota
	// HandOff means the developer wants the factory to refine the pass.
	HandOff
	// Keep means the developer left the pass in its worktree for later.
	Keep
	// Failed means the pass failed or was stopped; Outcome.Err says which.
	Failed
)

// Outcome is what came of the TUI.
type Outcome struct {
	Choice Choice
	Pass   Pass
	Err    error
}

type state int

const (
	statePrompt state = iota
	stateWorking
	stateDone
)

// shownActivity is how many of the agent's latest actions are shown.
const shownActivity = 5

// keyCtrlC stops the agent, or leaves the TUI when it is not working.
const keyCtrlC = "ctrl+c"

// shownStat caps the diff summary's lines, keeping the totals line.
const shownStat = 15

type activityMsg string

type passMsg struct {
	pass Pass
	err  error
}

type model struct {
	ctx      context.Context
	cancel   context.CancelFunc
	work     Work
	activity chan string

	state    state
	input    textarea.Model
	spinner  spinner.Model
	latest   []string
	stopping bool
	outcome  Outcome
}

func newModel(ctx context.Context, work Work) model {
	in := textarea.New()
	in.Placeholder = "Describe the change, e.g. add a --verbose flag"
	in.ShowLineNumbers = false
	in.Prompt = "│ "
	in.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	in.SetWidth(80)
	in.SetHeight(3)
	// Focused here, not in Init: Init has a copy of the model, so focus set
	// there would be lost.
	in.Focus()
	ctx, cancel := context.WithCancel(ctx)
	return model{
		ctx:      ctx,
		cancel:   cancel,
		work:     work,
		activity: make(chan string, 64),
		input:    in,
		spinner:  spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.input.SetWidth(max(20, min(msg.Width-2, 100)))
		return m, nil
	case tea.KeyPressMsg:
		return m.key(msg)
	case activityMsg:
		m.latest = append(m.latest, string(msg))
		if len(m.latest) > shownActivity {
			m.latest = m.latest[len(m.latest)-shownActivity:]
		}
		return m, m.waitActivity()
	case passMsg:
		m.outcome.Pass = msg.pass
		if msg.err != nil {
			m.outcome.Choice, m.outcome.Err = Failed, msg.err
			return m, tea.Quit
		}
		m.state = stateDone
		return m, nil
	case spinner.TickMsg:
		if m.state != stateWorking {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	if m.state == statePrompt {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case statePrompt:
		switch msg.String() {
		case keyCtrlC, "esc":
			m.outcome.Choice = Cancelled
			return m, tea.Quit
		case "enter":
			prompt := strings.TrimSpace(m.input.Value())
			if prompt == "" {
				return m, nil
			}
			m.state = stateWorking
			m.input.Blur()
			return m, tea.Batch(m.spinner.Tick, m.run(prompt), m.waitActivity())
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	case stateWorking:
		// The run reports back once the agent has stopped, so the work it
		// did so far is still there to keep.
		if msg.String() == keyCtrlC && !m.stopping {
			m.stopping = true
			m.cancel()
		}
		return m, nil
	case stateDone:
		switch msg.String() {
		case "enter":
			if m.outcome.Pass.Stat == "" {
				return m, nil
			}
			m.outcome.Choice = HandOff
			return m, tea.Quit
		case "q", "esc", keyCtrlC:
			m.outcome.Choice = Keep
			return m, tea.Quit
		}
	}
	return m, nil
}

// run does the work off the UI's goroutine and reports it as a passMsg.
func (m model) run(prompt string) tea.Cmd {
	ctx, work, activity := m.ctx, m.work, m.activity
	return func() tea.Msg {
		pass, err := work(ctx, prompt, func(s string) {
			// Activity is only shown, so it is dropped rather than hold up
			// the agent when the UI falls behind.
			select {
			case activity <- s:
			default:
			}
		})
		return passMsg{pass: pass, err: err}
	}
}

func (m model) waitActivity() tea.Cmd {
	ctx, activity := m.ctx, m.activity
	return func() tea.Msg {
		select {
		case s := <-activity:
			return activityMsg(s)
		case <-ctx.Done():
			return nil
		}
	}
}

func (m model) View() tea.View {
	var b strings.Builder
	switch m.state {
	case statePrompt:
		b.WriteString(ui.Bold("What should Claude do?") + "\n")
		b.WriteString(m.input.View() + "\n")
		b.WriteString(ui.Dim("enter start · alt+enter new line · esc cancel") + "\n")
	case stateWorking:
		if m.stopping {
			b.WriteString(m.spinner.View() + " Stopping Claude…\n")
		} else {
			b.WriteString(m.spinner.View() + " Claude is making a first pass…\n")
		}
		for _, a := range m.latest {
			b.WriteString(ui.Dim("  "+a) + "\n")
		}
		if !m.stopping {
			b.WriteString(ui.Dim("ctrl+c stop") + "\n")
		}
	case stateDone:
		p := m.outcome.Pass
		if p.Stat == "" {
			b.WriteString(ui.Warning("Claude made no changes.") + "\n")
		} else {
			b.WriteString(ui.Success("Claude made a first pass.") + "\n")
		}
		if s := strings.TrimSpace(p.Summary); s != "" {
			b.WriteString("\n" + clip(s, 8) + "\n")
		}
		if p.Stat != "" {
			b.WriteString("\n" + clip(p.Stat, shownStat) + "\n")
			b.WriteString("\n" + ui.Dim("enter hand off to the factory · q keep it in the worktree") + "\n")
		} else {
			b.WriteString("\n" + ui.Dim("q quit") + "\n")
		}
	}
	return tea.NewView(b.String())
}

// clip keeps s to n lines, keeping the last line, which for a diff summary is
// the totals.
func clip(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	cut := len(lines) - n + 1
	kept := make([]string, 0, n)
	kept = append(kept, lines[:n-2]...)
	kept = append(kept, fmt.Sprintf(" … %d more", cut), lines[len(lines)-1])
	return strings.Join(kept, "\n")
}

// Run shows the TUI until the developer hands off the pass, keeps it or
// leaves without sending a prompt. It returns ui.ErrNoTTY without a terminal.
func Run(ctx context.Context, work Work) (Outcome, error) {
	if err := ui.RequireStdoutTTY(); err != nil {
		return Outcome{}, err
	}
	m := newModel(ctx, work)
	defer m.cancel()
	res, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return Outcome{}, fmt.Errorf("factory start: %w", err)
	}
	final, ok := res.(model)
	if !ok {
		return Outcome{}, errors.New("factory start: unexpected model")
	}
	return final.outcome, nil
}
