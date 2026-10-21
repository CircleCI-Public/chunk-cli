package plan

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

// keyMap is every key the planning screen answers to. The handlers match
// against it and the footer is drawn from it, as in chunk watch, so a hint
// cannot name a key that does nothing: a binding the current state has no use
// for is disabled, which both stops it matching and drops it from the footer.
type keyMap struct {
	// Chat keys. Newline is the textarea's own; it is here for its hint.
	Send, Newline key.Binding
	// Plan keys.
	Up, Down, Top, Bottom key.Binding
	// SectionBack carries the hint for both.
	SectionBack, SectionNext key.Binding
	Comment, Delete, Next    key.Binding
	// While a comment is written.
	SaveComment, CancelComment key.Binding
	// While sending the plan is confirmed. Any other key says no.
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
		Send:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Newline:       key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"), key.WithHelp(newline, "new line")),
		Up:            key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/↓", "move")),
		Down:          key.NewBinding(key.WithKeys("down", "j")),
		Top:           key.NewBinding(key.WithKeys("g", "home")),
		Bottom:        key.NewBinding(key.WithKeys("G", "end")),
		SectionBack:   key.NewBinding(key.WithKeys("["), key.WithHelp("[/]", "section")),
		SectionNext:   key.NewBinding(key.WithKeys("]")),
		Comment:       key.NewBinding(key.WithKeys("c", "enter"), key.WithHelp("c", "comment")),
		Delete:        key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete comment")),
		Next:          key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next comment")),
		SaveComment:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save comment")),
		CancelComment: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		Confirm:       key.NewBinding(key.WithKeys("y", "enter")),
		Focus:         key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", focusHelp)),
		Stop:          key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "stop Claude")),
		SendPlan:      key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "send plan to factory")),
		Quit:          key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	}

	inChat := m.focus == paneChat && !m.commenting
	inPlan := m.focus == panePlan && !m.commenting
	for _, b := range []*key.Binding{&k.Send, &k.Newline} {
		b.SetEnabled(inChat)
	}
	for _, b := range []*key.Binding{&k.Up, &k.Down, &k.Top, &k.Bottom, &k.SectionBack, &k.SectionNext, &k.Comment} {
		b.SetEnabled(inPlan && len(m.blocks) > 0)
	}
	k.Delete.SetEnabled(inPlan && m.commentAt(m.cursor) >= 0)
	k.Next.SetEnabled(inPlan && len(m.comments) > 0)
	k.SaveComment.SetEnabled(m.commenting)
	k.CancelComment.SetEnabled(m.commenting)
	k.Focus.SetEnabled(!m.commenting)
	k.Stop.SetEnabled(m.busy && !m.commenting)
	k.Confirm.SetEnabled(m.confirming)
	return k
}

// ShortHelp is the footer's hints, most useful first: the footer drops hints
// from the end until it fits.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{
		k.SaveComment, k.CancelComment,
		k.Send, k.Newline,
		k.Comment, k.Up,
		k.Stop, k.Focus, k.SendPlan,
		k.SectionBack, k.Delete, k.Next, k.Quit,
	}
}

// FullHelp satisfies help.KeyMap; the screen only shows the short form.
func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

// newHelp is a footer styled like chunk watch's, fitting width.
func newHelp(dark bool, width int) help.Model {
	ld := lipgloss.LightDark(dark)
	h := help.New()
	h.ShortSeparator = "  ·  "
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(accentColor)
	h.Styles.ShortDesc = lipgloss.NewStyle().Foreground(ld(lipgloss.Color("248"), lipgloss.Color("246")))
	vdim := lipgloss.NewStyle().Foreground(ld(lipgloss.Color("244"), lipgloss.Color("242")))
	h.Styles.ShortSeparator = vdim
	h.Styles.Ellipsis = vdim
	h.SetWidth(max(width, 1))
	return h
}
