package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// plannerDaemon is a daemon whose planners read plans from plans, start
// fake sessions through starts, and keep their record in rec, driven through
// the real session routes.
type plannerDaemon struct {
	*daemon
	root   string
	plans  *fakePlans
	starts *fakeStarts
	rec    *memRecord
	mux    *http.ServeMux

	mu          sync.Mutex
	checkErr    error
	worktreeErr error
	opened      []string
}

func newPlannerDaemon(t *testing.T) *plannerDaemon {
	t.Helper()
	d, root := newSessionDaemon(t)
	old := planBuildPoll
	planBuildPoll = 10 * time.Millisecond
	t.Cleanup(func() { planBuildPoll = old })
	// Registered after the poll's restore, so it runs first: no planner
	// outlives the test.
	t.Cleanup(d.planners.stopAll)

	pd := &plannerDaemon{
		daemon: d, root: root,
		plans:  &fakePlans{id: "plan-1", state: "Planning - Agent", phase: circleci.PhaseStarted},
		starts: &fakeStarts{},
		rec:    &memRecord{},
		mux:    http.NewServeMux(),
	}
	d.rcfg.PlanAPI = pd.plans
	d.rcfg.StartPlanner = pd.starts.start
	d.rcfg.OpenPlanSession = func(_, planID string) (SessionRecord, error) {
		if planID != "plan-1" {
			return (&memRecord{}).open()
		}
		return pd.rec.open()
	}
	d.rcfg.CheckPlanner = func(context.Context) error {
		pd.mu.Lock()
		defer pd.mu.Unlock()
		return pd.checkErr
	}
	d.rcfg.OpenPlanWorktree = func(_ context.Context, _, planID string) (string, error) {
		pd.mu.Lock()
		defer pd.mu.Unlock()
		pd.opened = append(pd.opened, planID)
		return "/data/plans/" + planID, pd.worktreeErr
	}
	registerSessionRoutes(pd.mux, d)
	return pd
}

func (pd *plannerDaemon) setCheck(err error) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	pd.checkErr = err
}

// do sends one request through the routes.
func (pd *plannerDaemon) do(method, path string, body any) *httptest.ResponseRecorder {
	var r io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	w := httptest.NewRecorder()
	pd.mux.ServeHTTP(w, httptest.NewRequest(method, path, r))
	return w
}

func decodeLog(t *testing.T, w *httptest.ResponseRecorder) chunkd.SessionLog {
	t.Helper()
	assert.Assert(t, cmp.Equal(w.Code, http.StatusOK), "body: %s", w.Body.String())
	var log chunkd.SessionLog
	err := json.NewDecoder(w.Body).Decode(&log)
	assert.NilError(t, err)
	return log
}

func (pd *plannerDaemon) postRequest(planID, text string) *httptest.ResponseRecorder {
	return pd.do(http.MethodPost, "/factory/plans/"+planID+"/planner/request", chunkd.PlannerRequest{ProjectRoot: pd.root, Text: text})
}

func (pd *plannerDaemon) postResume(planID string) *httptest.ResponseRecorder {
	return pd.do(http.MethodPost, "/factory/plans/"+planID+"/planner/resume", chunkd.PlannerResume{ProjectRoot: pd.root})
}

// request sends plan planID's planner its request, which must be taken.
func (pd *plannerDaemon) request(t *testing.T, planID, text string) chunkd.SessionLog {
	t.Helper()
	return decodeLog(t, pd.postRequest(planID, text))
}

// resume resumes plan planID's planner, which must be taken.
func (pd *plannerDaemon) resume(t *testing.T, planID string) chunkd.SessionLog {
	t.Helper()
	return decodeLog(t, pd.postResume(planID))
}

// log reads plan planID's planner's log from cursor after.
func (pd *plannerDaemon) log(t *testing.T, planID string, after int) chunkd.SessionLog {
	t.Helper()
	path := "/factory/plans/" + planID + "/planner"
	if after >= 0 {
		path += fmt.Sprintf("?after=%d", after)
	}
	return decodeLog(t, pd.do(http.MethodGet, path, nil))
}

// poller reads plan planID's planner's log, as a screen does, until ctx
// ends; the channel closes once it has.
func (pd *plannerDaemon) poller(ctx context.Context, planID string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		cursor := -1
		for ctx.Err() == nil {
			path := "/factory/plans/" + planID + "/planner"
			if cursor >= 0 {
				path += fmt.Sprintf("?after=%d", cursor)
			}
			w := pd.do(http.MethodGet, path, nil)
			var log chunkd.SessionLog
			if json.NewDecoder(w.Body).Decode(&log) == nil {
				cursor = log.Next
			}
		}
	}()
	return done
}

func (pd *plannerDaemon) started(i int) factory.AgentConfig { return pd.starts.configs()[i] }
func (pd *plannerDaemon) session(i int) *fakeBuild          { return pd.starts.session(i) }

// waitEvents waits for plan planID's planner's log to hold n events, and
// returns them all.
func (pd *plannerDaemon) waitEvents(t *testing.T, planID string, n int) []chunkd.SessionLogEvent {
	t.Helper()
	waitUntil(t, fmt.Sprintf("%d events", n), func() bool { return pd.log(t, planID, -1).Next >= n })
	return pd.log(t, planID, 0).Events
}

// replied has the first session reply, and waits for the reply and anything
// the claim check appends after it: an activity sent behind the reply lands
// only once the check is done.
func (pd *plannerDaemon) replied(t *testing.T, s *fakeBuild, text string) []chunkd.SessionLogEvent {
	t.Helper()
	before := pd.log(t, "plan-1", -1).Next
	s.reply(text)
	s.act("marker " + text)
	marker := activityLine("marker " + text)
	var events []chunkd.SessionLogEvent
	waitUntil(t, "the reply", func() bool {
		events = pd.log(t, "plan-1", before).Events
		return len(events) > 0 && events[len(events)-1].Text == marker
	})
	return events[:len(events)-1]
}

func (pd *plannerDaemon) planner(planID string) *planner { return pd.planners.lookup(planID) }

func activityLine(detail string) string {
	return (claudecode.Activity{Tool: "Bash", Detail: detail}).Line()
}

func TestPlannerStartsOnTheRequestWithTheClaim(t *testing.T) {
	pd := newPlannerDaemon(t)
	log := pd.request(t, "plan-1", "add a flag")

	assert.Assert(t, cmp.Equal(pd.starts.sessions(), 1), "one session for the plan")
	cfg := pd.started(0)
	assert.Check(t, cmp.Equal(log.SessionID, cfg.SessionID))
	assert.Check(t, log.Busy)
	assert.Check(t, cmp.Equal(log.Next, 0))
	assert.Check(t, cmp.Len(log.Events, 0))
	assert.Check(t, !cfg.Resume)
	assert.Check(t, cmp.Equal(cfg.Dir, "/data/plans/plan-1"))
	assert.Check(t, cmp.Equal(pd.starts.first(0), factory.PlanMessage("plan-1", "add a flag")))
	assert.Check(t, cmp.DeepEqual(pd.rec.recordedIDs(), []string{cfg.SessionID}))

	w := pd.postRequest("plan-1", "keep it small")
	assert.Check(t, cmp.Equal(w.Code, http.StatusConflict))
	assert.Check(t, cmp.Contains(w.Body.String(), "the plan's request has already been sent to its planning session"))
	assert.Check(t, cmp.Len(pd.session(0).messages(), 0))

	// The session claimed the plan: the check after its reply says nothing.
	pd.plans.claim(cfg.SessionID)
	events := pd.replied(t, pd.session(0), "Claimed.")
	assert.Check(t, cmp.DeepEqual(events, []chunkd.SessionLogEvent{{Seq: 0, Kind: chunkd.SessionLogReply, Text: "Claimed."}}))
}

func TestPlannerSaysWhenTheDaemonHoldsNoClaim(t *testing.T) {
	pd := newPlannerDaemon(t) // a factory daemon on another store claims nothing here
	pd.request(t, "plan-1", "add a flag")
	s := pd.session(0)

	events := pd.replied(t, s, "I could not claim it.")
	assert.Assert(t, cmp.Len(events, 2))
	assert.Check(t, cmp.Equal(events[0].Kind, chunkd.SessionLogReply))
	assert.Check(t, cmp.Equal(events[0].Text, "I could not claim it."))
	assert.Check(t, cmp.Equal(events[1].Kind, chunkd.SessionLogProblem))
	assert.Check(t, cmp.Contains(events[1].Text, "FACTORY_PLAN_STORE=circleci"))

	// Only the reply to the claim is checked.
	events = pd.replied(t, s, "ok")
	assert.Check(t, cmp.Len(events, 1))
}

func TestPlannerSaysWhenAnotherSessionHoldsTheClaim(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	pd.plans.claim("s-other")

	events := pd.replied(t, pd.session(0), "ok")
	assert.Assert(t, cmp.Len(events, 2))
	assert.Check(t, cmp.Equal(events[1].Kind, chunkd.SessionLogProblem))
	assert.Check(t, cmp.Contains(events[1].Text, "claimed by another factory session"))
}

func TestPlannerSaysWhenItCannotReadTheClaim(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	pd.plans.mu.Lock()
	pd.plans.flowErr = errors.New("503 from factory")
	pd.plans.mu.Unlock()

	events := pd.replied(t, pd.session(0), "ok")
	assert.Assert(t, cmp.Len(events, 2))
	assert.Check(t, cmp.Contains(events[1].Text, "503 from factory"))
}

// A session that holds the plan's claim is not told to claim it again; one
// that has lost it, as a session that released the plan has, is, mid-turn
// or not.
func TestPlannerClaimsAgainOnceTheClaimIsLost(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	s := pd.session(0)
	pd.plans.claim(pd.started(0).SessionID)
	pd.replied(t, s, "Claimed it.")

	pd.resume(t, "plan-1")
	assert.Check(t, cmp.Len(s.messages(), 0), "nothing is owed to a session holding the claim")

	s.act("working on a task")
	pd.waitEvents(t, "plan-1", 3)
	pd.plans.claim("")
	log := pd.resume(t, "plan-1")
	assert.Check(t, cmp.DeepEqual(s.messages(), []string{factory.PlanRetryMessage("plan-1")}))
	assert.Check(t, log.Busy)

	// The claim it was just told to make is checked once it replies.
	events := pd.replied(t, s, "Claimed it again.")
	assert.Assert(t, cmp.Len(events, 2))
	assert.Check(t, cmp.Equal(events[1].Kind, chunkd.SessionLogProblem), "the claim is still missing")
}

// A blip reading the claim does not have the planner claim a plan it holds.
func TestPlannerKeepsTheClaimWhenItCannotReadIt(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	s := pd.session(0)
	pd.plans.claim(pd.started(0).SessionID)
	pd.replied(t, s, "Claimed it.")

	pd.plans.mu.Lock()
	pd.plans.flowErr = errors.New("503 from factory")
	pd.plans.mu.Unlock()
	pd.resume(t, "plan-1")
	assert.Check(t, cmp.Len(s.messages(), 0))
}

// A resume right after the request, before the planner has claimed, finds
// no claim; the claim the request asked for is not asked for again.
func TestResumeRightAfterTheRequestSendsNothing(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	pd.resume(t, "plan-1")
	assert.Check(t, cmp.Equal(pd.starts.sessions(), 1))
	assert.Check(t, cmp.Len(pd.session(0).messages(), 0))
}

