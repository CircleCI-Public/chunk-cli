package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// plannerReadTimeout bounds each read of a plan's claim or flow a planner
// makes: a request waits on them.
const plannerReadTimeout = 30 * time.Second

// Approval marks on a planner (Unit 3 sets them).
const (
	approving = "approving"
	approved  = "approved"
)

// errRequested refuses a second request to a plan's planner.
var errRequested = errors.New("the plan's request has already been sent to its planning session")

// planner is the planning session of one factory plan, run by the daemon so
// it outlives the screens that show it.
type planner struct {
	planID, root string
	api          PlanAPI
	ctx          context.Context // the planner's lifetime, ended by cancel
	run          *runner         // nil for a bare entry that only holds an approval mark (Unit 3)
	// sendMu is held through each request or resume, from the claim decision
	// to the send, so two at once decide one after the other.
	sendMu sync.Mutex

	mu sync.Mutex
	// All below are guarded by mu.
	requested bool // a session started on a request, or the record held one
	// claimed is whether a message has told the session to claim the plan
	// since it last held no claim; checkClaim, that the reply to it is owed
	// a check.
	claimed, checkClaim bool
	// approval is "", approving or approved: see approvePlan (Unit 3).
	approval string
	watching bool               // the watch goroutine has started
	cancel   context.CancelFunc // ends the watch goroutine
}

type plannerSet struct {
	ctx context.Context
	mu  sync.Mutex
	m   map[string]*planner
}

func newPlannerSet(ctx context.Context) *plannerSet {
	return &plannerSet{ctx: ctx, m: make(map[string]*planner)}
}

// lookup is plan planID's planner, nil when it has none.
func (s *plannerSet) lookup(planID string) *planner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[planID]
}

// get is plan planID's planner, made with a runner by newRun when it has
// none.
func (s *plannerSet) get(planID, root string, api PlanAPI, newRun func(ctx context.Context) *runner) *planner {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.m[planID]; p != nil {
		return p
	}
	ctx, cancel := context.WithCancel(s.ctx)
	p := &planner{planID: planID, root: root, api: api, ctx: ctx, cancel: cancel}
	p.run = newRun(ctx)
	p.run.onReply = p.onReply
	s.m[planID] = p
	return p
}

// drop forgets p, if it is still its plan's planner, and ends its watch.
func (s *plannerSet) drop(p *planner) {
	s.mu.Lock()
	if s.m[p.planID] == p {
		delete(s.m, p.planID)
	}
	s.mu.Unlock()
	p.cancel()
}

