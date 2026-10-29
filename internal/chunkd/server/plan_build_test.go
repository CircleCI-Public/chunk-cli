package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
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
	holder         string
	// events are the flow's applicable events.
	events []circleci.FlowEvent
	// task, when set, is the task a session takes to move the flow on, such
	// as a review running.
	task string
	// released are the sessions whose claim ReleaseClaim released, and sent
	// the events CreateFlowEvent sent; calls is both, in order.
	released []string
	sent     []circleci.FlowEvent
	calls    []string
	// releaseErr and eventErr fail ReleaseClaim and CreateFlowEvent.
	releaseErr, eventErr error
	// onRelease, when set, runs as ReleaseClaim is reached.
	onRelease func()
	// eventGate, when set, holds CreateFlowEvent until it is closed.
	eventGate chan struct{}
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
	return &circleci.Plan{ID: id, Branch: f.branch}, nil
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
	// As factory lists the events that apply in next, for the developer,
	// after any task a session takes.
	var next []circleci.Transition
	if f.task != "" {
		next = append(next, circleci.Transition{Actor: "session", TaskCategory: f.task, IsDispatched: true})
	}
	for _, e := range f.events {
		next = append(next, circleci.Transition{Actor: "human", Event: e})
	}
	return &circleci.Flow{
		ID: id, State: f.state, Phase: f.phase, Outcome: f.outcome, SessionID: f.holder,
		ApplicableEvents: append([]circleci.FlowEvent(nil), f.events...),
		Next:             next,
	}, nil
}

// ReleaseClaim releases sessionID's claim on the plan, as factory does:
// only the holder's release frees it.
func (f *fakePlans) ReleaseClaim(_ context.Context, sessionID, _ string) error {
	f.mu.Lock()
	hook := f.onRelease
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseErr != nil {
		return f.releaseErr
	}
	f.released = append(f.released, sessionID)
	f.calls = append(f.calls, "release "+sessionID)
	if f.holder == sessionID {
		f.holder = ""
	}
	return nil
}

// CreateFlowEvent records event, once eventGate (when set) is closed.
func (f *fakePlans) CreateFlowEvent(_ context.Context, _ string, event circleci.FlowEvent) error {
	f.mu.Lock()
	gate := f.eventGate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.eventErr != nil {
		return f.eventErr
	}
	f.sent = append(f.sent, event)
	f.calls = append(f.calls, "event "+string(event))
	return nil
}

func (f *fakePlans) releasedSessions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.released...)
}

func (f *fakePlans) sentEvents() []circleci.FlowEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]circleci.FlowEvent(nil), f.sent...)
}

func (f *fakePlans) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// claim has holder claim the plan, as factory plan claim would; "" releases
// it.
func (f *fakePlans) claim(holder string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holder = holder
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

// end ends the plan with outcome, as factory does: its flow is in its end
// state.
func (f *fakePlans) end(outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phase, f.outcome = circleci.PhaseEnded, outcome
}

// fakeBuild is a build session whose events a test sends, recording what it
// is sent.
type fakeBuild struct {
	events  chan factory.AgentEvent
	once    sync.Once
	mu      sync.Mutex
	stopped bool
	sent    []string
	sendErr error
	// stopGate, when set, holds Stop until it is closed, as a claude slow to
	// exit would.
	stopGate chan struct{}
}

func newFakeBuild() *fakeBuild { return &fakeBuild{events: make(chan factory.AgentEvent, 8)} }

func (b *fakeBuild) Send(message string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, message)
	return b.sendErr
}

// messages is what the session has been sent, failed sends included.
func (b *fakeBuild) messages() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.sent...)
}

func (b *fakeBuild) Events() <-chan factory.AgentEvent { return b.events }

func (b *fakeBuild) Stop() error {
	b.mu.Lock()
	gate := b.stopGate
	b.mu.Unlock()
	if gate != nil {
		<-gate
	}
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

// newSessionDaemon builds a daemon that tracks one real git repo holding two
// review prompts. It returns the daemon and the project's canonical root.
func newSessionDaemon(t *testing.T) (*daemon, string) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv("CHUNK_DAEMON_DIR", socketDir(t))

	dir := initRepo(t)
	prompts := filepath.Join(dir, ".chunk", "reviews")
	err := os.MkdirAll(prompts, 0o755)
	assert.NilError(t, err)
	err = os.WriteFile(filepath.Join(prompts, "bugs.md"), []byte("find bugs"), 0o644)
	assert.NilError(t, err)
	err = os.WriteFile(filepath.Join(prompts, "style.md"), []byte("check style"), 0o644)
	assert.NilError(t, err)

	dataDir, err := config.ProjectDataDir(dir)
	assert.NilError(t, err)
	err = sidecar.RegisterProjectRoot(dataDir, dir)
	assert.NilError(t, err)
	root, err := filepath.EvalSymlinks(dir)
	assert.NilError(t, err)

	d := newTestDaemon()
	d.poll()
	t.Cleanup(d.sessions.stopAll)
	return d, root
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitForSessionEnd waits for a session to finish and settle.
func waitForSessionEnd(t *testing.T, d *daemon, id string) chunkd.Session {
	t.Helper()
	entry := d.sessions.get(id)
	assert.Assert(t, entry != nil, "no session %s", id)
	waitUntil(t, "session to end", func() bool { return entry.snapshot().State.Finished() })
	<-entry.done
	return entry.snapshot()
}

func newPlanBuildDaemon(t *testing.T) *planBuildDaemon {
	t.Helper()
	d, root := newSessionDaemon(t)
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
	d.rcfg.StartBuild = func(_ context.Context, cfg factory.AgentConfig, message string) (AgentSession, error) {
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
func (pb *planBuildDaemon) stopBuild(t *testing.T, id string) chunkd.Session {
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
// plan's flow has, stopping the session.
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

// An ended plan is refused on its flow's phase.
func TestPlanBuildRefusesAnEndedPlan(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	pb.plans.end("rejected")
	_, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae), "err %v", err)
	assert.Check(t, cmp.Equal(ae.status, http.StatusConflict))
	assert.Check(t, cmp.Contains(ae.Error(), "rejected"))
	assert.Check(t, cmp.Len(pb.sessions.forProject(pb.root), 0))
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

// A plan with no flow has no flow state: its build starts, and runs until it
// is cancelled, as there is no flow to end.
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

	detail := pb.stopBuild(t, sess.ID)
	assert.Check(t, cmp.Equal(detail.State, chunkd.SessionCancelled))
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

	// The end is read from the flow, so the build sees it once a read
	// succeeds again.
	pb.plans.mu.Lock()
	pb.plans.flowErr = nil
	pb.plans.mu.Unlock()
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
	d, root := newSessionDaemon(t)
	d.client = nil
	_, err := d.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: root, PlanID: "plan-1"})
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae), "err %v", err)
	assert.Check(t, cmp.Equal(ae.status, http.StatusServiceUnavailable))
}

// nudge drives a build through a nudge test, inside a synctest bubble: it
// ends and starts the build session's turns, waiting for the daemon to see
// each, and moves the plan's claim between ticks.
type nudge struct {
	t     *testing.T
	pb    *planBuildDaemon
	entry *sessionEntry
	turns int
}

// own is the build session's own session ID.
func (n *nudge) own() string {
	n.pb.mu.Lock()
	defer n.pb.mu.Unlock()
	return n.pb.cfgs[0].SessionID
}

// idle ends the session's turn, and waits for the daemon to see it.
func (n *nudge) idle() {
	n.turns++
	reply := fmt.Sprintf("Turn %d ended.", n.turns)
	n.pb.build.events <- factory.AgentEvent{TurnEnded: &factory.AgentTurn{Reply: reply}}
	synctest.Wait()
}

