package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
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
	Send(message string) error
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
	f, err := planFlow(ctx, api, req.PlanID)
	if err != nil {
		return chunkd.Session{}, apiErr(http.StatusBadGateway, "read plan %s's flow from factory: %v", req.PlanID, err)
	}
	if f != nil && f.Phase == circleci.PhaseEnded {
		return chunkd.Session{}, apiErr(http.StatusConflict, "plan %s has ended (%s); there is nothing to build", req.PlanID, planEnd(f))
	}
	var state string
	if f != nil {
		state = f.State
	}
	text, err := api.PlanText(ctx, req.PlanID)
	if err != nil {
		return chunkd.Session{}, apiErr(http.StatusBadGateway, "read plan %s from factory: %v", req.PlanID, err)
	}
	if id := d.buildingPlan(req.PlanID); id != "" {
		return chunkd.Session{}, apiErr(http.StatusConflict, "plan %s is already being built, in session %s", req.PlanID, id)
	}

	entry, sctx := d.sessions.add(chunkd.Session{
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

// latestPlanBuild is plan planID's build session: the running one, else the
// one started last; nil when the plan has none.
func (d *daemon) latestPlanBuild(planID string) *sessionEntry {
	var best *sessionEntry
	var bestS chunkd.Session
	for _, e := range d.sessions.entries() {
		s := e.snapshot()
		if s.Factory == nil || s.Factory.PlanID != planID {
			continue
		}
		running := s.State == chunkd.SessionRunning
		bestRunning := bestS.State == chunkd.SessionRunning
		if best == nil || running && !bestRunning ||
			running == bestRunning && s.StartedAt.After(bestS.StartedAt) {
			best, bestS = e, s
		}
	}
	return best
}

// planBuildActivity is s's feed lines after the first after, numbered as the
// session wrote them. An after the session never reached (one an earlier
// session's client saw) or below 0 reads from the start.
func planBuildActivity(s chunkd.Session, after int) chunkd.PlanBuildActivity {
	feed := s.Factory.Progress
	kept := feed.Total - len(feed.Lines)
	if after < 0 || after > feed.Total {
		after = 0
	}
	from := max(after, kept)
	return chunkd.PlanBuildActivity{
		ID: s.ID, State: s.State, Error: s.Error,
		First: from, Lines: feed.Lines[from-kept:], Total: feed.Total,
	}
}

// planEnd says how the plan of ended flow f ended, even from a factory that
// serves no outcome.
func planEnd(f *circleci.Flow) string {
	if f.Outcome != "" {
		return f.Outcome
	}
	return "ended"
}

// planFlow is plan id's flow, nil when it has no flow (a 404), as a plan made
// before plans had flows has none.
func planFlow(ctx context.Context, api PlanAPI, id string) (*circleci.Flow, error) {
	f, err := api.GetFlow(ctx, id)
	var se *circleci.StatusError
	if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return f, nil
}

// executePlanBuild runs a plan build and settles it, once its plan ends, its
// session does, or it is cancelled.
//
// It tells the build session to claim the plan again only once a claim it saw
// held by another session is released, so a claim that failed for some other
// reason is not retried every tick.
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
	// idle is whether the session's last event ended its turn, with nothing
	// sent to it since: only an idle session is nudged.
	var idle atomic.Bool
	go func() {
		var problem error
		for ev := range agent.Events() {
			switch {
			case ev.Activity != nil:
				idle.Store(false)
				note(iostream.LevelInfo, ev.Activity.Line())
			case ev.TurnEnded != nil:
				idle.Store(true)
				if line := firstLine(ev.TurnEnded.Reply); line != "" {
					note(iostream.LevelStep, clip(line, maxSaidText))
				}
			case ev.Problem != nil:
				problem = ev.Problem
			}
		}
		ended <- problem
	}()

	// armed is whether a tick has seen another session hold the plan since the
	// last nudge; noted is the holder the feed last named.
	var armed bool
	var noted string
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
			// A failed read, or no flow, changes nothing: the last state is
			// kept, and an armed nudge stays armed.
			f, err := planFlow(ctx, api, req.PlanID)
			if err != nil || f == nil {
				continue
			}
			if f.Phase == circleci.PhaseEnded {
				end := planEnd(f)
				note(iostream.LevelDone, "Plan "+req.PlanID+" ended: "+end)
				stopBuild(agent, ended, req.PlanID)
				entry.mu.Lock()
				entry.loopNote = "plan " + end
				entry.mu.Unlock()
				d.settleSession(entry, nil)
				return
			}
			entry.mu.Lock()
			entry.s.Factory.PlanState = f.State
			entry.mu.Unlock()
			switch h := f.SessionID; {
			case h == cfg.SessionID:
				armed, noted = false, "" // it claimed the plan itself: a later release is its own
			case h != "":
				armed = true
				if idle.Load() && h != noted {
					noted = h
					note(iostream.LevelWarn, fmt.Sprintf("Plan %s is claimed by session %s; release it with factory daemon release %[1]s and the build picks it up", req.PlanID, h))
				}
			case armed && idle.Load():
				armed, noted = false, ""
				idle.Store(false)
				note(iostream.LevelStep, "Plan "+req.PlanID+"'s claim was released; telling the build session to claim it")
				if err := agent.Send(factory.BuildRetryMessage(req.PlanID)); err != nil {
					note(iostream.LevelWarn, "Could not tell the build session to claim the plan: "+err.Error())
				}
			}
		}
	}
}

// maxSaidText caps what the feed holds of one of the build session's replies:
// a reply can be a paragraph.
const maxSaidText = 160

// firstLine is the first line of s that is not blank.
func firstLine(s string) string {
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// clip shortens s to n runes, marking where it was cut.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
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
