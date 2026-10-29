package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/planapprove"
)

// planApproveTimeout bounds approving and starting a build, which a client
// that goes away does not cut short.
var planApproveTimeout = 2 * time.Minute

// approvePlan approves factory plan planID in factory, stops its planner and
// starts its build, all on ctx, which the caller has detached from the
// request so a client that goes away does not leave the plan approved and
// unbuilt.
func (d *daemon) approvePlan(ctx context.Context, planID string, req chunkd.PlanApproveRequest) (chunkd.Session, error) {
	api := d.planAPI()
	if api == nil {
		return chunkd.Session{}, apiErr(http.StatusServiceUnavailable, "not authenticated to CircleCI — plan builds unavailable (run: chunk auth login)")
	}
	ps, err := d.planProject(req.ProjectRoot)
	if err != nil {
		return chunkd.Session{}, err
	}
	p, done, err := d.planners.beginApproval(planID, ps.root, api)
	if err != nil {
		return chunkd.Session{}, err
	}
	if p.markWatching() {
		// The mark is dropped with the entry once the plan's flow ends.
		go d.watchPlanner(p, planBuildPoll)
	}
	// Read under sendMu: a request or resume already under way finishes
	// first, so a session it starts is the one whose claim is released;
	// later ones see the mark.
	p.sendMu.Lock()
	session, err := p.sessionID()
	p.sendMu.Unlock()
	if err != nil {
		done(false)
		return chunkd.Session{}, apiErr(http.StatusInternalServerError, "read the plan's planning session: %v", err)
	}
	wt, err := factory.PlanWorktreePath(ps.root, planID)
	if err != nil {
		done(false)
		return chunkd.Session{}, apiErr(http.StatusInternalServerError, "find the plan's worktree: %v", err)
	}
	if err := planapprove.Approve(ctx, api, d.sessionWorktree(), planID, session, wt); err != nil {
		done(false) // clears the mark; the planner takes resumes again
		return chunkd.Session{}, approveProblem(err)
	}
	done(true) // marked approved until the plan's flow ends
	// The claim is released, so the session is stopped without the request
	// waiting for it.
	go p.stopSession()
	sess, err := d.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: ps.root, PlanID: planID, Model: req.Model})
	if err != nil {
		return chunkd.Session{}, apiErr(http.StatusBadGateway,
			"the plan is approved in factory, but chunk could not start building it (%s); build it in Claude Code with /f:resume %s", apiMessage(err), planID)
	}
	return sess, nil
}

// sessionWorktree is where factory's daemon has a session working.
func (d *daemon) sessionWorktree() planapprove.Locate {
	if d.rcfg.SessionWorktree != nil {
		return d.rcfg.SessionWorktree
	}
	return factory.SessionWorktree
}

// beginApproval marks plan planID's planner as being approved, making a bare
// entry (no runner) to hold the mark when the plan has no planner. It
// refuses a plan already being approved, or approved. done settles the
// mark: true marks the plan approved; false clears it, dropping a bare
// entry, so a refused approval leaves nothing behind.
func (s *plannerSet) beginApproval(planID, root string, api PlanAPI) (*planner, func(ok bool), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.m[planID]
	if p == nil {
		ctx, cancel := context.WithCancel(s.ctx)
		p = &planner{planID: planID, root: root, api: api, ctx: ctx, cancel: cancel}
		s.m[planID] = p
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.approval != "" {
		return nil, nil, apiErr(http.StatusConflict, "the plan is being approved")
	}
	p.approval = approving
	done := func(ok bool) {
		if ok {
			p.mu.Lock()
			p.approval = approved
			p.mu.Unlock()
			return
		}
		s.mu.Lock()
		p.mu.Lock()
		p.approval = ""
		bare := p.run == nil && s.m[planID] == p
		if bare {
			delete(s.m, planID)
		}
		p.mu.Unlock()
		s.mu.Unlock()
		if bare {
			p.cancel()
		}
	}
	return p, done, nil
}

// markWatching records that p's watch goroutine is starting, reporting
// whether it had not yet.
func (p *planner) markWatching() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	start := !p.watching
	p.watching = true
	return start
}

// sessionID is the session planning p's plan: the runner's once it has
// started one, else the one recorded on disk (the daemon restarted), ""
// when none is.
func (p *planner) sessionID() (string, error) {
	if p.run != nil {
		if a, _ := p.run.live(); a != nil {
			return p.run.sessionID(), nil
		}
	}
	return factory.RecordedPlanSession(p.root, p.planID)
}

// stopSession stops p's session and closes its record, keeping p, which
// carries the approved mark until the plan's flow ends.
func (p *planner) stopSession() {
	if p.run != nil {
		p.run.stop()
	}
}

// approveProblem is a failed planapprove.Approve as the route answers it:
// a refusal is 409, a factory failure 502.
func approveProblem(err error) error {
	for _, s := range []error{planapprove.ErrNotReady, planapprove.ErrNoUnits, planapprove.ErrQuestionsOpen, planapprove.ErrBeingBuilt} {
		if errors.Is(err, s) {
			return apiErr(http.StatusConflict, "%s", err.Error())
		}
	}
	return apiErr(http.StatusBadGateway, "%s", err.Error())
}

// apiMessage is what a route would answer err with.
func apiMessage(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.msg
	}
	return err.Error()
}