// An existing plan whose planner is not running is resumed with the claim
// alone, never a request.
func TestResumeStartsAPlanWithNoSessionOnTheClaimAlone(t *testing.T) {
	pd := newPlannerDaemon(t)
	log := pd.resume(t, "plan-1")
	assert.Assert(t, cmp.Equal(pd.starts.sessions(), 1))
	first := pd.starts.first(0)
	assert.Check(t, cmp.Equal(first, factory.PlanRetryMessage("plan-1")))
	assert.Check(t, cmp.Contains(first, "never in a reply"))
	assert.Check(t, !strings.Contains(first, "request"))
	assert.Check(t, log.Busy)
}

func TestPlannerResumesASessionThatEnded(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	id := pd.started(0).SessionID
	pd.plans.claim(id)
	pd.session(0).end()
	waitEnded(t, pd.planner("plan-1").run)

	pd.resume(t, "plan-1")
	assert.Assert(t, cmp.Equal(pd.starts.sessions(), 2))
	cfg := pd.started(1)
	assert.Check(t, cfg.Resume)
	assert.Check(t, cmp.Equal(cfg.SessionID, id))
	// The ended session's claim was freed with it, so the resumed one claims
	// the plan again.
	assert.Check(t, cmp.Equal(pd.starts.first(1), factory.PlanRetryMessage("plan-1")))
}

// Each session, the resumed one too, starts in the plan's worktree rather
// than the developer's checkout, where another session ending would free the
// plan's claim.
func TestPlannerStartsEachSessionInThePlansWorktree(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	pd.session(0).end()
	waitEnded(t, pd.planner("plan-1").run)
	pd.resume(t, "plan-1")

	cfgs := pd.starts.configs()
	assert.Assert(t, cmp.Len(cfgs, 2))
	assert.Check(t, cmp.Equal(cfgs[0].Dir, "/data/plans/plan-1"))
	assert.Check(t, cmp.Equal(cfgs[1].Dir, "/data/plans/plan-1"))
	pd.mu.Lock()
	defer pd.mu.Unlock()
	assert.Check(t, cmp.DeepEqual(pd.opened, []string{"plan-1", "plan-1"}))
}

func TestPlannerWithoutAWorktreeStartsNoSession(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.mu.Lock()
	pd.worktreeErr = errors.New("no origin/main")
	pd.mu.Unlock()

	w := pd.postRequest("plan-1", "add a flag")
	assert.Check(t, cmp.Equal(w.Code, http.StatusInternalServerError))
	assert.Check(t, cmp.Contains(w.Body.String(), "open the plan's worktree: no origin/main"))
	assert.Check(t, cmp.Equal(pd.starts.sessions(), 0))
}

func TestPlannerStartFailureIsASentence(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.starts.setErrs(factory.ErrNoFactoryPlugin, nil)
	w := pd.postRequest("plan-1", "add a flag")
	assert.Check(t, cmp.Equal(w.Code, http.StatusServiceUnavailable))
	assert.Check(t, cmp.Contains(w.Body.String(), "f plugin is not loaded"))

	pd.starts.setErrs(errors.New("claude exited before it started"), nil)
	w = pd.postRequest("plan-1", "add a flag")
	assert.Check(t, cmp.Equal(w.Code, http.StatusBadGateway))
	assert.Check(t, cmp.Contains(w.Body.String(), "start the planning session: claude exited before it started"))

	// The claim goes on the request that does start the session.
	pd.starts.setErrs(nil, nil)
	pd.request(t, "plan-1", "add a flag")
	assert.Check(t, cmp.Equal(pd.starts.first(0), factory.PlanMessage("plan-1", "add a flag")))
}

// A plan reopened after the daemon restarted resumes the session recorded
// as planning it, so claude keeps that conversation, and claims the plan as
// that session, which may still hold the claim.
func TestPlannerResumesTheSessionRecordedForThePlan(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.rec.id = "s-0"
	pd.plans.claim("s-0")

	log := pd.resume(t, "plan-1")
	cfgs := pd.starts.configs()
	assert.Assert(t, cmp.Len(cfgs, 1))
	assert.Check(t, cfgs[0].Resume)
	assert.Check(t, cmp.Equal(cfgs[0].SessionID, "s-0"))
	assert.Check(t, cmp.Equal(log.SessionID, "s-0"))
	assert.Check(t, cmp.Equal(pd.starts.first(0), factory.PlanRetryMessage("plan-1")))
}

func TestPlannerRecordsTheSessionItStarts(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	assert.Check(t, cmp.DeepEqual(pd.rec.recordedIDs(), []string{pd.started(0).SessionID}))

	// A session that ends is resumed without opening the record again.
	pd.session(0).end()
	waitEnded(t, pd.planner("plan-1").run)
	pd.resume(t, "plan-1")
	pd.rec.mu.Lock()
	defer pd.rec.mu.Unlock()
	assert.Check(t, cmp.Equal(pd.rec.opens, 1))
}

// claude may no longer have a recorded session's conversation; the session
// then starts afresh under the recorded ID.
func TestPlannerStartsTheRecordedSessionAfreshWhenItCannotResume(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.rec.id = "s-0"
	pd.starts.setErrs(nil, errors.New("claude exited before it started: No conversation found"))

	pd.resume(t, "plan-1")
	cfgs := pd.starts.configs()
	assert.Assert(t, cmp.Len(cfgs, 2))
	assert.Check(t, cfgs[0].Resume)
	assert.Check(t, !cfgs[1].Resume)
	assert.Check(t, cmp.Equal(cfgs[1].SessionID, "s-0"))
	assert.Check(t, cmp.Equal(pd.rec.recordedIDs()[0], "s-0"))
}

// An older chunk binary plans in its own process, holding the plan's record
// open; the daemon leaves the plan to it.
func TestPlannerRefusesAPlanOpenInAnotherChunk(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.rec.held = true

	w := pd.postRequest("plan-1", "add a flag")
	assert.Check(t, cmp.Equal(w.Code, http.StatusConflict))
	assert.Check(t, cmp.Contains(w.Body.String(), "this plan is open for planning in an older chunk window; close it, then try again"))
	assert.Check(t, cmp.Equal(pd.starts.sessions(), 0))

	// Once the other chunk lets go, the next request starts the session.
	pd.rec.mu.Lock()
	pd.rec.held = false
	pd.rec.mu.Unlock()
	pd.request(t, "plan-1", "add a flag")
	assert.Check(t, cmp.Equal(pd.starts.sessions(), 1))
}

func TestPlannerRefusesToStart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(pd *plannerDaemon)
		request  any // nil to skip the request
		resume   any // nil to skip the resume
		status   int
		contains string
	}{
		{
			name:     "no Claude Code",
			setup:    func(pd *plannerDaemon) { pd.setCheck(factory.ErrNoClaude) },
			status:   http.StatusServiceUnavailable,
			contains: "the chunk daemon found no Claude Code to plan with",
		},
		{
			name:     "factory daemon down",
			setup:    func(pd *plannerDaemon) { pd.setCheck(factory.ErrNoFactoryDaemon) },
			status:   http.StatusServiceUnavailable,
			contains: "the factory daemon is not running; run `factory daemon` with FACTORY_PLAN_STORE=circleci to plan",
		},
		{
			name:     "claimed by another session",
			setup:    func(pd *plannerDaemon) { pd.plans.claim("s-other") },
			status:   http.StatusConflict,
			contains: "this plan is claimed by another factory session; you can read and comment without taking its claim",
		},
		{
			name:     "unknown project",
			request:  chunkd.PlannerRequest{ProjectRoot: "/nowhere", Text: "add a flag"},
			resume:   chunkd.PlannerResume{ProjectRoot: "/nowhere"},
			status:   http.StatusNotFound,
			contains: "the chunk daemon is not tracking",
		},
		{
			name:     "no factory API",
			setup:    func(pd *plannerDaemon) { pd.rcfg.PlanAPI = nil },
			status:   http.StatusServiceUnavailable,
			contains: "not authenticated to CircleCI",
		},
		{
			name:     "open in an older chunk",
			setup:    func(pd *plannerDaemon) { pd.rec.held = true },
			status:   http.StatusConflict,
			contains: "open for planning in an older chunk window",
		},
		{
			name:     "no project_root",
			request:  chunkd.PlannerRequest{Text: "add a flag"},
			resume:   chunkd.PlannerResume{},
			status:   http.StatusBadRequest,
			contains: "project_root",
		},
		{
			name:     "a request without text",
			request:  chunkd.PlannerRequest{ProjectRoot: "root"},
			status:   http.StatusBadRequest,
			contains: "text required",
		},
		{
			name:     "a request with a session recorded",
			setup:    func(pd *plannerDaemon) { pd.rec.id = "s-0" },
			request:  chunkd.PlannerRequest{ProjectRoot: "root", Text: "add a flag"},
			status:   http.StatusConflict,
			contains: "the plan's request has already been sent to its planning session",
		},
	} {
		for _, route := range []string{"request", "resume"} {
			t.Run(tc.name+"/"+route, func(t *testing.T) {
				body := tc.request
				if route == "resume" {
					body = tc.resume
				}
				defaults := tc.request == nil && tc.resume == nil
				if !defaults && body == nil {
					t.Skip("not a refusal of this route")
				}
				pd := newPlannerDaemon(t)
				if tc.setup != nil {
					tc.setup(pd)
				}
				switch b := body.(type) {
				case nil:
					if route == "request" {
						body = chunkd.PlannerRequest{ProjectRoot: pd.root, Text: "add a flag"}
					} else {
						body = chunkd.PlannerResume{ProjectRoot: pd.root}
					}
				case chunkd.PlannerRequest:
					if b.ProjectRoot == "root" {
						b.ProjectRoot = pd.root
					}
					body = b
				}
				w := pd.do(http.MethodPost, "/factory/plans/plan-1/planner/"+route, body)
				assert.Check(t, cmp.Equal(w.Code, tc.status))
				assert.Check(t, cmp.Contains(w.Body.String(), tc.contains))
				assert.Check(t, cmp.Equal(pd.starts.sessions(), 0), "a refused call started a session")
				pd.mu.Lock()
				defer pd.mu.Unlock()
				assert.Check(t, cmp.Len(pd.opened, 0), "a refused call opened a worktree")
			})
		}
	}
}

// Two screens resuming at once a planner that lost its claim send it one
// claim-retry between them.
func TestPlannerTakesOneResumeAtATime(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	s := pd.session(0)
	pd.plans.claim(pd.started(0).SessionID)
	pd.replied(t, s, "Claimed it.")
	pd.plans.claim("")

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Go(func() { codes[i] = pd.postResume("plan-1").Code })
	}
	wg.Wait()
	assert.Check(t, cmp.DeepEqual(codes, []int{http.StatusOK, http.StatusOK}))
	assert.Check(t, cmp.DeepEqual(s.messages(), []string{factory.PlanRetryMessage("plan-1")}))
	assert.Check(t, cmp.Equal(pd.starts.sessions(), 1))
}

