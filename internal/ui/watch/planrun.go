package watch

import (
	"context"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// Planner opens a conversation with Claude about a plan for a change in the
// project at root: the planning screen's turns, and the send of the plan to
// the factory. Its error says, as a sentence, why there can be none.
type Planner func(ctx context.Context, root string) (plan.Turner, plan.Submitter, error)

// planReadyMsg is the end of opening a planning conversation.
type planReadyMsg struct {
	root   string
	turn   plan.Turner
	submit plan.Submitter
	err    error
}

// WithPlans returns a copy of m on which p opens the planning screen, for a
// plan made with Claude through planner and sent to the factory. Plans are
// made in the project a draft would be. Claude stops when ctx is cancelled.
func (m Model) WithPlans(ctx context.Context, planner Planner) Model {
	m.planCtx, m.planner = ctx, planner
	return m
}

// HasPlan is whether the dashboard has a planning screen whose plan has not
// been sent to the factory.
func (m Model) HasPlan() bool { return m.planScreen != nil }

// openPlan shows the planning screen: the one left open, or a new one once
// the planner has checked the project and has a conversation for it.
func (m Model) openPlan() (tea.Model, tea.Cmd) {
	if m.planScreen != nil {
		m.planOpen = true
		return m, nil
	}
	if m.planOpening {
		return m, nil
	}
	root, planner, ctx := m.startRoot(), m.planner, m.planCtx
	m.planOpening, m.draftNote = true, "opening the plan…"
	return m, func() tea.Msg {
		turn, submit, err := planner(ctx, root)
		return planReadyMsg{root: root, turn: turn, submit: submit, err: err}
	}
}

// withPlanReady opens the planning screen on the conversation the planner
// made, or notes why it could not.
func (m Model) withPlanReady(msg planReadyMsg) (tea.Model, tea.Cmd) {
	m.planOpening = false
	if msg.err != nil {
		m.draftNote = "could not start a plan: " + msg.err.Error()
		return m, nil
	}
	m.draftNote = ""
	branch, _ := gitutil.CurrentBranchIn(msg.root)
	screen := plan.New(m.planCtx, msg.turn, msg.submit, "").
		WithTerminal(m.hasDarkBG, m.superEnter).
		WithBranch(branch).
		WithBack()
	screen, cmd := screen.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m.planScreen, m.planOpen, m.planRoot = &screen, true, msg.root
	return m, tea.Batch(screen.Init(), cmd)
}

// updateWithPlan handles msg while there is a planning screen, shown or
// left open behind the dashboard. Keys go to whichever is shown. Everything
// else goes to both: the screen keeps on with Claude while the dashboard is
// shown, and each passes over the other's messages.
func (m Model) updateWithPlan(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case plan.ClosedMsg:
		return m.withPlanClosed(msg), nil
	case tea.KeyPressMsg, tea.PasteMsg, tea.MouseMsg:
		if m.planOpen {
			return m.forwardPlan(msg)
		}
		return m.update(msg)
	case tea.WindowSizeMsg:
		// The screen gets the rows the dashboard has, short of its margin.
		next, cmd := m.update(msg)
		m = next.(Model)
		next, planCmd := m.forwardPlan(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		return next, tea.Batch(cmd, planCmd)
	}
	next, cmd := m.update(msg)
	m = next.(Model)
	next, planCmd := m.forwardPlan(msg)
	return next, tea.Batch(cmd, planCmd)
}

func (m Model) forwardPlan(msg tea.Msg) (Model, tea.Cmd) {
	screen, cmd := m.planScreen.Update(msg)
	m.planScreen = &screen
	return m, cmd
}

// withPlanClosed goes back to the dashboard. A plan sent to the factory is
// done with, and its run selected once a poll lists it; otherwise the screen
// is kept for p to open again as it was.
func (m Model) withPlanClosed(msg plan.ClosedMsg) Model {
	m.planOpen = false
	if msg.RunID == "" {
		return m
	}
	m.planScreen = nil
	m.draftNote = "sent the plan to the factory in " + filepath.Base(m.planRoot) + "; its run appears in the list in a moment"
	m.pendingRun = msg.RunID
	m.focusedPane = paneLeft
	return m.setQuery("")
}
