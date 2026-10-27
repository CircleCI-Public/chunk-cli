package factoryplan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// fakeSession is a session that records what it is sent and reports
// what a test puts on its events.
type fakeSession struct {
	first  string
	events chan factory.AgentEvent
	// block, when set, holds Send until it is closed, as a claude that
	// stopped reading its stdin would.
	block chan struct{}

	mu      sync.Mutex
	sent    []string
	stopped bool
}

func newFakeSession(first string) *fakeSession {
	return &fakeSession{first: first, events: make(chan factory.AgentEvent, 8)}
}

func (s *fakeSession) Send(message string) error {
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, message)
	return nil
}

func (s *fakeSession) Events() <-chan factory.AgentEvent { return s.events }

func (s *fakeSession) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.block != nil {
		// Stopping claude ends a write to it.
		select {
		case <-s.block:
		default:
			close(s.block)
		}
	}
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

// fakeRecord is a plan's session record: id is the session it records,
// and openErr fails opening it, as another chunk holding it open would.
type fakeRecord struct {
	mu      sync.Mutex
	id      string
	openErr error
	opened  int
	closed  bool
}

func (r *fakeRecord) ID() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id, nil
}

func (r *fakeRecord) Record(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.id = id
	return nil
}

func (r *fakeRecord) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *fakeRecord) recorded() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id
}

// testAgent is an Agent for a plan created as planID, whose sessions
// are fakes; starts records each session started and the config it was
// started with. holderID is the session factory says holds the plan's claim,
// and holderErr fails reading it.
type testAgent struct {
	*Agent
	mu        sync.Mutex
	sessions  []*fakeSession
	configs   []factory.AgentConfig
	startErr  error
	holderID  string
	holderErr error
	// worktreeErr fails opening the plan's worktree.
	worktreeErr error
	// started, when set, is called as each session starts.
	started func()
	// resumeErr fails starting a session that resumes one.
	resumeErr error
	// record is the plan's session record on this machine.
	record *fakeRecord
}

func (tp *testAgent) setHolder(id string, err error) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.holderID, tp.holderErr = id, err
}

func newTestAgent(t *testing.T, planID string) *testAgent {
	t.Helper()
	store := &Store{planID: planID}
	tp := &testAgent{Agent: NewAgent(t.Context(), factory.AgentConfig{Dir: "/src", SessionID: "s-1"}, store)}
	tp.start = func(_ context.Context, cfg factory.AgentConfig, message string) (session, error) {
		tp.mu.Lock()
		defer tp.mu.Unlock()
		if tp.startErr != nil {
			return nil, tp.startErr
		}
		if cfg.Resume && tp.resumeErr != nil {
			tp.configs = append(tp.configs, cfg)
			return nil, tp.resumeErr
		}
		s := newFakeSession(message)
		tp.sessions = append(tp.sessions, s)
		tp.configs = append(tp.configs, cfg)
		if tp.started != nil {
			tp.started()
		}
		return s, nil
	}
	tp.holder = func(context.Context) (string, error) {
		tp.mu.Lock()
		defer tp.mu.Unlock()
		return tp.holderID, tp.holderErr
	}
	tp.worktree = func(context.Context) (string, error) {
		tp.mu.Lock()
		defer tp.mu.Unlock()
		return "/worktrees/" + planID, tp.worktreeErr
	}
	tp.record = &fakeRecord{}
	tp.openRecord = func() (sessionRecord, error) {
		tp.record.mu.Lock()
		defer tp.record.mu.Unlock()
		if tp.record.openErr != nil {
			return nil, tp.record.openErr
		}
		tp.record.opened++
		return tp.record, nil
	}
	return tp
}

func (tp *testAgent) session(i int) *fakeSession {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.sessions[i]
}

// next is the Agent's next event, failing the test if none comes.
func next(t *testing.T, a *Agent) plan.Event {
	t.Helper()
	select {
	case ev := <-a.Events():
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event from the planning agent")
		return plan.Event{}
	}
}

func TestAgentStartsOnTheFirstMessageWithTheClaim(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.setHolder("s-1", nil)
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	err = tp.Send(t.Context(), "keep it small")
	assert.NilError(t, err)

	assert.Assert(t, cmp.Len(tp.sessions, 1), "one session for the conversation")
	s := tp.session(0)
	assert.Check(t, cmp.Equal(s.first, fmt.Sprintf(claimMessage, "plan-1", "add a flag")))
	assert.Check(t, cmp.DeepEqual(s.sent, []string{"keep it small"}))
	assert.Check(t, !tp.configs[0].Resume)
}

