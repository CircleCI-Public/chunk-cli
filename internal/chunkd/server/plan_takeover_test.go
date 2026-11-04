package server

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

func TestPlannerTakesOverAForeignClaim(t *testing.T) {
	tests := []struct {
		name string
		// first has the plan's planner running before the take-over.
		first bool
		// prep sets the world up, after the first request.
		prep func(t *testing.T, pd *plannerDaemon)
		// holder is the session the request confirms, s-other when nil.
		holder   func(pd *plannerDaemon) string
		code     int
		body     string
		releases int
		// retried is whether the planner is sent the retry message.
		retried bool
	}{
		{
			name: "held by a session working in the repository root",
			prep: func(_ *testing.T, pd *plannerDaemon) {
				pd.plans.claim("s-other")
				pd.place("s-other", pd.root)
			},
			code:     http.StatusOK,
			releases: 1,
			retried:  true,
		},
		{
			name:    "the claim is already gone",
			prep:    func(*testing.T, *plannerDaemon) {},
			code:    http.StatusOK,
			retried: true,
		},
		{
			name: "the claim moved to another session",
			prep: func(_ *testing.T, pd *plannerDaemon) {
				pd.plans.claim("s-newer")
				pd.place("s-other", pd.root)
				pd.place("s-newer", pd.root)
			},
			code: http.StatusConflict,
			body: "the claim moved to session s-newer",
		},
		{
			name: "factory's daemon does not know the holder",
			prep: func(_ *testing.T, pd *plannerDaemon) { pd.plans.claim("s-other") },
			code: http.StatusConflict,
			body: "factory daemon release plan-1",
		},
		{
			name: "the holder works in the plan's worktree",
			prep: func(t *testing.T, pd *plannerDaemon) {
				wt, err := factory.PlanWorktreePath(pd.root, "plan-1")
				assert.NilError(t, err)
				pd.plans.claim("s-other")
				pd.place("s-other", wt)
			},
			code: http.StatusConflict,
			body: "already being built",
		},
		{
			name:  "the holder is the planner's own session",
			first: true,
			prep: func(_ *testing.T, pd *plannerDaemon) {
				pd.plans.claim(pd.started(0).SessionID)
			},
			holder: func(pd *plannerDaemon) string { return pd.started(0).SessionID },
			code:   http.StatusConflict,
			body:   "this plan is held by chunk's own planner",
		},
		{
			name: "held by a worker with no session",
			prep: func(_ *testing.T, pd *plannerDaemon) { pd.plans.claimWorker("", "w-1") },
			code: http.StatusConflict,
			body: "held by factory worker w-1",
		},
		{
			name: "the plan has ended",
			prep: func(_ *testing.T, pd *plannerDaemon) {
				pd.plans.claim("s-other")
				pd.place("s-other", pd.root)
				pd.plans.end("merged")
			},
			code: http.StatusConflict,
			body: "the plan has ended",
		},
		{
			name: "the plan moved on to build",
			prep: func(_ *testing.T, pd *plannerDaemon) {
				pd.plans.claim("s-other")
				pd.place("s-other", pd.root)
				pd.plans.set("build", "")
			},
			code: http.StatusConflict,
			body: "the plan moved on to build",
		},
		{
			name: "the plan has no flow",
			prep: func(_ *testing.T, pd *plannerDaemon) {
				pd.plans.claim("s-other")
				pd.place("s-other", pd.root)
				pd.plans.mu.Lock()
				pd.plans.noFlow = true
				pd.plans.mu.Unlock()
			},
			code: http.StatusConflict,
			body: "nothing to release",
		},
		{
			name: "the holder cannot be located",
			prep: func(_ *testing.T, pd *plannerDaemon) {
				pd.plans.claim("s-other")
				pd.mu.Lock()
				pd.whereErr = factory.ErrNoFactoryDaemon
				pd.mu.Unlock()
			},
			code: http.StatusBadGateway,
			body: "find where session s-other works",
		},
		{
			name: "the release fails",
			prep: func(_ *testing.T, pd *plannerDaemon) {
				pd.plans.claim("s-other")
				pd.place("s-other", pd.root)
				pd.plans.mu.Lock()
				pd.plans.releaseErr = errors.New("factory said no")
				pd.plans.mu.Unlock()
			},
			code: http.StatusBadGateway,
			body: "release session s-other's claim",
		},
		{
			name:  "a live planner believing it holds the claim",
			first: true,
			prep: func(_ *testing.T, pd *plannerDaemon) {
				pd.plans.claim("s-other")
				pd.place("s-other", pd.root)
			},
			code:     http.StatusOK,
			releases: 1,
			retried:  true,
		},
		{
			name: "the take-over's flow read fails",
			prep: func(_ *testing.T, pd *plannerDaemon) {
				pd.plans.claim("s-other")
				pd.place("s-other", pd.root)
				pd.plans.mu.Lock()
				pd.plans.flowErr = errors.New("factory is down")
				pd.plans.mu.Unlock()
			},
			code: http.StatusBadGateway,
			body: "read the plan's flow",
		},
		{
			name: "the planner's own session cannot be read",
			prep: func(t *testing.T, pd *plannerDaemon) {
				pd.plans.claim("s-other")
				pd.place("s-other", pd.root)
				wt, err := factory.PlanWorktreePath(pd.root, "plan-1")
				assert.NilError(t, err)
				// A directory where the record is read fails the read.
				err = os.MkdirAll(wt+".session", 0o755)
				assert.NilError(t, err)
			},
			code: http.StatusInternalServerError,
			body: "read the plan's planning session",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pd := newPlannerDaemon(t)
			if tt.first {
				pd.request(t, "plan-1", "add a flag")
			}
			tt.prep(t, pd)

			holder := "s-other"
			if tt.holder != nil {
				holder = tt.holder(pd)
			}
			w := pd.postResumeHolder("plan-1", holder)

			assert.Check(t, cmp.Equal(w.Code, tt.code), "body: %s", w.Body.String())
			assert.Check(t, cmp.Contains(w.Body.String(), tt.body))
			assert.Check(t, cmp.Equal(pd.plans.releases(), tt.releases))
			var sent []string
			switch {
			case tt.first:
				sent = pd.session(0).messages()
			case pd.starts.sessions() > 0:
				sent = []string{pd.starts.first(0)}
			}
			if tt.retried {
				assert.Check(t, cmp.DeepEqual(sent, []string{factory.PlanRetryMessage("plan-1")}))
				return
			}
			if tt.first {
				assert.Check(t, cmp.Len(sent, 0))
				return
			}
			assert.Check(t, cmp.Equal(pd.starts.sessions(), 0), "no planner started")
		})
	}
}

