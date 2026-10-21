package plan

import (
	tea "charm.land/bubbletea/v2"
)

// Screen runs the planning screen as a program's whole screen, which ends
// when the screen closes.
type Screen struct {
	Model Model
}

func (s Screen) Init() tea.Cmd { return s.Model.Init() }

func (s Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(ClosedMsg); ok {
		return s, tea.Quit
	}
	var cmd tea.Cmd
	s.Model, cmd = s.Model.Update(msg)
	return s, cmd
}

func (s Screen) View() tea.View {
	v := tea.NewView(s.Model.Content())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
