package factoryplan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
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

// createdStore is a Store whose plan has been created in a fake factory.
func createdStore(t *testing.T) (*Store, *circleci.Client, *fakes.FakeCircleCI) {
	t.Helper()
	client, cci := testClient(t)
	store := NewStore(client)
	err := store.Create(context.Background(), "a plan")
	assert.NilError(t, err)
	return store, client, cci
}

// TestStoreTextReadsThePlanFromFactory checks that Text reads the plan's text
// from factory, where the agent writes it with plan_set, and is empty before
// the plan exists.
func TestStoreTextReadsThePlanFromFactory(t *testing.T) {
	client, _ := testClient(t)
	store := NewStore(client)
	ctx := context.Background()

	text, err := store.Text(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(text, ""))

	err = store.Create(ctx, "a plan")
	assert.NilError(t, err)
	err = client.SetPlanText(ctx, store.ID(), "# A plan\n")
	assert.NilError(t, err)
	text, err = store.Text(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(text, "# A plan\n"))
}

// The plan starts as the skeleton so the plan tools have sections to write
// into.
func TestStoreCreateSetsPlanID(t *testing.T) {
	client, cci := testClient(t)
	store := NewStore(client)

	err := store.Create(context.Background(), "add a --dry-run flag")
	assert.NilError(t, err)
	assert.Assert(t, store.ID() != "")

	p, ok := cci.FactoryPlan(store.ID())
	assert.Assert(t, ok)
	assert.Check(t, cmp.Equal(p.Title, "add a --dry-run flag"))
	assert.Check(t, cmp.Equal(p.Text, skeleton("add a --dry-run flag")))
	assert.Check(t, cmp.Equal(p.State, "Unprioritized"))
}

func TestStoreCommentsRoundTrip(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()

	c, err := store.AddComment(ctx, "quoted part", "do it differently")
	assert.NilError(t, err)
	assert.Check(t, c.ID != "")
	assert.Check(t, cmp.Equal(c.Quote, "quoted part"))
	assert.Check(t, cmp.Equal(c.Body, "do it differently"))
	assert.Check(t, !c.Resolved)

	cs, err := store.Comments(ctx)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(cs, 1))
	assert.Check(t, cmp.Equal(cs[0].ID, c.ID))

	err = store.EditComment(ctx, c.ID, "do it a third way")
	assert.NilError(t, err)
	cs, err = store.Comments(ctx)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(cs, 1))
	assert.Check(t, cmp.Equal(cs[0].Body, "do it a third way"))

	err = store.ResolveComment(ctx, c.ID)
	assert.NilError(t, err)
	cs, err = store.Comments(ctx)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(cs, 1))
	assert.Check(t, cs[0].Resolved)

	err = store.DeleteComment(ctx, c.ID)
	assert.NilError(t, err)
	cs, err = store.Comments(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(cs, 0))
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.Equal(p.Comments, 0))
}

// TestStoreAddressCommentsSendsTheEvent checks that AddressComments sends
// the plan's flow factory's address-comments event, and fails for a plan no
// session has claimed, which has no flow to send it.
func TestStoreAddressCommentsSendsTheEvent(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()

	err := store.AddressComments(ctx)
	assert.Check(t, err != nil, "a plan with no flow took the event")

	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), "Planning - Human")
	err = store.AddressComments(ctx)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"address-comments"}))
}

// TestStoreApproveDesignSendsTheEvent checks that ApproveDesign sends the
// plan's flow factory's approve-for-units event.
func TestStoreApproveDesignSendsTheEvent(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), plan.ReadyState)

	err := store.ApproveDesign(ctx)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-units"}))
}

// TestStoreApproveWaitsForTheReview checks that a plan is approved only once
// its review has passed: a plan still in Planning - Agent may have a review
// running, or findings unfixed.
func TestStoreApproveWaitsForTheReview(t *testing.T) {
	store, client, cci := createdStore(t)
	ctx := context.Background()
	err := client.SetPlanText(ctx, store.ID(), "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	assert.NilError(t, err)
	cci.ClaimFactoryPlan(store.ID(), "s-1")

	cci.SetFactoryPlanState(store.ID(), "Planning - Agent")
	err = store.Approve(ctx, "s-1")
	assert.Check(t, cmp.ErrorIs(err, ErrNotReady))
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.Len(p.Events, 0))
	assert.Check(t, cmp.Equal(p.Session, "s-1"))
}

