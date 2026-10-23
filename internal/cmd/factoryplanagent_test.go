package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// fakeSession is an agentSession that records what it is sent and reports
// what a test puts on its events.
type fakeSession struct {
	first  string
	events chan factory.AgentEvent
	claims []string
	// claimsErr fails Claims.
	claimsErr error

	mu      sync.Mutex
	sent    []string
	stopped bool
}

func newFakeSession(first string) *fakeSession {
	return &fakeSession{first: first, events: make(chan factory.AgentEvent, 8)}
}

func (s *fakeSession) Send(message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, message)
	return nil
}

func (s *fakeSession) Events() <-chan factory.AgentEvent { return s.events }

func (s *fakeSession) Claims(context.Context) ([]string, error) { return s.claims, s.claimsErr }

func (s *fakeSession) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped {
		s.stopped = true
		close(s.events)
	}
	return nil
}

// end closes the session's events as claude exiting would.
func (s *fakeSession) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped {
		s.stopped = true
		close(s.events)
	}
}

// testPlanAgent is a planAgent for a plan created as planID, whose sessions
// are fakes; starts records each session started and the config it was
// started with.
type testPlanAgent struct {
	*planAgent
	mu       sync.Mutex
	sessions []*fakeSession
	configs  []factory.AgentConfig
	claims   []string
	startErr error
}

func newTestPlanAgent(t *testing.T, planID string) *testPlanAgent {
	t.Helper()
	store := &apiStore{planID: planID}
	tp := &testPlanAgent{planAgent: newPlanAgent(t.Context(), factory.AgentConfig{Dir: "/src", SessionID: "s-1"}, store)}
	tp.start = func(_ context.Context, cfg factory.AgentConfig, message string) (agentSession, error) {
		tp.mu.Lock()
		defer tp.mu.Unlock()
		if tp.startErr != nil {
			return nil, tp.startErr
		}
		s := newFakeSession(message)
		s.claims = tp.claims
		tp.sessions = append(tp.sessions, s)
		tp.configs = append(tp.configs, cfg)
		return s, nil
	}
	return tp
}

func (tp *testPlanAgent) session(i int) *fakeSession {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.sessions[i]
}

// next is the planAgent's next event, failing the test if none comes.
func next(t *testing.T, a *planAgent) plan.Event {
	t.Helper()
	select {
	case ev := <-a.Events():
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event from the planning agent")
		return plan.Event{}
	}
}

func TestPlanAgentStartsOnTheFirstMessageWithTheClaim(t *testing.T) {
	tp := newTestPlanAgent(t, "plan-1")
	tp.claims = []string{"plan-1"}
	assert.NilError(t, tp.Send(t.Context(), "add a flag"))
	assert.NilError(t, tp.Send(t.Context(), "keep it small"))

	assert.Equal(t, len(tp.sessions), 1, "one session for the conversation")
	s := tp.session(0)
	assert.Equal(t, s.first, fmt.Sprintf(planClaim, "plan-1", "add a flag"))
	assert.DeepEqual(t, s.sent, []string{"keep it small"})
	assert.Equal(t, tp.configs[0].Resume, false)
}

func TestPlanAgentPassesEventsOn(t *testing.T) {
	tp := newTestPlanAgent(t, "plan-1")
	tp.claims = []string{"plan-1"}
	assert.NilError(t, tp.Send(t.Context(), "add a flag"))
	s := tp.session(0)

	s.events <- factory.AgentEvent{Activity: &claudecode.Activity{Tool: "Read", Detail: "main.go"}}
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "Claimed it."}}
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "Wrote the outcome.", ByTask: true}}

	assert.Assert(t, next(t, tp.planAgent).Activity != "")
	ev := next(t, tp.planAgent)
	assert.Equal(t, ev.Reply.Text, "Claimed it.")
	ev = next(t, tp.planAgent)
	assert.Equal(t, ev.Reply.Text, "Wrote the outcome.")
	assert.Assert(t, ev.Reply.ByTask)
}

func TestPlanAgentSaysWhenTheDaemonHoldsNoClaim(t *testing.T) {
	tp := newTestPlanAgent(t, "plan-1")
	tp.claims = []string{"FNDSF-12"} // a daemon on another store
	assert.NilError(t, tp.Send(t.Context(), "add a flag"))
	s := tp.session(0)
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "I could not claim it."}}

	assert.Equal(t, next(t, tp.planAgent).Reply.Text, "I could not claim it.")
	ev := next(t, tp.planAgent)
	assert.Assert(t, ev.Problem != nil)
	assert.Assert(t, strings.Contains(ev.Problem.Error(), "FACTORY_PLAN_STORE=circleci"), ev.Problem.Error())

	// Only the reply to the claim is checked.
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "ok"}}
	assert.Equal(t, next(t, tp.planAgent).Reply.Text, "ok")
}

func TestPlanAgentSaysWhenTheDaemonIsNotRunning(t *testing.T) {
	tp := newTestPlanAgent(t, "plan-1")
	assert.NilError(t, tp.Send(t.Context(), "add a flag"))
	s := tp.session(0)
	s.claimsErr = factory.ErrNoFactoryDaemon
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "ok"}}

	next(t, tp.planAgent)
	ev := next(t, tp.planAgent)
	assert.Assert(t, ev.Problem != nil && strings.Contains(ev.Problem.Error(), "factory daemon is not running"), "%v", ev.Problem)
}

func TestPlanAgentResumesASessionThatEnded(t *testing.T) {
	tp := newTestPlanAgent(t, "plan-1")
	tp.claims = []string{"plan-1"}
	assert.NilError(t, tp.Send(t.Context(), "add a flag"))
	tp.session(0).end()
	// The ended session's events close; wait for forward to see it.
	assert.Assert(t, waitFor(func() bool {
		tp.planAgent.mu.Lock()
		defer tp.planAgent.mu.Unlock()
		return tp.hasEnded()
	}))

	assert.NilError(t, tp.Send(t.Context(), "are you there?"))
	assert.Equal(t, len(tp.sessions), 2)
	assert.Equal(t, tp.configs[1].Resume, true)
	assert.Equal(t, tp.configs[1].SessionID, "s-1")
	assert.Equal(t, tp.session(1).first, "are you there?", "the plan was claimed already")
}

func TestPlanAgentStartFailureIsASentence(t *testing.T) {
	tp := newTestPlanAgent(t, "plan-1")
	tp.startErr = factory.ErrNoFactoryPlugin
	err := tp.Send(t.Context(), "add a flag")
	assert.ErrorContains(t, err, "f plugin is not loaded")

	// The claim goes on the message that does start the session.
	tp.startErr = nil
	assert.NilError(t, tp.Send(t.Context(), "add a flag"))
	assert.Equal(t, tp.session(0).first, fmt.Sprintf(planClaim, "plan-1", "add a flag"))
}

func TestPlanAgentStopsItsSessionAndRefusesMore(t *testing.T) {
	tp := newTestPlanAgent(t, "plan-1")
	assert.NilError(t, tp.Stop(), "nothing to stop before the first message")
	tp = newTestPlanAgent(t, "plan-1")
	assert.NilError(t, tp.Send(t.Context(), "add a flag"))
	assert.NilError(t, tp.Stop())
	assert.Assert(t, tp.session(0).stopped)
	assert.Assert(t, errors.Is(tp.Send(t.Context(), "more"), errPlanAgentStopped))
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
