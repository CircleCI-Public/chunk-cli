package server

import (
	"context"
	"net/http"
	"slices"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/planapprove"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
)

// takeOver releases holder's claim on p's plan, when it is still the
// developer's to release, so the planner can claim the plan itself. The
// caller holds p.sendMu. released is whether a claim was released; a nil
// error with released false means the claim was already gone.
func (d *daemon) takeOver(p *planner, holder string) (released bool, err error) {
	ctx, cancel := context.WithTimeout(p.ctx, plannerReadTimeout)
	defer cancel()
	f, err := planFlow(ctx, p.api, p.planID)
	if err != nil {
		return false, apiErr(http.StatusBadGateway, "read the plan's flow: %v", err)
	}
	if f == nil || f.Phase == circleci.PhaseEnded {
		return false, apiErr(http.StatusConflict, "the plan has ended; nothing to release")
	}
	planning := []string{planstate.Init, planstate.Ready, planstate.RefineProposal,
		planstate.RefineUnits, planstate.RecordSubPlans, planstate.StandBy}
	if !slices.Contains(planning, f.State) {
		return false, apiErr(http.StatusConflict, "the plan moved on to %s; nothing was released", f.State)
	}
	if f.SessionID == "" {
		return false, nil
	}
	if f.SessionID != holder {
		return false, apiErr(http.StatusConflict, "the claim moved to session %s; press R again", shortSession(f.SessionID))
	}
	own, err := p.sessionID()
	if err != nil {
		return false, apiErr(http.StatusInternalServerError, "read the plan's planning session: %v", err)
	}
	if own == holder {
		return false, apiErr(http.StatusConflict, "this plan is held by chunk's own planner")
	}
	wt, err := factory.PlanWorktreePath(p.root, p.planID)
	if err != nil {
		return false, apiErr(http.StatusInternalServerError, "find the plan's worktree: %v", err)
	}
	if err := planapprove.ReleaseForeign(ctx, p.api, d.sessionWorktree(), p.planID, holder, wt); err != nil {
		return false, approveProblem(err)
	}
	// A live planner that believed it held the claim must now send the
	// retry message.
	p.mu.Lock()
	p.claimed, p.checkClaim = false, false
	p.mu.Unlock()
	return true, nil
}

// shortSessionLen is how many characters of a session ID the screens show.
const shortSessionLen = 8

// shortSession is a session ID as the screens show it: its first
// shortSessionLen characters.
func shortSession(id string) string {
	if r := []rune(id); len(r) > shortSessionLen {
		return string(r[:shortSessionLen])
	}
	return id
}
