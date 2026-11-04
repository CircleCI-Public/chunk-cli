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
	// Chat shows the chat with the keyboard, gives it the keyboard back
	// when the screen beside it has it, and hides it when it has it.
	// Unfocus, in the chat, gives the keyboard to the screen beside it, or
	// hides a chat that covers the screen.
	Chat, Unfocus key.Binding
}

func (m Model) keys() keyMap {
	return keyMap{
		Flows:     key.NewBinding(key.WithKeys("ctrl+p")),
		ForceQuit: key.NewBinding(key.WithKeys("ctrl+c")),

		Chat:    key.NewBinding(key.WithKeys("ctrl+o")),
		Unfocus: key.NewBinding(key.WithKeys("esc")),
	}
}
