package flowlist

import "charm.land/bubbles/v2/key"

// keyMap is the flow list's keys, as its help bar shows them.
type keyMap struct {
	Select, Open, New, Runs, Quit key.Binding
}

func (m Model) keys() keyMap {
	k := keyMap{
		Select: key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑/↓", "select")),
		Open:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		New:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new plan")),
		Runs:   key.NewBinding(key.WithKeys("tab", "2", "esc"), key.WithHelp("tab", "runs")),
		Quit:   key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
	listed := len(m.flows) > 0
	k.Select.SetEnabled(listed)
	k.Open.SetEnabled(listed)
	return k
}

// ShortHelp puts what starts something and the way to the runs first, so
// they are the last to go on a narrow terminal.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.New, k.Runs, k.Select, k.Open, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }
