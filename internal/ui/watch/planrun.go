package watch

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/chat"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/flowlist"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// OpenedPlan is a plan workspace an opener made. Root is the project it is
// planned and built in, "" when there is none and the plan is read-only.
type OpenedPlan struct {
	Agent   plan.Agent
	Approve plan.Approver
	Store   plan.Store
	Root    string
	// Close stops the workspace's agent, when the dashboard lets the
	// workspace go before it ends; nil when there is nothing to stop.
	Close func()
}

// PlanOpener opens a workspace without starting an agent: the agent the
// planning screen talks to, the store its plan and comments are kept in, and
// the approval of the plan in factory. An empty id creates a plan on the
// first message; otherwise the store addresses the existing plan. root is
// the project the dashboard was opened in, where a new plan is made; an
// existing plan may resolve to a checkout of another repo, which the opened
// plan's Root names.
type PlanOpener func(ctx context.Context, root, id string) (OpenedPlan, error)

// settleDelay is how long the selection rests on a flow before the pane opens
// its workspace, so arrowing through the list does not open every flow.
const settleDelay = 250 * time.Millisecond

// settledMsg is the end of the settle delay of the selection generation gen.
type settledMsg struct{ gen uint64 }

// focus is who has the keyboard: the flow list, or the pane's workspace.
type focus int

const (
	focusList focus = iota
	focusPane
)

// workspaceMsg routes every asynchronous result, including polls, comment
// saves and the open itself, to the workspace that issued it rather than
// whichever is shown now.
type workspaceMsg struct {
	ws  *planWorkspace
	msg tea.Msg
}

func workspaceCmd(ws *planWorkspace, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			cmds := make([]tea.Cmd, len(batch))
			for i, c := range batch {
				cmds[i] = workspaceCmd(ws, c)
			}
			return tea.BatchMsg(cmds)
		}
		return workspaceMsg{ws: ws, msg: msg}
	}
}

