package watch

import (
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
	// Plan opens the planning screen, to make a plan with Claude and send
	// it to the factory, or goes back to the one left open.
	Plan key.Binding
	// Rounds shows or hides what a round is and how to change the cap on
	// them, under a running run's headline.
	Rounds key.Binding
	Cancel key.Binding
	// Continue starts a run that picks up the work of a finished one whose
	// committed work has not passed, as --continue does.
	Continue key.Binding
	// ToggleEnded shows or hides finished runs, and Delete forgets one.
	ToggleEnded, Delete key.Binding
	// Attention narrows the list to the runs that need a person.
	Attention key.Binding
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
		Up:            key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/↓", "select")),
		Down:          key.NewBinding(key.WithKeys("down", "j")),
		Left:          key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←", "back")),
		Right:         key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→", "run")),
		Open:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "output")),
		OpenPR:        key.NewBinding(key.WithKeys("o", "super+enter")),
		Copy:          key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy failure")),
		CopyBranch:    key.NewBinding(key.WithKeys("b"), key.WithHelp("b/u", "copy branch/PR")),
		CopyPR:        key.NewBinding(key.WithKeys("u")),
		Toggle:        key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle")),
		Search:        key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search runs")),
		Keep:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "keep filter")),
		Clear:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear")),
		New:           key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new run")),
		Plan:          key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "plan a run")),
		Send:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "go to step 2")),
		Discard:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		StopDraft:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "stop Claude")),
		ScrollDraft:   key.NewBinding(key.WithKeys("up", "down", "k", "j", "pgup", "pgdown"), key.WithHelp("↑/↓", "scroll")),
		ToggleSection: key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "expand/collapse")),
		FocusSection:  key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "switch section")),
		SendDraft:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "Send to factory")),
		KeepDraft:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "keep for later")),
		Rounds:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "about rounds")),
		Cancel:        key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "cancel run")),
		Continue:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "continue run")),
		ToggleEnded:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "show finished")),
		Attention:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "needs attention")),
		Delete:        key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete run")),
		Back:          key.NewBinding(key.WithKeys("esc")),
		Quit:          key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		ForceQuit:     key.NewBinding(key.WithKeys("ctrl+c")),
	}
	k.Search.SetEnabled(!right && len(m.allRuns) > 0)
	k.Keep.SetEnabled(m.searching)
	k.Clear.SetEnabled(m.searching)
	k.New.SetEnabled(m.drafter != nil && m.sender != nil && m.startRoot() != "")
	k.Plan.SetEnabled(m.planner != nil && (m.planScreen != nil || m.startRoot() != ""))
	if m.planScreen != nil {
		k.Plan.SetHelp("p", "back to plan")
	}
	if m.flows != nil {
		k.Plan = key.NewBinding(key.WithKeys("p", "tab", "1"), key.WithHelp("p/tab", "flows"))
	}
	k.Send.SetEnabled(m.draft == draftPrompting)
	k.Discard.SetEnabled(m.draft == draftPrompting)
	k.StopDraft.SetEnabled(m.draft == draftWorking)
	changed := m.draft == draftReady && m.draftResult.Stat != ""
	k.ScrollDraft.SetEnabled(changed)
	k.ToggleSection.SetEnabled(changed)
	if changed {
		open := "expand"
		if m.draftSectionOpen(m.draftFocus) {
			open = "collapse"
		}
		k.ToggleSection.SetHelp("space", open)
	}
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
	k.Cancel.SetEnabled(run != nil && !run.s.State.Finished())
	k.Rounds.SetEnabled(run != nil && hasRoundCap(run.s))
	k.Delete.SetEnabled(run != nil && run.s.State.Finished())
	k.Continue.SetEnabled(m.canContinue(run))
	k.ToggleEnded.SetEnabled(m.endedCount() > 0 || m.showEnded)
	if m.showEnded {
		k.ToggleEnded.SetHelp("e", "hide finished")
	}
	k.Attention.SetEnabled(len(m.allRuns) > 0)
	if m.attention {
		k.Attention.SetHelp("a", "show all runs")
	}
	k.setToggleHint(m, run, right)
	if m.searching || m.draft != draftNone {
		// Typing goes to the search or the prompt, and a draft being made or
		// shown has keys of its own, so no other key does anything.
		for _, b := range []*key.Binding{&k.Up, &k.Down, &k.Left, &k.Right, &k.Open, &k.OpenPR, &k.Copy, &k.CopyBranch, &k.CopyPR, &k.Toggle, &k.Search, &k.New, &k.Plan, &k.Rounds, &k.Cancel, &k.Continue, &k.Delete, &k.ToggleEnded, &k.Attention, &k.Quit} {
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
		hint := "enter/o"
		if m.superEnter {
			hint = "enter/⌘enter/o"
		}
		k.Open.SetHelp(hint, "open")
		k.Copy.SetHelp("c", "copy URL")
	case onBranch:
		k.Copy.SetHelp("c", "copy branch")
	}
}