// TestStoreApproveReleasesThePlanningSession checks that approving releases
// the planning session's claim before it sends the event, so factory hands
// the build to a session that claims the plan to build it rather than to the
// planning session.
func TestStoreApproveReleasesThePlanningSession(t *testing.T) {
	store, client, cci := createdStore(t)
	ctx := context.Background()
	err := client.SetPlanText(ctx, store.ID(), "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	assert.NilError(t, err)
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), plan.ReadyState)

	err = store.Approve(ctx, "s-1")
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
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

// TestStoreApproveKeepsAnotherSessionsClaim checks that approving a plan
// another session claims leaves it that session's, to build.
func TestStoreApproveKeepsAnotherSessionsClaim(t *testing.T) {
	store, client, cci := createdStore(t)
	ctx := context.Background()
	err := client.SetPlanText(ctx, store.ID(), "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	assert.NilError(t, err)
	cci.ClaimFactoryPlan(store.ID(), "builder")
	cci.SetFactoryPlanState(store.ID(), plan.ReadyState)

	err = store.Approve(ctx, "s-1")
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
	assert.Check(t, cmp.Equal(p.Session, "builder"))
}

// TestStoreApproveWaitsForUnits checks that a plan in plan.ReadyState with no
// units is not approved: it waits there after early-plan too, for its design
// to be reviewed, before write-units has written anything to build.
func TestStoreApproveWaitsForUnits(t *testing.T) {
	store, client, cci := createdStore(t)
	ctx := context.Background()
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), plan.ReadyState)

	for _, text := range []string{
		"# A plan\n\n## 1. Outcome\n\nThe flag exists.\n\n## 5. Units\n",
		// A heading in another section is no unit.
		"# A plan\n\n## 2. Design\n\n### Context\n\n## 5. Units\n",
	} {
		err := client.SetPlanText(ctx, store.ID(), text)
		assert.NilError(t, err)
		err = store.Approve(ctx, "s-1")
		assert.Check(t, cmp.ErrorIs(err, ErrNoUnits), "text %q", text)
	}
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.Len(p.Events, 0))
}

// TestStoreWritesFailBeforeCreate checks that a write before Create has
// addressed the plan fails rather than silently doing nothing, while reads
// come back empty.
func TestStoreWritesFailBeforeCreate(t *testing.T) {
	client, _ := testClient(t)
	store := NewStore(client)
	ctx := context.Background()

	_, err := store.AddComment(ctx, "quote", "body")
	assert.Check(t, cmp.ErrorIs(err, errNotCreated))
	err = store.AddressComments(ctx)
	assert.Check(t, cmp.ErrorIs(err, errNotCreated))
	err = store.EditComment(ctx, "comment-1", "body")
	assert.Check(t, cmp.ErrorIs(err, errNotCreated))
	err = store.ResolveComment(ctx, "comment-1")
	assert.Check(t, cmp.ErrorIs(err, errNotCreated))
	err = store.DeleteComment(ctx, "comment-1")
	assert.Check(t, cmp.ErrorIs(err, errNotCreated))

	state, err := store.State(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(state, ""))
	cs, err := store.Comments(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(cs, 0))
}

// TestStoreHolderReadsTheClaimFromFactory checks that holder is the session
// the plan's flow says holds its claim, and "" before the plan exists, before
// it has a flow, and once its claim is released.
func TestStoreHolderReadsTheClaimFromFactory(t *testing.T) {
	client, cci := testClient(t)
	store := NewStore(client)
	ctx := context.Background()

	h, err := store.holder(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(h, ""))

	err = store.Create(ctx, "a plan")
	assert.NilError(t, err)
	h, err = store.holder(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(h, ""))

	cci.ClaimFactoryPlan(store.ID(), "s-1")
	h, err = store.holder(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(h, "s-1"))

	cci.ReleaseFactoryPlan(store.ID())
	h, err = store.holder(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(h, ""))
}
