package plan

import (
	"charm.land/bubbles/v2/key"

	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
)

// keyMap is every key the planning screen answers to. The handlers match
// against it and the footer is drawn from it, as in chunk ui, so a hint
// cannot name a key that does nothing: a binding the current state has no use
// for is disabled, which both stops it matching and drops it from the footer.
type keyMap struct {
	// Request keys, for the left pane before the plan exists. Newline is the
	// textarea's own; it is here for its hint.
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
	// Edit opens the highlighted part of the plan in $EDITOR.
	Edit key.Binding
	// Next and Prev share a hint while both have somewhere to go.
	Next, Prev key.Binding
	// prevUnlisted is set when Next's hint already names Prev.
	prevUnlisted bool
	// SendComments saves the queued comments to factory, from the plan or the
	// Overview.
	SendComments key.Binding
	// While a comment is written.
	SaveComment, CancelComment key.Binding
	// Open opens the Sub-flows or Related row under the Overview's cursor, or
	// expands or collapses its Outcome or stage row.
	Open key.Binding
	// While approving the plan, or its design, is confirmed. Any other key,
	// enter too, says no: approving moves the plan on in factory, so it
	// takes a y.
	Confirm key.Binding
	// End asks for a reason, then ends the plan without its PR merging;
	// DeletePlan asks for a y, then deletes it. At the reason prompt the
	// End* keys pick the reason.
	End, DeletePlan                                   key.Binding
	EndComplete, EndCanceled, EndWontDo, EndDuplicate key.Binding

	Focus  key.Binding
	Switch key.Binding
	// Chat is the host's; it is here for its hint.
	Chat     key.Binding
	SendPlan key.Binding
	// Resume starts the plan's agent again when factory waits on one that is
	// not there.
	Resume key.Binding
	// StopBuild stops the plan's running build session, after a y.
	StopBuild key.Binding
	Quit      key.Binding
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
	k := keyMap{
		Send:           key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Newline:        key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"), key.WithHelp(newline, "new line")),
		ChatPageUp:     key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup/pgdn", "scroll")),
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
		Edit:           key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Delete:         key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete comment")),
		Resolve:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "resolve comment")),
		Next:           key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next comment")),
		Prev:           key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "previous comment")),
		SendComments:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "save comments")),
		SaveComment:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save comment")),
		CancelComment:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		Open:           key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open plan")),
		Confirm:        key.NewBinding(key.WithKeys("y")),
		End:            key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "end plan")),
		DeletePlan:     key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "delete plan")),
		Focus:          key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", m.focusHelp())),
		Switch:         key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "switch plan")),
		Chat:           key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("ctrl+o", "chat")),
		SendPlan:       key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "approve plan")),
		Resume:         key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "resume")),
		StopBuild:      key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "stop build")),
		Quit:           key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	}
	// The reason prompt says what these do, so they carry no hint.
	k.EndComplete = key.NewBinding(key.WithKeys("m"))
	k.EndCanceled = key.NewBinding(key.WithKeys("c"))
	k.EndWontDo = key.NewBinding(key.WithKeys("w"))
	k.EndDuplicate = key.NewBinding(key.WithKeys("d"))

	// Esc leaves, unless it has a comment to cancel, a selection to clear or a
	// question to answer no to. q leaves wherever no text is being typed. A
	// screen with a host goes back to it, and leaving stops nothing: the host
	// keeps the screen, and chunk works on. One without closes, asking first
	// when that would drop comments or a message.
	quit := "quit"
	if m.back {
		quit = "back"
	}
	if m.inbox {
		quit = "back to plans"
	}
	k.Switch.SetEnabled(m.inbox)
	k.Quit = key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", quit))
	if !m.commenting && !m.confirming && !m.askingReason && !m.selecting() {
		keys := []string{"esc", "ctrl+c"}
		if m.focus == panePlan || m.created {
			keys = append(keys, "q")
		}
		k.Quit = key.NewBinding(key.WithKeys(keys...), key.WithHelp("esc", quit))
	}

	// The plan's keys act in the plan only; the Overview has its own.
	inPlan := m.focus == panePlan && !m.commenting
	inOverview := m.focus == paneOverview && m.created && !m.commenting
	m.chatKeys(&k)
	for _, b := range []*key.Binding{&k.ExtendUp, &k.ExtendDown, &k.Up, &k.Down, &k.Top, &k.Bottom, &k.PageUp, &k.PageDown, &k.HalfUp, &k.HalfDown, &k.SectionBack, &k.SectionNext, &k.Comment} {
		b.SetEnabled(inPlan && len(m.blocks) > 0)
	}
	m.overviewKeys(&k, inOverview)
	// Esc clears a selection before it leaves.
	k.ClearSelection.SetEnabled(inPlan && m.selecting())
	if m.selecting() {
		k.Comment.SetHelp("c", "comment on selection")
	}
	m.commentKeys(&k, inPlan)
	// A comment on a part since rewritten has no text in the plan to edit.
	k.Edit.SetEnabled(inPlan && m.cursor < len(m.blocks) && !m.editing && !m.savingComments)
	k.SendComments.SetEnabled((inPlan || inOverview) && !m.savingComments && m.takesComments() &&
		(len(queuedComments(m.comments)) > 0 || m.pendingHandOff))
	k.SaveComment.SetEnabled(m.commenting)
	k.CancelComment.SetEnabled(m.commenting)
	k.Focus.SetEnabled(!m.commenting)
	k.Confirm.SetEnabled(m.confirming)
	k.Resume.SetEnabled(m.resumeOffered())
	k.StopBuild.SetEnabled(m.stopOffered())
	m.endKeys(&k)
	return k
}

