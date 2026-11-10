package planapprove

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// testClient builds a circleci.Client against a fake factory server, and
// returns it alongside the fake so a test can inspect its state.
func testClient(t *testing.T) (*circleci.Client, *fakes.FakeCircleCI) {
	t.Helper()
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return client, cci
}

// locatedAt is a Locate that answers err when it is set, and otherwise
// each session's worktree from dirs, or factory.ErrUnknownSession for a
// session not in it.
func locatedAt(dirs map[string]string, err error) Locate {
	return func(_ context.Context, session string) (string, error) {
		if err != nil {
			return "", err
		}
		dir, ok := dirs[session]
		if !ok {
			return "", factory.ErrUnknownSession
		}
		return dir, nil
	}
}

// noDaemon is a Locate that fails, so a test that looks a session up
// without saying where it works fails loudly rather than asking a real
// factory daemon.
var noDaemon = locatedAt(nil, errors.New("no factory daemon in tests"))

// createdPlan is a plan created in a fake factory, with text, returning its ID.
func createdPlan(t *testing.T, text string) (*circleci.Client, *fakes.FakeCircleCI, string) {
	t.Helper()
	client, cci := testClient(t)
	p, err := client.CreatePlan(context.Background(), "a-plan", text)
	assert.NilError(t, err)
	return client, cci, p.ID
}

// TestApproveWaitsForTheReview checks that a plan is approved only while
// its flow waits on the developer: a plan whose review is still running may
// have findings unfixed.
func TestApproveWaitsForTheReview(t *testing.T) {
	client, cci, id := createdPlan(t, "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	ctx := context.Background()
	cci.ClaimFactoryPlan(id, "s-1")

	cci.SetFactoryPlanState(id, "refine-proposal")
	cci.SetFactoryFlowStatus(id, fakes.FactoryFlowStatus{WaitingOn: "daemon"})
	err := Approve(ctx, client, noDaemon, id, "s-1", "", "/data/plans/"+id)
	assert.Check(t, cmp.ErrorIs(err, ErrNotReady))
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))
	assert.Check(t, cmp.Equal(p.Session, "s-1"))

	cci.SetFactoryPlanState(id, "refine-units")
	cci.SetFactoryApplicableEvents(id, "approve-for-building")
	cci.SetFactoryTask(id, "review-plan")
	cci.SetFactoryFlowStatus(id, fakes.FactoryFlowStatus{WaitingOn: "session"})
	err = Approve(ctx, client, noDaemon, id, "s-1", "", "/data/plans/"+id)
	assert.Check(t, cmp.ErrorIs(err, ErrNotReady), "a review running")
	p, _ = cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))

	cci.DropFactoryFlow(id)
	err = Approve(ctx, client, noDaemon, id, "s-1", "", "/data/plans/"+id)
	assert.Check(t, cmp.ErrorIs(err, ErrNotReady))
	p, _ = cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))
}

// TestApproveRefusesAPlanWithNoFlow checks that a plan factory has no flow
// for, so GET /flows answers 404, is refused as not ready, with nothing
// sent.
func TestApproveRefusesAPlanWithNoFlow(t *testing.T) {
	client, cci, id := approvablePlan(t, "s-1")
	cci.DropFactoryFlow(id)
	before := len(posts(cci))

	err := Approve(context.Background(), client, noDaemon, id, "s-1", "", "/data/plans/"+id)
	assert.Check(t, cmp.ErrorIs(err, ErrNotReady))
	assert.Check(t, cmp.Len(posts(cci), before))
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))
	assert.Check(t, cmp.Equal(p.Session, "s-1"))
}

// TestApproveReleasesThePlanningSession checks that approving releases
// the planning session's claim before it sends the event, so factory hands
// the build to a session that claims the plan to build it rather than to the
// planning session.
func TestApproveReleasesThePlanningSession(t *testing.T) {
	client, cci, id := createdPlan(t, "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	ctx := context.Background()
	cci.ClaimFactoryPlan(id, "s-1")
	cci.SetFactoryPlanState(id, "refine-proposal")
	cci.SetFactoryFlowStatus(id, fakes.FactoryFlowStatus{WaitingOn: "human"})
	cci.SetFactoryRequiredEvents(id, "approve-for-building")

	err := Approve(ctx, client, noDaemon, id, "s-1", "", "/data/plans/"+id)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
	assert.Check(t, cmp.Equal(p.Session, ""))

	var paths []string
	for _, r := range cci.Recorder.AllRequests() {
		if r.Method == http.MethodPost {
			paths = append(paths, r.URL.Path)
		}
	}
	assert.Check(t, cmp.DeepEqual(paths[len(paths)-2:], []string{
		"/api/v3/factory/flows/" + id + "/release",
		"/api/v3/factory/events",
	}))
}

// TestApproveHoldsAWorkerOnlyClaim checks that a plan a worker claims with
// no session working it is refused, unless it is the worker the planning
// session was last read working it under.
func TestApproveHoldsAWorkerOnlyClaim(t *testing.T) {
	t.Run("another worker is refused", func(t *testing.T) {
		client, cci, id := approvablePlan(t, "")
		cci.ClaimFactoryPlanForWorker(id, "w-2")
		err := Approve(context.Background(), client, noDaemon, id, "s-1", "w-1", "/data/plans/"+id)
		assert.Check(t, cmp.ErrorIs(err, ErrBeingBuilt))
		assert.Check(t, cmp.ErrorContains(err, "held by factory worker w-2 with no session"))
		p, _ := cci.FactoryPlan(id)
		assert.Check(t, cmp.Equal(p.Worker, "w-2"))
		assert.Check(t, cmp.Len(p.Events, 0))
	})
	t.Run("an unknown worker is refused", func(t *testing.T) {
		client, cci, id := approvablePlan(t, "")
		cci.ClaimFactoryPlanForWorker(id, "w-2")
		err := Approve(context.Background(), client, noDaemon, id, "s-1", "", "/data/plans/"+id)
		assert.Check(t, cmp.ErrorIs(err, ErrBeingBuilt))
		p, _ := cci.FactoryPlan(id)
		assert.Check(t, cmp.Equal(p.Worker, "w-2"))
		assert.Check(t, cmp.Len(p.Events, 0))
	})
	t.Run("the planning session's worker is released", func(t *testing.T) {
		client, cci, id := approvablePlan(t, "")
		cci.ClaimFactoryPlanForWorker(id, "w-1")
		err := Approve(context.Background(), client, noDaemon, id, "s-1", "w-1", "/data/plans/"+id)
		assert.NilError(t, err)
		p, _ := cci.FactoryPlan(id)
		assert.Check(t, cmp.Equal(p.Worker, ""))
		assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
	})
	t.Run("the worker's session listing the plan is the planning session", func(t *testing.T) {
		client, cci, id := approvablePlan(t, "")
		cci.ClaimFactoryPlanForWorker(id, "w-1")
		cci.SetFactoryWorkerSessions("w-1", fakes.FactoryWorkerSession{ID: "s-1", FlowIDs: []string{id}})
		err := Approve(context.Background(), client, noDaemon, id, "s-1", "", "/data/plans/"+id)
		assert.NilError(t, err)
		p, _ := cci.FactoryPlan(id)
		assert.Check(t, cmp.Equal(p.Worker, ""))
		assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
	})
	t.Run("a headless session listing the plan is not another session's", func(t *testing.T) {
		client, cci, id := approvablePlan(t, "")
		cci.ClaimFactoryPlanForWorker(id, "w-1")
		cci.SetFactoryWorkerSessions("w-1", fakes.FactoryWorkerSession{ID: "h-1", Kind: "headless", FlowIDs: []string{id}})
		err := Approve(context.Background(), client, noDaemon, id, "s-1", "w-1", "/data/plans/"+id)
		assert.NilError(t, err)
		p, _ := cci.FactoryPlan(id)
		assert.Check(t, cmp.Equal(p.Worker, ""))
		assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
	})
	t.Run("a failed worker sessions read stops approve", func(t *testing.T) {
		client, cci, id := approvablePlan(t, "")
		cci.ClaimFactoryPlanForWorker(id, "w-1")
		cci.DropFactoryWorker("w-1")
		err := Approve(context.Background(), client, noDaemon, id, "s-1", "w-1", "/data/plans/"+id)
		assert.Check(t, err != nil)
		p, _ := cci.FactoryPlan(id)
		assert.Check(t, cmp.Equal(p.Worker, "w-1"))
		assert.Check(t, cmp.Len(p.Events, 0))
	})
	t.Run("a release answering 409 is reported", func(t *testing.T) {
		client, cci, id := approvablePlan(t, "")
		cci.ClaimFactoryPlanForWorker(id, "w-1")
		cci.FailFactoryRelease(id, http.StatusConflict)
		err := Approve(context.Background(), client, noDaemon, id, "s-1", "w-1", "/data/plans/"+id)
		assert.Check(t, cmp.ErrorContains(err, "release the planning session's claim"))
		p, _ := cci.FactoryPlan(id)
		assert.Check(t, cmp.Len(p.Events, 0))
	})
}

// TestApproveReadsTheNewName checks that a plan in refine-units, waiting
// on the developer, is approved as one in refine-proposal is.
func TestApproveReadsTheNewName(t *testing.T) {
	client, cci, id := approvablePlan(t, "s-1")
	cci.SetFactoryPlanState(id, "refine-units")
	err := Approve(context.Background(), client, noDaemon, id, "s-1", "", "/data/plans/"+id)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
}

// approvablePlan is a created plan factory offers to approve for building,
// claimed by holder unless that is "".
func approvablePlan(t *testing.T, holder string) (*circleci.Client, *fakes.FakeCircleCI, string) {
	t.Helper()
	client, cci, id := createdPlan(t, "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	if holder != "" {
		cci.ClaimFactoryPlan(id, holder)
	}
	cci.SetFactoryPlanState(id, "refine-proposal")
	cci.SetFactoryFlowStatus(id, fakes.FactoryFlowStatus{WaitingOn: "human"})
	cci.SetFactoryRequiredEvents(id, "approve-for-building")
	return client, cci, id
}

// posts is the paths of every POST the fake has had.
func posts(cci *fakes.FakeCircleCI) []string {
	var paths []string
	for _, r := range cci.Recorder.AllRequests() {
		if r.Method == http.MethodPost {
			paths = append(paths, r.URL.Path)
		}
	}
	return paths
}

// TestApproveWithAnotherSessionsClaim checks what approving does with a
// claim another session holds, by where that session works.
func TestApproveWithAnotherSessionsClaim(t *testing.T) {
	const holder = "parent"
	for _, tc := range []struct {
		name string
		// dir is where the holder works; "PLAN" stands for the plan's worktree.
		dir         string
		unknown     bool
		lookupErr   error
		wantErr     error
		wantErrText string
		released    bool
	}{
		{
			name:        "a builder in the plan's worktree",
			dir:         "PLAN",
			wantErr:     ErrBeingBuilt,
			wantErrText: "already being built by session parent",
		},
		{
			name:     "a parent planner in its own worktree",
			dir:      "/data/plans/parent-1",
			released: true,
		},
		{
			name:        "an unknown holder",
			unknown:     true,
			wantErr:     ErrBeingBuilt,
			wantErrText: "which factory's daemon does not know",
		},
		{
			name:        "the lookup fails",
			lookupErr:   factory.ErrNoFactoryDaemon,
			wantErr:     factory.ErrNoFactoryDaemon,
			wantErrText: "find where session parent works",
		},
		{
			name:     "a missing holder dir other than the plan's",
			dir:      "/no/such/dir/parent-1",
			released: true,
		},
		{
			name:    "a missing holder dir spelled as the plan's",
			dir:     "PLAN/.",
			wantErr: ErrBeingBuilt,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cci, id := approvablePlan(t, holder)
			ctx := context.Background()
			worktree := "/no/such/data/plans/" + id
			dirs := map[string]string{}
			if !tc.unknown {
				dirs[holder] = strings.Replace(tc.dir, "PLAN", worktree, 1)
			}

			err := Approve(ctx, client, locatedAt(dirs, tc.lookupErr), id, "s-1", "", worktree)
			p, _ := cci.FactoryPlan(id)
			if tc.released {
				assert.NilError(t, err)
				assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
				assert.Check(t, cmp.Equal(p.Session, ""))
				paths := posts(cci)
				assert.Check(t, cmp.DeepEqual(paths[len(paths)-2:], []string{
					"/api/v3/factory/flows/" + id + "/release",
					"/api/v3/factory/events",
				}))
				return
			}
			assert.Check(t, cmp.ErrorIs(err, tc.wantErr))
			if tc.wantErrText != "" {
				assert.Check(t, cmp.ErrorContains(err, tc.wantErrText))
			}
			assert.Check(t, cmp.Len(p.Events, 0))
			assert.Check(t, cmp.Equal(p.Session, holder))
			for _, path := range posts(cci) {
				assert.Check(t, !strings.HasSuffix(path, "/release"), path)
			}
		})
	}
}

