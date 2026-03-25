package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.canceled = true
			return m, tea.Quit
		case tea.KeyEnter:
			m.done = true
			m.result = m.defaultYes
			return m, tea.Quit
		case tea.KeyRunes:
			s := strings.ToLower(msg.String())
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

func (m confirmModel) View() string {
	if m.done || m.canceled {
		return ""
	}
	hint := "[y/N]"
	if m.defaultYes {
		hint = "[Y/n]"
	}
	return fmt.Sprintf("%s %s ", m.label, hint)
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