// stopOffered is whether S is offered now.
func (m Model) stopOffered() bool {
	return m.build != nil && m.created && m.buildRunning() && !m.stoppingBuild &&
		!m.commenting && !m.confirming && !m.askingReason
}

// resumeOffered is whether R is offered now.
func (m Model) resumeOffered() bool {
	return m.created && !m.commenting && !m.confirming && !m.askingReason &&
		!m.resumingBuild && !m.takingOver && !m.resuming && m.resumable().Kind != planstate.ResumeNone
}

// chatKeys enables the request's keys: the left pane has an input to type in,
// and a new line to type, until the plan exists.
func (m Model) chatKeys(k *keyMap) {
	inRequest := m.focus == paneOverview && !m.created && !m.commenting
	for _, b := range []*key.Binding{&k.Send, &k.ChatPageUp, &k.ChatPageDown} {
		b.SetEnabled(inRequest)
	}
	k.Newline.SetEnabled(inRequest || m.commenting)
}

// endKeys enables the keys that end the plan: end, its reasons, and
// delete.
func (m Model) endKeys(k *keyMap) {
	// The plan and the Overview, the pane a plan opens on once it is being
	// built, both end and delete it; the request has an input to type in.
	idle := (m.focus == panePlan || m.created) && !m.commenting && !m.confirming && !m.askingReason && !m.ending && !m.deleting
	k.End.SetEnabled(idle && m.created && m.state != "" && !m.flow.Ended)
	k.DeletePlan.SetEnabled(idle && m.created && !m.flow.Claimed)
	for _, b := range []*key.Binding{&k.EndComplete, &k.EndCanceled, &k.EndWontDo, &k.EndDuplicate} {
		b.SetEnabled(m.askingReason)
	}
	k.Chat.SetEnabled(m.chatKey && !m.commenting)
}

// focusHelp is where tab goes: to the plan from the Overview, or the request
// before it exists, and to the Overview from the plan.
func (m Model) focusHelp() string {
	if m.focus == panePlan {
		return "overview"
	}
	return "plan"
}

// overviewKeys enables the Overview's keys, inOverview being whether it has
// the keyboard.
func (m Model) overviewKeys(k *keyMap, inOverview bool) {
	k.Open.SetEnabled(false)
	if !inOverview {
		return
	}
	// ↑/↓ move over the rows, and the page keys scroll them.
	rows := m.overviewRows()
	k.Up.SetEnabled(len(rows) > 1)
	k.Down.SetEnabled(len(rows) > 1)
	k.PageUp.SetEnabled(true)
	k.PageDown.SetEnabled(true)
	if m.overviewCursor >= len(rows) {
		return
	}
	r := rows[m.overviewCursor]
	switch r.kind {
	case rowOutcome:
		m.expandKey(k, "outcome", m.outcomeFolds || m.expanded["outcome"])
	case rowStage:
		m.expandKey(k, r.stage, m.stageHasLog(r.stage))
	case rowSubFlow, rowRelated:
		// A relative opens only in a host that can open another plan.
		k.Open.SetEnabled(m.inbox)
		k.Open.SetHelp("enter", "open plan")
	}
}

// expandKey offers enter on a row that opens and closes, when it has
// something to show.
func (m Model) expandKey(k *keyMap, id string, can bool) {
	k.Open.SetEnabled(can)
	if m.expanded[id] {
		k.Open.SetHelp("enter", "collapse")
		return
	}
	k.Open.SetHelp("enter", "expand")
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
	// In the Overview the cursor's keys come first, move before open.
	moves := []key.Binding{k.Open, k.Comment, k.Edit, k.Next, k.SendComments, k.ClearSelection, k.Up}
	if k.Open.Enabled() {
		moves = []key.Binding{k.Up, k.Open, k.SendComments}
	}
	hints := []key.Binding{
		k.SaveComment, k.CancelComment,
		k.Switch,
		k.Send, k.Newline,
	}
	hints = append(hints, moves...)
	return append(hints,
		k.Focus, k.SendPlan, k.Resume, k.StopBuild, k.Quit, k.Chat, k.End, k.DeletePlan,
		k.SectionBack, k.Delete, k.Resolve, prev, k.ExtendUp, k.PageUp, k.ChatPageUp,
	)
}

// FullHelp satisfies help.KeyMap; the screen only shows the short form.
func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }
