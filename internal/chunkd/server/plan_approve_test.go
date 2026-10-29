package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/planapprove"
)

// oneUnitPlan is a plan's text with one unit written.
const oneUnitPlan = "# Add a flag\n\n## 5. Units\n\n### Unit 1: Add it\n"

// approveDaemon is a plannerDaemon whose plan-1 is Ready with one unit, so
// it can be approved, starting fake build sessions through builds, with
// factory's daemon placing sessions at locate.
type approveDaemon struct {
	*plannerDaemon
	builds *fakeStarts

	locMu  sync.Mutex
	locate map[string]string
}

func newApproveDaemon(t *testing.T) *approveDaemon {
	t.Helper()
	ad := &approveDaemon{
		plannerDaemon: newPlannerDaemon(t),
		builds:        &fakeStarts{},
		locate:        map[string]string{},
	}
	// The builds approval starts read planBuildPoll: they stop before
	// newPlannerDaemon puts it back.
	t.Cleanup(ad.sessions.stopAll)
	ad.plans.mu.Lock()
	ad.plans.state = "Planning - Human"
	ad.plans.events = []circleci.FlowEvent{circleci.ApproveForBuilding}
	ad.plans.text = oneUnitPlan
	ad.plans.mu.Unlock()
	ad.rcfg.StartBuild = ad.builds.start
	ad.rcfg.SessionWorktree = func(_ context.Context, session string) (string, error) {
		ad.locMu.Lock()
		defer ad.locMu.Unlock()
		dir, ok := ad.locate[session]
		if !ok {
			return "", factory.ErrUnknownSession
		}
		return dir, nil
	}
	return ad
}

// placeAt has factory's daemon place session in dir.
func (ad *approveDaemon) placeAt(session, dir string) {
	ad.locMu.Lock()
	defer ad.locMu.Unlock()
	ad.locate[session] = dir
}

func (ad *approveDaemon) postApprove(planID string) *httptest.ResponseRecorder {
	return ad.do(http.MethodPost, "/factory/plans/"+planID+"/approve", chunkd.PlanApproveRequest{ProjectRoot: ad.root})
}

// approve approves plan planID through the route.
func (ad *approveDaemon) approve(t *testing.T, planID string) *httptest.ResponseRecorder {
	t.Helper()
	return ad.postApprove(planID)
}