// setToggleHint says in the footer what Space will do at the cursor: expand
// or collapse the fold that has it. A row with nothing to fold — the branch,
// the PR, a check that passed, a review still running — gets no hint.
func (k *keyMap) setToggleHint(m Model, run *sessionInfo, right bool) {
	it := m.selectedRunItem()
	if run == nil || it == nil {
		return
	}
	switch it.kind {
	case itemTask:
		k.Toggle.SetEnabled(right && len(taskLines(run.s.Factory.Prompt, m.taskWidth(), false)) > maxGoalLines)
		k.Toggle.SetHelp("space", foldHint(m.taskExpanded(run.s.ID)))
		k.Copy.SetHelp("c", "copy task")
	case itemRound:
		k.Toggle.SetHelp("space", foldHint(m.roundOpen(run.s, it.round)))
	case itemDetails:
		k.Toggle.SetHelp("space", foldHint(m.detailsShown(run.s.ID)))
	case itemImplement, itemCheck, itemReview:
		k.Toggle.SetEnabled(right && rowFoldable(run.s, *it))
		k.Toggle.SetHelp("space", foldHint(m.rowOpen(run.s.ID, *it)))
	case itemBranch, itemPR:
		k.Toggle.SetEnabled(false)
	}
}

// ShortHelp is the footer's hints, in the order they are shown.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Send, k.Discard, k.StopDraft, k.SendDraft, k.ScrollDraft, k.ToggleSection, k.KeepDraft, k.Keep, k.Clear, k.New, k.Plan, k.Continue, k.Up, k.Right, k.Open, k.Copy, k.CopyBranch, k.Toggle, k.Search, k.Rounds, k.Cancel, k.Delete, k.ToggleEnded, k.Attention, k.Left, k.Quit}
}

// FullHelp satisfies help.KeyMap; the dashboard only shows the short form.
func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

// outputKeyMap is the output pane's keys.
type outputKeyMap struct {
	Close              key.Binding
	ScrollUp, ScrollDn key.Binding
	PageUp, PageDown   key.Binding
	HalfUp, HalfDown   key.Binding
	Top, Follow        key.Binding
	ForceQuit          key.Binding
}

func newOutputKeyMap() outputKeyMap {
	return outputKeyMap{
		Close:     key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "back")),
		ScrollUp:  key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/↓", "scroll")),
		ScrollDn:  key.NewBinding(key.WithKeys("down", "j")),
		PageUp:    key.NewBinding(key.WithKeys("pgup")),
		PageDown:  key.NewBinding(key.WithKeys("pgdown")),
		HalfUp:    key.NewBinding(key.WithKeys("ctrl+u")),
		HalfDown:  key.NewBinding(key.WithKeys("ctrl+d")),
		Top:       key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g/G", "top/follow")),
		Follow:    key.NewBinding(key.WithKeys("G", "end")),
		ForceQuit: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	}
}

func (k outputKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Close, k.ScrollUp, k.Top, k.ForceQuit}
}

func (k outputKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }
