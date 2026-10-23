package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

	mu sync.Mutex
	// All fields below are guarded by mu.
	agent   agentSession
	ended   chan struct{} // closed once agent's events end
	stopped bool
	// claimed is whether a message has told the agent to claim the plan.
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
	Claims(ctx context.Context) ([]string, error)
	Stop() error
}

func newPlanAgent(ctx context.Context, cfg factory.AgentConfig, store *apiStore) *planAgent {
	return &planAgent{
		ctx:    ctx,
		cfg:    cfg,
		store:  store,
		events: make(chan plan.Event),
		start: func(ctx context.Context, cfg factory.AgentConfig, message string) (agentSession, error) {
			return factory.StartAgent(ctx, cfg, message)
		},
	}
}

func (p *planAgent) Events() <-chan plan.Event { return p.events }

// Send delivers message to the session, starting it on the first message and
// resuming it if it has ended. The first message after the plan is created
// tells the agent to claim it.
func (p *planAgent) Send(_ context.Context, message string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return errPlanAgentStopped
	}
	message, claimed := withPlanClaim(p.claimed, p.store.id(), message)
	if p.agent != nil && !p.hasEnded() {
		if err := p.agent.Send(message); err != nil {
			return err
		}
		p.claim(claimed)
		return nil
	}
	cfg := p.cfg
	// A session that ended is resumed rather than started afresh, so claude
	// keeps the conversation so far.
	cfg.Resume = p.agent != nil
	a, err := p.start(p.ctx, cfg, message)
	if err != nil {
		return agentStartProblem(err)
	}
	p.agent, p.ended = a, make(chan struct{})
	p.claim(claimed)
	go p.forward(a, p.ended)
	return nil
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

// Stop ends the session, if one started, and waits for it.
func (p *planAgent) Stop() error {
	p.mu.Lock()
	p.stopped = true
	a := p.agent
	p.mu.Unlock()
	if a == nil {
		return nil
	}
	return a.Stop()
}

// forward passes a's events on to the screen until they end. Once ctx ends
// nothing reads them, so they are drained instead: a factory.Agent's events
// must be read until they close.
func (p *planAgent) forward(a agentSession, ended chan struct{}) {
	defer close(ended)
	for ev := range a.Events() {
		out := planEvent(ev)
		if ev.TurnEnded != nil && !ev.TurnEnded.ByTask && p.takeClaimCheck() {
			p.emit(out)
			if err := p.checkClaimed(a); err != nil {
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
func (p *planAgent) checkClaimed(a agentSession) error {
	id := p.store.id()
	claims, err := a.Claims(p.ctx)
	if errors.Is(err, factory.ErrNoFactoryDaemon) {
		return errors.New("the factory daemon is not running, so chunk cannot plan through it. Run `factory daemon` in a terminal, with FACTORY_PLAN_STORE=circleci")
	}
	if err != nil {
		return fmt.Errorf("could not check chunk's claim on the plan: %w", err)
	}
	if slices.Contains(claims, id) {
		return nil
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
