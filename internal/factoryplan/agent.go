package factoryplan

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// Agent is a plan.Agent over a long-lived factory.Agent. The session
// starts on the first Send, since claude needs a message to start on, and
// is resumed by the next Send if it ends without being stopped.
type Agent struct {
	ctx    context.Context // the session's lifetime
	cfg    factory.AgentConfig
	store  *Store
	events chan plan.Event
	// start starts a session; factory.StartAgent but in tests.
	start func(context.Context, factory.AgentConfig, string) (session, error)
	// done is closed by Stop, after which nothing reads events.
	done     chan struct{}
	stopOnce sync.Once
	// holder is the session holding the plan's claim in factory, "" when
	// none does; the store's but in tests.
	holder func(context.Context) (string, error)
	// worktree is the directory each session starts in: the plan's
	// worktree, opened by planWorktree but in tests.
	worktree func(context.Context) (string, error)
	// check is what must hold before the session starts or resumes, as
	// CheckStart sets it; nil for nothing.
	check func(context.Context) error
	// openRecord opens this machine's record of the session planning the
	// plan; factory.OpenPlanSession but in tests.
	openRecord func() (sessionRecord, error)

	// sendMu keeps Sends in order. It is not mu: a Send can block writing to
	// a claude that stopped reading, and Stop must not wait on it.
	sendMu sync.Mutex

	mu sync.Mutex
	// All fields below are guarded by mu.
	agent   session
	ended   chan struct{} // closed once agent's events end
	stopped bool
	// claimed is whether a message has told the agent to claim the plan
	// since it last held no claim on it.
	claimed bool
	// checkClaim is set until the reply to that message, when the daemon
	// should hold the plan's claim for this session.
	checkClaim bool
	// record is the plan's session record, held open from the first Send
	// that opens it until Stop, whether or not a session then starts; nil
	// before then.
	record sessionRecord
}

// sessionRecord is what Agent needs of a factory.PlanSession.
type sessionRecord interface {
	ID() (string, error)
	Record(id string) error
	Close() error
}

// ErrStopped is returned by a Send after Stop.
var ErrStopped = errors.New("the planning session has been stopped")

// session is what Agent needs of a factory.Agent.
type session interface {
	Send(message string) error
	Events() <-chan factory.AgentEvent
	Stop() error
}

// NewAgent is the planning session for store's plan, in cfg.Dir, which lives
// as long as ctx. The caller must Stop it.
func NewAgent(ctx context.Context, cfg factory.AgentConfig, store *Store) *Agent {
	return &Agent{
		ctx:    ctx,
		cfg:    cfg,
		store:  store,
		events: make(chan plan.Event),
		done:   make(chan struct{}),
		start: func(ctx context.Context, cfg factory.AgentConfig, message string) (session, error) {
			return factory.StartAgent(ctx, cfg, message)
		},
		holder: store.Holder,
		worktree: func(ctx context.Context) (string, error) {
			return planWorktree(ctx, cfg.Dir, store)
		},
		openRecord: func() (sessionRecord, error) {
			return factory.OpenPlanSession(cfg.Dir, store.ID())
		},
	}
}

func (p *Agent) Events() <-chan plan.Event { return p.events }

// SessionID is the factory session the agent claims its plan as: the one
// recorded as planning the plan on this machine, once a Send has opened the
// record, so a plan reopened in a new screen is planned by the same session.
func (p *Agent) SessionID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.SessionID
}

// CheckStart has check run before the session starts or resumes, refusing to
// start it when check fails, so a plan can be opened and read before what
// planning needs is in place. It must be called before the first Send.
func (p *Agent) CheckStart(check func(context.Context) error) { p.check = check }

// Send delivers message to the session, starting it on the first message and
// resuming it if it has ended. A message tells the agent to claim the plan
// when it is the first since the plan was created, or the agent has since
// lost its claim: factory frees the claims of a session that ends, and an
// agent can release its plan, after which the daemon hands it no tasks.
func (p *Agent) Send(ctx context.Context, message string) error {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return ErrStopped
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
	message, claimed := withClaim(p.claimed, p.store.ID(), message)
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
	recorded := false
	if a == nil {
		var err error
		if recorded, err = p.openSession(); err != nil {
			return err
		}
	}
	p.mu.Lock()
	cfg := p.cfg
	p.mu.Unlock()
	// A session that ended is resumed rather than started afresh, so claude
	// keeps the conversation so far, as is the one recorded as planning the
	// plan when an earlier screen started it. The plan's worktree is where it
	// was started, so claude finds that conversation there.
	cfg.Resume = a != nil || recorded
	if p.check != nil {
		if err := p.check(p.ctx); err != nil {
			return err
		}
	}
	dir, err := p.worktree(p.ctx)
	if err != nil {
		return err
	}
	cfg.Dir = dir
	started, err := p.start(p.ctx, cfg, message)
	if err != nil && recorded && p.ctx.Err() == nil {
		// The recorded session's conversation may be gone, deleted with
		// claude's history say; start the session afresh under its ID.
		cfg.Resume = false
		started, err = p.start(p.ctx, cfg, message)
	}
	if err != nil {
		return startProblem(err)
	}
	p.mu.Lock()
	if p.record != nil {
		// A failed write only costs the next screen the conversation.
		_ = p.record.Record(cfg.SessionID)
	}
	p.agent, p.ended = started, make(chan struct{})
	stopped := p.stopped
	p.claim(claimed)
	go p.forward(started, p.ended)
	p.mu.Unlock()
	if stopped {
		// Stop came while the session was starting, before it had one to
		// stop.
		_ = started.Stop()
		return ErrStopped
	}
	return nil
}

