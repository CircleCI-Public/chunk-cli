package watch

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/chat"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/flowlist"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// OpenedPlan is a plan workspace an opener made. Root is the project it is
// planned and built in, "" when there is none and the plan is read-only.
type OpenedPlan struct {
	Agent   plan.Agent
	Approve plan.Approver
	Store   plan.Store
	Root    string
}

// PlanOpener opens a workspace without starting an agent: the agent the
// planning screen talks to, the store its plan and comments are kept in, and
// the approval of the plan in factory. An empty id creates a plan on the
// first message; otherwise the store addresses the existing plan. root is
// the project the dashboard was opened in, where a new plan is made; an
// existing plan may resolve to a checkout of another repo, which the opened
// plan's Root names.
type PlanOpener func(ctx context.Context, root, id string) (OpenedPlan, error)

type planReadyMsg struct {
	// id is the plan's, or "" for a new plan.
	id, root, title string
	// request is a new plan's first request, sent once its screen opens;
	// "" sends none.
	request string
	agent   plan.Agent
	approve plan.Approver
	store   plan.Store
	err     error
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

// WithPlans returns a copy of m that opens plans with open. root is the
// project the dashboard was opened in, or "" outside one, where new plans
// are made.
func (m Model) WithPlans(ctx context.Context, root string, open PlanOpener) Model {
	m.planCtx, m.root, m.planner = ctx, root, open
	m.planWorkspaces = make(map[uint64]*planWorkspace)
	return m
}

// WithPlanBuilds returns a copy of m whose flow screens show their plan's
// build, read from b.
func (m Model) WithPlanBuilds(b plan.Build) Model {
	m.builds = b
	return m
}

// WithPlanResume returns a copy of m that starts plans' agents again with r,
// from the flow list and each flow screen.
func (m Model) WithPlanResume(r plan.Resumer) Model {
	m.resumer = r
	return m
}

// planResumedMsg is the end of a resume started from the flow list.
type planResumedMsg struct {
	title string
	err   error
}

// resumePlan starts the agent of the flow the list asks for, passing the
// resumer the project the dashboard was opened in.
func (m Model) resumePlan(msg flowlist.ResumeMsg) (Model, tea.Cmd) {
	if m.resumer == nil {
		return m, nil
	}
	m.note = "resuming " + msg.Title + "…"
	r, ctx, root := m.resumer, m.planCtx, m.root
	return m, func() tea.Msg {
		return planResumedMsg{title: msg.Title, err: r.Resume(ctx, root, msg.ID, msg.Kind, msg.Holder)}
	}
}

// planResumed notes how a resume ended, and refreshes the list so its claim
// shows.
func (m Model) planResumed(msg planResumedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.note = "could not resume " + msg.title + ": " + msg.err.Error()
	} else {
		m.note = "resumed " + msg.title
	}
	if m.flows == nil {
		return m, nil
	}
	return m, m.flows.Refresh()
}

// Deleter deletes plan planID.
type Deleter interface {
	Delete(ctx context.Context, planID string) error
}

// WithPlanDelete returns a copy of m that deletes plans with d, from the flow
// list.
func (m Model) WithPlanDelete(d Deleter) Model {
	m.deleter = d
	return m
}

// planDeletedMsg is the end of a delete started from the flow list.
type planDeletedMsg struct {
	id, title string
	err       error
}

// deletePlan deletes the plan the list asks for. One delete runs at a time,
// and asking again for the one running is ignored.
func (m Model) deletePlan(msg flowlist.DeleteMsg) (Model, tea.Cmd) {
	if m.deleter == nil || m.deleting == msg.ID {
		return m, nil
	}
	if m.deleting != "" {
		m.note = "a delete is in progress"
		return m, nil
	}
	m.deleting = msg.ID
	m.note = "deleting " + msg.Title + "…"
	if m.flows != nil {
		*m.flows = m.flows.WithDeleting(msg.ID)
	}
	d, ctx := m.deleter, m.planCtx
	return m, func() tea.Msg {
		return planDeletedMsg{id: msg.ID, title: msg.Title, err: d.Delete(ctx, msg.ID)}
	}
}

