package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/google/uuid"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// AgentSession is a Claude Code session the daemon runs: *factory.Agent.
type AgentSession interface {
	Send(message string) error
	Events() <-chan factory.AgentEvent
	Stop() error
}

// SessionRecord is a daemon-run session's record on disk, so a later start
// resumes its conversation: *factory.PlanSession.
type SessionRecord interface {
	ID() (string, error)
	Record(id string) error
	Close() error
}

// errRunnerStopped is a send to a runner that has been stopped.
var errRunnerStopped = errors.New("the session has been stopped")

// sessionStartProblem is a failure to start a daemon-run session, saying how
// to fix it. verb is what the session does: "build" or "plan".
func sessionStartProblem(err error, verb string) error {
	switch {
	case errors.Is(err, factory.ErrNoClaude):
		return fmt.Errorf("the chunk daemon found no Claude Code to %s with; install it from https://claude.com/claude-code", verb)
	case errors.Is(err, factory.ErrNoFactory):
		return errors.New("the chunk daemon found no factory; install it and put it on PATH, then restart the daemon")
	case errors.Is(err, factory.ErrNoFactoryPlugin):
		return errors.New("factory's f plugin is not loaded in Claude Code; install it")
	case errors.Is(err, factory.ErrFactoryMCP):
		return errors.New("factory's MCP server is not connected; make sure factory is on PATH")
	case errors.Is(err, factory.ErrNoFactoryDaemon):
		return errors.New("the factory daemon is not running; run `factory daemon` with FACTORY_PLAN_STORE=circleci to plan")
	}
	return fmt.Errorf("start the %s session: %w", sessionName(verb), err)
}

// isStartSentinel is whether err is one of the failures sessionStartProblem
// has a sentence for: something missing on this machine, not a fault.
func isStartSentinel(err error) bool {
	for _, s := range []error{factory.ErrNoClaude, factory.ErrNoFactory, factory.ErrNoFactoryPlugin, factory.ErrFactoryMCP, factory.ErrNoFactoryDaemon} {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

// sessionName is the session a verb's session is called: the build session,
// the planning session.
func sessionName(verb string) string {
	if verb == "plan" {
		return "planning"
	}
	return verb
}

// runner runs one long-lived Claude Code session for the daemon, other than
// a build: started on its first message, resumed from its record or once it
// ends, its events kept in a bounded log any number of clients read.
type runner struct {
	ctx   context.Context // the session's lifetime: the daemon's, not a request's
	start func(ctx context.Context, cfg factory.AgentConfig, message string) (AgentSession, error)
	// onReply runs after each reply is in the log, outside mu; the planner's
	// claim check. nil for none. Set before the first send.
	onReply func(byTask bool)

	// sendMu keeps sends in order; it is not mu, as a Send can block on a
	// claude that stopped reading, and reads of the log must not wait.
	sendMu sync.Mutex

	mu sync.Mutex
	// All below are guarded by mu.
	cfg   factory.AgentConfig
	agent AgentSession  // nil until the first send starts it
	ended chan struct{} // closed once agent's events end
	// record is the session's record, adopted by open; nil before then.
	record SessionRecord
	// recorded is whether the record held a session ID, so the first start
	// resumes it.
	recorded bool
	busy     bool
	activity string
	// sends counts the messages sent, so a reply's end of the turn does not
	// undo a send made while onReply ran.
	sends   int
	log     sessionLog
	stopped bool
}

// sessionLog is a runner's last maxSessionLog events.
type sessionLog struct {
	id     string // uuid.NewString() per runner, so a client can tell a restart
	oldest int
	events []chunkd.SessionLogEvent
}

const maxSessionLog = 500

// newRunner is a runner for sessions started with cfg, under a fresh session
// ID unless open replaces it, living as long as ctx.
func newRunner(ctx context.Context, cfg factory.AgentConfig, start func(ctx context.Context, cfg factory.AgentConfig, message string) (AgentSession, error)) *runner {
	cfg.SessionID = uuid.NewString()
	return &runner{ctx: ctx, cfg: cfg, start: start, log: sessionLog{id: uuid.NewString()}}
}

// open adopts rec as the session's record, once, reporting whether it holds
// a session to resume; that session's ID becomes the runner's.
func (r *runner) open(rec SessionRecord) (resumable bool, err error) {
	id, err := rec.ID()
	if err != nil {
		_ = rec.Close()
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		_ = rec.Close()
		return false, errRunnerStopped
	}
	if r.record != nil {
		_ = rec.Close()
		return r.recorded, nil
	}
	r.record = rec
	if id == "" {
		return false, nil
	}
	r.cfg.SessionID, r.recorded = id, true
	return true, nil
}

// opened is whether open has adopted a record.
func (r *runner) opened() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.record != nil
}

// sessionID is the session the runner starts or resumes.
func (r *runner) sessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg.SessionID
}

// live is the current session, nil before the first start, and whether its
// events have not ended.
func (r *runner) live() (AgentSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agent, r.agent != nil && !r.hasEnded()
}

// hasEnded is whether the current session's events have ended. The caller
// must hold mu.
func (r *runner) hasEnded() bool {
	select {
	case <-r.ended:
		return true
	default:
		return false
	}
}

