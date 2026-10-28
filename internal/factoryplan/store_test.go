package factoryplan

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

// newStore is NewStore with a namer that gives no name, so Create titles
// the plan with fallbackTitle and runs no claude.
func newStore(client *circleci.Client, id string) *Store {
	s := NewStore(client, id)
	s.name = namedAs("", errors.New("no claude in tests"))
	return s
}

// namedAs is a namer that gives name and err whatever the message.
func namedAs(name string, err error) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return name, err }
}

// createdStore is a Store whose plan has been created in a fake factory.
func createdStore(t *testing.T) (*Store, *circleci.Client, *fakes.FakeCircleCI) {
	t.Helper()
	client, cci := testClient(t)
	store := newStore(client, "")
	err := store.Create(context.Background(), "a plan")
	assert.NilError(t, err)
	return store, client, cci
}

// TestStoreTextReadsThePlanFromFactory checks that Text reads the plan's text
// from factory, where the agent writes it with plan_set, and is empty before
// the plan exists.
func TestStoreTextReadsThePlanFromFactory(t *testing.T) {
	client, _ := testClient(t)
	store := newStore(client, "")
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

// A new plan is titled with Claude's name for it, made a slug, or with a
// slug of the message's first line when Claude gives none, and starts as
// the skeleton under that title.
func TestStoreCreateNamesThePlan(t *testing.T) {
	const msg = "add a --dry-run flag"
	for _, tc := range []struct {
		name    string
		message string
		reply   string
		err     error
		want    string
	}{
		{
			name:    "claude's name",
			message: msg,
			reply:   "dry-run-flag",
			want:    "dry-run-flag",
		},
		{
			name:    "quoted with a period",
			message: msg,
			reply:   `"Dry-run flag."`,
			want:    "dry-run-flag",
		},
		{
			name:    "cut to three words",
			message: msg,
			reply:   "add dry run flag support",
			want:    "add-dry-run",
		},
		{
			name:    "one long word",
			message: msg,
			reply:   strings.Repeat("é", 50),
			want:    strings.Repeat("é", 40),
		},
		{
			name:    "namer fails",
			message: msg,
			err:     errors.New("claude exited 1"),
			want:    "add-a-dry-run-flag",
		},
		{
			name:    "empty reply",
			message: msg,
			reply:   "",
			want:    "add-a-dry-run-flag",
		},
		{
			name:    "reply with no words",
			message: msg,
			reply:   "?!",
			want:    "add-a-dry-run-flag",
		},
		{
			name:    "no words anywhere",
			message: "?!\nsecond line",
			err:     errors.New("no"),
			want:    "plan",
		},
		{
			name:    "empty message",
			message: "",
			err:     errors.New("no"),
			want:    "plan",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cci := testClient(t)
			store := NewStore(client, "")
			store.name = namedAs(tc.reply, tc.err)

			err := store.Create(context.Background(), tc.message)
			assert.NilError(t, err)

			p, ok := cci.FactoryPlan(store.ID())
			assert.Assert(t, ok)
			assert.Check(t, cmp.Equal(p.Title, tc.want))
			assert.Check(t, cmp.Equal(p.Text, skeleton(tc.want)))
			assert.Check(t, cmp.Equal(p.State, "Unprioritized"))
		})
	}
}

// With no claude installed, the real namer fails and the plan is still made,
// titled with the slug.
func TestStoreCreateWithoutClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	client, cci := testClient(t)
	store := NewStore(client, "")

	err := store.Create(context.Background(), "add a --dry-run flag")
	assert.NilError(t, err)

	p, ok := cci.FactoryPlan(store.ID())
	assert.Assert(t, ok)
	assert.Check(t, cmp.Equal(p.Title, "add-a-dry-run-flag"))
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
// the plan's flow factory's address-comments event, and fails for a plan with
// no flow to send it.
func TestStoreAddressCommentsSendsTheEvent(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()
	cci.DropFactoryFlow(store.ID())

	err := store.AddressComments(ctx)
	assert.Check(t, err != nil, "a plan with no flow took the event")

	store, _, cci = createdStore(t)
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), "Planning - Human")
	err = store.AddressComments(ctx)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"address-comments"}))
}