// planDeleted notes how a delete ended. A deleted plan's open screen is
// dropped, and the list refreshes to select a neighbour.
func (m Model) planDeleted(msg planDeletedMsg) (Model, tea.Cmd) {
	m.deleting = ""
	if m.flows != nil {
		*m.flows = m.flows.WithDeleting("")
	}
	if msg.err != nil {
		m.note = "could not delete " + msg.title + ": " + msg.err.Error()
	} else {
		m.note = "deleted " + msg.title
		for key, ws := range m.planWorkspaces {
			if ws.store.ID() != msg.id {
				continue
			}
			delete(m.planWorkspaces, key)
			if key == m.planKey {
				m.planScreen = nil
				m.planOpen, m.flowsOpen = false, true
			}
		}
	}
	if m.flows == nil {
		return m, nil
	}
	return m, m.flows.Refresh()
}

// WithFlowList adds the flow list, the dashboard's first screen.
func (m Model) WithFlowList(flows flowlist.Model) Model {
	if m.newChat != nil {
		flows = flows.WithChatKey()
	}
	m.flows, m.flowsOpen = &flows, !m.onboardingOpen
	return m
}

func (m Model) HasPlan() bool { return len(m.planWorkspaces) > 0 }

func (m Model) openPlanID(id, title string) (tea.Model, tea.Cmd) {
	return m.openPlan(id, title, "")
}

// openPlanRequest opens a new plan whose first request is request, or, when
// request is empty, goes to the new plan's workspace as n does.
func (m Model) openPlanRequest(request string) (tea.Model, tea.Cmd) {
	return m.openPlan("", "New plan", strings.TrimSpace(request))
}

// openPlan goes to plan id's workspace, opening it when none is open. A
// request always opens a new workspace, so it is never dropped on one
// already open.
func (m Model) openPlan(id, title, request string) (tea.Model, tea.Cmd) {
	if m.planOpening {
		return m, nil
	}
	for key, ws := range m.planWorkspaces {
		if request == "" && ws.store.ID() == id {
			m.planKey, m.planScreen = key, &ws.screen
			m.planOpen, m.flowsOpen = true, false
			return m, nil
		}
	}
	if m.planner == nil {
		return m, nil
	}
	// A new plan is made in the project the dashboard was opened in; the
	// opener says where an existing one goes.
	root, planner, ctx := m.root, m.planner, m.planCtx
	m.planOpening, m.note = true, "opening the plan…"
	return m, func() tea.Msg {
		opened, err := planner(ctx, root, id)
		return planReadyMsg{
			id: id, root: opened.Root, title: title, request: request,
			agent: opened.Agent, approve: opened.Approve, store: opened.Store, err: err,
		}
	}
}

func (m Model) withPlanReady(msg planReadyMsg) (tea.Model, tea.Cmd) {
	m.planOpening = false
	if msg.err != nil {
		m.note = "could not open the plan: " + msg.err.Error()
		return m, nil
	}
	props := map[string]any{"is_new": msg.id == ""}
	if msg.id != "" {
		props["plan_id"] = msg.id
	}
	telemetry.Track(m.planCtx, "plan_opened", props)
	m.note = ""
	branch, _ := gitutil.CurrentBranchIn(msg.root)
	screen := plan.New(m.planCtx, msg.agent, msg.approve, msg.store, msg.request).
		WithTerminal(m.hasDarkBG, m.superEnter).WithBranch(branch).WithInbox(msg.title, msg.root).
		WithBuild(m.builds).WithResume(m.resumer)
	if m.newChat != nil {
		screen = screen.WithChatKey()
	}
	screen, cmd := screen.Update(tea.WindowSizeMsg{Width: m.mainWidth(), Height: m.height})
	m.planSeq++
	ws := &planWorkspace{screen: screen, store: msg.store, root: msg.root}
	m.planWorkspaces[m.planSeq] = ws
	m.planKey, m.planScreen = m.planSeq, &ws.screen
	m.planOpen, m.flowsOpen = true, false
	return m, workspaceCmd(m.planKey, tea.Batch(screen.Init(), cmd))
}

// showFlows shows the flow list, with the last plan shown selected.
func (m Model) showFlows() (Model, tea.Cmd) {
	m.planOpen, m.flowsOpen = false, true
	m.note = ""
	if m.flows == nil {
		return m, nil
	}
	if ws := m.planWorkspaces[m.planKey]; ws != nil {
		*m.flows = m.flows.Select(ws.store.ID())
	}
	return m, m.flows.Refresh()
}

