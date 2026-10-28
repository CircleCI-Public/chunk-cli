package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// fakePlans is factory's plan API with one plan in it and its flow, whose
// state a test moves as factory would.
type fakePlans struct {
	mu             sync.Mutex
	id, state      string
	phase, outcome string
	branch, text   string
	getErr         error
	noFlow         bool
	flowErr        error
}

func (f *fakePlans) GetPlan(_ context.Context, id string) (*circleci.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	if id != f.id {
		return nil, errors.New("plan not found")
	}
	return &circleci.Plan{ID: id, Branch: f.branch, Phase: f.phase, Outcome: f.outcome}, nil
}

func (f *fakePlans) GetFlow(_ context.Context, id string) (*circleci.Flow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.flowErr != nil {
		return nil, f.flowErr
	}
	if id != f.id || f.noFlow {
		return nil, &circleci.StatusError{StatusCode: http.StatusNotFound}
	}
	return &circleci.Flow{ID: id, State: f.state}, nil
}

func (f *fakePlans) PlanText(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.text, nil
}

func (f *fakePlans) set(state, branch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state, f.branch = state, branch
}

// end ends the plan with outcome, as factory does: its phase is ended, and
// its flow is gone.
func (f *fakePlans) end(outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phase, f.outcome, f.noFlow = circleci.PhaseEnded, outcome, true
}

// fakeBuild is a build session whose events a test sends.
type fakeBuild struct {
	events  chan factory.AgentEvent
	once    sync.Once
	mu      sync.Mutex
	stopped bool
}

func newFakeBuild() *fakeBuild { return &fakeBuild{events: make(chan factory.AgentEvent, 8)} }

func (b *fakeBuild) Events() <-chan factory.AgentEvent { return b.events }

func (b *fakeBuild) Stop() error {
	b.mu.Lock()
	b.stopped = true
	b.mu.Unlock()
	b.end()
	return nil
}

// end closes the events, as claude exiting would.
func (b *fakeBuild) end() { b.once.Do(func() { close(b.events) }) }

func (b *fakeBuild) wasStopped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopped
}

// planBuildDaemon is a daemon whose plan builds read plans from plans and
// start build sessions as build, recording the config and first message of
// each.
type planBuildDaemon struct {
	*daemon
	root    string
	plans   *fakePlans
	build   *fakeBuild
	mu      sync.Mutex
	cfgs    []factory.AgentConfig
	firsts  []string
	opened  []string
	openErr error
}

func newPlanBuildDaemon(t *testing.T) *planBuildDaemon {
	t.Helper()
	d, root := newSessionDaemon(t, &fakeBackend{})
	old := planBuildPoll
	planBuildPoll = 10 * time.Millisecond
	t.Cleanup(func() { planBuildPoll = old })
	pb := &planBuildDaemon{
		daemon: d, root: root, build: newFakeBuild(),
		plans: &fakePlans{id: "plan-1", state: "Planning - Human", phase: circleci.PhaseStarted, text: "# Add a flag\n\n## 5. Units\n\n### Unit 1: Add it\n"},
	}
	d.rcfg.PlanAPI = pb.plans
	d.rcfg.OpenPlanWorktree = func(_ context.Context, root, planID string) (string, error) {
		pb.mu.Lock()
		defer pb.mu.Unlock()
		pb.opened = append(pb.opened, planID)
		return "/data/plans/" + planID, pb.openErr
	}
	d.rcfg.StartBuild = func(_ context.Context, cfg factory.AgentConfig, message string) (BuildAgent, error) {
		pb.mu.Lock()
		defer pb.mu.Unlock()
		pb.cfgs = append(pb.cfgs, cfg)
		pb.firsts = append(pb.firsts, message)
		return pb.build, nil
	}
	return pb
}

func TestPlanBuildStartsABuildSessionInThePlansWorktree(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1", Model: "sonnet"})
	assert.NilError(t, err)

	assert.Check(t, cmp.Equal(sess.State, chunkd.SessionRunning))
	f := sess.Factory
	assert.Assert(t, f != nil)
	assert.Check(t, cmp.Equal(f.PlanID, "plan-1"))
	assert.Check(t, cmp.Equal(f.SpecOrigin, "factory plan plan-1"))
	assert.Check(t, cmp.Equal(f.Prompt, pb.plans.text))
	assert.Check(t, cmp.Len(sess.Rounds, 0))

	entry := pb.sessions.get(sess.ID)
	waitUntil(t, "the build session to start", func() bool {
		pb.mu.Lock()
		defer pb.mu.Unlock()
		return len(pb.cfgs) == 1
	})
	pb.mu.Lock()
	cfg, first := pb.cfgs[0], pb.firsts[0]
	pb.mu.Unlock()
	assert.Check(t, cfg.Build, "the session may not build")
	assert.Check(t, cmp.Equal(cfg.Dir, "/data/plans/plan-1"))
	assert.Check(t, cmp.Equal(cfg.Model, "sonnet"))
	assert.Check(t, cfg.SessionID != "")
	assert.Check(t, cmp.Equal(first, factory.BuildMessage("plan-1")))
	waitUntil(t, "the worktree to be recorded", func() bool {
		return entry.snapshot().Factory.Worktree == "/data/plans/plan-1"
	})

	_ = pb.stopBuild(t, sess.ID)
}

