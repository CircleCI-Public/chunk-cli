package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type textInputModel struct {
	input    textinput.Model
	label    string
	done     bool
	value    string
	canceled bool
}

func newTextInputModel(label, defaultVal string) textInputModel {
	ti := textinput.New()
	ti.Placeholder = defaultVal
	ti.Focus()

	return textInputModel{
		input: ti,
		label: label,
	}
}

func (m textInputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m textInputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.Type { //nolint:exhaustive
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

func (m textInputModel) View() string {
	if m.done || m.canceled {
		return ""
	}
	return fmt.Sprintf("%s: %s", m.label, m.input.View())
}

// PromptText prompts for text input with an optional default value shown as placeholder.
// If the user enters nothing and presses Enter, the default value is returned.
// Returns ErrCancelled if the user presses Ctrl+C or Esc.
func PromptText(label, defaultVal string) (string, error) {
	model := newTextInputModel(label, defaultVal)
	p := tea.NewProgram(model)
	result, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("text prompt: %w", err)
	}

	m := result.(textInputModel)
	if m.canceled {
		return "", ErrCancelled
	}
	if m.value == "" {
		return defaultVal, nil
	}
	return m.value, nil
}