func (m Model) updateWithPlan(msg tea.Msg) (tea.Model, tea.Cmd) {
	if chat.IsMsg(msg) && m.chatView != nil {
		var cmd tea.Cmd
		*m.chatView, cmd = m.chatView.Update(msg)
		return m, cmd
	}
	switch msg := msg.(type) {
	case workspaceMsg:
		return m.updateWorkspace(msg.key, msg.msg)
	case planReadyMsg:
		return m.withPlanReady(msg)
	case flowlist.OpenMsg:
		return m.openPlanID(msg.ID, msg.Title)
	case flowlist.ResumeMsg:
		return m.resumePlan(msg)
	case planResumedMsg:
		return m.planResumed(msg)
	case flowlist.DeleteMsg:
		return m.deletePlan(msg)
	case planDeletedMsg:
		return m.planDeleted(msg)
	case flowlist.NewMsg:
		return m.openPlanRequest(msg.Request)
	case flowlist.QuitMsg:
		return m.quitWithPlans()
	case onboarding.LeaveMsg, onboarding.DoneMsg:
		return m.closeOnboarding()
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	case tea.PasteMsg, tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.WindowSizeMsg, tea.BackgroundColorMsg, tea.KeyboardEnhancementsMsg:
		next, cmd := m.update(msg)
		m = next.(Model)
		if _, ok := msg.(tea.WindowSizeMsg); ok {
			next, size := m.resize()
			return next, tea.Batch(cmd, size)
		}
		cmds := []tea.Cmd{cmd}
		if bg, ok := msg.(tea.BackgroundColorMsg); ok && m.chatView != nil {
			*m.chatView, _ = m.chatView.Update(bg)
		}
		for key := range m.planWorkspaces {
			m, cmd = m.updateWorkspace(key, msg)
			cmds = append(cmds, cmd)
		}
		if m.flows != nil {
			*m.flows, cmd = m.flows.Update(msg)
			cmds = append(cmds, cmd)
		}
		m, cmd = m.forwardOnboarding(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}
	next, cmd := m.update(msg)
	m = next.(Model)
	if m.flows != nil {
		var flowsCmd tea.Cmd
		*m.flows, flowsCmd = m.flows.Update(msg)
		cmd = tea.Batch(cmd, flowsCmd)
	}
	m, onboardingCmd := m.forwardOnboarding(msg)
	return m, tea.Batch(cmd, onboardingCmd)
}

// updateKey hands a key to the chat, onboarding, the open plan screen or the
// flow list, after the dashboard's own.
func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := m.keys()
	if m.confirmPlanQuit {
		if key.Matches(msg, k.ForceQuit) {
			return m, tea.Quit
		}
		m.confirmPlanQuit, m.note = false, ""
	}
	if next, cmd, ok := m.updateChat(msg); ok {
		return next, cmd
	}
	if m.onboardingOpen {
		// Onboarding takes every key but ctrl+c, ctrl+p included: it
		// asks before going to flows, as esc does, so leaving is saved.
		if key.Matches(msg, k.ForceQuit) {
			return m.quitWithPlans()
		}
		return m.forwardOnboarding(msg)
	}
	if key.Matches(msg, k.Flows) {
		return m.showFlows()
	}
	if m.planOpen {
		return m.forwardPlan(msg)
	}
	if m.flowsOpen && m.flows != nil {
		var cmd tea.Cmd
		*m.flows, cmd = m.flows.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) forwardPlan(msg tea.Msg) (Model, tea.Cmd) {
	return m.updateWorkspace(m.planKey, msg)
}

func (m Model) updateWorkspace(key uint64, msg tea.Msg) (Model, tea.Cmd) {
	ws := m.planWorkspaces[key]
	if ws == nil {
		return m, nil
	}
	switch msg := msg.(type) {
	case plan.ClosedMsg:
		// Left, or approved: an approved plan's build is followed on its
		// Flow tab, from the flow list.
		return m.showFlows()
	case plan.BuildingMsg:
		// The screen stays open on its Flow tab, which follows the build.
		return m, nil
	case plan.OpenMsg:
		next, cmd := m.openPlanID(msg.ID, msg.Title)
		return next.(Model), cmd
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
	onboardingUnsent := m.onboarding != nil && m.onboarding.Unsent() != ""
	if (count > 0 || onboardingUnsent) && !m.confirmPlanQuit {
		m.confirmPlanQuit = true
		m.note = fmt.Sprintf("%d plan workspace(s) have unsent input. Press ctrl+c again to discard it and quit; any other key keeps it.", count)
		if count == 0 {
			m.note = "Onboarding has unsent input. Press ctrl+c again to discard it and quit; any other key keeps it."
		}
		return m, nil
	}
	return m, tea.Quit
}