// TestStoreResolveConflictsSendsTheEvent checks that ResolveConflicts sends
// the plan's flow factory's resolve-conflicts event, and fails for a plan
// with no flow to send it.
func TestStoreResolveConflictsSendsTheEvent(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()
	cci.DropFactoryFlow(store.ID())

	err := store.ResolveConflicts(ctx)
	assert.Check(t, cmp.ErrorContains(err, "send the resolve-conflicts event"))

	store, _, cci = createdStore(t)
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), "Integrating - Human")
	cci.SetFactoryApplicableEvents(store.ID(), "address-comments", "resolve-conflicts", "abandon-integrate", "abandon")
	err = store.ResolveConflicts(ctx)
	assert.Check(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"resolve-conflicts"}))
}

// TestStoreApproveDesignSendsTheEvent checks that ApproveDesign sends the
// plan's flow factory's approve-for-units event.
func TestStoreApproveDesignSendsTheEvent(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), plan.ReadyState)
	cci.SetFactoryApplicableEvents(store.ID(), "address-comments", "approve-for-units", "abandon")

	err := store.ApproveDesign(ctx)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-units"}))
}

// TestStoreApproveDesignRefusesWhatFactoryDoesNotOffer checks that
// ApproveDesign sends nothing for a plan whose flow does not offer design
// approval, and keeps the planning session's claim.
func TestStoreApproveDesignRefusesWhatFactoryDoesNotOffer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
		want   error
	}{
		{
			name:   "questions open",
			events: []string{"address-comments", "abandon"},
			want:   ErrQuestionsOpen,
		},
		{
			name:   "only building offered",
			events: []string{"approve-for-building"},
			want:   ErrDesignApproved,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, cci := createdStore(t)
			ctx := context.Background()
			cci.ClaimFactoryPlan(store.ID(), "s-1")
			cci.SetFactoryPlanState(store.ID(), plan.ReadyState)
			cci.SetFactoryApplicableEvents(store.ID(), tc.events...)

			err := store.ApproveDesign(ctx)
			assert.Check(t, cmp.ErrorIs(err, tc.want))
			p, _ := cci.FactoryPlan(store.ID())
			assert.Check(t, cmp.Len(p.Events, 0))
			assert.Check(t, cmp.Equal(p.Session, "s-1"))
		})
	}
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

	cci.DropFactoryFlow(store.ID())
	err = store.Approve(ctx, "s-1")
	assert.Check(t, cmp.ErrorIs(err, ErrNotReady))
	p, _ = cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.Len(p.Events, 0))
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
	cci.SetFactoryApplicableEvents(store.ID(), "approve-for-building")

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
	cci.SetFactoryApplicableEvents(store.ID(), "approve-for-building")

	err = store.Approve(ctx, "s-1")
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
	assert.Check(t, cmp.Equal(p.Session, "builder"))
}

// TestStoreApproveSendsBuildingWhenFactoryOffersIt checks that a plan
// whose flow offers only building approval is approved even when its units
// are a list rather than headings, as a parent plan's sub-plans are.
func TestStoreApproveSendsBuildingWhenFactoryOffersIt(t *testing.T) {
	store, client, cci := createdStore(t)
	ctx := context.Background()
	err := client.SetPlanText(ctx, store.ID(), "# A plan\n\n## 5. Units\n\n1. FNDSF-21 api: add it\n")
	assert.NilError(t, err)
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), plan.ReadyState)
	cci.SetFactoryApplicableEvents(store.ID(), "address-comments", "approve-for-building", "abandon")

	err = store.Approve(ctx, "s-1")
	assert.Check(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
}

// TestStoreApproveRefusesAPlanWithQuestionsOpen checks that Approve sends
// nothing for a plan whose flow offers neither approval, and keeps the
// planning session's claim.
func TestStoreApproveRefusesAPlanWithQuestionsOpen(t *testing.T) {
	store, client, cci := createdStore(t)
	ctx := context.Background()
	err := client.SetPlanText(ctx, store.ID(), "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	assert.NilError(t, err)
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), plan.ReadyState)
	cci.SetFactoryApplicableEvents(store.ID(), "address-comments", "abandon")

	err = store.Approve(ctx, "s-1")
	assert.Check(t, cmp.ErrorIs(err, ErrQuestionsOpen))
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.Len(p.Events, 0))
	assert.Check(t, cmp.Equal(p.Session, "s-1"))
}

// TestStoreApproveWaitsForUnits checks that Approve refuses a plan whose design is what waits for approval.
func TestStoreApproveWaitsForUnits(t *testing.T) {
	store, client, cci := createdStore(t)
	ctx := context.Background()
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), plan.ReadyState)

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
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := client.SetPlanText(ctx, store.ID(), tc.text)
			assert.NilError(t, err)
			cci.SetFactoryApplicableEvents(store.ID(), tc.events...)
			err = store.Approve(ctx, "s-1")
			assert.Check(t, cmp.ErrorIs(err, ErrNoUnits))
		})
	}
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.Len(p.Events, 0))
}

// TestStoreWritesFailBeforeCreate checks that a write before Create has
// addressed the plan fails rather than silently doing nothing, while reads
// come back empty.
func TestStoreWritesFailBeforeCreate(t *testing.T) {
	client, _ := testClient(t)
	store := newStore(client, "")
	ctx := context.Background()

	_, err := store.AddComment(ctx, "quote", "body")
	assert.Check(t, cmp.ErrorIs(err, errNotCreated))
	err = store.AddressComments(ctx)
	assert.Check(t, cmp.ErrorIs(err, errNotCreated))
	err = store.ResolveConflicts(ctx)
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
// a session claims it, and once its claim is released.
func TestStoreHolderReadsTheClaimFromFactory(t *testing.T) {
	client, cci := testClient(t)
	store := newStore(client, "")
	ctx := context.Background()

	h, err := store.Holder(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(h, ""))

	err = store.Create(ctx, "a plan")
	assert.NilError(t, err)
	h, err = store.Holder(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(h, ""))

	cci.ClaimFactoryPlan(store.ID(), "s-1")
	h, err = store.Holder(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(h, "s-1"))

	cci.ReleaseFactoryPlan(store.ID())
	h, err = store.Holder(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(h, ""))
}

// TestStoreFlowReadsTheFlow checks that Flow is the events the plan's flow
// lists as applicable and the category of the task it has out, and empty
// before the plan exists or has a flow.
func TestStoreFlowReadsTheFlow(t *testing.T) {
	client, cci := testClient(t)
	store := newStore(client, "")
	ctx := context.Background()

	f, err := store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(f.Events, 0))
	assert.Check(t, cmp.Equal(f.Task, ""))

	err = store.Create(ctx, "a plan")
	assert.NilError(t, err)
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(f.Events, 0))
	assert.Check(t, cmp.Equal(f.Task, ""))

	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryApplicableEvents(store.ID(), "address-comments", "abandon")
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(f.Events, []string{"address-comments", "abandon"}))
	assert.Check(t, cmp.Equal(f.Task, ""), "a flow with no task out")

	cci.SetFactoryTask(store.ID(), "early-plan")
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(f.Task, "early-plan"))

	// An older factory references the task by ID alone, with no category.
	cci.SetFactoryTaskIDOnly(store.ID())
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(f.Task, ""))

	cci.DropFactoryFlow(store.ID())
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(f.Events, 0))
	assert.Check(t, cmp.Equal(f.Task, ""))
}

// TestStoreStateReadsTheFlow checks that State is the plan's flow state, ""
// before the plan exists or once it has no flow, and an error when factory
// fails.
func TestStoreStateReadsTheFlow(t *testing.T) {
	store, client, cci := createdStore(t)
	ctx := context.Background()

	cci.SetFactoryPlanState(store.ID(), "Planning - Human")
	st, err := store.State(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(st, "Planning - Human"))

	cci.DropFactoryFlow(store.ID())
	st, err = store.State(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(st, ""))

	// The fake answers 400 for an ID that is no UUID: an error that is not a 404.
	_, err = NewStore(client, "not-a-uuid").State(ctx)
	assert.Check(t, err != nil, "a failed flow read is not read as no state")
}

func TestStoreSetTextReplacesThePlanInFactory(t *testing.T) {
	client, _ := testClient(t)
	store := newStore(client, "")
	ctx := context.Background()

	err := store.SetText(ctx, "# A plan\n")
	assert.Check(t, cmp.ErrorIs(err, errNotCreated))

	err = store.Create(ctx, "a plan")
	assert.NilError(t, err)
	err = store.SetText(ctx, "# A plan\n\n## 1. Outcome\n\nEdited by hand.\n")
	assert.NilError(t, err)
	text, err := store.Text(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(text, "# A plan\n\n## 1. Outcome\n\nEdited by hand.\n"))
}