// stopAll stops every planner, as the daemon stops.
func (s *plannerSet) stopAll() {
	s.mu.Lock()
	all := make([]*planner, 0, len(s.m))
	for id, p := range s.m {
		all = append(all, p)
		delete(s.m, id)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range all {
		wg.Go(func() {
			if p.run != nil {
				p.run.stop()
			}
			p.cancel()
		})
	}
	wg.Wait()
}

// requestPlanner starts plan planID's planner on the plan's request.
func (d *daemon) requestPlanner(planID string, req chunkd.PlannerRequest) (chunkd.SessionLog, error) {
	return d.plannerSend(planID, req.ProjectRoot, req.Model, req.Text)
}

// resumePlanner has plan planID's planner claim the plan again when it holds
// no claim.
func (d *daemon) resumePlanner(planID string, req chunkd.PlannerResume) (chunkd.SessionLog, error) {
	return d.plannerSend(planID, req.ProjectRoot, req.Model, "")
}

// plannerLog is plan planID's planner's log from cursor after.
func (d *daemon) plannerLog(planID string, after int) chunkd.SessionLog {
	p := d.planners.lookup(planID)
	if p == nil || p.run == nil {
		return chunkd.SessionLog{Events: []chunkd.SessionLogEvent{}}
	}
	return p.run.view(after)
}

// plannerSend sends plan planID's planner a request's text, or for a resume
// ("" text) the claim again when it holds none, starting or resuming its
// session as needed. What is sent tells the session to claim the plan when
// it is the first since the plan was created, or the session has since lost
// its claim: factory frees the claims of a session that ends, and a session
// can release its plan, after which the factory daemon hands it no tasks.
func (d *daemon) plannerSend(planID, root, model, text string) (chunkd.SessionLog, error) {
	api := d.planAPI()
	if api == nil {
		return chunkd.SessionLog{}, apiErr(http.StatusServiceUnavailable, "not authenticated to CircleCI — plan builds unavailable (run: chunk auth login)")
	}
	ps, err := d.planProject(root)
	if err != nil {
		return chunkd.SessionLog{}, err
	}
	p := d.planners.get(planID, ps.root, api, func(ctx context.Context) *runner {
		start := d.rcfg.StartPlanner
		if start == nil {
			start = func(ctx context.Context, cfg factory.AgentConfig, message string) (AgentSession, error) {
				return factory.StartAgent(ctx, cfg, message)
			}
		}
		return newRunner(ctx, factory.AgentConfig{Model: model}, start)
	})

	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	p.mu.Lock()
	approval, requested := p.approval, p.requested
	p.mu.Unlock()
	if err := approvalRefusal(approval); err != nil {
		return chunkd.SessionLog{}, err
	}
	if p.run == nil {
		return chunkd.SessionLog{}, apiErr(http.StatusConflict, "the plan has no planner")
	}
	if text != "" && requested {
		return chunkd.SessionLog{}, apiErr(http.StatusConflict, "%s", errRequested.Error())
	}
	run := p.run

	a, live := run.live()
	if !live {
		if err := d.plannerCanStart(p, text != ""); err != nil {
			return chunkd.SessionLog{}, err
		}
	}

	p.mu.Lock()
	if a != nil && !live {
		// factory frees the ended session's claim on its own time, so the
		// plan may still read as claimed; the resumed session claims it
		// again either way.
		p.claimed = false
	}
	recheck := live && p.claimed && !p.checkClaim
	p.mu.Unlock()
	if recheck && !p.holds() {
		p.mu.Lock()
		p.claimed = false
		p.mu.Unlock()
	}

	p.mu.Lock()
	message, claimed := planMessage(p.claimed, planID, text)
	if message == "" {
		p.mu.Unlock()
		return run.view(-1), nil
	}
	// Marked before the send, so the reply to it, however soon it comes, is
	// checked; put back when the send fails.
	wasClaimed, wasCheck := p.claimed, p.checkClaim
	p.claim(claimed)
	p.mu.Unlock()

	started, err := run.send(message, func() (string, error) { return d.plannerWorktree(p) })
	if err != nil {
		p.mu.Lock()
		p.claimed, p.checkClaim = wasClaimed, wasCheck
		p.mu.Unlock()
		return chunkd.SessionLog{}, plannerStartProblem(err)
	}
	p.mu.Lock()
	if text != "" {
		p.requested = true
	}
	watch := started && !p.watching
	p.watching = p.watching || started
	p.mu.Unlock()
	if watch {
		go d.watchPlanner(p, planBuildPoll)
	}
	return run.view(-1), nil
}

// approvalRefusal is why a planner marked approval takes no request or
// resume, nil when it is not marked.
func approvalRefusal(approval string) error {
	switch approval {
	case approving:
		return apiErr(http.StatusConflict, "the plan is being approved")
	case approved:
		return apiErr(http.StatusConflict, "the plan is approved; it is being built")
	}
	return nil
}

// plannerCanStart checks what must hold before p's session starts or
// resumes, opening its session record the first time. request is whether a
// request is starting it.
func (d *daemon) plannerCanStart(p *planner, request bool) error {
	check := d.rcfg.CheckPlanner
	if check == nil {
		check = func(ctx context.Context) error {
			if err := factory.CheckClaude(); err != nil {
				return err
			}
			return factory.CheckFactoryDaemon(ctx)
		}
	}
	ctx, cancel := context.WithTimeout(p.ctx, plannerReadTimeout)
	defer cancel()
	if err := check(ctx); err != nil {
		return apiErr(http.StatusServiceUnavailable, "%s", sessionStartProblem(err, "plan").Error())
	}

	// The record comes before the claim check: the session it holds is the
	// planner's own, and may hold the claim still.
	if !p.run.opened() {
		openRecord := d.rcfg.OpenPlanSession
		if openRecord == nil {
			openRecord = func(root, planID string) (SessionRecord, error) {
				return factory.OpenPlanSession(root, planID)
			}
		}
		rec, err := openRecord(p.root, p.planID)
		if errors.Is(err, factory.ErrPlanSessionOpen) {
			return apiErr(http.StatusConflict, "this plan is open for planning in an older chunk window; close it, then try again")
		}
		if err != nil {
			return fmt.Errorf("open the plan's session record: %w", err)
		}
		resumable, err := p.run.open(rec)
		if err != nil {
			return fmt.Errorf("read the plan's session record: %w", err)
		}
		if resumable {
			// The record is only written once a session started, which a new
			// plan's first call is always a request for.
			p.mu.Lock()
			p.requested = true
			p.mu.Unlock()
			if request {
				return apiErr(http.StatusConflict, "%s", errRequested.Error())
			}
		}
	}

	if h, err := p.holder(); err == nil && h != "" && h != p.run.sessionID() {
		return apiErr(http.StatusConflict, "this plan is claimed by another factory session; you can read and comment without taking its claim")
	}
	return nil
}

// plannerStartProblem is a failure to send to or start a planner's session
// as the route answers it.
func plannerStartProblem(err error) error {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		return err
	case errors.Is(err, errRunnerStopped):
		return apiErr(http.StatusConflict, "the plan's planning session has been stopped")
	case isStartSentinel(err):
		return apiErr(http.StatusServiceUnavailable, "%s", sessionStartProblem(err, "plan").Error())
	}
	return apiErr(http.StatusBadGateway, "%s", sessionStartProblem(err, "plan").Error())
}

