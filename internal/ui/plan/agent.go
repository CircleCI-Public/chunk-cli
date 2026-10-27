package plan

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// Agent is the planning agent: one long-lived factory session the screen
// sends the developer's messages to and reads what it does from. It plans
// through factory's tasks as well as the developer's messages, so it can be
// working with nothing sent, and a message can reach it mid-turn.
type Agent interface {
	// Send delivers message to the agent, starting it on the first.
	Send(ctx context.Context, message string) error
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
	sentMsg struct {
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

func sendCmd(ctx context.Context, a Agent, text string) tea.Cmd {
	return func() tea.Msg { return sentMsg{agent: a, err: a.Send(ctx, text)} }
}