// buildsStarted are the build sessions the daemon has recorded for plan-1.
func (ad *approveDaemon) buildsStarted() []string {
	var ids []string
	for _, e := range ad.sessions.entries() {
		s := e.snapshot()
		if s.Factory != nil && s.Factory.PlanID == "plan-1" {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// livePlanner starts plan-1's planner on a request, and has it claim the
// plan and reply, returning its session ID.
func (ad *approveDaemon) livePlanner(t *testing.T) string {
	t.Helper()
	ad.request(t, "plan-1", "add a flag")
	id := ad.started(0).SessionID
	ad.plans.claim(id)
	ad.replied(t, ad.session(0), "Claimed.")
	return id
}

// recordOnDisk records id as plan-1's planning session in the real record,
// and holds the record open until the test ends, as another process would.
func (ad *approveDaemon) recordOnDisk(t *testing.T, id string) {
	t.Helper()
	rec, err := factory.OpenPlanSession(ad.root, "plan-1")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = rec.Close() })
	err = rec.Record(id)
	assert.NilError(t, err)
}

func approveResponse(t *testing.T, w *httptest.ResponseRecorder) chunkd.SessionStartResponse {
	t.Helper()
	assert.Assert(t, cmp.Equal(w.Code, http.StatusAccepted), "body: %s", w.Body.String())
	var resp chunkd.SessionStartResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	assert.NilError(t, err)
	return resp
}

func TestPlanApproveApprovesStopsThePlannerAndBuilds(t *testing.T) {
	ad := newApproveDaemon(t)
	id := ad.livePlanner(t)

	resp := approveResponse(t, ad.approve(t, "plan-1"))
	assert.Check(t, cmp.DeepEqual(ad.plans.callLog(), []string{"release " + id, "event approve-for-building"}))
	assert.Check(t, cmp.DeepEqual(ad.buildsStarted(), []string{resp.ID}))
	waitUntil(t, "the planner to stop", ad.session(0).wasStopped)
	waitUntil(t, "the plan's session record to close", ad.rec.wasClosed)

	p := ad.planner("plan-1")
	assert.Assert(t, p != nil, "the approved planner's entry was dropped")
	p.mu.Lock()
	defer p.mu.Unlock()
	assert.Check(t, cmp.Equal(p.approval, approved))
}

// After a daemon restart no planner is live, but the session recorded as
// planning the plan may still hold its claim: that one is released.
func TestPlanApproveReleasesARecordedSessionsClaim(t *testing.T) {
	ad := newApproveDaemon(t)
	ad.recordOnDisk(t, "s-old") // held open: read without its lock
	ad.plans.claim("s-old")

	rec := ad.approve(t, "plan-1")
	assert.Check(t, cmp.Equal(rec.Code, http.StatusAccepted), "body: %s", rec.Body.String())
	assert.Check(t, cmp.DeepEqual(ad.plans.releasedSessions(), []string{"s-old"}))
	assert.Check(t, cmp.DeepEqual(ad.plans.sentEvents(), []circleci.FlowEvent{circleci.ApproveForBuilding}))
	assert.Check(t, cmp.Len(ad.buildsStarted(), 1))
	assert.Check(t, cmp.Equal(ad.starts.sessions(), 0), "approval started a planner")
}

// A refused or failed approval changes nothing: the planner, live or not,
// takes resumes again.
func TestPlanApproveRefuses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(ad *approveDaemon)
		status   int
		contains string
	}{
		{
			name: "not ready",
			setup: func(ad *approveDaemon) {
				ad.plans.set("Planning - Agent", "")
				ad.plans.mu.Lock()
				defer ad.plans.mu.Unlock()
				ad.plans.task = "review-plan"
			},
			status:   http.StatusConflict,
			contains: planapprove.ErrNotReady.Error(),
		},
		{
			name: "no units",
			setup: func(ad *approveDaemon) {
				ad.plans.mu.Lock()
				defer ad.plans.mu.Unlock()
				ad.plans.text = "# Add a flag\n\n## 2. Design\n"
				ad.plans.events = []circleci.FlowEvent{circleci.ApproveForUnits}
			},
			status:   http.StatusConflict,
			contains: planapprove.ErrNoUnits.Error(),
		},
		{
			name: "questions open",
			setup: func(ad *approveDaemon) {
				ad.plans.mu.Lock()
				defer ad.plans.mu.Unlock()
				ad.plans.events = []circleci.FlowEvent{circleci.AnswerOpenQuestions}
			},
			status:   http.StatusConflict,
			contains: planapprove.ErrQuestionsOpen.Error(),
		},
		{
			name: "built from the plan's worktree",
			setup: func(ad *approveDaemon) {
				wt, _ := factory.PlanWorktreePath(ad.root, "plan-1")
				ad.placeAt("s-build", wt)
				ad.plans.claim("s-build")
			},
			status:   http.StatusConflict,
			contains: "plan plan-1 is already being built by session s-build",
		},
		{
			name: "held by a session factory does not know",
			setup: func(ad *approveDaemon) {
				ad.plans.claim("s-unknown")
			},
			status:   http.StatusConflict,
			contains: "plan plan-1 is held by session s-unknown, which factory's daemon does not know",
		},
		{
			name: "factory fails the event",
			setup: func(ad *approveDaemon) {
				ad.plans.mu.Lock()
				defer ad.plans.mu.Unlock()
				ad.plans.eventErr = errors.New("500 from factory")
			},
			status:   http.StatusBadGateway,
			contains: "send the approve-for-building event: 500 from factory",
		},
		{
			name: "factory fails the read",
			setup: func(ad *approveDaemon) {
				ad.plans.mu.Lock()
				defer ad.plans.mu.Unlock()
				ad.plans.flowErr = errors.New("503 from factory")
			},
			status:   http.StatusBadGateway,
			contains: "get the plan's flow: 503 from factory",
		},
		{
			name: "no factory API",
			setup: func(ad *approveDaemon) {
				ad.rcfg.PlanAPI = nil
			},
			status:   http.StatusServiceUnavailable,
			contains: "not authenticated to CircleCI",
		},
	} {
		for _, live := range []bool{true, false} {
			name := tc.name + "/no planner"
			if live {
				name = tc.name + "/live planner"
			}
			t.Run(name, func(t *testing.T) {
				ad := newApproveDaemon(t)
				if live {
					ad.livePlanner(t)
				}
				tc.setup(ad)

				w := ad.approve(t, "plan-1")
				assert.Check(t, cmp.Equal(w.Code, tc.status))
				assert.Check(t, cmp.Contains(w.Body.String(), tc.contains))
				assert.Check(t, cmp.Len(ad.plans.sentEvents(), 0))
				assert.Check(t, cmp.Len(ad.buildsStarted(), 0))

				// Put factory right, with the claim free, and resume.
				ad.rcfg.PlanAPI = ad.plans
				ad.plans.mu.Lock()
				ad.plans.flowErr, ad.plans.eventErr = nil, nil
				ad.plans.mu.Unlock()
				ad.plans.claim("")
				if live {
					assert.Check(t, !ad.session(0).wasStopped(), "a refused approval stopped the planner")
					ad.resume(t, "plan-1")
					assert.Check(t, cmp.DeepEqual(ad.session(0).messages(), []string{factory.PlanRetryMessage("plan-1")}))
					return
				}
				assert.Check(t, ad.planner("plan-1") == nil, "a refused approval left an entry behind")
				ad.resume(t, "plan-1")
				assert.Check(t, cmp.Equal(ad.starts.sessions(), 1), "the resume after a refusal started no planner")
			})
		}
	}
}

