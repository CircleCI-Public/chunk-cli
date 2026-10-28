package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// planBuildPoll is how often a plan build reads its plan and flow from
// factory, to show where factory has it and to stop once it has ended.
var planBuildPoll = 30 * time.Second

// PlanAPI is what a plan build reads of a factory plan and its flow:
// *circleci.Client.
type PlanAPI interface {
	GetPlan(ctx context.Context, id string) (*circleci.Plan, error)
	GetFlow(ctx context.Context, planID string) (*circleci.Flow, error)
	PlanText(ctx context.Context, id string) (string, error)
}

// BuildAgent is what a plan build needs of the build session:
// *factory.Agent.
type BuildAgent interface {
	Events() <-chan factory.AgentEvent
	Stop() error
}

// planAPI is the factory API plan builds read plans through, or nil when
// the daemon has no CircleCI client.
func (d *daemon) planAPI() PlanAPI {
	if d.rcfg.PlanAPI != nil {
		return d.rcfg.PlanAPI
	}
	if d.client == nil {
		return nil
	}
	return d.client
}

// startPlanBuild records a plan build and starts it in the background,
// returning as soon as it is accepted. The plan must be in factory, and not
// over, and not already being built here.
func (d *daemon) startPlanBuild(req chunkd.PlanBuildRequest) (chunkd.Session, error) {
	api := d.planAPI()
	if api == nil {
		return chunkd.Session{}, apiErr(http.StatusServiceUnavailable, "not authenticated to CircleCI — plan builds unavailable (run: chunk auth login)")
	}
	ps := d.lookupProject(req.ProjectRoot)
	if ps == nil {
		ps = d.adoptProject(req.ProjectRoot)
	}
	if ps == nil {
		return chunkd.Session{}, apiErr(http.StatusNotFound, "the chunk daemon is not tracking %q, and it is not the top of a git repository", req.ProjectRoot)
	}
	// Bounded: the request waits on it, and the build has not started.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := api.GetPlan(ctx, req.PlanID)
	if err != nil {
		return chunkd.Session{}, apiErr(http.StatusBadGateway, "read plan %s from factory: %v", req.PlanID, err)
	}
	if p.Phase == circleci.PhaseEnded {
		return chunkd.Session{}, apiErr(http.StatusConflict, "plan %s has ended (%s); there is nothing to build", req.PlanID, planEnd(p))
	}
	state, err := flowState(ctx, api, req.PlanID)
	if err != nil {
		return chunkd.Session{}, apiErr(http.StatusBadGateway, "read plan %s's flow from factory: %v", req.PlanID, err)
	}
	text, err := api.PlanText(ctx, req.PlanID)
	if err != nil {
		return chunkd.Session{}, apiErr(http.StatusBadGateway, "read plan %s from factory: %v", req.PlanID, err)
	}
	if id := d.buildingPlan(req.PlanID); id != "" {
		return chunkd.Session{}, apiErr(http.StatusConflict, "plan %s is already being built, in session %s", req.PlanID, id)
	}

	entry, sctx, _ := d.sessions.add(chunkd.Session{
		ProjectRoot: ps.root,
		Branch:      p.Branch,
		HeadSHA:     headRef(ps.root),
		Factory: &chunkd.FactoryRun{
			Prompt:     text,
			SpecOrigin: "factory plan " + req.PlanID,
			PlanID:     req.PlanID,
			PlanState:  state,
			Branch:     p.Branch,
		},
	})
	go d.executePlanBuild(sctx, entry, api, ps.root, req)
	return entry.snapshot(), nil
}

// buildingPlan is the live session building factory plan planID, or "" when
// none is.
func (d *daemon) buildingPlan(planID string) string {
	for _, e := range d.sessions.entries() {
		s := e.snapshot()
		if s.State == chunkd.SessionRunning && s.Factory != nil && s.Factory.PlanID == planID {
			return s.ID
		}
	}
	return ""
}

// planEnd says how ended plan p ended, even from a factory that serves no
// outcome.
func planEnd(p *circleci.Plan) string {
	if p.Outcome != "" {
		return p.Outcome
	}
	return "ended"
}

// flowState is plan id's flow state, "" when it has no flow (a 404), as once
// the plan has ended.
func flowState(ctx context.Context, api PlanAPI, id string) (string, error) {
	f, err := api.GetFlow(ctx, id)
	var se *circleci.StatusError
	if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return f.State, nil
}

