package watch

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
)

// keyMap is every key the dashboard answers to. The handlers match against it
// and the footer is drawn from it, so a hint cannot name a key that does
// nothing, or leave out one that does: a binding the current state has no use
// for is disabled, which both stops it matching and drops it from the footer.
type keyMap struct {
	Up, Down    key.Binding
	Left, Right key.Binding
	Open        key.Binding
	Toggle      key.Binding
	Cancel      key.Binding
	// Back is Esc: back to the list from the right pane, and quit from the
	// list. It goes unlisted, since q and ← already say both.
	Back      key.Binding
	Quit      key.Binding
	ForceQuit key.Binding
}

// keys returns the dashboard's bindings as they stand for the current focus
// and selection.
func (m Model) keys() keyMap {
	run := m.selectedRun()
	right := m.focusedPane == paneRight
	k := keyMap{
		Up:        key.NewBinding(key.WithKeys("up", "k", "w"), key.WithHelp("↑/↓", "select")),
		Down:      key.NewBinding(key.WithKeys("down", "j", "s")),
		Left:      key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←", "back")),
		Right:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→", "details")),
		Open:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "output")),
		Toggle:    key.NewBinding(key.WithKeys("space"), key.WithHelp("Space", "toggle")),
		Cancel:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "cancel run")),
		Back:      key.NewBinding(key.WithKeys("esc")),
		Quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		ForceQuit: key.NewBinding(key.WithKeys("ctrl+c")),
	}
	k.Left.SetEnabled(right)
	k.Right.SetEnabled(!right)
	k.Open.SetEnabled(right)
	k.Toggle.SetEnabled(right && run == nil)
	k.Cancel.SetEnabled(run != nil && !run.s.State.Finished())
	if run == nil {
		k.Right.SetHelp("→", "activity")
		if right {
			k.Up.SetHelp("↑/↓", "navigate")
		}
	}
	return k
}

// ShortHelp is the footer's hints, in the order they are shown.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Right, k.Open, k.Toggle, k.Cancel, k.Left, k.Quit}
}

// FullHelp satisfies help.KeyMap; the dashboard only shows the short form.
func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

// outputKeyMap is the output pane's keys.
type outputKeyMap struct {
	Close              key.Binding
	ScrollUp, ScrollDn key.Binding
	PageUp, PageDown   key.Binding
	Top, Follow        key.Binding
	ForceQuit          key.Binding
}

func newOutputKeyMap() outputKeyMap {
	return outputKeyMap{
		Close:     key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "back")),
		ScrollUp:  key.NewBinding(key.WithKeys("up", "k", "w"), key.WithHelp("↑/↓", "scroll")),
		ScrollDn:  key.NewBinding(key.WithKeys("down", "j", "s")),
		PageUp:    key.NewBinding(key.WithKeys("pgup")),
		PageDown:  key.NewBinding(key.WithKeys("pgdown")),
		Top:       key.NewBinding(key.WithKeys("g"), key.WithHelp("g/G", "top/follow")),
		Follow:    key.NewBinding(key.WithKeys("G")),
		ForceQuit: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl-c", "quit")),
	}
}

func (k outputKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Close, k.ScrollUp, k.Top, k.ForceQuit}
}

func (k outputKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

// newHelp is a help view styled like the rest of the dashboard, fitting width.
func newHelp(st watchStyles, width int) help.Model {
	h := help.New()
	h.ShortSeparator = "  ·  "
	h.Styles.ShortKey = st.VDim
	h.Styles.ShortDesc = st.Dim
	h.Styles.ShortSeparator = st.VDim
	h.Styles.Ellipsis = st.VDim
	h.SetWidth(max(width, 1))
	return h
}