func TestPlanApproveNeedsAProjectRoot(t *testing.T) {
	ad := newApproveDaemon(t)
	w := ad.do(http.MethodPost, "/factory/plans/plan-1/approve", chunkd.PlanApproveRequest{})
	assert.Check(t, cmp.Equal(w.Code, http.StatusBadRequest))
	assert.Check(t, cmp.Contains(w.Body.String(), "project_root required"))

	w = ad.do(http.MethodPost, "/factory/plans/plan-1/approve", chunkd.PlanApproveRequest{ProjectRoot: "/nowhere"})
	assert.Check(t, cmp.Equal(w.Code, http.StatusNotFound))
	assert.Check(t, cmp.Len(ad.plans.sentEvents(), 0))
}

// gateEvents holds factory's CreateFlowEvent until the returned func runs,
// which a test must call (cleanup does too).
func (ad *approveDaemon) gateEvents(t *testing.T) func() {
	t.Helper()
	gate := make(chan struct{})
	ad.plans.mu.Lock()
	ad.plans.eventGate = gate
	ad.plans.mu.Unlock()
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)
	return open
}

func (ad *approveDaemon) approvalMark() string {
	p := ad.planner("plan-1")
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.approval
}

// While approval is under way the planner takes no request or resume.
func TestPlannerWhileApprovingIsRefused(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(map[bool]string{true: "live planner", false: "no planner"}[live], func(t *testing.T) {
			ad := newApproveDaemon(t)
			if live {
				ad.livePlanner(t)
			}
			open := ad.gateEvents(t)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- ad.postApprove("plan-1") }()
			waitUntil(t, "the approval to start", func() bool { return ad.approvalMark() == approving })

			for _, w := range []*httptest.ResponseRecorder{ad.postResume("plan-1"), ad.postRequest("plan-1", "add a flag")} {
				assert.Check(t, cmp.Equal(w.Code, http.StatusConflict))
				assert.Check(t, cmp.Contains(w.Body.String(), "the plan is being approved"))
			}
			open()
			approveResponse(t, <-done)
			assert.Check(t, cmp.Equal(ad.starts.sessions(), map[bool]int{true: 1, false: 0}[live]))
		})
	}
}