// TestReleaseForeign checks what ReleaseForeign does with a claim, by where
// the holder works.
func TestReleaseForeign(t *testing.T) {
	const holder = "S"
	for _, tc := range []struct {
		name string
		// dir is where the holder works; "PLAN" stands for the plan's worktree.
		dir       string
		unknown   bool
		lookupErr error
		// dropClaim has the release answer 404 from the lookup on, as for a flow that moved, so ReleaseClaim fails.
		dropClaim   bool
		wantErr     error
		wantErrText []string
		released    bool
	}{
		{
			name:        "holder in the plan's worktree",
			dir:         "PLAN",
			wantErr:     ErrBeingBuilt,
			wantErrText: []string{"already being built by session S"},
		},
		{
			name:    "unknown holder",
			unknown: true,
			wantErr: ErrBeingBuilt,
			wantErrText: []string{
				"which factory's daemon does not know",
				"factory daemon release",
			},
		},
		{
			name:        "lookup fails",
			lookupErr:   factory.ErrNoFactoryDaemon,
			wantErr:     factory.ErrNoFactoryDaemon,
			wantErrText: []string{"find where session S works"},
		},
		{
			name:     "holder elsewhere",
			dir:      "/data/plans/other",
			released: true,
		},
		{
			name:        "ReleaseClaim fails",
			dir:         "/data/plans/other",
			dropClaim:   true,
			wantErrText: []string{"release session S's claim"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cci, id := approvablePlan(t, holder)
			worktree := "/no/such/data/plans/" + id
			dirs := map[string]string{}
			if !tc.unknown {
				dirs[holder] = strings.Replace(tc.dir, "PLAN", worktree, 1)
			}
			locate := locatedAt(dirs, tc.lookupErr)
			if tc.dropClaim {
				inner := locate
				locate = func(ctx context.Context, session string) (string, error) {
					cci.FailFactoryRelease(id, http.StatusNotFound)
					return inner(ctx, session)
				}
			}

			err := ReleaseForeign(context.Background(), client, locate, id, holder, worktree)
			p, _ := cci.FactoryPlan(id)
			if tc.released {
				assert.NilError(t, err)
				assert.Check(t, cmp.Equal(p.Session, ""))
				assert.Check(t, cmp.DeepEqual(posts(cci)[len(posts(cci))-1:], []string{
					"/api/v3/factory/flows/" + id + "/release",
				}))
				return
			}
			assert.Assert(t, err != nil)
			if tc.wantErr != nil {
				assert.Check(t, cmp.ErrorIs(err, tc.wantErr))
			}
			for _, text := range tc.wantErrText {
				assert.Check(t, cmp.ErrorContains(err, text))
			}
			if tc.dropClaim {
				return
			}
			assert.Check(t, cmp.Equal(p.Session, holder))
			for _, path := range posts(cci) {
				assert.Check(t, !strings.HasSuffix(path, "/release"), path)
			}
		})
	}
}

