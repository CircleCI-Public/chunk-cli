package plan

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// Agent is the planning session of one plan. The screen gives it a new
// plan's request, once, and has it claim the plan again when factory waits
// on a planner that is not there; everything else it gets from factory's
// tasks.
type Agent interface {
	// Request sends a new plan's request, starting the planning session on
	// it. A request that failed to start the session is sent again, rather
	// than the session resumed.
	Request(ctx context.Context, text string) error
	// Resume has the planning session claim the plan again when it holds no
	// claim, starting or resuming the session; it does nothing while the
	// session holds the claim.
	Resume(ctx context.Context) error
	// Events is everything the agent reports, in order.
	Events() <-chan Event
}

// Event is one thing the agent reports. Exactly one field is set.
type Event struct {
	// Activity is a tool call the agent made, or a piece of text it wrote, as
	// a line for the chat.
	Activity string
	// Reply is the agent's reply ending a turn.
	Reply *Reply
	// Problem is something wrong with the session.
	Problem error
}

// Reply is one reply of the agent's.
type Reply struct {
	Text string
	// ByTask is true when the agent started the turn on its own, on a factory
	// task, rather than on a message from the developer.
	ByTask bool
}

type (
	// eventMsg is an event from agent. A host can outlive a screen, so an
	// event can reach a screen on another agent, which ignores it.
	eventMsg struct {
		agent Agent
		Event
	}
	// sentMsg is the end of a Request.
	sentMsg struct {
		agent Agent
		err   error
	}
	// resumedMsg is the end of a Resume.
	resumedMsg struct {
		agent Agent
		err   error
	}
)

// waitEvent reads a's next event. The screen calls it again after each one,
// so it reads them all, in order, until ctx ends.
func waitEvent(ctx context.Context, a Agent) tea.Cmd {
	events := a.Events()
	return func() tea.Msg {
		select {
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			return eventMsg{agent: a, Event: ev}
		case <-ctx.Done():
			return nil
		}
	}
}

func requestCmd(ctx context.Context, a Agent, text string) tea.Cmd {
	return func() tea.Msg { return sentMsg{agent: a, err: a.Request(ctx, text)} }
}

func resumeCmd(ctx context.Context, a Agent) tea.Cmd {
	return func() tea.Msg { return resumedMsg{agent: a, err: a.Resume(ctx)} }
}
