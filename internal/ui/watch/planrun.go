package watch

import (
	"context"
	"fmt"
	"path/filepath"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/planlist"
)

// Planner opens a workspace without starting an agent: the agent the
// planning screen talks to, the store its plan and comments are kept in, and
// the approval of the plan in factory. An empty id creates a plan on the
// first message; otherwise the store addresses the existing plan.
type Planner func(ctx context.Context, root, id string) (plan.Agent, plan.Approver, plan.Store, error)

type planReadyMsg struct {
	root, title string
	agent       plan.Agent
	approve     plan.Approver
	store       plan.Store
	err         error
}

type planWorkspace struct {
	screen plan.Model
	store  plan.Store
	root   string
}

// workspaceMsg routes every asynchronous result, including polls and comment
// saves, to the screen that issued it rather than whichever is visible now.
type workspaceMsg struct {
	key uint64
	msg tea.Msg
}

func workspaceCmd(key uint64, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			cmds := make([]tea.Cmd, len(batch))
			for i, c := range batch {
				cmds[i] = workspaceCmd(key, c)
			}
			return tea.BatchMsg(cmds)
		}
		return workspaceMsg{key: key, msg: msg}
	}
}

func (m Model) WithPlans(ctx context.Context, planner Planner) Model {
	m.planCtx, m.planner = ctx, planner
	m.planWorkspaces = make(map[uint64]*planWorkspace)
	return m
}

// WithPlanInbox adds the read-only plan browser as the initial destination.
func (m Model) WithPlanInbox(inbox planlist.Model) Model {
	m.inbox, m.inboxOpen = &inbox, true
	return m
}

func (m Model) HasPlan() bool { return len(m.planWorkspaces) > 0 }

func (m Model) openPlan() (tea.Model, tea.Cmd) {
	if m.inbox != nil {
		return m.showInbox(false)
	}
	if m.planScreen != nil {
		m.planOpen = true
		return m, nil
	}
	return m.openPlanID("", "New plan")
}

func (m Model) openPlanID(id, title string) (tea.Model, tea.Cmd) {
	if m.planOpening {
		return m, nil
	}
	for key, ws := range m.planWorkspaces {
		if ws.store.ID() == id {
			m.planKey, m.planScreen = key, &ws.screen
			m.planOpen, m.inboxOpen, m.switchingPlan = true, false, false
			return m, nil
		}
	}
	if m.planner == nil {
		return m, nil
	}
	root, planner, ctx := m.startRoot(), m.planner, m.planCtx
	if id != "" {
		// The API has no repository association. Never infer an existing
		// plan's project from an unrelated run selected in the Runs tab.
		root = m.startDir
	}
	m.planOpening, m.draftNote = true, "opening the plan…"
	return m, func() tea.Msg {
		agent, approve, store, err := planner(ctx, root, id)
		return planReadyMsg{root: root, title: title, agent: agent, approve: approve, store: store, err: err}
	}
}

func (m Model) withPlanReady(msg planReadyMsg) (tea.Model, tea.Cmd) {
	m.planOpening = false
	if msg.err != nil {
		m.draftNote = "could not open the plan: " + msg.err.Error()
		return m, nil
	}
	m.draftNote = ""
	branch, _ := gitutil.CurrentBranchIn(msg.root)
	screen := plan.New(m.planCtx, msg.agent, msg.approve, msg.store, "").
		WithTerminal(m.hasDarkBG, m.superEnter).WithBranch(branch).WithBack()
	if m.inbox != nil {
		screen = screen.WithInbox(msg.title, msg.root)
	}
	screen, cmd := screen.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m.planSeq++
	ws := &planWorkspace{screen: screen, store: msg.store, root: msg.root}
	m.planWorkspaces[m.planSeq] = ws
	m.planKey, m.planScreen = m.planSeq, &ws.screen
	m.planOpen, m.inboxOpen, m.switchingPlan = true, false, false
	return m, workspaceCmd(m.planKey, tea.Batch(screen.Init(), cmd))
}

// showInbox shows the plans. switching is ctrl+p from a plan, whose way back
// returns to that plan rather than to the runs.
func (m Model) showInbox(switching bool) (Model, tea.Cmd) {
	m.switchingPlan = switching && m.planOpen
	m.planOpen, m.inboxOpen = false, true
	m.draftNote = ""
	if ws := m.planWorkspaces[m.planKey]; ws != nil {
		*m.inbox = m.inbox.Select(ws.store.ID())
	}
	return m, m.inbox.Refresh()
}