// Once approved, the planner stays stopped: a resume or request, while it is
// still stopping or once it has gone, starts no session that would claim
// the approved plan.
func TestPlannerAfterApprovalIsRefused(t *testing.T) {
	ad := newApproveDaemon(t)
	ad.livePlanner(t)
	s := ad.session(0)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	s.mu.Lock()
	s.stopGate = gate
	s.mu.Unlock()

	approveResponse(t, ad.approve(t, "plan-1"))
	refused := func(when string) {
		t.Helper()
		for _, w := range []*httptest.ResponseRecorder{ad.postResume("plan-1"), ad.postRequest("plan-1", "add a flag")} {
			assert.Check(t, cmp.Equal(w.Code, http.StatusConflict), when)
			assert.Check(t, cmp.Contains(w.Body.String(), "the plan is approved; it is being built"), when)
		}
		assert.Check(t, cmp.Equal(ad.starts.sessions(), 1), "%s: a planner started again", when)
		assert.Check(t, cmp.Len(s.messages(), 0), when)
	}
	refused("while stopping")

	release()
	waitUntil(t, "the planner to stop", s.wasStopped)
	waitEnded(t, ad.planner("plan-1").run)
	refused("once stopped")

	// A second approval of the approved plan is refused too.
	w := ad.approve(t, "plan-1")
	assert.Check(t, cmp.Equal(w.Code, http.StatusConflict))
	assert.Check(t, cmp.Len(ad.plans.sentEvents(), 1))
}

// Two approvals at once: one approves and builds, the other is refused.
func TestPlanApproveTakesOneAtATime(t *testing.T) {
	ad := newApproveDaemon(t)
	ad.livePlanner(t)
	open := ad.gateEvents(t)

	codes := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { codes <- ad.postApprove("plan-1") })
	}
	// The gate holds the one approving, so the first to answer is the other.
	first := <-codes
	open()
	wg.Wait()
	second := <-codes

	assert.Check(t, cmp.Equal(first.Code, http.StatusConflict), "body: %s", first.Body.String())
	assert.Check(t, cmp.Contains(first.Body.String(), "the plan is being approved"))
	assert.Check(t, cmp.Equal(second.Code, http.StatusAccepted), "body: %s", second.Body.String())
	assert.Check(t, cmp.Len(ad.plans.sentEvents(), 1))
	assert.Check(t, cmp.Len(ad.buildsStarted(), 1))
}

// The plan is approved, but its build would not start: the planner is
// stopped anyway, and the answer says how to build it.
func TestPlanApproveSaysWhenTheBuildWouldNotStart(t *testing.T) {
	ad := newApproveDaemon(t)
	ad.livePlanner(t)
	ad.plans.mu.Lock()
	ad.plans.getErr = errors.New("502 from factory")
	ad.plans.mu.Unlock()

	w := ad.approve(t, "plan-1")
	assert.Check(t, cmp.Equal(w.Code, http.StatusBadGateway))
	assert.Check(t, cmp.Contains(w.Body.String(),
		"the plan is approved in factory, but chunk could not start building it (read plan plan-1 from factory: 502 from factory); build it in Claude Code with /f:resume plan-1"))
	assert.Check(t, cmp.DeepEqual(ad.plans.sentEvents(), []circleci.FlowEvent{circleci.ApproveForBuilding}))
	waitUntil(t, "the planner to stop", ad.session(0).wasStopped)
	assert.Check(t, cmp.Equal(ad.approvalMark(), approved))
}

// A client that hangs up once it has sent the approval leaves the daemon to
// finish it: the plan is approved, its planner stopped and its build
// started.
func TestPlanApproveSurvivesTheClientHangingUp(t *testing.T) {
	ad := newApproveDaemon(t)
	ad.livePlanner(t)
	srv := httptest.NewServer(ad.mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	// The client hangs up as factory is releasing the claim, and the daemon
	// goes on only once it has gone.
	gone := make(chan struct{})
	ad.plans.mu.Lock()
	ad.plans.onRelease = func() {
		cancel()
		select {
		case <-gone:
		case <-time.After(10 * time.Second):
		}
	}
	ad.plans.mu.Unlock()

	body, err := json.Marshal(chunkd.PlanApproveRequest{ProjectRoot: ad.root})
	assert.NilError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/factory/plans/plan-1/approve", bytes.NewReader(body))
	assert.NilError(t, err)
	resp, err := srv.Client().Do(req)
	close(gone)
	if err == nil {
		_ = resp.Body.Close()
	}
	assert.Check(t, errors.Is(err, context.Canceled), "the client did not hang up: %v", err)

	waitUntil(t, "the approval", func() bool { return len(ad.plans.sentEvents()) == 1 })
	waitUntil(t, "the planner to stop", ad.session(0).wasStopped)
	waitUntil(t, "the build to start", func() bool { return len(ad.buildsStarted()) == 1 })
}