// stopBuild cancels a build a test is done with, and waits for it to settle.
func (pb *planBuildDaemon) stopBuild(t *testing.T, id string) chunkd.SessionDetail {
	t.Helper()
	pb.sessions.cancelSession(id)
	return waitForSessionEnd(t, pb.daemon, id)
}

func TestPlanBuildFeedsWhatTheSessionDoes(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	assert.NilError(t, err)

	pb.build.events <- factory.AgentEvent{Activity: &claudecode.Activity{Tool: "Bash", Detail: "factory daemon wait"}}
	pb.build.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: "Built unit 1.\n\nMore detail.", ByTask: true}}
	entry := pb.sessions.get(sess.ID)
	waitUntil(t, "the reply in the feed", func() bool {
		lines := entry.snapshot().Factory.Progress.Lines
		return len(lines) > 0 && lines[len(lines)-1].Text == "Built unit 1."
	})
	var texts []string
	for _, l := range entry.snapshot().Factory.Progress.Lines {
		texts = append(texts, l.Text)
	}
	assert.Check(t, cmp.Contains(texts, "Building plan plan-1 in /data/plans/plan-1"))
	assert.Check(t, cmp.Contains(texts, (claudecode.Activity{Tool: "Bash", Detail: "factory daemon wait"}).Line()))

	_ = pb.stopBuild(t, sess.ID)
}

// The build follows factory's plan, its branch included, and ends once the
// plan's phase has, though its flow is gone, stopping the session.
func TestPlanBuildEndsWithThePlan(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		note    string
	}{
		{
			outcome: "merged",
			note:    "plan merged",
		},
		{
			outcome: "",
			note:    "plan ended",
		},
	} {
		t.Run(tc.note, func(t *testing.T) {
			pb := newPlanBuildDaemon(t)
			sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
			assert.NilError(t, err)
			entry := pb.sessions.get(sess.ID)

			pb.plans.set("Building - Agent", "scott/add-a-flag")
			waitUntil(t, "the plan's state and branch", func() bool {
				f := entry.snapshot().Factory
				return f.PlanState == "Building - Agent" && f.Branch == "scott/add-a-flag"
			})
			assert.Check(t, cmp.Equal(entry.snapshot().Branch, "scott/add-a-flag"))

			pb.plans.end(tc.outcome)
			detail := waitForSessionEnd(t, pb.daemon, sess.ID)
			assert.Check(t, cmp.Equal(detail.State, chunkd.SessionDone))
			assert.Check(t, cmp.Equal(detail.Stages[0].Note, tc.note))
			assert.Check(t, pb.build.wasStopped(), "the build session was left running")
		})
	}
}

func TestPlanBuildCancelStopsTheSession(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	assert.NilError(t, err)

	detail := pb.stopBuild(t, sess.ID)
	assert.Check(t, cmp.Equal(detail.State, chunkd.SessionCancelled))
	assert.Check(t, pb.build.wasStopped(), "the build session was left running")
}

// A build session that exits by itself, before its plan is over, fails the
// build, saying why.
func TestPlanBuildFailsWhenTheSessionEnds(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	assert.NilError(t, err)

	pb.build.events <- factory.AgentEvent{Problem: errors.New("claude exited with code 1")}
	pb.build.end()
	detail := waitForSessionEnd(t, pb.daemon, sess.ID)
	assert.Check(t, cmp.Equal(detail.State, chunkd.SessionFailed))
	assert.Check(t, cmp.Contains(detail.Error, "claude exited with code 1"))
}

func TestPlanBuildFailsWithoutAWorktree(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	pb.openErr = errors.New("no origin")
	sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	assert.NilError(t, err)

	detail := waitForSessionEnd(t, pb.daemon, sess.ID)
	assert.Check(t, cmp.Equal(detail.State, chunkd.SessionFailed))
	assert.Check(t, cmp.Contains(detail.Error, "no origin"))
	pb.mu.Lock()
	defer pb.mu.Unlock()
	assert.Check(t, cmp.Len(pb.cfgs, 0), "a build session started with no worktree")
}