// busy has the session do something, and waits for the daemon to see it.
func (n *nudge) busy() {
	n.turns++
	a := &claudecode.Activity{Tool: "Bash", Detail: fmt.Sprintf("step %d", n.turns)}
	n.pb.build.events <- factory.AgentEvent{Activity: a}
	synctest.Wait()
}

// claim has holder claim the plan, "" releasing it, and lets a couple of
// ticks pass.
func (n *nudge) claim(holder string) {
	n.pb.plans.claim(holder)
	ticks()
}

// ticks lets a couple of the build's polls pass in the bubble's clock, and
// waits for the build to act on them.
func ticks() {
	time.Sleep(2 * planBuildPoll)
	synctest.Wait()
}

// lines is the feed's lines about the plan's claim.
func (n *nudge) lines() []string {
	var out []string
	for _, l := range n.entry.snapshot().Factory.Progress.Lines {
		if strings.HasPrefix(l.Text, "Plan plan-1") || strings.HasPrefix(l.Text, "Could not tell") {
			out = append(out, l.Text)
		}
	}
	return out
}

func claimedLine(holder string) string {
	return "Plan plan-1 is claimed by session " + holder + "; release it with factory daemon release plan-1 and the build picks it up"
}

const releasedLine = "Plan plan-1's claim was released; telling the build session to claim it"

// A build session that could not claim its plan, because another session
// held it, is told once to claim it again after the claim is released, and
// only while it is idle.
func TestPlanBuildNudgesOnceTheClaimIsReleased(t *testing.T) {
	retry := factory.BuildRetryMessage("plan-1")
	for _, tc := range []struct {
		name    string
		sendErr error
		run     func(n *nudge)
		lines   []string
		sent    int
	}{
		{
			name: "claimed by one session then another",
			run: func(n *nudge) {
				n.idle()
				n.claim("parent")
				n.claim("other")
			},
			lines: []string{claimedLine("parent"), claimedLine("other")},
		},
		{
			name: "released while idle",
			run: func(n *nudge) {
				n.idle()
				n.claim("parent")
				n.claim("")
				ticks()
			},
			lines: []string{claimedLine("parent"), releasedLine},
			sent:  1,
		},
		{
			name: "released while busy",
			run: func(n *nudge) {
				n.idle()
				n.claim("parent")
				n.busy()
				n.claim("")
				assert.Check(n.t, cmp.Len(n.pb.build.messages(), 0), "a busy session was nudged")
				n.idle()
				ticks()
			},
			lines: []string{claimedLine("parent"), releasedLine},
			sent:  1,
		},
		{
			name: "claimed by the build session itself",
			run: func(n *nudge) {
				n.idle()
				n.claim(n.own())
				n.claim("")
			},
		},
		{
			name: "claimed by another session, then by the build session",
			run: func(n *nudge) {
				n.idle()
				n.claim("parent")
				n.claim(n.own())
				n.claim("")
			},
			lines: []string{claimedLine("parent")},
		},
		{
			name: "claimed while busy, noted once idle",
			run: func(n *nudge) {
				n.idle()
				n.busy()
				n.claim("parent")
				assert.Check(n.t, cmp.Len(n.lines(), 0), "a busy session's plan was said to be claimed")
				n.idle()
				ticks()
				n.claim("")
			},
			lines: []string{claimedLine("parent"), releasedLine},
			sent:  1,
		},
		{
			name: "never claimed",
			run: func(n *nudge) {
				n.idle()
				ticks()
				n.busy()
				n.idle()
				ticks()
			},
		},
		{
			name: "claimed and released again after a nudge",
			run: func(n *nudge) {
				n.idle()
				n.claim("parent")
				n.claim("")
				n.idle()
				ticks()
				assert.Check(n.t, cmp.Len(n.pb.build.messages(), 1), "an unclaimed plan was nudged again")
				n.claim("parent")
				n.claim("")
			},
			lines: []string{claimedLine("parent"), releasedLine, claimedLine("parent"), releasedLine},
			sent:  2,
		},
		{
			name: "failed and missing flow reads keep it armed",
			run: func(n *nudge) {
				n.idle()
				n.claim("parent")
				n.pb.plans.mu.Lock()
				n.pb.plans.flowErr = errors.New("factory is down")
				n.pb.plans.mu.Unlock()
				n.claim("")
				n.pb.plans.mu.Lock()
				n.pb.plans.flowErr, n.pb.plans.noFlow = nil, true
				n.pb.plans.mu.Unlock()
				ticks()
				assert.Check(n.t, cmp.Len(n.pb.build.messages(), 0), "nudged on a failed or missing flow")
				n.pb.plans.mu.Lock()
				n.pb.plans.noFlow = false
				n.pb.plans.mu.Unlock()
				ticks()
			},
			lines: []string{claimedLine("parent"), releasedLine},
			sent:  1,
		},
		{
			name:    "a failed send is not retried",
			sendErr: errors.New("claude's stdin is closed"),
			run: func(n *nudge) {
				n.idle()
				n.claim("parent")
				n.claim("")
				ticks()
			},
			lines: []string{claimedLine("parent"), releasedLine, "Could not tell the build session to claim the plan: claude's stdin is closed"},
			sent:  1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The daemon, its fakes and the build all live in the bubble, so
			// a tick is a step of its clock rather than a wait.
			synctest.Test(t, func(t *testing.T) {
				pb := newPlanBuildDaemon(t)
				pb.build.sendErr = tc.sendErr
				sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
				assert.NilError(t, err)
				n := &nudge{t: t, pb: pb, entry: pb.sessions.get(sess.ID)}
				synctest.Wait()
				pb.mu.Lock()
				started := len(pb.cfgs)
				pb.mu.Unlock()
				assert.Assert(t, cmp.Equal(started, 1), "the build session did not start")

				tc.run(n)

				assert.Check(t, cmp.DeepEqual(n.lines(), tc.lines))
				sent := pb.build.messages()
				assert.Check(t, cmp.Len(sent, tc.sent))
				for _, m := range sent {
					assert.Check(t, cmp.Equal(m, retry))
				}
				_ = pb.stopBuild(t, sess.ID)
			})
		})
	}
}