// openSession opens the plan's session record, once, and adopts the session
// it records, reporting whether there was one to resume.
func (p *Agent) openSession() (bool, error) {
	p.mu.Lock()
	opened := p.record != nil
	p.mu.Unlock()
	if opened || p.store.ID() == "" {
		return false, nil
	}
	rec, err := p.openRecord()
	if errors.Is(err, factory.ErrPlanSessionOpen) {
		return false, errors.New("this plan is open for planning in another chunk window; send messages from there")
	}
	if err != nil {
		return false, err
	}
	id, err := rec.ID()
	if err != nil {
		_ = rec.Close()
		return false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		_ = rec.Close()
		return false, ErrStopped
	}
	p.record = rec
	if id == "" {
		return false, nil
	}
	p.cfg.SessionID = id
	return true, nil
}

// holds is whether this session holds the plan's claim in factory. A failed
// read counts as holding it, so a blip in the API does not have the agent
// claim a plan it has.
func (p *Agent) holds(ctx context.Context) bool {
	h, err := p.holder(ctx)
	return err != nil || h == p.SessionID()
}

// claim records whether the message just sent left the plan claimed, as
// withClaim reported it; a message that first told the agent to claim it
// is owed a check once the agent replies. The caller must hold mu.
func (p *Agent) claim(claimed bool) {
	if claimed && !p.claimed {
		p.claimed, p.checkClaim = true, true
	}
}

// hasEnded is whether the current session's events have ended. The caller
// must hold mu.
func (p *Agent) hasEnded() bool {
	select {
	case <-p.ended:
		return true
	default:
		return false
	}
}

// Stop ends the session, if one started, and waits for it. A session still
// starting is stopped by the Send starting it, once it has started.
func (p *Agent) Stop() error {
	p.stopOnce.Do(func() { close(p.done) })
	p.mu.Lock()
	p.stopped = true
	a, rec := p.agent, p.record
	p.record = nil
	p.mu.Unlock()
	var err error
	if a != nil {
		err = a.Stop()
	}
	if rec != nil {
		err = errors.Join(err, rec.Close())
	}
	return err
}

// forward passes a's events on to the screen until they end. Once ctx ends,
// or Stop is called, nothing reads them, so they are drained instead: a
// factory.Agent's events must be read until they close.
func (p *Agent) forward(a session, ended chan struct{}) {
	defer close(ended)
	for ev := range a.Events() {
		out := event(ev)
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

func (p *Agent) emit(ev plan.Event) {
	select {
	case p.events <- ev:
	case <-p.ctx.Done():
	case <-p.done:
	}
}

// takeClaimCheck is whether a claim is owed a check, clearing it.
func (p *Agent) takeClaimCheck() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	owed := p.checkClaim
	p.checkClaim = false
	return owed
}

// checkClaimed reports why the agent holds no claim on the plan after its
// reply to the message telling it to claim it. Without the claim the daemon
// hands it no planning tasks, and the plan never moves.
func (p *Agent) checkClaimed() error {
	id := p.store.ID()
	h, err := p.holder(p.ctx)
	switch {
	case err != nil:
		return fmt.Errorf("could not check chunk's claim on the plan: %w", err)
	case h == p.SessionID():
		return nil
	case h != "":
		return fmt.Errorf("plan %s is claimed by another factory session, so no planning tasks will come to chunk", id)
	}
	return fmt.Errorf("chunk could not claim plan %s in factory, so no planning tasks will come. "+
		"Run the factory daemon with FACTORY_PLAN_STORE=circleci and FACTORY_PLAN_CIRCLECI_TOKEN "+
		"set to a token for the CircleCI user chunk is signed in as", id)
}

// event is ev as the planning screen shows it.
func event(ev factory.AgentEvent) plan.Event {
	switch {
	case ev.Activity != nil:
		return plan.Event{Activity: ev.Activity.Line()}
	case ev.TurnEnded != nil:
		return plan.Event{Reply: &plan.Reply{Text: ev.TurnEnded.Reply, ByTask: ev.TurnEnded.ByTask}}
	default:
		return plan.Event{Problem: ev.Problem}
	}
}

// startProblem is a failure to start the session as a sentence for the
// chat, saying how to fix it.
func startProblem(err error) error {
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

// claimMessage is what the first message after the plan is created starts
// with. Claiming alone leaves the agent waiting for a change to be asked for,
// so it is told the conversation is planning, and how the plan gets written:
// by the daemon's tasks, not in the chat.
const claimMessage = "This conversation plans a change through factory, as plan %s. " +
	"Claim it by running `factory plan claim %[1]s` in Bash, then run `factory daemon wait` in the background and do the planning tasks it hands out. " +
	"The plan is written with the plan tools, never in your replies; keep replies short. " +
	"The developer's request:\n\n%s"

// withClaim prepends claimMessage to message when it is the first message
// sent since planID was created, so the agent claims the plan and starts
// planning it; a later message, or one sent before the plan exists, passes
// through unchanged. claimed is carried by the caller across calls.
func withClaim(claimed bool, planID, message string) (string, bool) {
	if claimed || planID == "" {
		return message, claimed
	}
	return fmt.Sprintf(claimMessage, planID, message), true
}
