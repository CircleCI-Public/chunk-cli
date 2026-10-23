package cmd

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
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

// TestAPIStoreTextReadsThePlanFile checks that Text delegates to the
// session's own plan file, the way chunk factory plan has always kept it:
// nothing in factory's real API is ever told what Claude has written here,
// so reading it back from there would show nothing.
func TestAPIStoreTextReadsThePlanFile(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	client, _ := apiStoreTestClient(t)
	store := &apiStore{api: client, session: &factory.PlanSession{PlanPath: planPath}}

	text, err := store.Text(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, text, "")

	assert.NilError(t, os.WriteFile(planPath, []byte("# A plan\n"), 0o600))
	text, err = store.Text(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, text, "# A plan\n")
}

// TestAPIStoreCreateSetsPlanID checks that Create posts the title with an
// empty text, as the API requires, and keeps the returned ID on the
// session so every later call addresses that plan.
func TestAPIStoreCreateSetsPlanID(t *testing.T) {
	client, cci := apiStoreTestClient(t)
	session := &factory.PlanSession{}
	store := &apiStore{api: client, session: session}

	assert.NilError(t, store.Create(context.Background(), "add a --dry-run flag"))
	assert.Assert(t, session.PlanID != "")

	plan, ok := cci.FactoryPlan(session.PlanID)
	assert.Assert(t, ok)
	assert.Equal(t, plan.Title, "add a --dry-run flag")
	assert.Equal(t, plan.Text, "")
	assert.Equal(t, plan.State, "Unprioritized")
}

// TestAPIStoreCommentsRoundTrip checks that AddComment, EditComment,
// ResolveComment and DeleteComment each reach the real API, so a comment
// queued in the planning screen is no longer permanently stuck: this is the
// behavior the old localPlanStore stopgap never had, always failing instead.
func TestAPIStoreCommentsRoundTrip(t *testing.T) {
	client, cci := apiStoreTestClient(t)
	session := &factory.PlanSession{}
	store := &apiStore{api: client, session: session}
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
	plan, _ := cci.FactoryPlan(session.PlanID)
	assert.Equal(t, plan.Comments, 0)
}

// TestAPIStoreMoveFollowsTheEdge checks that Move reaches the real API and
// the fake's edge check accepts the Planning - Human to Planning - Agent
// move sending comments takes.
func TestAPIStoreMoveFollowsTheEdge(t *testing.T) {
	client, cci := apiStoreTestClient(t)
	session := &factory.PlanSession{}
	store := &apiStore{api: client, session: session}
	ctx := context.Background()
	assert.NilError(t, store.Create(ctx, "a plan"))
	cci.SetFactoryPlanState(session.PlanID, "Planning - Human")

	assert.NilError(t, store.Move(ctx, "Planning - Agent"))
	state, err := store.State(ctx)
	assert.NilError(t, err)
	assert.Equal(t, state, "Planning - Agent")
}

// TestAPIStoreWritesFailBeforeCreate checks that a write before Create has
// addressed the plan fails rather than silently doing nothing, unlike the
// old localPlanStore stopgap's no-op Create and Move.
func TestAPIStoreWritesFailBeforeCreate(t *testing.T) {
	client, _ := apiStoreTestClient(t)
	store := &apiStore{api: client, session: &factory.PlanSession{}}
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
	assert.Equal(t, msg, "Plan plan-1 in factory is for this conversation; claim it with plan_claim.\n\nhello")

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

// TestStorePlanIDReadsTheCreatedPlan checks the "not sent" message's lookup
// of the plan's ID: empty for a Store that never created one, and the
// created ID once it has.
func TestStorePlanIDReadsTheCreatedPlan(t *testing.T) {
	client, _ := apiStoreTestClient(t)
	session := &factory.PlanSession{}
	store := &apiStore{api: client, session: session}
	assert.Equal(t, storePlanID(store), "")

	assert.NilError(t, store.Create(context.Background(), "a plan"))
	assert.Equal(t, storePlanID(store), session.PlanID)
}