// An ended plan is refused on its phase alone: its flow is not read.
func TestPlanBuildRefusesAnEndedPlan(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flowErr error
	}{
		{
			name: "no flow",
		},
		{
			name:    "a failed flow read",
			flowErr: errors.New("factory is down"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pb := newPlanBuildDaemon(t)
			pb.plans.end("rejected")
			pb.plans.mu.Lock()
			pb.plans.flowErr = tc.flowErr
			pb.plans.mu.Unlock()
			_, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
			var ae *apiError
			assert.Assert(t, errors.As(err, &ae), "err %v", err)
			assert.Check(t, cmp.Equal(ae.status, http.StatusConflict))
			assert.Check(t, cmp.Contains(ae.Error(), "rejected"))
			assert.Check(t, cmp.Len(pb.sessions.forProject(pb.root), 0))
		})
	}
}

func TestPlanBuildRefusesWhenTheFlowReadFails(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	pb.plans.flowErr = errors.New("factory is down")
	_, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae), "err %v", err)
	assert.Check(t, cmp.Equal(ae.status, http.StatusBadGateway))
	pb.mu.Lock()
	defer pb.mu.Unlock()
	assert.Check(t, cmp.Len(pb.cfgs, 0), "a build started on a failed flow read")
}

// A plan with no flow has no flow state: its build starts, and runs until the
// plan ends.
func TestPlanBuildOfAPlanWithNoFlow(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	pb.plans.noFlow = true
	sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(sess.Factory.PlanState, ""))
	entry := pb.sessions.get(sess.ID)

	time.Sleep(10 * planBuildPoll)
	assert.Check(t, cmp.Equal(entry.snapshot().State, chunkd.SessionRunning))
	assert.Check(t, !pb.build.wasStopped(), "the build of a plan with no flow was stopped")

	pb.plans.end("merged")
	detail := waitForSessionEnd(t, pb.daemon, sess.ID)
	assert.Check(t, cmp.Equal(detail.State, chunkd.SessionDone))
	assert.Check(t, pb.build.wasStopped(), "the build session was left running")
}

// A flow read that fails mid-build keeps the last flow state, and does not
// hold the build up once the plan ends.
func TestPlanBuildSkipsAFailedFlowRead(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	assert.NilError(t, err)
	entry := pb.sessions.get(sess.ID)

	pb.plans.mu.Lock()
	pb.plans.flowErr = errors.New("factory is down")
	pb.plans.state = "Done"
	pb.plans.mu.Unlock()
	time.Sleep(10 * planBuildPoll)
	f := entry.snapshot().Factory
	assert.Check(t, cmp.Equal(entry.snapshot().State, chunkd.SessionRunning))
	assert.Check(t, cmp.Equal(f.PlanState, "Planning - Human"))

	pb.plans.end("merged")
	detail := waitForSessionEnd(t, pb.daemon, sess.ID)
	assert.Check(t, cmp.Equal(detail.State, chunkd.SessionDone))
	assert.Check(t, pb.build.wasStopped(), "the build session was left running")
}

// A plan read that fails mid-build skips the tick: the build goes on, and
// ends once a later read says the plan has.
func TestPlanBuildSkipsAFailedPlanRead(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	assert.NilError(t, err)
	entry := pb.sessions.get(sess.ID)

	pb.plans.mu.Lock()
	pb.plans.getErr = errors.New("factory is down")
	pb.plans.mu.Unlock()
	pb.plans.end("merged")
	time.Sleep(10 * planBuildPoll)
	assert.Check(t, cmp.Equal(entry.snapshot().State, chunkd.SessionRunning))
	assert.Check(t, !pb.build.wasStopped(), "the build was stopped on a failed plan read")

	pb.plans.mu.Lock()
	pb.plans.getErr = nil
	pb.plans.mu.Unlock()
	detail := waitForSessionEnd(t, pb.daemon, sess.ID)
	assert.Check(t, cmp.Equal(detail.State, chunkd.SessionDone))
}

func TestPlanBuildRefusesAPlanAlreadyBuilding(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	assert.NilError(t, err)

	_, err = pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae), "err %v", err)
	assert.Check(t, cmp.Equal(ae.status, http.StatusConflict))
	assert.Check(t, cmp.Contains(ae.msg, sess.ID))

	_ = pb.stopBuild(t, sess.ID)
}

func TestPlanBuildNeedsCircleCI(t *testing.T) {
	d, root := newSessionDaemon(t, &fakeBackend{})
	d.client = nil
	_, err := d.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: root, PlanID: "plan-1"})
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae), "err %v", err)
	assert.Check(t, cmp.Equal(ae.status, http.StatusServiceUnavailable))
}