// send sends message to the live session, or starts one on it in dir():
// resuming the session that ended, or the one the record holds, and
// starting the recorded one afresh under its ID when it cannot be resumed.
// started is whether a session started. A start error comes back as start
// gave it.
func (r *runner) send(message string, dir func() (string, error)) (started bool, err error) {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()

	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return false, errRunnerStopped
	}
	a, live := r.agent, r.agent != nil && !r.hasEnded()
	cfg, recorded := r.cfg, r.recorded
	if live {
		// Busy before the send, so a reply that comes before Send returns
		// is not followed by a stale busy.
		r.busy, r.activity = true, ""
		r.sends++
	}
	r.mu.Unlock()

	if live {
		if err := a.Send(message); err != nil {
			r.mu.Lock()
			r.busy = false
			r.mu.Unlock()
			return false, err
		}
		return false, nil
	}

	d, err := dir()
	if err != nil {
		return false, err
	}
	cfg.Dir = d
	// A session that ended is resumed rather than started afresh, so claude
	// keeps the conversation so far, as is the one the record holds.
	cfg.Resume = a != nil || recorded
	s, err := r.start(r.ctx, cfg, message)
	if err != nil && a == nil && recorded && r.ctx.Err() == nil {
		// The recorded session's conversation may be gone, deleted with
		// claude's history say; start the session afresh under its ID.
		cfg.Resume = false
		s, err = r.start(r.ctx, cfg, message)
	}
	if err != nil {
		return false, err
	}

	r.mu.Lock()
	if r.record != nil {
		// A failed write only costs a later start the conversation.
		_ = r.record.Record(cfg.SessionID)
	}
	r.agent, r.ended = s, make(chan struct{})
	r.busy, r.activity = true, ""
	r.sends++
	ended, stopped := r.ended, r.stopped
	go r.forward(s, ended)
	r.mu.Unlock()
	if stopped {
		// stop came while the session was starting, before it had one to
		// stop.
		if err := s.Stop(); err != nil {
			log.Printf("chunkd: stop a session started while stopping: %v", err)
		}
		<-ended
		return false, errRunnerStopped
	}
	return true, nil
}

// forward appends a's events to the log until they end. Those of a session
// that is no longer the runner's, or once the runner is stopped, are read
// and dropped: a factory.Agent's events must be read until they close.
func (r *runner) forward(a AgentSession, ended chan struct{}) {
	defer close(ended)
	for ev := range a.Events() {
		r.mu.Lock()
		if a != r.agent || r.stopped {
			r.mu.Unlock()
			continue
		}
		var reply, byTask bool
		switch {
		case ev.Activity != nil:
			line := ev.Activity.Line()
			r.appendLocked(chunkd.SessionLogActivity, line, false)
			r.busy, r.activity = true, line
		case ev.TurnEnded != nil:
			reply, byTask = true, ev.TurnEnded.ByTask
			r.appendLocked(chunkd.SessionLogReply, ev.TurnEnded.Reply, byTask)
		case ev.Problem != nil:
			r.appendLocked(chunkd.SessionLogProblem, ev.Problem.Error(), false)
			r.busy, r.activity = false, ""
		}
		onReply, sends := r.onReply, r.sends
		r.mu.Unlock()
		if !reply {
			continue
		}
		if onReply != nil {
			onReply(byTask)
		}
		// The turn is over once onReply is, so a session read as idle has
		// had its reply seen to; a send since then started another turn.
		r.mu.Lock()
		if a == r.agent && r.sends == sends {
			r.busy, r.activity = false, ""
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	if a == r.agent {
		// Nothing is working once the session has gone.
		r.busy, r.activity = false, ""
	}
	r.mu.Unlock()
}

// note appends an event the runner's owner has to say.
func (r *runner) note(kind chunkd.SessionLogKind, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendLocked(kind, text, false)
}

// appendLocked appends an event to the log, dropping the oldest past
// maxSessionLog. The caller must hold mu.
func (r *runner) appendLocked(kind chunkd.SessionLogKind, text string, byTask bool) {
	l := &r.log
	l.events = append(l.events, chunkd.SessionLogEvent{Seq: l.oldest + len(l.events), Kind: kind, Text: text, ByTask: byTask})
	if drop := len(l.events) - maxSessionLog; drop > 0 {
		n := copy(l.events, l.events[drop:])
		clear(l.events[n:])
		l.events = l.events[:n]
		l.oldest += drop
	}
}

// view is the log from cursor after, the Next of an earlier view: the
// events with Seq at or past it, from Oldest at the earliest. A negative
// after reads none, only where the log ends.
func (r *runner) view(after int) chunkd.SessionLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.log
	out := chunkd.SessionLog{
		SessionID: r.cfg.SessionID,
		LogID:     l.id,
		Oldest:    l.oldest,
		Busy:      r.busy,
		Activity:  r.activity,
		Next:      l.oldest + len(l.events),
		Events:    []chunkd.SessionLogEvent{},
	}
	if after < 0 {
		return out
	}
	from := max(after-l.oldest, 0)
	if from < len(l.events) {
		out.Events = append(out.Events, l.events[from:]...)
	}
	return out
}

// stop stops the session, closes the record, and waits for the session's
// events to end. It may be called more than once.
func (r *runner) stop() {
	r.mu.Lock()
	already := r.stopped
	r.stopped = true
	a, ended, rec := r.agent, r.ended, r.record
	r.record = nil
	r.busy, r.activity = false, ""
	r.mu.Unlock()
	if a != nil && !already {
		if err := a.Stop(); err != nil {
			log.Printf("chunkd: stop a daemon-run session: %v", err)
		}
	}
	if rec != nil {
		if err := rec.Close(); err != nil {
			log.Printf("chunkd: close a daemon-run session's record: %v", err)
		}
	}
	if ended != nil {
		<-ended
	}
}