// WithPlans returns a copy of m that opens plans with open. root is the
// project the dashboard was opened in, or "" outside one, where new plans
// are made.
func (m Model) WithPlans(ctx context.Context, root string, open PlanOpener) Model {
	m.planCtx, m.root, m.planner = ctx, root, open
	m.planWorkspaces = make(map[string]*planWorkspace)
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

// planDeleted notes how a delete ended. A deleted plan's workspace is dropped,
// and the list refreshes to select a neighbour.
func (m Model) planDeleted(msg planDeletedMsg) (Model, tea.Cmd) {
	m.deleting = ""
	if m.flows != nil {
		*m.flows = m.flows.WithDeleting("")
	}
	if msg.err != nil {
		m.note = "could not delete " + msg.title + ": " + msg.err.Error()
	} else {
		m.note = "deleted " + msg.title
		m.dropWorkspace(msg.id)
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
	m.flows = &flows
	return m
}

// HasPlan is whether the developer opened a plan workspace, as opposed to the
// ones the pane opened to show a flow.
func (m Model) HasPlan() bool {
	for _, ws := range m.planWorkspaces {
		if ws.active {
			return true
		}
	}
	return false
}

// shown is the workspace the pane shows: the one that has the keyboard, else
// the selected flow's; nil when there is none yet.
func (m Model) shown() *planWorkspace {
	if m.focus == focusList && m.planKey == "" {
		// "" is a new plan's key, which the pane shows only while it has
		// the keyboard; here it is no selection.
		return nil
	}
	return m.planWorkspaces[m.planKey]
}

// planScreen is the plan screen the pane shows, nil while its workspace has
// none.
func (m Model) planScreen() *plan.Model {
	if ws := m.shown(); ws != nil {
		return ws.screen
	}
	return nil
}

// dropWorkspace stops the workspace under key and forgets it. When the pane's
// has the keyboard, the list takes it back.
func (m *Model) dropWorkspace(key string) {
	ws := m.planWorkspaces[key]
	if ws == nil {
		return
	}
	if ws.close != nil {
		ws.close()
	}
	delete(m.planWorkspaces, key)
	if m.focus == focusPane && m.planKey == key {
		m.focus = focusList
	}
}

// tidyWorkspaces re-keys a new plan's workspace to its plan's ID once it has
// one, and drops the workspaces that are no longer wanted.
func (m *Model) tidyWorkspaces() {
	if ws := m.planWorkspaces[""]; ws != nil && ws.screen != nil {
		if id := ws.screen.ID(); id != "" {
			// A workspace the pane opened meanwhile, for the plan the list
			// has now, gives way.
			m.dropWorkspace(id)
			delete(m.planWorkspaces, "")
			ws.key = id
			m.planWorkspaces[id] = ws
			if m.focus == focusPane && m.planKey == "" {
				m.planKey = id
			}
		}
	}
	selected, inList := "", map[string]bool{}
	if m.flows != nil {
		selected, _ = m.flows.Selected()
		for _, f := range m.flows.Flows() {
			inList[f.ID] = true
		}
	}
	for key, ws := range m.planWorkspaces {
		if inList[key] {
			ws.listed = true
		}
		if (ws.listed && !inList[key]) || (!ws.active && (key == "" || key != selected)) {
			m.dropWorkspace(key)
		}
	}
}

// followSelection points the pane at the list's selection, which has a new
// settle delay to rest through. Its workspace opens once the selection has
// rested on it.
func (m *Model) followSelection() tea.Cmd {
	if m.flows == nil || m.focus == focusPane {
		return nil
	}
	id, _ := m.flows.Selected()
	if id == m.planKey {
		return nil
	}
	m.planKey = id
	m.settleGen++
	if id == "" {
		return nil
	}
	gen := m.settleGen
	return tea.Tick(settleDelay, func(time.Time) tea.Msg { return settledMsg{gen: gen} })
}

// settled opens the workspace of the flow the selection rested on, to show it.
func (m Model) settled(msg settledMsg) (Model, tea.Cmd) {
	if msg.gen != m.settleGen || m.planKey == "" || m.planner == nil || m.flows == nil || m.planWorkspaces[m.planKey] != nil {
		return m, nil
	}
	_, title := m.flows.Selected()
	ws := m.addWorkspace(m.planKey, title)
	return m, workspaceCmd(ws, ws.Init())
}

// addWorkspace makes the workspace of plan id, loading; its Init opens it.
func (m *Model) addWorkspace(id, title string) *planWorkspace {
	ws := &planWorkspace{
		key: id, id: id, title: title,
		ctx: m.planCtx, opener: m.planner, projectRoot: m.root,
	}
	m.planWorkspaces[id] = ws
	return ws
}

// openPlanID gives plan id's workspace the keyboard, making it first when it
// is not there, or when its open failed. An empty id is a new plan, whose
// workspace is reused while it has no ID.
func (m Model) openPlanID(id, title string) (Model, tea.Cmd) {
	var open tea.Cmd
	ws := m.planWorkspaces[id]
	if ws != nil && ws.state == wsFailed {
		m.dropWorkspace(id)
		ws = nil
	}
	if ws == nil {
		if m.planner == nil {
			return m, nil
		}
		ws = m.addWorkspace(id, title)
		open = workspaceCmd(ws, ws.Init())
	}
	m, act := m.activate(ws)
	return m, tea.Batch(open, act)
}

// activate gives ws the keyboard: in the pane, or across the screen where no
// pane fits. A workspace's planner resumes once it is active, so a resume owed
// is started at once; one still loading does so when it is ready.
func (m Model) activate(ws *planWorkspace) (Model, tea.Cmd) {
	if prev := m.planWorkspaces[m.planKey]; prev != nil && prev != ws {
		prev.setFocused(false)
	}
	if !ws.active {
		ws.setActive(true)
		if ws.screen != nil {
			m.trackOpened(ws)
		}
	}
	ws.setFocused(true)
	m.planKey, m.focus = ws.key, focusPane
	m.note = ""
	// The list selects the flow the pane shows.
	if m.flows != nil && ws.key != "" {
		*m.flows = m.flows.Select(ws.key)
	}
	if ws.screen == nil {
		return m, nil
	}
	return m, workspaceCmd(ws, ws.screen.Poll())
}

// trackOpened tells telemetry the developer opened ws's plan.
func (m Model) trackOpened(ws *planWorkspace) {
	id := ws.planID()
	props := map[string]any{"is_new": id == ""}
	if id != "" {
		props["plan_id"] = id
	}
	telemetry.Track(m.planCtx, "plan_opened", props)
}

// paneSize is the size of the pane beside the list: 0, 0 where there is none.
func (m Model) paneSize() (int, int) {
	if m.flows == nil {
		return 0, 0
	}
	return m.flows.DetailSize()
}

// fullScreen is whether the pane's workspace is drawn across the screen: it
// has the keyboard where no pane fits.
func (m Model) fullScreen() bool {
	w, _ := m.paneSize()
	return m.focus == focusPane && w == 0 && m.planWorkspaces[m.planKey] != nil
}

// workspaceOpened is the end of ws's open: its screen, made ready, or why it
// has none.
func (m Model) workspaceOpened(ws *planWorkspace, msg openedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		asked := ws.active
		ws.fail(msg.err)
		if m.focus == focusPane && m.planKey == ws.key {
			m.focus = focusList
		}
		if asked {
			m.note = "could not open the plan: " + msg.err.Error()
		}
		if ws.key == "" {
			// Nothing to show for a new plan; n starts it again.
			m.dropWorkspace("")
		}
		return m, nil
	}
	opened := msg.plan
	branch, _ := gitutil.CurrentBranchIn(opened.Root)
	screen := plan.New(m.planCtx, opened.Agent, opened.Approve, opened.Store, "").
		WithTerminal(m.hasDarkBG, m.superEnter).WithBranch(branch).WithInbox(ws.title, opened.Root).
		WithBuild(m.builds).WithResume(m.resumer).WithTabs()
	if m.newChat != nil {
		screen = screen.WithChatKey()
	}
	w, h := m.workspaceSize()
	screen, size := screen.Update(tea.WindowSizeMsg{Width: w, Height: h})
	ws.ready(screen, opened)
	cmds := []tea.Cmd{workspaceCmd(ws, tea.Batch(ws.screen.Init(), size))}
	if ws.active {
		m.trackOpened(ws)
		cmds = append(cmds, workspaceCmd(ws, ws.screen.Poll()))
	}
	return m, tea.Batch(cmds...)
}

// blurPane gives the keyboard back to the list.
func (m Model) blurPane() Model {
	m.focus = focusList
	if ws := m.planWorkspaces[m.planKey]; ws != nil {
		ws.setFocused(false)
	}
	return m
}

// showFlows gives the keyboard back to the flow list, with the last plan shown
// selected.
func (m Model) showFlows() (Model, tea.Cmd) {
	m = m.blurPane()
	m.note = ""
	if m.flows == nil {
		return m, nil
	}
	if ws := m.planWorkspaces[m.planKey]; ws != nil && ws.key != "" {
		*m.flows = m.flows.Select(ws.key)
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
		return m.updateWorkspace(msg.ws, msg.msg)
	case settledMsg:
		return m.settled(msg)
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
		return m.openPlanID("", "New plan")
	case flowlist.QuitMsg:
		return m.quitWithPlans()
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
		for _, ws := range m.planWorkspaces {
			m, cmd = m.updateWorkspace(ws, msg)
			cmds = append(cmds, cmd)
		}
		if m.flows != nil {
			*m.flows, cmd = m.flows.Update(msg)
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}
	next, cmd := m.update(msg)
	m = next.(Model)
	if m.flows != nil {
		var flowsCmd tea.Cmd
		*m.flows, flowsCmd = m.flows.Update(msg)
		cmd = tea.Batch(cmd, flowsCmd)
	}
	return m, cmd
}

// updateKey sends a key to the chat, the workspace that has the keyboard, or
// the flow list. ctrl+p gives the keyboard from a workspace to the list.
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
	if m.focus == focusPane {
		if key.Matches(msg, k.Flows) {
			return m.showFlows()
		}
		return m.forwardPlan(msg)
	}
	if m.flows != nil {
		var cmd tea.Cmd
		*m.flows, cmd = m.flows.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) forwardPlan(msg tea.Msg) (Model, tea.Cmd) {
	return m.updateWorkspace(m.planWorkspaces[m.planKey], msg)
}

// updateWorkspace hands msg to ws. What reaches a workspace that was dropped
// meanwhile is ignored, and a plan it opened is closed.
func (m Model) updateWorkspace(ws *planWorkspace, msg tea.Msg) (Model, tea.Cmd) {
	if ws == nil || m.planWorkspaces[ws.key] != ws {
		if opened, ok := msg.(openedMsg); ok && opened.err == nil && opened.plan.Close != nil {
			opened.plan.Close()
		}
		return m, nil
	}
	switch msg := msg.(type) {
	case openedMsg:
		return m.workspaceOpened(ws, msg)
	case plan.ClosedMsg:
		// Left, or approved: an approved plan's build is followed in its
		// Overview, from the flow list.
		return m.showFlows()
	case plan.BuildingMsg:
		// The screen stays open on its Overview, which follows the build.
		return m, nil
	case plan.OpenMsg:
		if m.flows != nil {
			*m.flows = m.flows.Select(msg.ID)
		}
		return m.openPlanID(msg.ID, msg.Title)
	}
	return m, workspaceCmd(ws, ws.Update(msg))
}

func (m Model) quitWithPlans() (Model, tea.Cmd) {
	count := 0
	for _, ws := range m.planWorkspaces {
		if ws.unsent() != "" {
			count++
		}
	}
	if count > 0 && !m.confirmPlanQuit {
		m.confirmPlanQuit = true
		m.note = fmt.Sprintf("%d plan workspace(s) have unsent input. Press ctrl+c again to discard it and quit; any other key keeps it.", count)
		return m, nil
	}
	return m, tea.Quit
}
