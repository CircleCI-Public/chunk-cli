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
	// OpenPR opens the run's pull request from anywhere in the run pane. It is
	// not listed in the footer: on the pull request row Open's hint names it.
	OpenPR key.Binding
	Copy   key.Binding
	Toggle key.Binding
	Search key.Binding
	// Keep and Clear end a search: Enter keeps its filter, Esc drops it.
	Keep, Clear key.Binding
	Details     key.Binding
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
		OpenPR:    key.NewBinding(key.WithKeys("o", "super+enter")),
		Copy:      key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy failure")),
		Toggle:    key.NewBinding(key.WithKeys("space"), key.WithHelp("Space", "toggle")),
		Search:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search runs")),
		Keep:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "keep filter")),
		Clear:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "clear")),
		Details:   key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "infra")),
		Cancel:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "cancel run")),
		Back:      key.NewBinding(key.WithKeys("esc")),
		Quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		ForceQuit: key.NewBinding(key.WithKeys("ctrl+c")),
	}
	k.Search.SetEnabled(!right && len(m.allRuns) > 0)
	k.Keep.SetEnabled(m.searching)
	k.Clear.SetEnabled(m.searching)
	k.Left.SetEnabled(right)
	k.Right.SetEnabled(!right)
	item := m.selectedRunItem()
	onPR := right && item != nil && item.kind == itemPR
	onBranch := right && item != nil && item.kind == itemBranch
	// Enter on the branch has nothing to do, so it is not offered there.
	k.Open.SetEnabled(right && !onBranch)
	k.OpenPR.SetEnabled(right && run != nil && run.s.Factory != nil && run.s.Factory.PullRequest.Opened())
	k.Copy.SetEnabled(right && run != nil)
	switch {
	case onPR:
		hint := "Enter/o"
		if m.superEnter {
			hint = "Enter/⌘Enter/o"
		}
		k.Open.SetHelp(hint, "open")
		k.Copy.SetHelp("c", "copy URL")
	case onBranch:
		k.Copy.SetHelp("c", "copy branch")
	}
	k.Toggle.SetEnabled(right)
	// A run with nothing under infra yet (it failed before its worktree was
	// made, say) draws no infra row, so i would do nothing.
	k.Details.SetEnabled(run != nil && m.runDetailLines(m.styles(), *run) != nil)
	k.Cancel.SetEnabled(run != nil && !run.s.State.Finished())
	if run != nil {
		k.Toggle.SetHelp("Space", "expand/collapse")
	}
	if run == nil {
		k.Right.SetHelp("→", "activity")
		if right {
			k.Up.SetHelp("↑/↓", "navigate")
		}
	}
	if m.searching {
		// Typing goes to the search, so no other key does anything.
		for _, b := range []*key.Binding{&k.Up, &k.Down, &k.Left, &k.Right, &k.Open, &k.OpenPR, &k.Copy, &k.Toggle, &k.Search, &k.Details, &k.Cancel, &k.Quit} {
			b.SetEnabled(false)
		}
	}
	return k
}

// ShortHelp is the footer's hints, in the order they are shown.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Keep, k.Clear, k.Up, k.Right, k.Open, k.Copy, k.Toggle, k.Search, k.Details, k.Cancel, k.Left, k.Quit}
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
	h.Styles.ShortKey = st.Teal
	h.Styles.ShortDesc = st.Dim
	h.Styles.ShortSeparator = st.VDim
	h.Styles.Ellipsis = st.VDim
	h.SetWidth(max(width, 1))
	return h
}
