package watch

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// workspaceState is how far a workspace's open has got.
type workspaceState int

const (
	wsLoading workspaceState = iota
	wsReady
	wsFailed
)

// openedMsg is the end of a workspace's open, as its Init answers it.
type openedMsg struct {
	plan OpenedPlan
	err  error
}

// planWorkspace is a plan's workspace in the pane. It is made at once, loading,
// and its Init runs the opener; once the opener answers it holds the plan
// screen, or why there is none.
type planWorkspace struct {
	state workspaceState
	err   error
	// screen is the plan screen, set once the workspace is ready.
	screen *plan.Model
	store  plan.Store
	root   string
	// close stops the workspace's agent, when the host lets it go.
	close func()

	// key is what the workspace is kept under in the host: the plan's ID, or
	// "" for a new plan until it has one. id and title are what it was opened
	// for.
	key, id, title string
	// active is whether the developer has given the workspace the keyboard:
	// until then it was opened only to be read, and does not resume its
	// planner. focused is whether it is drawn as having the keyboard. Both are
	// kept while it loads, and applied to the screen when it is ready.
	active, focused bool
	// listed is whether the workspace's flow has been in the flow list, so
	// that its leaving the list can be told from its not being there yet.
	listed bool

	// What Init opens the workspace with.
	ctx         context.Context
	opener      PlanOpener
	projectRoot string
}

// Init runs the opener, which the host answers as an openedMsg.
func (ws *planWorkspace) Init() tea.Cmd {
	ctx, open, root, id := ws.ctx, ws.opener, ws.projectRoot, ws.id
	return func() tea.Msg {
		// A new plan is made in the project the dashboard was opened in; the
		// opener says where an existing one goes.
		opened, err := open(ctx, root, id)
		return openedMsg{plan: opened, err: err}
	}
}

// Update hands msg to the plan screen. A workspace without one has nothing to
// do with it, but esc: that leaves, as the screen's own would.
func (ws *planWorkspace) Update(msg tea.Msg) tea.Cmd {
	if ws.screen != nil {
		var cmd tea.Cmd
		*ws.screen, cmd = ws.screen.Update(msg)
		return cmd
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
		return func() tea.Msg { return plan.ClosedMsg{} }
	}
	return nil
}

// Content is what the pane draws: the plan screen, or that there is none.
func (ws *planWorkspace) Content(st watchStyles) string {
	switch {
	case ws.screen != nil:
		return ws.screen.Content()
	case ws.state == wsFailed:
		return st.dim("Could not open the plan: " + oneLine(ws.err.Error()))
	}
	return openingContent(st)
}

// openingContent is what the pane says while a flow's workspace is not yet
// there to show.
func openingContent(st watchStyles) string { return st.dim("Opening the plan…") }

// ready gives the workspace its plan screen.
func (ws *planWorkspace) ready(screen plan.Model, opened OpenedPlan) {
	ws.state, ws.screen = wsReady, &screen
	ws.store, ws.root, ws.close = opened.Store, opened.Root, opened.Close
	*ws.screen = ws.screen.WithActive(ws.active).WithFocused(ws.focused)
}

// fail ends the open with err. A workspace that failed to open was not opened
// by anyone.
func (ws *planWorkspace) fail(err error) {
	ws.state, ws.err = wsFailed, err
	ws.active, ws.focused = false, false
}

func (ws *planWorkspace) setActive(active bool) {
	ws.active = active
	if ws.screen != nil {
		*ws.screen = ws.screen.WithActive(active)
	}
}

func (ws *planWorkspace) setFocused(focused bool) {
	ws.focused = focused
	if ws.screen != nil {
		*ws.screen = ws.screen.WithFocused(focused)
	}
}

// planID is the workspace's plan's ID, "" for a new plan that has none.
func (ws *planWorkspace) planID() string {
	if ws.store != nil {
		return ws.store.ID()
	}
	return ws.id
}

// unsent is the local input the workspace would lose if it were dropped.
func (ws *planWorkspace) unsent() string {
	if ws.screen == nil {
		return ""
	}
	return ws.screen.Unsent()
}