// seedPlanBuild records a build session of planID that has written n feed
// lines, "line 0" to "line n-1", started at started and in state.
func seedPlanBuild(t *testing.T, d *daemon, planID string, n int, started time.Time, state chunkd.SessionState, errText string) string {
	t.Helper()
	entry, _ := d.sessions.add(chunkd.Session{Factory: &chunkd.FactoryRun{PlanID: planID}})
	// No build goroutine runs to settle the session, so stopAll would wait on
	// it forever.
	close(entry.done)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	for i := range n {
		addFeedLine(&entry.s.Factory.Progress, iostream.LevelInfo, fmt.Sprintf("line %d", i))
	}
	entry.s.StartedAt, entry.s.State, entry.s.Error = started, state, errText
	return entry.s.ID
}

func getPlanBuild(t *testing.T, d *daemon, path string) (int, chunkd.PlanBuildActivity, string) {
	t.Helper()
	rec := serve(d, httptest.NewRequest(http.MethodGet, path, nil))
	body := strings.TrimSpace(rec.Body.String())
	var got chunkd.PlanBuildActivity
	if rec.Code == http.StatusOK {
		err := json.Unmarshal([]byte(body), &got)
		assert.NilError(t, err)
	}
	return rec.Code, got, body
}

func TestPlanBuildActivityReadsTheFeedAfter(t *testing.T) {
	d := newTestDaemon()
	t.Cleanup(d.sessions.stopAll)
	id := seedPlanBuild(t, d, "plan-1", 130, time.Now(), chunkd.SessionRunning, "")

	tests := []struct {
		query     string
		first     int
		n         int
		firstLine string
	}{
		{
			query:     "",
			first:     30,
			n:         100,
			firstLine: "line 30",
		},
		{
			query:     "?after=abc",
			first:     30,
			n:         100,
			firstLine: "line 30",
		},
		{
			query:     "?after=-1",
			first:     30,
			n:         100,
			firstLine: "line 30",
		},
		{
			query:     "?after=10",
			first:     30,
			n:         100,
			firstLine: "line 30",
		},
		{
			query:     "?after=125",
			first:     125,
			n:         5,
			firstLine: "line 125",
		},
		{
			query: "?after=130",
			first: 130,
			n:     0,
		},
		{
			query:     "?after=500",
			first:     30,
			n:         100,
			firstLine: "line 30",
		},
	}
	for _, tt := range tests {
		t.Run("query "+tt.query, func(t *testing.T) {
			code, got, body := getPlanBuild(t, d, "/factory/plans/plan-1/build"+tt.query)
			assert.Equal(t, code, http.StatusOK, body)
			assert.Check(t, cmp.Equal(got.ID, id))
			assert.Check(t, cmp.Equal(got.State, chunkd.SessionRunning))
			assert.Check(t, cmp.Equal(got.First, tt.first))
			assert.Check(t, cmp.Equal(got.Total, 130))
			assert.Assert(t, cmp.Len(got.Lines, tt.n))
			if tt.n > 0 {
				assert.Check(t, cmp.Equal(got.Lines[0].Text, tt.firstLine))
				assert.Check(t, cmp.Equal(got.Lines[len(got.Lines)-1].Text, "line 129"))
			}
		})
	}
}