// Two screens polling one planner each see everything it does.
func TestPlannerLogHasManyPollers(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	s := pd.session(0)
	const n = 20
	var wg sync.WaitGroup
	got := make([][]string, 2)
	for i := range got {
		wg.Go(func() {
			cursor := 0
			for len(got[i]) < n {
				w := pd.do(http.MethodGet, fmt.Sprintf("/factory/plans/plan-1/planner?after=%d", cursor), nil)
				var log chunkd.SessionLog
				if json.NewDecoder(w.Body).Decode(&log) != nil {
					continue
				}
				for _, ev := range log.Events {
					got[i] = append(got[i], ev.Text)
				}
				cursor = log.Next
			}
		})
	}
	var want []string
	for i := range n {
		text := fmt.Sprintf("step %d", i)
		want = append(want, activityLine(text))
		s.act(text)
	}
	wg.Wait()
	assert.Check(t, cmp.DeepEqual(got[0], want))
	assert.Check(t, cmp.DeepEqual(got[1], want))
}

// A planner whose plan ends is stopped and dropped, an approved mark
// included.
func TestPlannerStopsWhenItsPlanEnds(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	p := pd.planner("plan-1")
	p.mu.Lock()
	p.approval = approved
	p.mu.Unlock()

	pd.plans.end("merged")
	waitUntil(t, "the planner to be dropped", func() bool { return pd.planner("plan-1") == nil })
	assert.Check(t, pd.session(0).wasStopped(), "the planning session was left running")
	assert.Check(t, pd.rec.wasClosed(), "the plan's session record was left open")
	log := pd.log(t, "plan-1", -1)
	assert.Check(t, cmp.Equal(log.LogID, ""))
}

// A flow read that fails keeps the planner, which still stops once a read
// says the plan has ended. The test runs in a synctest bubble, so the
// watcher's polls tick on the fake clock.
func TestPlannerSkipsAFailedFlowRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pd := newPlannerDaemon(t)
		pd.request(t, "plan-1", "add a flag")
		pd.plans.mu.Lock()
		pd.plans.flowErr = errors.New("factory is down")
		pd.plans.mu.Unlock()
		pd.plans.end("merged")

		// Ten polls pass, each a failed read.
		time.Sleep(10 * planBuildPoll)
		synctest.Wait()
		assert.Check(t, pd.planner("plan-1") != nil, "dropped on a failed flow read")
		assert.Check(t, !pd.session(0).wasStopped())

		pd.plans.mu.Lock()
		pd.plans.flowErr = nil
		pd.plans.mu.Unlock()
		time.Sleep(planBuildPoll)
		synctest.Wait()
		assert.Check(t, pd.planner("plan-1") == nil, "kept once the plan has ended")
		assert.Check(t, pd.session(0).wasStopped())
	})
}

// The daemon stops every planner as it stops.
func TestPlannerStopAllStopsEveryPlanner(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")
	pd.request(t, "plan-2", "add another flag")
	assert.Assert(t, cmp.Equal(pd.starts.sessions(), 2))

	pd.planners.stopAll()
	assert.Check(t, pd.session(0).wasStopped())
	assert.Check(t, pd.session(1).wasStopped())
	assert.Check(t, pd.rec.wasClosed())
	assert.Check(t, pd.planner("plan-1") == nil)
	assert.Check(t, pd.planner("plan-2") == nil)
}

// Planners are not sessions: a live one lists in no project's sessions,
// which is what decides whether a stale daemon may be replaced.
func TestPlannerIsNotASession(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.request(t, "plan-1", "add a flag")

	snap := pd.snapshot(nil)
	assert.Assert(t, len(snap.Projects) > 0)
	for _, p := range snap.Projects {
		assert.Check(t, cmp.Len(p.Sessions, 0), "project %s lists a session", p.Root)
	}
}

func TestPlannerOutlivesItsClient(t *testing.T) {
	pd := newPlannerDaemon(t)
	log := pd.request(t, "plan-1", "add a flag")
	assert.Check(t, cmp.Equal(log.Busy, true))

	ctx, cancel := context.WithCancel(t.Context())
	poll := pd.poller(ctx, "plan-1")
	cancel()
	<-poll

	pd.plans.claim(pd.started(0).SessionID)
	pd.session(0).reply("Claimed.")
	waitUntil(t, "the reply", func() bool { return !pd.log(t, "plan-1", -1).Busy })
	assert.Check(t, !pd.session(0).wasStopped())

	pd.plans.claim("") // factory freed the claim
	_ = pd.resume(t, "plan-1")
	assert.Check(t, cmp.DeepEqual(pd.session(0).messages(), []string{factory.PlanRetryMessage("plan-1")}))
}

