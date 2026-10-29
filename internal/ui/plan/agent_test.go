package plan

import (
	"context"
	"sync"
)

// fakeAgent is an Agent that records what it is sent. What it says back is
// queued, for drive to hand to the screen once it settles, rather than put
// on Events: a test's timed-out command could otherwise be left reading
// Events and swallow it.
type fakeAgent struct {
	// reply is what each message is answered with, if anything.
	reply func(message string) []Event
	// err fails every Request.
	err error
	// resumeErr fails every Resume.
	resumeErr error

	mu      sync.Mutex
	sent    []string
	resumes int
	pending []Event
	closed  chan Event
}

func newFakeAgent() *fakeAgent {
	closed := make(chan Event)
	close(closed)
	return &fakeAgent{closed: closed}
}

func (a *fakeAgent) Request(_ context.Context, message string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, message)
	if a.err != nil {
		return a.err
	}
	if a.reply != nil {
		a.pending = append(a.pending, a.reply(message)...)
	}
	return nil
}

func (a *fakeAgent) Resume(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resumes++
	return a.resumeErr
}

func (a *fakeAgent) Resumes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resumes
}

// Events is closed, so the screen's wait on it ends at once.
func (a *fakeAgent) Events() <-chan Event { return a.closed }

// Sent is every request made so far, failed ones too.
func (a *fakeAgent) Sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.sent...)
}

// reset forgets what was sent.
func (a *fakeAgent) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = nil
}

// take is what the agent has said since it was last asked, oldest first.
func (a *fakeAgent) take() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	evs := a.pending
	a.pending = nil
	return evs
}

// eventFor is ev as m's agent reported it.
func eventFor(m Model, ev Event) eventMsg { return eventMsg{agent: m.agent, Event: ev} }
