package tui

import (
	"fmt"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
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
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch msg.Code {
		case tea.KeyEnter:
			m.done = true
			m.value = m.input.Value()
			return m, tea.Quit
		case tea.KeyEscape:
			m.canceled = true
			return m, tea.Quit
		case 'c':
			if msg.Mod == tea.ModCtrl {
				m.canceled = true
				return m, tea.Quit
			}
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m textInputModel) View() tea.View {
	if m.done || m.canceled {
		return tea.NewView("")
	}
	return tea.NewView(fmt.Sprintf("%s: %s", m.label, m.input.View()))
}

// PromptText prompts for text input with an optional default value shown as placeholder.
// If the user enters nothing and presses Enter, the default value is returned.
// Returns ErrCancelled if the user presses Ctrl+C or Esc.
// Returns ErrNoTTY if stdin is not a terminal or CI is set.
func PromptText(label, defaultVal string) (string, error) {
	if err := requireTTY(); err != nil {
		return "", err
	}
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