func TestPlannerLogWithNoPlanner(t *testing.T) {
	pd := newPlannerDaemon(t)
	w := pd.do(http.MethodGet, "/factory/plans/plan-1/planner", nil)
	assert.Check(t, cmp.Equal(w.Code, http.StatusOK))
	assert.Check(t, cmp.Contains(w.Body.String(), `"events":[]`))
	log := decodeLog(t, w)
	assert.Check(t, cmp.DeepEqual(log, chunkd.SessionLog{Events: []chunkd.SessionLogEvent{}}))

	w = pd.do(http.MethodGet, "/factory/plans/plan-1/planner?after=soon", nil)
	assert.Check(t, cmp.Equal(w.Code, http.StatusBadRequest))
}

// The planner's routes leave the build and cancel routes theirs.
func TestPlannerRoutesShadowNoOthers(t *testing.T) {
	pd := newPlannerDaemon(t)
	w := pd.do(http.MethodPost, "/factory/plans", chunkd.PlanBuildRequest{})
	assert.Check(t, cmp.Equal(w.Code, http.StatusBadRequest))
	assert.Check(t, cmp.Contains(w.Body.String(), "project_root and plan_id required"))

	w = pd.do(http.MethodPost, "/factory/nope/cancel", nil)
	assert.Check(t, cmp.Equal(w.Code, http.StatusNotFound))
	assert.Check(t, cmp.Contains(w.Body.String(), "no such session"))
}

// prunePlans is fakePlans with other plans beside plan-1, for pruning.
type prunePlans struct{ *fakePlans }

func (p prunePlans) GetFlow(ctx context.Context, id string) (*circleci.Flow, error) {
	switch id {
	case "ended":
		return &circleci.Flow{ID: id, Phase: circleci.PhaseEnded, Outcome: "merged"}, nil
	case "live":
		return &circleci.Flow{ID: id, Phase: circleci.PhaseStarted}, nil
	case "gone":
		return nil, &circleci.StatusError{StatusCode: http.StatusNotFound}
	}
	return p.fakePlans.GetFlow(ctx, id)
}

func (p prunePlans) GetPlan(ctx context.Context, id string) (*circleci.Plan, error) {
	switch id {
	case "ended", "live":
		return &circleci.Plan{ID: id}, nil
	case "gone":
		return nil, &circleci.StatusError{StatusCode: http.StatusNotFound}
	}
	return p.fakePlans.GetPlan(ctx, id)
}

// Before it opens its plan's worktree, a planner removes those of plans that
// have ended, never its own.
func TestPlannerPrunesEndedPlansWorktrees(t *testing.T) {
	pd := newPlannerDaemon(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(t, pd.root, "init", "--bare", "-b", "main", origin)
	git(t, pd.root, "remote", "add", "origin", origin)
	git(t, pd.root, "push", "origin", "HEAD:main")
	ids := []string{"ended", "gone", "live", "plan-1"}
	for _, id := range ids {
		_, err := factory.OpenPlanWorktree(t.Context(), pd.root, id)
		assert.NilError(t, err)
	}
	pd.plans.noFlow = true // plan-1 has no flow, but is there
	pd.rcfg.PlanAPI = prunePlans{pd.plans}

	pd.request(t, "plan-1", "add a flag")
	var kept []string
	for _, id := range ids {
		path, err := factory.PlanWorktreePath(pd.root, id)
		assert.NilError(t, err)
		if _, err := os.Stat(path); err == nil {
			kept = append(kept, id)
		}
	}
	assert.Check(t, cmp.DeepEqual(kept, []string{"live", "plan-1"}))
}
