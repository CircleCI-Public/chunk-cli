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
	Comment, Delete, Resolve key.Binding
	// Next and Prev share a hint while both have somewhere to go.
	Next, Prev key.Binding
	// prevUnlisted is set when Next's hint already names Prev.
	prevUnlisted bool
	// SendComments saves the queued comments to factory from the plan,
	// without a trip to the chat; enter does it there.
	SendComments key.Binding
	// While a comment is written.
	SaveComment, CancelComment key.Binding
	// While sending the plan is confirmed. Any other key, enter too, says
	// no: sending starts a factory run, so it takes a y.
	Confirm key.Binding

	Focus    key.Binding
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
		Resolve:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "resolve comment")),
		Next:           key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next comment")),
		Prev:           key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "previous comment")),
		SendComments:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "save comments")),
		SaveComment:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save comment")),
		CancelComment:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		Confirm:        key.NewBinding(key.WithKeys("y")),
		Focus:          key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", focusHelp)),
		SendPlan:       key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "send plan to factory")),
		Quit:           key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	}

	// Esc leaves, unless it has a comment to cancel, a selection to clear or a
	// question to answer no to. q leaves from the plan, where no text is being
	// typed. A screen with a host goes back to it, and leaving stops nothing:
	// the host keeps the screen, and chunk works on. One without closes, asking
	// first when that would drop comments or a message.
	quit := "quit"
	if m.back {
		quit = "back to runs"
	}
	k.Quit = key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", quit))
	if !m.commenting && !m.confirming && !m.selecting() {
		keys := []string{"esc", "ctrl+c"}
		if m.focus == panePlan {
			keys = append(keys, "q")
		}
		k.Quit = key.NewBinding(key.WithKeys(keys...), key.WithHelp("esc", quit))
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
	// Esc clears a selection before it leaves.
	k.ClearSelection.SetEnabled(inPlan && m.selecting())
	if m.selecting() {
		k.Comment.SetHelp("c", "comment on selection")
	}
	m.commentKeys(&k, inPlan)
	k.SendComments.SetEnabled(inPlan && !m.savingComments && (len(queuedComments(m.comments)) > 0 || m.pendingHandOff))
	k.SaveComment.SetEnabled(m.commenting)
	k.CancelComment.SetEnabled(m.commenting)
	k.Focus.SetEnabled(!m.commenting)
	k.Confirm.SetEnabled(m.confirming)
	return k
}

// commentKeys enables the keys that act on the plan's comments, inPlan being
// whether the plan has the keyboard.
func (m Model) commentKeys(k *keyMap, inPlan bool) {
	// While the queued comments are saved they stay as they are: the save
	// drops them as it saves them, so one changed meanwhile would be lost.
	k.Comment.SetEnabled(k.Comment.Enabled() && !m.onResolved(m.cursor) && !m.savingComments)
	k.Delete.SetEnabled(inPlan && !m.selecting() && !m.savingComments && m.commentAt(m.cursor) >= 0)
	hasID := func() bool {
		i := m.commentAt(m.cursor)
		return i >= 0 && m.comments[i].id != ""
	}
	k.Resolve.SetEnabled(inPlan && !m.selecting() && !m.savingComments && hasID())
	// n has nowhere to go with nothing commented below the cursor, so the
	// footer does not offer it there.
	hasNext := inPlan && m.nextComment() != m.cursor
	hasPrev := inPlan && m.prevComment() != m.cursor
	k.Next.SetEnabled(hasNext)
	k.Prev.SetEnabled(hasPrev)
	if hasNext && hasPrev {
		// One hint for both, as the paging keys have.
		k.Next.SetHelp("n/N", "next/prev comment")
		k.prevUnlisted = true
	}
}

// ShortHelp is the footer's hints, most useful first: the footer drops hints
// from the end until it fits.
func (k keyMap) ShortHelp() []key.Binding {
	prev := k.Prev
	if k.prevUnlisted {
		prev.SetEnabled(false)
	}
	return []key.Binding{
		k.SaveComment, k.CancelComment,
		k.Send, k.Newline,
		k.Comment, k.Next, k.SendComments, k.ClearSelection, k.Up,
		k.Focus, k.SendPlan, k.Quit,
		k.SectionBack, k.Delete, k.Resolve, prev, k.ExtendUp, k.PageUp, k.ChatPageUp,
	}
}

// FullHelp satisfies help.KeyMap; the screen only shows the short form.
func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }
