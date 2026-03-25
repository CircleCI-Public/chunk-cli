package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type selectModel struct {
	label    string
	items    []string
	cursor   int
	done     bool
	canceled bool
}

func (m selectModel) Init() tea.Cmd {
	return nil
}

func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.canceled = true
			return m, tea.Quit
		case tea.KeyEnter:
			m.done = true
			return m, tea.Quit
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown:
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case tea.KeyRunes:
			s := msg.String()
			if s == "k" && m.cursor > 0 {
				m.cursor--
			}
			if s == "j" && m.cursor < len(m.items)-1 {
				m.cursor++
			}
		}
	}
	return m, nil
}

func (m selectModel) View() string {
	if m.done || m.canceled {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", m.label)
	for i, item := range m.items {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		fmt.Fprintf(&b, "%s%s\n", cursor, item)
	}
	b.WriteString("\n(↑/↓ to move, Enter to select, Esc to cancel)\n")
	return b.String()
}

// SelectFromList presents a list of items and returns the selected index.
// Returns ErrCancelled if the user presses Ctrl+C or Esc.
func SelectFromList(label string, items []string) (int, error) {
	if len(items) == 0 {
		return -1, fmt.Errorf("no items to select from")
	}

	model := selectModel{label: label, items: items}
	p := tea.NewProgram(model)
	result, err := p.Run()
	if err != nil {
		return -1, fmt.Errorf("select prompt: %w", err)
	}

	m := result.(selectModel)
	if m.canceled {
		return -1, ErrCancelled
	}
	return m.cursor, nil
}
