package cmd

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// planAgent is a plan.Agent over a long-lived factory.Agent. The session
// starts on the first Send, since claude needs a message to start on, and
// is resumed by the next Send if it ends without being stopped.
type planAgent struct {
	ctx    context.Context // the session's lifetime
	cfg    factory.AgentConfig
	store  *apiStore
	events chan plan.Event
	// start starts a session; factory.StartAgent but in tests.
	start func(context.Context, factory.AgentConfig, string) (agentSession, error)
	// done is closed by Stop, after which nothing reads events.
	done     chan struct{}
	stopOnce sync.Once
	// holder is the session holding the plan's claim in factory, "" when
	// none does; the store's but in tests.
	holder func(context.Context) (string, error)

	// sendMu keeps Sends in order. It is not mu: a Send can block writing to
	// a claude that stopped reading, and Stop must not wait on it.
	sendMu sync.Mutex

	mu sync.Mutex
	// All fields below are guarded by mu.
	agent   agentSession
	ended   chan struct{} // closed once agent's events end
	stopped bool
	// claimed is whether a message has told the agent to claim the plan
	// since it last held no claim on it.
	claimed bool
	// checkClaim is set until the reply to that message, when the daemon
	// should hold the plan's claim for this session.
	checkClaim bool
}

// errPlanAgentStopped is returned by a Send after Stop.
var errPlanAgentStopped = errors.New("the planning session has been stopped")

// agentSession is what planAgent needs of a factory.Agent.
type agentSession interface {
	Send(message string) error
	Events() <-chan factory.AgentEvent
	Stop() error
}

func newPlanAgent(ctx context.Context, cfg factory.AgentConfig, store *apiStore) *planAgent {
	return &planAgent{
		ctx:    ctx,
		cfg:    cfg,
		store:  store,
		events: make(chan plan.Event),
		done:   make(chan struct{}),
		start: func(ctx context.Context, cfg factory.AgentConfig, message string) (agentSession, error) {
			return factory.StartAgent(ctx, cfg, message)
		},
		holder: store.holder,
	}
}

func (p *planAgent) Events() <-chan plan.Event { return p.events }

// Send delivers message to the session, starting it on the first message and
// resuming it if it has ended. A message tells the agent to claim the plan
// when it is the first since the plan was created, or the agent has since
// lost its claim: a session that ends frees its claims (factory's SessionEnd
// hook), and an agent can release its plan, after which the daemon hands it
// no tasks.
func (p *planAgent) Send(ctx context.Context, message string) error {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return errPlanAgentStopped
	}
	a, live := p.agent, p.agent != nil && !p.hasEnded()
	if a != nil && !live {
		// factory frees the ended session's claim on its own time, so the
		// plan may still read as claimed; the resumed session claims it
		// again either way.
		p.claimed = false
	}
	recheck := live && p.claimed && !p.checkClaim
	p.mu.Unlock()

	if recheck && !p.holds(ctx) {
		p.mu.Lock()
		p.claimed = false
		p.mu.Unlock()
	}

	p.mu.Lock()
	message, claimed := withPlanClaim(p.claimed, p.store.id(), message)
	p.mu.Unlock()

	if live {
		if err := a.Send(message); err != nil {
			return err
		}
		p.mu.Lock()
		p.claim(claimed)
		p.mu.Unlock()
		return nil
	}
	cfg := p.cfg
	// A session that ended is resumed rather than started afresh, so claude
	// keeps the conversation so far.
	cfg.Resume = a != nil
	started, err := p.start(p.ctx, cfg, message)
	if err != nil {
		return agentStartProblem(err)
	}
	p.mu.Lock()
	p.agent, p.ended = started, make(chan struct{})
	stopped := p.stopped
	p.claim(claimed)
	go p.forward(started, p.ended)
	p.mu.Unlock()
	if stopped {
		// Stop came while the session was starting, before it had one to
		// stop.
		_ = started.Stop()
		return errPlanAgentStopped
	}
	return nil
}

// holds is whether this session holds the plan's claim in factory. A failed
// read counts as holding it, so a blip in the API does not have the agent
// claim a plan it has.
func (p *planAgent) holds(ctx context.Context) bool {
	h, err := p.holder(ctx)
	return err != nil || h == p.cfg.SessionID
}