// executePlanBuild runs a plan build and settles it, once its plan ends, its
// session does, or it is cancelled.
func (d *daemon) executePlanBuild(ctx context.Context, entry *sessionEntry, api PlanAPI, root string, req chunkd.PlanBuildRequest) {
	defer close(entry.done)
	defer entry.cancel()
	note := func(level iostream.Level, msg string) {
		log.Printf("chunkd: plan build %s (%s): %s", req.PlanID, root, msg)
		entry.mu.Lock()
		defer entry.mu.Unlock()
		addFeedLine(&entry.s.Factory.Progress, level, msg)
	}

	open := d.rcfg.OpenPlanWorktree
	if open == nil {
		open = factory.OpenPlanWorktree
	}
	dir, err := open(ctx, root, req.PlanID)
	if err != nil {
		d.settleSession(entry, fmt.Errorf("open the plan's worktree: %w", err))
		return
	}
	entry.mu.Lock()
	entry.s.Factory.Worktree = dir
	entry.mu.Unlock()

	start := d.rcfg.StartBuild
	if start == nil {
		start = func(ctx context.Context, cfg factory.AgentConfig, message string) (BuildAgent, error) {
			return factory.StartAgent(ctx, cfg, message)
		}
	}
	cfg := factory.AgentConfig{Dir: dir, Model: req.Model, SessionID: uuid.NewString(), Build: true}
	agent, err := start(ctx, cfg, factory.BuildMessage(req.PlanID))
	if err != nil {
		d.settleSession(entry, buildStartProblem(err))
		return
	}
	note(iostream.LevelStep, "Building plan "+req.PlanID+" in "+dir)

	// ended carries why the session ended, nil once it was stopped.
	ended := make(chan error, 1)
	go func() {
		var problem error
		for ev := range agent.Events() {
			switch {
			case ev.Activity != nil:
				note(iostream.LevelInfo, ev.Activity.Line())
			case ev.TurnEnded != nil:
				if line := firstLine(ev.TurnEnded.Reply); line != "" {
					note(iostream.LevelStep, clip(line, maxSaidText))
				}
			case ev.Problem != nil:
				problem = ev.Problem
			}
		}
		ended <- problem
	}()

	tick := time.NewTicker(planBuildPoll)
	defer tick.Stop()
	for {
		select {
		case err := <-ended:
			if err == nil {
				err = errors.New("the build session ended before the plan did")
			}
			d.settleSession(entry, err)
			return
		case <-ctx.Done():
			stopBuild(agent, ended, req.PlanID)
			d.settleSession(entry, nil)
			return
		case <-tick.C:
			// A failed read is tried again at the next tick: the build goes
			// on without it.
			p, err := api.GetPlan(ctx, req.PlanID)
			if err != nil {
				continue
			}
			entry.mu.Lock()
			if p.Branch != "" {
				entry.s.Factory.Branch, entry.s.Branch = p.Branch, p.Branch
			}
			entry.mu.Unlock()
			if p.Phase == circleci.PhaseEnded {
				end := planEnd(p)
				note(iostream.LevelDone, "Plan "+req.PlanID+" ended: "+end)
				stopBuild(agent, ended, req.PlanID)
				entry.mu.Lock()
				entry.loopNote = "plan " + end
				entry.mu.Unlock()
				d.settleSession(entry, nil)
				return
			}
			// The flow is only shown: a failed read keeps the last state.
			state, err := flowState(ctx, api, req.PlanID)
			if err != nil {
				continue
			}
			entry.mu.Lock()
			entry.s.Factory.PlanState = state
			entry.mu.Unlock()
		}
	}
}

// stopBuild stops a plan's build session and waits for its events to end,
// logging what it reported on the way out: the build is being stopped, so
// there is no one else to tell.
func stopBuild(agent BuildAgent, ended <-chan error, planID string) {
	if err := agent.Stop(); err != nil {
		log.Printf("chunkd: plan build %s: stop the build session: %v", planID, err)
	}
	if err := <-ended; err != nil {
		log.Printf("chunkd: plan build %s: %v", planID, err)
	}
}

// buildStartProblem is a failure to start the build session, saying how to
// fix it.
func buildStartProblem(err error) error {
	switch {
	case errors.Is(err, factory.ErrNoClaude):
		return errors.New("the chunk daemon found no Claude Code to build with; install it from https://claude.com/claude-code")
	case errors.Is(err, factory.ErrNoFactory):
		return errors.New("the chunk daemon found no factory; install it and put it on PATH, then restart the daemon")
	case errors.Is(err, factory.ErrNoFactoryPlugin):
		return errors.New("factory's f plugin is not loaded in Claude Code; install it")
	case errors.Is(err, factory.ErrFactoryMCP):
		return errors.New("factory's MCP server is not connected; make sure factory is on PATH")
	}
	return fmt.Errorf("start the build session: %w", err)
}
