package flowlist

import (
	"charm.land/bubbles/v2/key"

	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
)

// keyMap is the flow list's keys, as its help bar shows them.
type keyMap struct {
	Select, Open, Resume, New, Quit key.Binding
	// Chat is the host's; it is here for its hint.
	Chat key.Binding
}

func (m Model) keys() keyMap {
	k := keyMap{
		Select: key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑/↓", "select")),
		Open:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Resume: key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "resume")),
		New:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new plan")),
		Quit:   key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "quit")),

		Chat: key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("ctrl+o", "chat")),
	}
	k.Chat.SetEnabled(m.chatKey)
	listed := len(m.flows) > 0
	k.Select.SetEnabled(listed)
	k.Open.SetEnabled(listed)
	k.Resume.SetEnabled(m.resume().Kind != planstate.ResumeNone)
	return k
}

// ShortHelp puts what starts something first, so it is the last to go on a
// narrow terminal.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.New, k.Select, k.Open, k.Resume, k.Chat, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }
