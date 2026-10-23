package cmd

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// apiStoreTestClient builds a circleci.Client against a fake factory server,
// and returns it alongside the fake so a test can inspect its state.
func apiStoreTestClient(t *testing.T) (*circleci.Client, *fakes.FakeCircleCI) {
	t.Helper()
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return client, cci
}

// TestAPIStoreTextReadsThePlanFromFactory checks that Text reads the plan's
// text from factory, where the agent writes it with plan_set, and is empty
// before the plan exists.
func TestAPIStoreTextReadsThePlanFromFactory(t *testing.T) {
	client, _ := apiStoreTestClient(t)
	store := &apiStore{api: client}
	ctx := context.Background()

	text, err := store.Text(ctx)
	assert.NilError(t, err)
	assert.Equal(t, text, "")

	assert.NilError(t, store.Create(ctx, "a plan"))
	assert.NilError(t, client.SetPlanText(ctx, store.id(), "# A plan\n"))
	text, err = store.Text(ctx)
	assert.NilError(t, err)
	assert.Equal(t, text, "# A plan\n")
}

// TestAPIStoreCreateSetsPlanID checks that Create posts the title with the
// plan skeleton as its text, so the plan tools have sections to write into,
// and keeps the returned ID so every later call addresses that plan.
func TestAPIStoreCreateSetsPlanID(t *testing.T) {
	client, cci := apiStoreTestClient(t)
	store := &apiStore{api: client}

	assert.NilError(t, store.Create(context.Background(), "add a --dry-run flag"))
	assert.Assert(t, store.id() != "")

	plan, ok := cci.FactoryPlan(store.id())
	assert.Assert(t, ok)
	assert.Equal(t, plan.Title, "add a --dry-run flag")
	assert.Equal(t, plan.Text, planSkeleton("add a --dry-run flag"))
	assert.Equal(t, plan.State, "Unprioritized")
}

// TestAPIStoreCommentsRoundTrip checks that AddComment, EditComment,
// ResolveComment and DeleteComment each reach the real API, so a comment
// queued in the planning screen is no longer permanently stuck: this is the
// behavior the old localPlanStore stopgap never had, always failing instead.
func TestAPIStoreCommentsRoundTrip(t *testing.T) {
	client, cci := apiStoreTestClient(t)
	store := &apiStore{api: client}
	ctx := context.Background()
	assert.NilError(t, store.Create(ctx, "a plan"))

	c, err := store.AddComment(ctx, "quoted part", "do it differently")
	assert.NilError(t, err)
	assert.Assert(t, c.ID != "")
	assert.Equal(t, c.Quote, "quoted part")
	assert.Equal(t, c.Body, "do it differently")
	assert.Equal(t, c.Resolved, false)

	cs, err := store.Comments(ctx)
	assert.NilError(t, err)
	assert.Equal(t, len(cs), 1)
	assert.Equal(t, cs[0].ID, c.ID)

	assert.NilError(t, store.EditComment(ctx, c.ID, "do it a third way"))
	cs, err = store.Comments(ctx)
	assert.NilError(t, err)
	assert.Equal(t, cs[0].Body, "do it a third way")

	assert.NilError(t, store.ResolveComment(ctx, c.ID))
	cs, err = store.Comments(ctx)
	assert.NilError(t, err)
	assert.Equal(t, cs[0].Resolved, true)

	assert.NilError(t, store.DeleteComment(ctx, c.ID))
	cs, err = store.Comments(ctx)
	assert.NilError(t, err)
	assert.Equal(t, len(cs), 0)
	plan, _ := cci.FactoryPlan(store.id())
	assert.Equal(t, plan.Comments, 0)
}

// TestAPIStoreMoveFollowsTheEdge checks that Move reaches the real API and
// the fake's edge check accepts the Planning - Human to Planning - Agent
// move sending comments takes.
func TestAPIStoreMoveFollowsTheEdge(t *testing.T) {
	client, cci := apiStoreTestClient(t)
	store := &apiStore{api: client}
	ctx := context.Background()
	assert.NilError(t, store.Create(ctx, "a plan"))
	cci.SetFactoryPlanState(store.id(), "Planning - Human")

	assert.NilError(t, store.Move(ctx, "Planning - Agent"))
	state, err := store.State(ctx)
	assert.NilError(t, err)
	assert.Equal(t, state, "Planning - Agent")
}

// TestAPIStoreReadyWaitsForTheReview checks that the plan sent to the
// factory is factory's text, and only once its review has passed: a plan
// still in Planning - Agent may have a review running, or findings unfixed.
func TestAPIStoreReadyWaitsForTheReview(t *testing.T) {
	client, cci := apiStoreTestClient(t)
	store := &apiStore{api: client}
	ctx := context.Background()
	assert.NilError(t, store.Create(ctx, "a plan"))
	assert.NilError(t, client.SetPlanText(ctx, store.id(), "# A plan\n"))

	cci.SetFactoryPlanState(store.id(), "Planning - Agent")
	_, err := store.ready(ctx)
	assert.ErrorIs(t, err, errPlanNotReady)

	cci.SetFactoryPlanState(store.id(), plan.ReadyState)
	text, err := store.ready(ctx)
	assert.NilError(t, err)
	assert.Equal(t, text, "# A plan\n")
}

// TestAPIStoreWritesFailBeforeCreate checks that a write before Create has
// addressed the plan fails rather than silently doing nothing, unlike the
// old localPlanStore stopgap's no-op Create and Move.
func TestAPIStoreWritesFailBeforeCreate(t *testing.T) {
	client, _ := apiStoreTestClient(t)
	store := &apiStore{api: client}
	ctx := context.Background()

	_, err := store.AddComment(ctx, "quote", "body")
	assert.ErrorIs(t, err, errPlanNotCreated)
	assert.ErrorIs(t, store.Move(ctx, "Planning - Agent"), errPlanNotCreated)

	state, err := store.State(ctx)
	assert.NilError(t, err)
	assert.Equal(t, state, "")
	cs, err := store.Comments(ctx)
	assert.NilError(t, err)
	assert.Equal(t, len(cs), 0)
}

// TestWithPlanClaimOnlyPrependsOnce checks that the plan-claim instruction
// goes only on the first message sent after the plan exists: the agent
// needs the plan's ID once, to claim it, and every message after would
// otherwise repeat an instruction it has already followed.
func TestWithPlanClaimOnlyPrependsOnce(t *testing.T) {
	msg, claimed := withPlanClaim(false, "plan-1", "hello")
	assert.Equal(t, claimed, true)
	assert.Equal(t, msg, fmt.Sprintf(planClaim, "plan-1", "hello"))

	msg, claimed = withPlanClaim(claimed, "plan-1", "a later message")
	assert.Equal(t, claimed, true)
	assert.Equal(t, msg, "a later message")
}

// TestWithPlanClaimWaitsForAnID checks that a message sent before the plan
// has been created passes through unchanged, since there is no ID yet to
// tell the agent to claim.
func TestWithPlanClaimWaitsForAnID(t *testing.T) {
	msg, claimed := withPlanClaim(false, "", "hello")
	assert.Equal(t, claimed, false)
	assert.Equal(t, msg, "hello")
}

// TestAPIStoreHolderReadsTheClaimFromFactory checks that holder is the
// session factory says holds the plan's claim, and "" before the plan exists
// or while nothing claims it.
func TestAPIStoreHolderReadsTheClaimFromFactory(t *testing.T) {
	client, cci := apiStoreTestClient(t)
	store := &apiStore{api: client}
	ctx := context.Background()

	h, err := store.holder(ctx)
	assert.NilError(t, err)
	assert.Equal(t, h, "")

	assert.NilError(t, store.Create(ctx, "a plan"))
	h, err = store.holder(ctx)
	assert.NilError(t, err)
	assert.Equal(t, h, "")

	cci.ClaimFactoryPlan(store.id(), "s-1", "/src")
	h, err = store.holder(ctx)
	assert.NilError(t, err)
	assert.Equal(t, h, "s-1")
}

func TestPlanOriginNamesTheFactoryPlan(t *testing.T) {
	assert.Equal(t, planOrigin("1234"), "factory plan 1234")
	assert.Equal(t, planOrigin(""), "")
}
