package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type confirmModel struct {
	label      string
	defaultYes bool
	done       bool
	result     bool
	canceled   bool
}

func (m confirmModel) Init() tea.Cmd {
	return nil
}

func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch msg.Code {
		case tea.KeyEscape:
			m.canceled = true
			return m, tea.Quit
		case 'c':
			if msg.Mod == tea.ModCtrl {
				m.canceled = true
				return m, tea.Quit
			}
		case tea.KeyEnter:
			m.done = true
			m.result = m.defaultYes
			return m, tea.Quit
		default:
			s := strings.ToLower(msg.Text)
			if s == "y" {
				m.done = true
				m.result = true
				return m, tea.Quit
			}
			if s == "n" {
				m.done = true
				m.result = false
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m confirmModel) View() tea.View {
	if m.done || m.canceled {
		return tea.NewView("")
	}
	hint := "[y/N]"
	if m.defaultYes {
		hint = "[Y/n]"
	}
	return tea.NewView(fmt.Sprintf("%s %s ", m.label, hint))
}

// Confirm presents a y/n prompt and returns the boolean result.
// Returns ErrCancelled if the user presses Ctrl+C or Esc.
func Confirm(label string, defaultYes bool) (bool, error) {
	model := confirmModel{label: label, defaultYes: defaultYes}
	p := tea.NewProgram(model)
	result, err := p.Run()
	if err != nil {
		return false, fmt.Errorf("confirm prompt: %w", err)
	}

	m := result.(confirmModel)
	if m.canceled {
		return false, ErrCancelled
	}
	return m.result, nil
}
