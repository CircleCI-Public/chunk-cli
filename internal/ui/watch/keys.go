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
	// CopyBranch and CopyPR copy the run's branch name and pull request URL
	// from anywhere, so neither needs the cursor on its row. CopyPR goes
	// unlisted: CopyBranch's hint names both.
	CopyBranch, CopyPR key.Binding
	Toggle             key.Binding
	Search             key.Binding
	// Keep and Clear end a search: Enter keeps its filter, Esc drops it.
	Keep, Clear key.Binding
	// The new run's step keys are drawn bold in the accent colour, so a new
	// user reads them as the key to press. New opens a prompt for Claude to
	// draft a change, and Send and Discard end it: Enter has Claude draft it,
	// Esc drops the prompt. StopDraft stops Claude while it drafts. Once the
	// draft is shown in the right pane, ScrollDraft scrolls it, SendDraft
	// hands it to the factory and KeepDraft leaves it for later.
	New, Send, Discard                key.Binding
	StopDraft                         key.Binding
	ScrollDraft, SendDraft, KeepDraft key.Binding
	// ToggleSection folds or unfolds the draft section in focus, and
	// FocusSection moves the focus between them.
	ToggleSection, FocusSection key.Binding
	Details                     key.Binding
	Cancel                      key.Binding
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
		Up:            key.NewBinding(key.WithKeys("up", "k", "w"), key.WithHelp("↑/↓", "select")),
		Down:          key.NewBinding(key.WithKeys("down", "j", "s")),
		Left:          key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←", "back")),
		Right:         key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→", "run")),
		Open:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "output")),
		OpenPR:        key.NewBinding(key.WithKeys("o", "super+enter")),
		Copy:          key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy failure")),
		CopyBranch:    key.NewBinding(key.WithKeys("b"), key.WithHelp("b/p", "copy branch/PR")),
		CopyPR:        key.NewBinding(key.WithKeys("p")),
		Toggle:        key.NewBinding(key.WithKeys("space"), key.WithHelp("Space", "toggle")),
		Search:        key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search runs")),
		Keep:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "keep filter")),
		Clear:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "clear")),
		Details:       key.NewBinding(key.WithKeys("d", "i"), key.WithHelp("d", "details")),
		New:           key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new run")),
		Send:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "go to step 2")),
		Discard:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		StopDraft:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "stop Claude")),
		ScrollDraft:   key.NewBinding(key.WithKeys("up", "down", "k", "j", "pgup", "pgdown"), key.WithHelp("↑/↓", "scroll the change")),
		ToggleSection: key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "expand/collapse")),
		FocusSection:  key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "switch section")),
		SendDraft:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send to factory (last step)")),
		KeepDraft:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "keep for later")),
		Cancel:        key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "cancel run")),
		Back:          key.NewBinding(key.WithKeys("esc")),
		Quit:          key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		ForceQuit:     key.NewBinding(key.WithKeys("ctrl+c")),
	}
	k.Search.SetEnabled(!right && len(m.allRuns) > 0)
	k.Keep.SetEnabled(m.searching)
	k.Clear.SetEnabled(m.searching)
	k.New.SetEnabled(m.drafter != nil && m.sender != nil && m.startRoot() != "")
	k.Send.SetEnabled(m.draft == draftPrompting)
	k.Discard.SetEnabled(m.draft == draftPrompting)
	k.StopDraft.SetEnabled(m.draft == draftWorking)
	changed := m.draft == draftReady && m.draftResult.Stat != ""
	k.ScrollDraft.SetEnabled(changed)
	k.ToggleSection.SetEnabled(changed)
	k.FocusSection.SetEnabled(changed)
	k.SendDraft.SetEnabled(changed)
	k.KeepDraft.SetEnabled(m.draft == draftReady)
	if m.draft == draftReady && !changed {
		k.KeepDraft.SetHelp("esc", "close")
	}
	k.Left.SetEnabled(right)
	k.Right.SetEnabled(!right && run != nil)
	k.enableBranchAndPR(m, run)
	k.Toggle.SetEnabled(right && run != nil)
	k.Up.SetEnabled(len(m.sessions) > 0)
	k.Down.SetEnabled(len(m.sessions) > 0)
	// A run with nothing to detail yet (it failed before its worktree was
	// made, say) draws no details row, so the key would do nothing.
	k.Details.SetEnabled(run != nil && m.runDetailLines(m.styles(), *run) != nil)
	k.Cancel.SetEnabled(run != nil && !run.s.State.Finished())
	if run != nil {
		k.Toggle.SetHelp("Space", "expand/collapse")
		if it := m.selectedRunItem(); it != nil && it.kind == itemTask {
			k.Copy.SetHelp("c", "copy task")
		}
	}
	if m.searching || m.draft != draftNone {
		// Typing goes to the search or the prompt, and a draft being made or
		// shown has keys of its own, so no other key does anything: d is the
		// draft's diff then, not a run's details.
		for _, b := range []*key.Binding{&k.Up, &k.Down, &k.Left, &k.Right, &k.Open, &k.OpenPR, &k.Copy, &k.CopyBranch, &k.CopyPR, &k.Toggle, &k.Search, &k.New, &k.Details, &k.Cancel, &k.Quit} {
			b.SetEnabled(false)
		}
	}
	return k
}

// enableBranchAndPR turns on the keys for the run's branch and pull request,
// and words Open and Copy for the row that has the cursor.
func (k *keyMap) enableBranchAndPR(m Model, run *sessionInfo) {
	right := m.focusedPane == paneRight
	item := m.selectedRunItem()
	onPR := right && item != nil && item.kind == itemPR
	onBranch := right && item != nil && item.kind == itemBranch
	factory := run != nil && run.s.Factory != nil
	hasPR := factory && run.s.Factory.PullRequest.Opened()
	// Enter on the branch has nothing to do, so it is not offered there.
	k.Open.SetEnabled(right && run != nil && !onBranch)
	k.OpenPR.SetEnabled(right && hasPR)
	k.Copy.SetEnabled(right && run != nil)
	k.CopyBranch.SetEnabled(factory && run.s.Factory.Branch != "")
	k.CopyPR.SetEnabled(hasPR)
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
}

// ShortHelp is the footer's hints, in the order they are shown.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Send, k.Discard, k.StopDraft, k.SendDraft, k.ScrollDraft, k.ToggleSection, k.KeepDraft, k.Keep, k.Clear, k.New, k.Up, k.Right, k.Open, k.Copy, k.CopyBranch, k.Toggle, k.Search, k.Details, k.Cancel, k.Left, k.Quit}
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