// TestApproveFollowsSymlinks checks that a holder whose worktree is a
// symlink to the plan's is read as working in the plan's worktree.
func TestApproveFollowsSymlinks(t *testing.T) {
	client, cci, id := approvablePlan(t, "builder")
	worktree := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	err := os.Symlink(worktree, link)
	assert.NilError(t, err)

	err = Approve(context.Background(), client, locatedAt(map[string]string{"builder": link}, nil), id, "s-1", "", worktree)
	assert.Check(t, cmp.ErrorIs(err, ErrBeingBuilt))
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))
	assert.Check(t, cmp.Equal(p.Session, "builder"))
}

// TestApproveWithNoClaim checks that a plan no session claims is
// approved with nothing released.
func TestApproveWithNoClaim(t *testing.T) {
	client, cci, id := approvablePlan(t, "")

	err := Approve(context.Background(), client, noDaemon, id, "s-1", "", "/data/plans/"+id)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
	for _, path := range posts(cci) {
		assert.Check(t, !strings.HasSuffix(path, "/release"), path)
	}
}

// TestApproveStopsWhenTheReleaseFails checks that a claim released
// between reading the holder and releasing it, so the release 404s, stops the
// approval before the event is sent.
func TestApproveStopsWhenTheReleaseFails(t *testing.T) {
	client, cci, id := approvablePlan(t, "parent")
	locate := func(context.Context, string) (string, error) {
		cci.FailFactoryRelease(id, http.StatusNotFound)
		return "/data/plans/parent-1", nil
	}

	err := Approve(context.Background(), client, locate, id, "s-1", "", "/data/plans/"+id)
	var se *circleci.StatusError
	isStatus := errors.As(err, &se)
	assert.Assert(t, isStatus)
	assert.Check(t, cmp.Equal(se.StatusCode, http.StatusNotFound))
	assert.Check(t, cmp.ErrorContains(err, "release session parent's claim"))
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))
}

