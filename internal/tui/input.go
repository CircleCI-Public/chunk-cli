package tui

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// ErrCancelled is returned when the user cancels input (Ctrl+C or Esc).
var ErrCancelled = errors.New("cancelled")

type hiddenInputModel struct {
	input    textinput.Model
	label    string
	done     bool
	value    string
	canceled bool
}

func newHiddenInputModel(label string) hiddenInputModel {
	ti := textinput.New()
	ti.Placeholder = ""
	ti.EchoMode = textinput.EchoNone
	ti.Focus()

	return hiddenInputModel{
		input: ti,
		label: label,
	}
}

func (m hiddenInputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m hiddenInputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			m.done = true
			m.value = m.input.Value()
			return m, tea.Quit
		case tea.KeyCtrlC, tea.KeyEsc:
			m.canceled = true
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m hiddenInputModel) View() string {
	if m.done || m.canceled {
		return ""
	}
	return fmt.Sprintf("%s: %s", m.label, m.input.View())
}

// PromptHidden prompts the user for hidden input (e.g. API keys).
// Returns ErrCancelled if the user presses Ctrl+C or Esc.
func PromptHidden(label string) (string, error) {
	model := newHiddenInputModel(label)
	p := tea.NewProgram(model)
	result, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("input prompt: %w", err)
	}

	m := result.(hiddenInputModel)
	if m.canceled {
		return "", ErrCancelled
	}
	return m.value, nil
}