func (m Model) updateWithPlan(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspaceMsg:
		return m.updateWorkspace(msg.key, msg.msg)
	case planlist.OpenMsg:
		return m.openPlanID(msg.ID, msg.Title)
	case planlist.NewMsg:
		return m.openPlanID("", "New plan")
	case planlist.BackMsg:
		m.inboxOpen = false
		m.planOpen = m.switchingPlan && m.planScreen != nil
		m.switchingPlan = false
		return m, nil
	case planlist.QuitMsg:
		return m.quitWithPlans()
	case tea.KeyPressMsg:
		if m.confirmPlanQuit {
			if key.Matches(msg, m.keys().ForceQuit) {
				return m, tea.Quit
			}
			m.confirmPlanQuit, m.draftNote = false, ""
		}
		if m.inbox != nil && msg.String() == "ctrl+p" {
			return m.showInbox(true)
		}
		if m.planOpen {
			return m.forwardPlan(msg)
		}
		if m.inboxOpen {
			var cmd tea.Cmd
			*m.inbox, cmd = m.inbox.Update(msg)
			return m, cmd
		}
		if m.inbox != nil && m.draft == draftNone && !m.searching && m.output == nil && (msg.String() == "tab" || msg.String() == "1") {
			return m.showInbox(false)
		}
		return m.update(msg)
	case tea.PasteMsg, tea.MouseMsg:
		if m.planOpen {
			return m.forwardPlan(msg)
		}
		if m.inboxOpen {
			var cmd tea.Cmd
			*m.inbox, cmd = m.inbox.Update(msg)
			return m, cmd
		}
		return m.updateInput(msg)
	case tea.WindowSizeMsg, tea.BackgroundColorMsg, tea.KeyboardEnhancementsMsg:
		next, cmd := m.update(msg)
		m = next.(Model)
		if _, ok := msg.(tea.WindowSizeMsg); ok {
			msg = tea.WindowSizeMsg{Width: m.width, Height: m.height}
		}
		cmds := []tea.Cmd{cmd}
		for key := range m.planWorkspaces {
			m, cmd = m.updateWorkspace(key, msg)
			cmds = append(cmds, cmd)
		}
		if m.inbox != nil {
			// The inbox draws under the dashboard's header.
			if size, ok := msg.(tea.WindowSizeMsg); ok {
				size.Height = max(size.Height-1, 0)
				msg = size
			}
			*m.inbox, cmd = m.inbox.Update(msg)
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}
	next, cmd := m.update(msg)
	m = next.(Model)
	if m.inbox != nil {
		var inboxCmd tea.Cmd
		*m.inbox, inboxCmd = m.inbox.Update(msg)
		cmd = tea.Batch(cmd, inboxCmd)
	}
	return m, cmd
}

func (m Model) forwardPlan(msg tea.Msg) (Model, tea.Cmd) {
	return m.updateWorkspace(m.planKey, msg)
}

func (m Model) updateWorkspace(key uint64, msg tea.Msg) (Model, tea.Cmd) {
	ws := m.planWorkspaces[key]
	if ws == nil {
		return m, nil
	}
	if closed, ok := msg.(plan.ClosedMsg); ok {
		if closed.BuildID == "" {
			m.planOpen = false
			if m.inbox != nil {
				return m.showInbox(false)
			}
			return m, nil
		}
		delete(m.planWorkspaces, key)
		m.planScreen, m.planOpen, m.inboxOpen = nil, false, false
		m.draftNote = "approved the plan; it is building in " + filepath.Base(ws.root) + " and appears in the list in a moment"
		m.pendingRun, m.focusedPane = closed.BuildID, paneLeft
		return m.setQuery(""), nil
	}
	var cmd tea.Cmd
	ws.screen, cmd = ws.screen.Update(msg)
	return m, workspaceCmd(key, cmd)
}

func (m Model) quitWithPlans() (Model, tea.Cmd) {
	count := 0
	for _, ws := range m.planWorkspaces {
		if ws.screen.Unsent() != "" {
			count++
		}
	}
	if count > 0 && !m.confirmPlanQuit {
		m.confirmPlanQuit = true
		m.draftNote = fmt.Sprintf("%d plan workspace(s) have unsent input. Press ctrl+c again to discard it and quit; any other key keeps it.", count)
		return m, nil
	}
	return m, tea.Quit
}