func TestPlanBuildActivityOfAPlanWithNoBuild(t *testing.T) {
	d := newTestDaemon()
	t.Cleanup(d.sessions.stopAll)
	seedPlanBuild(t, d, "plan-2", 3, time.Now(), chunkd.SessionRunning, "")

	code, _, body := getPlanBuild(t, d, "/factory/plans/plan-1/build")
	assert.Check(t, cmp.Equal(code, http.StatusNotFound))
	assert.Check(t, cmp.Equal(body, "no build of plan plan-1"))
}

func TestPlanBuildActivityPrefersTheRunningSession(t *testing.T) {
	now := time.Now()

	t.Run("running over a later ended one", func(t *testing.T) {
		d := newTestDaemon()
		t.Cleanup(d.sessions.stopAll)
		seedPlanBuild(t, d, "plan-1", 1, now, chunkd.SessionDone, "")
		running := seedPlanBuild(t, d, "plan-1", 1, now.Add(-time.Hour), chunkd.SessionRunning, "")
		for range 10 {
			code, got, body := getPlanBuild(t, d, "/factory/plans/plan-1/build")
			assert.Equal(t, code, http.StatusOK, body)
			assert.Check(t, cmp.Equal(got.ID, running))
		}
	})

	t.Run("the latest of ended ones", func(t *testing.T) {
		d := newTestDaemon()
		t.Cleanup(d.sessions.stopAll)
		seedPlanBuild(t, d, "plan-1", 1, now.Add(-time.Hour), chunkd.SessionFailed, "boom")
		latest := seedPlanBuild(t, d, "plan-1", 1, now, chunkd.SessionDone, "")
		for range 10 {
			code, got, body := getPlanBuild(t, d, "/factory/plans/plan-1/build")
			assert.Equal(t, code, http.StatusOK, body)
			assert.Check(t, cmp.Equal(got.ID, latest))
		}
	})

	t.Run("a failed session says why", func(t *testing.T) {
		d := newTestDaemon()
		t.Cleanup(d.sessions.stopAll)
		seedPlanBuild(t, d, "plan-1", 1, now, chunkd.SessionFailed, "boom")
		code, got, body := getPlanBuild(t, d, "/factory/plans/plan-1/build")
		assert.Equal(t, code, http.StatusOK, body)
		assert.Check(t, cmp.Equal(got.State, chunkd.SessionFailed))
		assert.Check(t, cmp.Equal(got.Error, "boom"))
	})
}