// plannerWorktree is the worktree p's sessions start in, added when it has
// none. The worktrees of plans that have ended are removed first; one that
// could not be is left for the next plan to remove.
func (d *daemon) plannerWorktree(p *planner) (string, error) {
	_ = factory.PrunePlanWorktrees(p.ctx, p.root, func(ctx context.Context, planID string) (bool, error) {
		if planID == p.planID {
			return false, nil
		}
		ctx, cancel := context.WithTimeout(ctx, plannerReadTimeout)
		defer cancel()
		return planEnded(ctx, p.api, planID)
	})
	open := d.rcfg.OpenPlanWorktree
	if open == nil {
		open = factory.OpenPlanWorktree
	}
	dir, err := open(p.ctx, p.root, p.planID)
	if err != nil {
		return "", apiErr(http.StatusInternalServerError, "open the plan's worktree: %v", err)
	}
	return dir, nil
}

// planEnded is whether factory plan planID is over, so its worktree can go.
// A plan with no flow is over only once it is gone: one that is still there
// is a live plan made before plans had flows.
func planEnded(ctx context.Context, api PlanAPI, planID string) (bool, error) {
	f, err := planFlow(ctx, api, planID)
	if err != nil {
		return false, fmt.Errorf("get the state of plan %s: %w", planID, err)
	}
	if f != nil {
		return f.Phase == circleci.PhaseEnded, nil
	}
	_, err = api.GetPlan(ctx, planID)
	var se *circleci.StatusError
	if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get plan %s: %w", planID, err)
	}
	return false, nil
}

// planMessage is the message the planner is next sent, or "" when nothing
// is owed, and whether the plan is then claimed. claimed is carried by the
// caller across calls.
func planMessage(claimed bool, planID, text string) (string, bool) {
	switch {
	case claimed || planID == "":
		return text, claimed
	case text == "":
		return factory.PlanRetryMessage(planID), true
	}
	return factory.PlanMessage(planID, text), true
}

// holder is the session holding p's plan's claim in factory, "" when none
// does.
func (p *planner) holder() (string, error) {
	ctx, cancel := context.WithTimeout(p.ctx, plannerReadTimeout)
	defer cancel()
	f, err := planFlow(ctx, p.api, p.planID)
	if err != nil || f == nil {
		return "", err
	}
	return f.SessionID, nil
}

// holds is whether p's session holds the plan's claim in factory. A failed
// read counts as holding it, so a blip in the API does not have the planner
// claim a plan it has.
func (p *planner) holds() bool {
	h, err := p.holder()
	return err != nil || h == p.run.sessionID()
}

// claim records whether the message about to be sent leaves the plan
// claimed, as planMessage reported it; a message that first tells the
// session to claim it is owed a check once the session replies. The caller
// must hold mu.
func (p *planner) claim(claimed bool) {
	if claimed && !p.claimed {
		p.claimed, p.checkClaim = true, true
	}
}

// onReply checks, after the reply to a message telling the session to claim
// the plan, that it holds the claim, noting a problem when it does not.
func (p *planner) onReply(byTask bool) {
	if byTask {
		return
	}
	p.mu.Lock()
	owed := p.checkClaim
	p.checkClaim = false
	p.mu.Unlock()
	if !owed {
		return
	}
	if err := p.checkClaimed(); err != nil {
		p.run.note(chunkd.SessionLogProblem, err.Error())
	}
}

// checkClaimed reports why the session holds no claim on the plan after its
// reply to the message telling it to claim it. Without the claim the factory
// daemon hands it no planning tasks, and the plan never moves.
func (p *planner) checkClaimed() error {
	h, err := p.holder()
	switch {
	case err != nil:
		return fmt.Errorf("could not check chunk's claim on the plan: %w", err)
	case h == p.run.sessionID():
		return nil
	case h != "":
		return fmt.Errorf("plan %s is claimed by another factory session, so no planning tasks will come to chunk", p.planID)
	}
	return fmt.Errorf("chunk could not claim plan %s in factory, so no planning tasks will come. "+
		"Run the factory daemon with FACTORY_PLAN_STORE=circleci and FACTORY_PLAN_CIRCLECI_TOKEN "+
		"set to a token for the CircleCI user chunk is signed in as", p.planID)
}

// watchPlanner reads p's plan's flow every poll (planBuildPoll), and stops
// and drops p once the plan has ended. It ends with p.
func (d *daemon) watchPlanner(p *planner, poll time.Duration) {
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-tick.C:
			ctx, cancel := context.WithTimeout(p.ctx, plannerReadTimeout)
			f, err := planFlow(ctx, p.api, p.planID)
			cancel()
			// A failed read is tried again at the next tick.
			if err != nil || f == nil || f.Phase != circleci.PhaseEnded {
				continue
			}
			if p.run != nil {
				p.run.stop()
			}
			d.planners.drop(p)
			return
		}
	}
}