// A release that works but a planner that will not start leaves the claim
// released, and says so.
func TestPlannerTakeOverReleasesBeforeAFailedStart(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.plans.claim("s-other")
	pd.place("s-other", pd.root)
	pd.starts.setErrs(errors.New("claude would not start"), nil)

	w := pd.postResumeHolder("plan-1", "s-other")

	assert.Check(t, cmp.Equal(w.Code, http.StatusBadGateway), "body: %s", w.Body.String())
	assert.Check(t, strings.HasPrefix(w.Body.String(), "the claim was released, but the planner could not start: "), "body: %s", w.Body.String())
	assert.Check(t, cmp.Equal(pd.plans.releases(), 1))

	// A second R resumes as for an unclaimed plan.
	pd.starts.setErrs(nil, nil)
	pd.resume(t, "plan-1")
	assert.Check(t, cmp.Equal(pd.starts.first(0), factory.PlanRetryMessage("plan-1")))
	assert.Check(t, cmp.Equal(pd.plans.releases(), 1))
}

// Two take-overs together release once and send one retry message.
func TestPlannerTakeOversTogetherReleaseOnce(t *testing.T) {
	pd := newPlannerDaemon(t)
	pd.plans.claim("s-other")
	pd.place("s-other", pd.root)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Go(func() { codes[i] = pd.postResumeHolder("plan-1", "s-other").Code })
	}
	wg.Wait()

	assert.Check(t, cmp.DeepEqual(codes, []int{http.StatusOK, http.StatusOK}))
	assert.Check(t, cmp.Equal(pd.plans.releases(), 1))
	assert.Assert(t, cmp.Equal(pd.starts.sessions(), 1))
	assert.Check(t, cmp.Equal(pd.starts.first(0), factory.PlanRetryMessage("plan-1")))
	assert.Check(t, cmp.Len(pd.session(0).messages(), 0))
}
