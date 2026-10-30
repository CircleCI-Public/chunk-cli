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

// TestApproveWaitsForTheReview checks that a plan is approved only once
// its review has passed: a plan still in Planning - Agent may have a review
// running, or findings unfixed.
func TestApproveWaitsForTheReview(t *testing.T) {
	client, cci, id := createdPlan(t, "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	ctx := context.Background()
	cci.ClaimFactoryPlan(id, "s-1")

	cci.SetFactoryPlanState(id, "Planning - Agent")
	err := Approve(ctx, client, noDaemon, id, "s-1", "/data/plans/"+id)
	assert.Check(t, cmp.ErrorIs(err, ErrNotReady))
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))
	assert.Check(t, cmp.Equal(p.Session, "s-1"))

	cci.SetFactoryPlanState(id, "refine-units")
	cci.SetFactoryApplicableEvents(id, "approve-for-building")
	cci.SetFactoryTask(id, "review-plan")
	err = Approve(ctx, client, noDaemon, id, "s-1", "/data/plans/"+id)
	assert.Check(t, cmp.ErrorIs(err, ErrNotReady), "a review running")
	p, _ = cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))

	cci.DropFactoryFlow(id)
	err = Approve(ctx, client, noDaemon, id, "s-1", "/data/plans/"+id)
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

	err := Approve(context.Background(), client, noDaemon, id, "s-1", "/data/plans/"+id)
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
	cci.SetFactoryPlanState(id, "Planning - Human")
	cci.SetFactoryApplicableEvents(id, "approve-for-building")

	err := Approve(ctx, client, noDaemon, id, "s-1", "/data/plans/"+id)
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
		"/api/v3/factory/sessions/s-1/release",
		"/api/v3/factory/events",
	}))
}

// TestApproveReadsTheNewName checks that a plan in refine-units, waiting
// on the developer, is approved as one in Planning - Human is.
func TestApproveReadsTheNewName(t *testing.T) {
	client, cci, id := approvablePlan(t, "s-1")
	cci.SetFactoryPlanState(id, "refine-units")
	err := Approve(context.Background(), client, noDaemon, id, "s-1", "/data/plans/"+id)
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
	cci.SetFactoryPlanState(id, "Planning - Human")
	cci.SetFactoryApplicableEvents(id, "approve-for-building")
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

			err := Approve(ctx, client, locatedAt(dirs, tc.lookupErr), id, "s-1", worktree)
			p, _ := cci.FactoryPlan(id)
			if tc.released {
				assert.NilError(t, err)
				assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
				assert.Check(t, cmp.Equal(p.Session, ""))
				paths := posts(cci)
				assert.Check(t, cmp.DeepEqual(paths[len(paths)-2:], []string{
					"/api/v3/factory/sessions/parent/release",
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
				assert.Check(t, !strings.HasPrefix(path, "/api/v3/factory/sessions/"), path)
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

	err = Approve(context.Background(), client, locatedAt(map[string]string{"builder": link}, nil), id, "s-1", worktree)
	assert.Check(t, cmp.ErrorIs(err, ErrBeingBuilt))
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))
	assert.Check(t, cmp.Equal(p.Session, "builder"))
}

// TestApproveWithNoClaim checks that a plan no session claims is
// approved with nothing released.
func TestApproveWithNoClaim(t *testing.T) {
	client, cci, id := approvablePlan(t, "")

	err := Approve(context.Background(), client, noDaemon, id, "s-1", "/data/plans/"+id)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
	for _, path := range posts(cci) {
		assert.Check(t, !strings.HasPrefix(path, "/api/v3/factory/sessions/"), path)
	}
}

// TestApproveStopsWhenTheReleaseFails checks that a claim released
// between reading the holder and releasing it, so the release 404s, stops the
// approval before the event is sent.
func TestApproveStopsWhenTheReleaseFails(t *testing.T) {
	client, cci, id := approvablePlan(t, "parent")
	locate := func(context.Context, string) (string, error) {
		cci.ReleaseFactoryPlan(id)
		return "/data/plans/parent-1", nil
	}

	err := Approve(context.Background(), client, locate, id, "s-1", "/data/plans/"+id)
	var se *circleci.StatusError
	isStatus := errors.As(err, &se)
	assert.Assert(t, isStatus)
	assert.Check(t, cmp.Equal(se.StatusCode, http.StatusNotFound))
	assert.Check(t, cmp.ErrorContains(err, "release session parent's claim"))
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))
}