// TestApproveSendsBuildingWhenFactoryOffersIt checks that a plan whose flow
// waits on the developer and requires approve-for-building is approved.
func TestApproveSendsBuildingWhenFactoryOffersIt(t *testing.T) {
	client, cci, id := createdPlan(t, "# A plan\n\n## 5. Units\n\n1. FNDSF-21 api: add it\n")
	ctx := context.Background()
	cci.ClaimFactoryPlan(id, "s-1")
	cci.SetFactoryPlanState(id, "refine-units")
	cci.SetFactoryFlowStatus(id, fakes.FactoryFlowStatus{WaitingOn: "human"})
	cci.SetFactoryRequiredEvents(id, "approve-for-building")
	cci.SetFactoryApplicableEvents(id, "request-revision", "end")

	err := Approve(ctx, client, noDaemon, id, "s-1", "", "/data/plans/"+id)
	assert.Check(t, err)
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
}

// TestApproveSendsBuildingWhenBothAreRequired checks that a flow requiring
// both approvals is sent approve-for-building, not refused with ErrNoUnits.
func TestApproveSendsBuildingWhenBothAreRequired(t *testing.T) {
	client, cci, id := createdPlan(t, "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	ctx := context.Background()
	cci.ClaimFactoryPlan(id, "s-1")
	cci.SetFactoryPlanState(id, "refine-proposal")
	cci.SetFactoryFlowStatus(id, fakes.FactoryFlowStatus{WaitingOn: "human"})
	cci.SetFactoryRequiredEvents(id, "approve-for-units", "approve-for-building")
	cci.SetFactoryApplicableEvents(id, "address-comments", "end")

	err := Approve(ctx, client, noDaemon, id, "s-1", "", "/data/plans/"+id)
	assert.Check(t, err)
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
}

// TestApproveIgnoresRequestRevision checks that a listed request-revision
// changes nothing: a plan whose flow requires approve-for-building is
// approved beside it.
func TestApproveIgnoresRequestRevision(t *testing.T) {
	client, cci, id := createdPlan(t, "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	ctx := context.Background()
	cci.ClaimFactoryPlan(id, "s-1")
	cci.SetFactoryPlanState(id, "refine-units")
	cci.SetFactoryFlowStatus(id, fakes.FactoryFlowStatus{WaitingOn: "human", Next: []fakes.FactoryTransition{
		{Actor: "human", Event: "approve-for-building", IsRequired: true},
		{Actor: "session", Event: "request-revision"},
		{Actor: "human", Event: "end"},
	}})

	err := Approve(ctx, client, noDaemon, id, "s-1", "", "/data/plans/"+id)
	assert.Check(t, err)
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
}

// TestApproveRefusesWhatFactoryDoesNotRequire checks that Approve sends
// nothing, and keeps the planning session's claim, for a plan waiting on the
// developer whose flow does not require approve-for-building.
func TestApproveRefusesWhatFactoryDoesNotRequire(t *testing.T) {
	for _, tc := range []struct {
		name       string
		required   []string
		applicable []string
		want       error
	}{
		{
			name:       "open questions",
			required:   []string{"answer-open-questions"},
			applicable: []string{"approve-for-units", "approve-for-building", "end"},
			want:       ErrNoApproval,
		},
		{
			name: "nothing listed",
			want: ErrNoApproval,
		},
		{
			name:       "the design is required",
			required:   []string{"approve-for-units"},
			applicable: []string{"approve-for-building", "end"},
			want:       ErrNoUnits,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cci, id := createdPlan(t, "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
			ctx := context.Background()
			cci.ClaimFactoryPlan(id, "s-1")
			cci.SetFactoryPlanState(id, "refine-proposal")
			cci.SetFactoryFlowStatus(id, fakes.FactoryFlowStatus{WaitingOn: "human"})
			cci.SetFactoryRequiredEvents(id, tc.required...)
			cci.SetFactoryApplicableEvents(id, tc.applicable...)

			err := Approve(ctx, client, noDaemon, id, "s-1", "", "/data/plans/"+id)
			assert.Check(t, cmp.ErrorIs(err, tc.want))
			p, _ := cci.FactoryPlan(id)
			assert.Check(t, cmp.Len(p.Events, 0))
			assert.Check(t, cmp.Equal(p.Session, "s-1"))
		})
	}
}