func TestAgentStartsOnlyOnceItsCheckPasses(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	problem := errors.New("open chunk watch in this plan's repository")
	tp.CheckStart(func(context.Context) error { return problem })
	err := tp.Send(t.Context(), "add a flag")
	assert.Check(t, cmp.ErrorIs(err, problem))
	tp.mu.Lock()
	assert.Check(t, cmp.Len(tp.sessions, 0))
	tp.mu.Unlock()

	tp.CheckStart(func(context.Context) error { return nil })
	err = tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	assert.Check(t, cmp.Contains(tp.session(0).first, "factory plan claim plan-1"))
}

func TestAgentPassesEventsOn(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.setHolder("s-1", nil)
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	s := tp.session(0)

	s.events <- factory.AgentEvent{Activity: &claudecode.Activity{Tool: "Read", Detail: "main.go"}}
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "Claimed it."}}
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "Wrote the outcome.", ByTask: true}}

	ev := next(t, tp.Agent)
	assert.Check(t, cmp.Equal(ev.Activity, "Read main.go"))
	ev = next(t, tp.Agent)
	assert.Assert(t, ev.Reply != nil)
	assert.Check(t, cmp.Equal(ev.Reply.Text, "Claimed it."))
	ev = next(t, tp.Agent)
	assert.Assert(t, ev.Reply != nil)
	assert.Check(t, cmp.Equal(ev.Reply.Text, "Wrote the outcome."))
	assert.Check(t, ev.Reply.ByTask)
}

func TestAgentSaysWhenTheDaemonHoldsNoClaim(t *testing.T) {
	tp := newTestAgent(t, "plan-1") // a daemon on another store claims nothing here
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	s := tp.session(0)
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "I could not claim it."}}

	ev := next(t, tp.Agent)
	assert.Assert(t, ev.Reply != nil)
	assert.Check(t, cmp.Equal(ev.Reply.Text, "I could not claim it."))
	ev = next(t, tp.Agent)
	assert.Assert(t, ev.Problem != nil)
	assert.Check(t, cmp.ErrorContains(ev.Problem, "FACTORY_PLAN_STORE=circleci"))

	// Only the reply to the claim is checked.
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "ok"}}
	ev = next(t, tp.Agent)
	assert.Assert(t, ev.Reply != nil)
	assert.Check(t, cmp.Equal(ev.Reply.Text, "ok"))
}

func TestAgentSaysWhenAnotherSessionHoldsTheClaim(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.setHolder("s-other", nil)
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	tp.session(0).events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "ok"}}

	next(t, tp.Agent)
	ev := next(t, tp.Agent)
	assert.Assert(t, ev.Problem != nil)
	assert.Check(t, cmp.ErrorContains(ev.Problem, "claimed by another factory session"))
}

func TestAgentSaysWhenItCannotReadTheClaim(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.setHolder("", errors.New("503 from factory"))
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	tp.session(0).events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "ok"}}

	next(t, tp.Agent)
	ev := next(t, tp.Agent)
	assert.Assert(t, ev.Problem != nil)
	assert.Check(t, cmp.ErrorContains(ev.Problem, "503 from factory"))
}

// A session that holds the plan's claim is not told to claim it again; one
// that has lost it, as an agent that released the plan has, is.
func TestAgentClaimsAgainOnceTheClaimIsLost(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.setHolder("s-1", nil)
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	s := tp.session(0)
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "Claimed it."}}
	next(t, tp.Agent)

	err = tp.Send(t.Context(), "keep it small")
	assert.NilError(t, err)
	tp.setHolder("", nil)
	err = tp.Send(t.Context(), "and fast")
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(s.sent, []string{"keep it small", fmt.Sprintf(claimMessage, "plan-1", "and fast")}))

	// The claim it was just told to make is checked once it replies.
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "Claimed it again."}}
	ev := next(t, tp.Agent)
	assert.Assert(t, ev.Reply != nil)
	assert.Check(t, cmp.Equal(ev.Reply.Text, "Claimed it again."))
	ev = next(t, tp.Agent)
	assert.Check(t, ev.Problem != nil, "the claim is still missing")
}

// A blip reading the claim does not have the agent claim a plan it holds.
func TestAgentKeepsTheClaimWhenItCannotReadIt(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.setHolder("s-1", nil)
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	s := tp.session(0)
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "Claimed it."}}
	next(t, tp.Agent)

	tp.setHolder("", errors.New("503 from factory"))
	err = tp.Send(t.Context(), "keep it small")
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(s.sent, []string{"keep it small"}))
}