// TestApproveSendsBuildingWhenFactoryOffersIt checks that a plan
// whose flow offers only building approval is approved even when its units
// are a list rather than headings, as a parent plan's sub-plans are.
func TestApproveSendsBuildingWhenFactoryOffersIt(t *testing.T) {
	client, cci, id := createdPlan(t, "# A plan\n\n## 5. Units\n\n1. FNDSF-21 api: add it\n")
	ctx := context.Background()
	cci.ClaimFactoryPlan(id, "s-1")
	cci.SetFactoryPlanState(id, "Planning - Human")
	cci.SetFactoryApplicableEvents(id, "address-comments", "approve-for-building", "abandon")

	err := Approve(ctx, client, noDaemon, id, "s-1", "/data/plans/"+id)
	assert.Check(t, err)
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
}

// TestApproveIgnoresRequestRevision checks that a plan in Planning - Human is
// approved though its next lists request-revision for a session, as factory
// lists it in every live state without waiting on it.
func TestApproveIgnoresRequestRevision(t *testing.T) {
	client, cci, id := createdPlan(t, "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	ctx := context.Background()
	cci.ClaimFactoryPlan(id, "s-1")
	cci.SetFactoryPlanState(id, "Planning - Human")
	cci.SetFactoryFlowStatus(id, fakes.FactoryFlowStatus{Next: []fakes.FactoryTransition{
		{Actor: "human", Event: "address-comments"},
		{Actor: "human", Event: "approve-for-building"},
		{Actor: "session", Event: "request-revision"},
		{Actor: "human", Event: "abandon"},
	}})

	err := Approve(ctx, client, noDaemon, id, "s-1", "/data/plans/"+id)
	assert.Check(t, err)
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
}

// TestApproveRefusesAPlanWithQuestionsOpen checks that Approve sends
// nothing for a plan whose flow offers neither approval, and keeps the
// planning session's claim.
func TestApproveRefusesAPlanWithQuestionsOpen(t *testing.T) {
	client, cci, id := createdPlan(t, "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	ctx := context.Background()
	cci.ClaimFactoryPlan(id, "s-1")
	cci.SetFactoryPlanState(id, "Planning - Human")
	cci.SetFactoryApplicableEvents(id, "address-comments", "abandon")

	err := Approve(ctx, client, noDaemon, id, "s-1", "/data/plans/"+id)
	assert.Check(t, cmp.ErrorIs(err, ErrQuestionsOpen))
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))
	assert.Check(t, cmp.Equal(p.Session, "s-1"))
}

// TestApproveWaitsForUnits checks that Approve refuses a plan whose design is what waits for approval.
func TestApproveWaitsForUnits(t *testing.T) {
	client, cci, id := createdPlan(t, "# A plan\n")
	ctx := context.Background()
	cci.ClaimFactoryPlan(id, "s-1")
	cci.SetFactoryPlanState(id, "Planning - Human")

	for _, tc := range []struct {
		name   string
		text   string
		events []string
	}{
		{
			name:   "no units",
			text:   "# A plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n",
			events: []string{"approve-for-units", "approve-for-building"},
		},
		{
			name:   "a heading in another section is no unit",
			text:   "# A plan\n\n## 2. Design\n\n### Context\n\n## 5. Units\n",
			events: []string{"approve-for-units", "approve-for-building"},
		},
		{
			name:   "factory offers only design approval",
			text:   "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n",
			events: []string{"address-comments", "approve-for-units", "abandon"},
		},
		{
			name:   "fenced headings are no units",
			text:   "# A plan\n\n## 5. Units\n\n```md\n### Unit 1: example\n```\n",
			events: []string{"approve-for-units", "approve-for-building"},
		},
		{
			name:   "only done units still count",
			text:   "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag (done)\n",
			events: []string{"address-comments", "approve-for-units", "abandon"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := client.SetPlanText(ctx, id, tc.text)
			assert.NilError(t, err)
			cci.SetFactoryApplicableEvents(id, tc.events...)
			err = Approve(ctx, client, noDaemon, id, "s-1", "/data/plans/"+id)
			assert.Check(t, cmp.ErrorIs(err, ErrNoUnits))
		})
	}
	p, _ := cci.FactoryPlan(id)
	assert.Check(t, cmp.Len(p.Events, 0))
}

// TestApproveCountsDoneUnits checks that a plan whose only unit is done
// has units: with both approvals offered, Approve sends the plan rather than
// refusing it with ErrNoUnits.
func TestApproveCountsDoneUnits(t *testing.T) {
	client, cci, id := createdPlan(t, "# A plan\n")
	ctx := context.Background()
	cci.SetFactoryPlanState(id, "Planning - Human")
	err := client.SetPlanText(ctx, id, "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag (done)\n")
	assert.NilError(t, err)
	cci.SetFactoryApplicableEvents(id, "approve-for-units", "approve-for-building")

	err = Approve(ctx, client, noDaemon, id, "s-1", "/data/plans/"+id)
	assert.Check(t, !errors.Is(err, ErrNoUnits), "a done unit was not counted: %v", err)
	assert.NilError(t, err)
}
