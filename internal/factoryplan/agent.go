package factoryplan

import (
	"context"
	"errors"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// Agent is a plan.Agent over the planner the chunk daemon runs for store's
// plan: a request and resumes go to the daemon, and the planner's log is
// read back every pollInterval while the screen lives.
type Agent struct {
	ctx    context.Context
	root   string
	model  string
	store  *Store
	events chan plan.Event
	check  func(context.Context) error // CheckStart's; nil for none
	// request, resume and fetch are chunkd.RequestPlanner, ResumePlanner and
	// FetchPlanner but in tests.
	request func(planID string, req chunkd.PlannerRequest) (chunkd.SessionLog, error)
	resume  func(planID string, req chunkd.PlannerResume) (chunkd.SessionLog, error)
	fetch   func(planID string, after int) (chunkd.SessionLog, error)
	stop    context.CancelFunc
}

// pollInterval is how often an Agent reads its planner's log.
var pollInterval = 500 * time.Millisecond

// errUnreachable is what the screen is told once while the daemon cannot be
// read.
var errUnreachable = errors.New("the chunk daemon is not reachable; chunk keeps trying")

// NewAgent is the planner of store's plan in the project at root, followed
// for as long as ctx lives or until Stop.
func NewAgent(ctx context.Context, root, model string, store *Store) *Agent {
	return newAgent(ctx, root, model, store, chunkd.RequestPlanner, chunkd.ResumePlanner, chunkd.FetchPlanner)
}

// newAgent is NewAgent over the given daemon calls; the poller starts here,
// so tests pass their fakes before it can call fetch.
func newAgent(
	ctx context.Context, root, model string, store *Store,
	request func(string, chunkd.PlannerRequest) (chunkd.SessionLog, error),
	resume func(string, chunkd.PlannerResume) (chunkd.SessionLog, error),
	fetch func(string, int) (chunkd.SessionLog, error),
) *Agent {
	ctx, stop := context.WithCancel(ctx)
	p := &Agent{
		ctx:     ctx,
		root:    root,
		model:   model,
		store:   store,
		events:  make(chan plan.Event),
		request: request,
		resume:  resume,
		fetch:   fetch,
		stop:    stop,
	}
	go p.poll()
	return p
}

// CheckStart has check run before each Request or Resume, refusing it when
// check fails, so a plan can be opened and read before what planning needs
// is in place. It must be called before the first Request or Resume.
func (p *Agent) CheckStart(check func(context.Context) error) { p.check = check }

// Request starts the planner on a new plan's request. The daemon refuses a
// second one once a session started on it.
func (p *Agent) Request(ctx context.Context, text string) error {
	id, err := p.ready(ctx)
	if err != nil {
		return err
	}
	_, err = p.request(id, chunkd.PlannerRequest{ProjectRoot: p.root, Text: text, Model: p.model})
	return daemonProblem(err)
}

// Resume has the planner claim the plan again when it holds no claim.
func (p *Agent) Resume(ctx context.Context) error {
	id, err := p.ready(ctx)
	if err != nil {
		return err
	}
	_, err = p.resume(id, chunkd.PlannerResume{ProjectRoot: p.root, Model: p.model})
	return daemonProblem(err)
}

// ready is the plan's ID once CheckStart's check passes.
func (p *Agent) ready(ctx context.Context) (string, error) {
	if p.check != nil {
		if err := p.check(ctx); err != nil {
			return "", err
		}
	}
	id := p.store.ID()
	if id == "" {
		return "", errNotCreated
	}
	return id, nil
}

// daemonProblem is a failed request or resume as the screen shows it.
func daemonProblem(err error) error {
	var refused *chunkd.SessionRefused
	switch {
	case errors.As(err, &refused):
		return errors.New(refused.Message)
	case errors.Is(err, chunkd.ErrDaemonUnavailable):
		return errors.New("the chunk daemon is not reachable")
	}
	return err
}

func (p *Agent) Events() <-chan plan.Event { return p.events }

// Stop ends polling; the planner runs on in the daemon.
func (p *Agent) Stop() error {
	p.stop()
	return nil
}

// poll reads the planner's log every pollInterval until ctx ends.
func (p *Agent) poll() {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	var c cursor
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
		}
		if id := p.store.ID(); id != "" {
			p.tick(id, &c)
		}
	}
}

// cursor is where the poller has read the planner's log to.
type cursor struct {
	set   bool   // whether a first fetch has set next and logID
	next  int    // the next seq to read
	logID string // the log next came from
	// warned is whether the screen has been told the daemon is unreachable
	// since the last fetch that succeeded.
	warned bool
}

// tick is one read of plan id's planner's log from c, passing what it holds
// on to the screen.
func (p *Agent) tick(id string, c *cursor) {
	if !c.set {
		l, ok := p.read(id, -1, c)
		if !ok {
			return
		}
		c.set, c.next, c.logID = true, l.Next, l.LogID
		if l.Busy {
			// A screen opened mid-turn shows the planner working; the screen
			// acts only on an activity that says something.
			activity := l.Activity
			if activity == "" {
				activity = "Working…"
			}
			p.emit(plan.Event{Activity: activity})
		}
		return
	}
	l, ok := p.read(id, c.next, c)
	if !ok {
		return
	}
	if l.LogID != c.logID {
		// The daemon restarted, or the planner was made again: the cursor
		// means nothing in this log, so it is read from the oldest event the
		// log keeps, at the next tick.
		c.next, c.logID = l.Oldest, l.LogID
		return
	}
	for _, ev := range l.Events {
		if !p.emit(event(ev)) {
			return
		}
	}
	c.next = l.Next
}

// read is plan id's planner's log after after, telling the screen once when
// the daemon cannot be read.
func (p *Agent) read(id string, after int, c *cursor) (chunkd.SessionLog, bool) {
	l, err := p.fetch(id, after)
	if err != nil {
		if !c.warned && p.ctx.Err() == nil {
			c.warned = true
			p.emit(plan.Event{Problem: errUnreachable})
		}
		return chunkd.SessionLog{}, false
	}
	c.warned = false
	return l, true
}

// emit passes ev on to the screen, reporting false once ctx ends and nothing
// reads it.
func (p *Agent) emit(ev plan.Event) bool {
	select {
	case p.events <- ev:
		return true
	case <-p.ctx.Done():
		return false
	}
}

// event is ev as the planning screen shows it.
func event(ev chunkd.SessionLogEvent) plan.Event {
	switch ev.Kind {
	case chunkd.SessionLogReply:
		return plan.Event{Reply: &plan.Reply{Text: ev.Text, ByTask: ev.ByTask}}
	case chunkd.SessionLogProblem:
		return plan.Event{Problem: errors.New(ev.Text)}
	case chunkd.SessionLogActivity:
	}
	// An activity, or a kind from a newer daemon, is shown as a line.
	return plan.Event{Activity: ev.Text}
}