func TestAgentResumesASessionThatEnded(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.setHolder("s-1", nil)
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	tp.session(0).end()
	// The ended session's events close; wait for forward to see it.
	ended := waitFor(func() bool {
		tp.Agent.mu.Lock()
		defer tp.Agent.mu.Unlock()
		return tp.hasEnded()
	})
	assert.Assert(t, ended)

	err = tp.Send(t.Context(), "are you there?")
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(tp.sessions, 2))
	assert.Check(t, tp.configs[1].Resume)
	assert.Check(t, cmp.Equal(tp.configs[1].SessionID, "s-1"))
	// The ended session's claim was freed with it, so the resumed one claims
	// the plan again.
	assert.Check(t, cmp.Equal(tp.session(1).first, fmt.Sprintf(claimMessage, "plan-1", "are you there?")))
}

// Each session, the resumed one too, starts in the plan's worktree rather
// than the developer's checkout, where another session ending would free the
// plan's claim.
func TestAgentStartsEachSessionInThePlansWorktree(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.setHolder("s-1", nil)
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	tp.session(0).end()
	ended := waitFor(func() bool {
		tp.Agent.mu.Lock()
		defer tp.Agent.mu.Unlock()
		return tp.hasEnded()
	})
	assert.Assert(t, ended)
	err = tp.Send(t.Context(), "are you there?")
	assert.NilError(t, err)

	assert.Assert(t, cmp.Len(tp.configs, 2))
	assert.Check(t, cmp.Equal(tp.configs[0].Dir, "/worktrees/plan-1"))
	assert.Check(t, cmp.Equal(tp.configs[1].Dir, "/worktrees/plan-1"))
}

func TestAgentWithoutAWorktreeStartsNoSession(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.worktreeErr = errors.New("open the plan's worktree: no origin/main")

	err := tp.Send(t.Context(), "add a flag")
	assert.Check(t, cmp.ErrorContains(err, "no origin/main"))
	assert.Check(t, cmp.Len(tp.sessions, 0))
}

func TestAgentStartFailureIsASentence(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.startErr = factory.ErrNoFactoryPlugin
	err := tp.Send(t.Context(), "add a flag")
	assert.Check(t, cmp.ErrorContains(err, "f plugin is not loaded"))

	// The claim goes on the message that does start the session.
	tp.startErr = nil
	err = tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(tp.session(0).first, fmt.Sprintf(claimMessage, "plan-1", "add a flag")))
}

func TestAgentStopsItsSessionAndRefusesMore(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	err := tp.Stop()
	assert.Check(t, err, "nothing to stop before the first message")

	tp = newTestAgent(t, "plan-1")
	err = tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	err = tp.Stop()
	assert.NilError(t, err)
	assert.Check(t, tp.session(0).stopped)
	err = tp.Send(t.Context(), "more")
	assert.Check(t, cmp.ErrorIs(err, ErrStopped))
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

// A Send stuck writing to a claude that stopped reading must not hold up
// Stop, which ends the write.
func TestAgentStopWhileSendIsBlocked(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.setHolder("s-1", nil)
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	s := tp.session(0)
	s.mu.Lock()
	s.block = make(chan struct{})
	s.mu.Unlock()

	sent := make(chan error, 1)
	go func() { sent <- tp.Send(t.Context(), "more") }()
	select {
	case err := <-sent:
		t.Fatalf("Send returned before the session stopped: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	stopped := make(chan error, 1)
	go func() { stopped <- tp.Stop() }()
	select {
	case err := <-stopped:
		assert.Check(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Stop waited on the blocked Send")
	}
	<-sent
}

// A Stop that comes while the session is starting stops it once it has.
func TestAgentStopWhileStartingStopsTheSession(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.started = func() {
		go func() { _ = tp.Stop() }()
		stopped := waitFor(func() bool {
			tp.Agent.mu.Lock()
			defer tp.Agent.mu.Unlock()
			return tp.stopped
		})
		assert.Check(t, stopped)
	}
	err := tp.Send(t.Context(), "add a flag")
	assert.Check(t, cmp.ErrorIs(err, ErrStopped))
	assert.Check(t, tp.sessions[0].stopped)
}

// Once stopped, the agent's events are drained, not held for a screen that
// no longer reads them.
func TestAgentDrainsEventsOnceStopped(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.setHolder("s-1", nil)
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	s := tp.session(0)
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "unread"}}
	s.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "unread too"}}
	err = tp.Stop()
	assert.NilError(t, err)
	drained := waitFor(func() bool {
		tp.Agent.mu.Lock()
		defer tp.Agent.mu.Unlock()
		return tp.hasEnded()
	})
	assert.Check(t, drained, "forward is still waiting to hand on an event")
}

// TestWithClaimOnlyPrependsOnce checks that the claim instruction goes only on
// the first message sent after the plan exists: the agent needs the plan's ID
// once, to claim it, and every message after would otherwise repeat an
// instruction it has already followed.
func TestWithClaimOnlyPrependsOnce(t *testing.T) {
	msg, claimed := withClaim(false, "plan-1", "hello")
	assert.Check(t, claimed)
	assert.Check(t, cmp.Equal(msg, fmt.Sprintf(claimMessage, "plan-1", "hello")))

	msg, claimed = withClaim(claimed, "plan-1", "a later message")
	assert.Check(t, claimed)
	assert.Check(t, cmp.Equal(msg, "a later message"))
}

// TestWithClaimWaitsForAnID checks that a message sent before the plan has
// been created passes through unchanged, since there is no ID yet to tell the
// agent to claim.
func TestWithClaimWaitsForAnID(t *testing.T) {
	msg, claimed := withClaim(false, "", "hello")
	assert.Check(t, !claimed)
	assert.Check(t, cmp.Equal(msg, "hello"))
}

// A plan reopened in a new screen resumes the session an earlier screen
// recorded as planning it, so claude keeps that conversation, and claims
// the plan as that session.
func TestAgentResumesTheSessionRecordedForThePlan(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.record.id = "s-0"
	var checked string
	tp.CheckStart(func(context.Context) error {
		checked = tp.SessionID()
		return nil
	})

	err := tp.Send(t.Context(), "can you keep going with this")
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(tp.configs, 1))
	assert.Check(t, tp.configs[0].Resume)
	assert.Check(t, cmp.Equal(tp.configs[0].SessionID, "s-0"))
	assert.Check(t, cmp.Equal(tp.SessionID(), "s-0"))
	assert.Check(t, cmp.Equal(checked, "s-0"), "the start check saw the screen's own session")
	assert.Check(t, cmp.Equal(tp.session(0).first, fmt.Sprintf(claimMessage, "plan-1", "can you keep going with this")))
}

func TestAgentRecordsTheSessionItStarts(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(tp.configs, 1))
	assert.Check(t, !tp.configs[0].Resume)
	assert.Check(t, cmp.Equal(tp.record.recorded(), "s-1"))

	// A session that ends is resumed without opening the record again.
	tp.session(0).end()
	ended := waitFor(func() bool {
		tp.Agent.mu.Lock()
		defer tp.Agent.mu.Unlock()
		return tp.hasEnded()
	})
	assert.Assert(t, ended)
	err = tp.Send(t.Context(), "are you there?")
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(tp.record.opened, 1))
}

// claude may no longer have a recorded session's conversation; the session
// then starts afresh under the recorded ID.
func TestAgentStartsTheRecordedSessionAfreshWhenItCannotResume(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.record.id = "s-0"
	tp.resumeErr = errors.New("claude exited before it started: No conversation found")

	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(tp.configs, 2))
	assert.Check(t, tp.configs[0].Resume)
	assert.Check(t, !tp.configs[1].Resume)
	assert.Check(t, cmp.Equal(tp.configs[1].SessionID, "s-0"))
	assert.Check(t, cmp.Equal(tp.record.recorded(), "s-0"))
}

func TestAgentRefusesAPlanOpenInAnotherChunk(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	tp.record.openErr = factory.ErrPlanSessionOpen

	err := tp.Send(t.Context(), "add a flag")
	assert.Check(t, cmp.ErrorContains(err, "open for planning in another chunk window"))
	assert.Check(t, cmp.Len(tp.configs, 0))

	// Once the other chunk lets go, the next message starts the session.
	tp.record.openErr = nil
	err = tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(tp.configs, 1))
}

func TestAgentStopClosesThePlansSessionRecord(t *testing.T) {
	tp := newTestAgent(t, "plan-1")
	err := tp.Send(t.Context(), "add a flag")
	assert.NilError(t, err)
	err = tp.Stop()
	assert.NilError(t, err)
	tp.record.mu.Lock()
	defer tp.record.mu.Unlock()
	assert.Check(t, tp.record.closed)
}