// claim records whether the message just sent left the plan claimed, as
// withPlanClaim reported it; a message that first told the agent to claim it
// is owed a check once the agent replies. The caller must hold mu.
func (p *planAgent) claim(claimed bool) {
	if claimed && !p.claimed {
		p.claimed, p.checkClaim = true, true
	}
}

// hasEnded is whether the current session's events have ended. The caller
// must hold mu.
func (p *planAgent) hasEnded() bool {
	select {
	case <-p.ended:
		return true
	default:
		return false
	}
}

// Stop ends the session, if one started, and waits for it. A session still
// starting is stopped by the Send starting it, once it has started.
func (p *planAgent) Stop() error {
	p.stopOnce.Do(func() { close(p.done) })
	p.mu.Lock()
	p.stopped = true
	a := p.agent
	p.mu.Unlock()
	if a == nil {
		return nil
	}
	return a.Stop()
}

// forward passes a's events on to the screen until they end. Once ctx ends,
// or Stop is called, nothing reads them, so they are drained instead: a
// factory.Agent's events must be read until they close.
func (p *planAgent) forward(a agentSession, ended chan struct{}) {
	defer close(ended)
	for ev := range a.Events() {
		out := planEvent(ev)
		if ev.TurnEnded != nil && !ev.TurnEnded.ByTask && p.takeClaimCheck() {
			p.emit(out)
			if err := p.checkClaimed(); err != nil {
				p.emit(plan.Event{Problem: err})
			}
			continue
		}
		p.emit(out)
	}
}

func (p *planAgent) emit(ev plan.Event) {
	select {
	case p.events <- ev:
	case <-p.ctx.Done():
	case <-p.done:
	}
}

// takeClaimCheck is whether a claim is owed a check, clearing it.
func (p *planAgent) takeClaimCheck() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	owed := p.checkClaim
	p.checkClaim = false
	return owed
}

// checkClaimed reports why the agent holds no claim on the plan after its
// reply to the message telling it to claim it. Without the claim the daemon
// hands it no planning tasks, and the plan never moves.
func (p *planAgent) checkClaimed() error {
	id := p.store.id()
	h, err := p.holder(p.ctx)
	switch {
	case err != nil:
		return fmt.Errorf("could not check chunk's claim on the plan: %w", err)
	case h == p.cfg.SessionID:
		return nil
	case h != "":
		return fmt.Errorf("plan %s is claimed by another factory session, so no planning tasks will come to chunk", id)
	}
	return fmt.Errorf("chunk could not claim plan %s in factory, so no planning tasks will come. "+
		"Run the factory daemon with FACTORY_PLAN_STORE=circleci and FACTORY_PLAN_CIRCLECI_TOKEN "+
		"set to a token for the CircleCI user chunk is signed in as", id)
}

// planEvent is ev as the planning screen shows it.
func planEvent(ev factory.AgentEvent) plan.Event {
	switch {
	case ev.Activity != nil:
		return plan.Event{Activity: activityLine(*ev.Activity)}
	case ev.TurnEnded != nil:
		return plan.Event{Reply: &plan.Reply{Text: ev.TurnEnded.Reply, ByTask: ev.TurnEnded.ByTask}}
	default:
		return plan.Event{Problem: ev.Problem}
	}
}

// agentStartProblem is a failure to start the session as a sentence for the
// chat, saying how to fix it.
func agentStartProblem(err error) error {
	switch {
	case errors.Is(err, factory.ErrNoClaude):
		return errors.New("this machine has no Claude Code to plan with; install it from https://claude.com/claude-code")
	case errors.Is(err, factory.ErrNoFactory):
		return errors.New("factory is not installed; install it and put it on PATH")
	case errors.Is(err, factory.ErrNoFactoryPlugin):
		return errors.New("factory's f plugin is not loaded in Claude Code; install it")
	case errors.Is(err, factory.ErrFactoryMCP):
		return errors.New("factory's MCP server is not connected; make sure factory is on PATH")
	}
	return fmt.Errorf("start the planning session: %w", err)
}
