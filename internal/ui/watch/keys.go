package watch

import (
	"charm.land/bubbles/v2/key"
)

// keyMap is the keys the dashboard answers to itself. Every other key goes to
// the flow list or the open plan screen, which draw their own hints.
type keyMap struct {
	// Flows shows the flow list from a plan screen, to switch to another plan.
	Flows key.Binding
	// ForceQuit confirms quitting when plans have unsent input.
	ForceQuit key.Binding
	// Chat opens or closes the chat, which CloseChat also closes.
	Chat, CloseChat key.Binding
}

func (m Model) keys() keyMap {
	return keyMap{
		Flows:     key.NewBinding(key.WithKeys("ctrl+p")),
		ForceQuit: key.NewBinding(key.WithKeys("ctrl+c")),

		Chat:      key.NewBinding(key.WithKeys("ctrl+o")),
		CloseChat: key.NewBinding(key.WithKeys("esc")),
	}
}
