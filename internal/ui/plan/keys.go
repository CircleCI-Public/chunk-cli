package plan

import (
	"charm.land/bubbles/v2/key"
)

// keyMap is every key the planning screen answers to. The handlers match
// against it and the footer is drawn from it, as in chunk watch, so a hint
// cannot name a key that does nothing: a binding the current state has no use
// for is disabled, which both stops it matching and drops it from the footer.
type keyMap struct {
	// Chat keys. Newline is the textarea's own; it is here for its hint.
	Send, Newline key.Binding
	// ChatPageUp carries the hint for both; the input has no use for them.
	ChatPageUp, ChatPageDown key.Binding
	// Plan keys. The page keys move the highlight a page or half a page at a
	// time, as well as the view; PageUp carries the hint for all four.
	Up, Down, Top, Bottom key.Binding
	// ExtendUp carries the hint for both.
	ExtendUp, ExtendDown, ClearSelection key.Binding
	PageUp, PageDown                     key.Binding
	HalfUp, HalfDown                     key.Binding
	// SectionBack carries the hint for both.
	SectionBack, SectionNext key.Binding
	Comment, Delete, Next    key.Binding
	// While a comment is written.
	SaveComment, CancelComment key.Binding
	// While sending the plan is confirmed. Any other key, enter too, says
	// no: sending starts a factory run, so it takes a y.
	Confirm key.Binding

	Focus    key.Binding
	Stop     key.Binding
	SendPlan key.Binding
	Quit     key.Binding
}

// keys returns the screen's bindings as they stand for the current focus and
// state.
func (m Model) keys() keyMap {
	newline := "ctrl+j"
	// A terminal that does not tell shift+enter from enter sends the
	// message on shift+enter, so it is only offered where it works.
	if m.disambiguates {
		newline = "shift+enter"
	}
	focusHelp := "plan"
	if m.focus == panePlan {
		focusHelp = "chat"
	}
	k := keyMap{
		Send:           key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Newline:        key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"), key.WithHelp(newline, "new line")),
		ChatPageUp:     key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup/pgdn", "scroll chat")),
		ChatPageDown:   key.NewBinding(key.WithKeys("pgdown")),
		Up:             key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/↓", "move")),
		Down:           key.NewBinding(key.WithKeys("down", "j")),
		ExtendUp:       key.NewBinding(key.WithKeys("shift+up", "K"), key.WithHelp("shift+↑/↓", "select")),
		ExtendDown:     key.NewBinding(key.WithKeys("shift+down", "J")),
		ClearSelection: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear selection")),
		Top:            key.NewBinding(key.WithKeys("g", "home")),
		Bottom:         key.NewBinding(key.WithKeys("G", "end")),
		PageUp:         key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup/pgdn", "page")),
		PageDown:       key.NewBinding(key.WithKeys("pgdown")),
		HalfUp:         key.NewBinding(key.WithKeys("ctrl+u")),
		HalfDown:       key.NewBinding(key.WithKeys("ctrl+d")),
		SectionBack:    key.NewBinding(key.WithKeys("["), key.WithHelp("[/]", "section")),
		SectionNext:    key.NewBinding(key.WithKeys("]")),
		Comment:        key.NewBinding(key.WithKeys("c", "enter"), key.WithHelp("c", "comment")),
		Delete:         key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete comment")),
		Next:           key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next comment")),
		SaveComment:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save comment")),
		CancelComment:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		Confirm:        key.NewBinding(key.WithKeys("y")),
		Focus:          key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", focusHelp)),
		Stop:           key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "stop Claude")),
		SendPlan:       key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "send plan to factory")),
		Quit:           key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	}

	if m.back {
		// Esc leaves, as it does elsewhere in the host, unless it has
		// Claude to stop, a comment to cancel or a question to answer no to.
		k.Quit = key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "back to runs"))
		if !m.busy && !m.commenting && !m.confirming && !m.selecting() {
			k.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "back to runs"))
		}
	}

	inChat := m.focus == paneChat && !m.commenting
	inPlan := m.focus == panePlan && !m.commenting
	for _, b := range []*key.Binding{&k.Send, &k.ChatPageUp, &k.ChatPageDown} {
		b.SetEnabled(inChat)
	}
	k.Newline.SetEnabled(inChat || m.commenting)
	for _, b := range []*key.Binding{&k.ExtendUp, &k.ExtendDown, &k.Up, &k.Down, &k.Top, &k.Bottom, &k.PageUp, &k.PageDown, &k.HalfUp, &k.HalfDown, &k.SectionBack, &k.SectionNext, &k.Comment} {
		b.SetEnabled(inPlan && len(m.blocks) > 0)
	}
	// Esc clears a selection before it stops Claude or leaves.
	k.ClearSelection.SetEnabled(inPlan && m.selecting())
	if m.selecting() {
		k.Comment.SetHelp("c", "comment on selection")
	}
	k.Delete.SetEnabled(inPlan && !m.selecting() && m.commentAt(m.cursor) >= 0)
	// n has nowhere to go with nothing commented below the cursor, so the
	// footer does not offer it there.
	k.Next.SetEnabled(inPlan && m.nextComment() != m.cursor)
	k.SaveComment.SetEnabled(m.commenting)
	k.CancelComment.SetEnabled(m.commenting)
	k.Focus.SetEnabled(!m.commenting)
	k.Stop.SetEnabled(m.busy && !m.commenting && !m.selecting())
	k.Confirm.SetEnabled(m.confirming)
	return k
}

// ShortHelp is the footer's hints, most useful first: the footer drops hints
// from the end until it fits.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{
		k.SaveComment, k.CancelComment,
		k.Send, k.Newline,
		k.Comment, k.ClearSelection, k.Up,
		k.Stop, k.Focus, k.SendPlan, k.Quit,
		k.SectionBack, k.Delete, k.Next, k.ExtendUp, k.PageUp, k.ChatPageUp,
	}
}

// FullHelp satisfies help.KeyMap; the screen only shows the short form.
func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }
