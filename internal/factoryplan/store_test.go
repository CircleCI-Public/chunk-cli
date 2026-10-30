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
	"github.com/CircleCI-Public/chunk-cli/internal/planapprove"
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
	_, err := store.Create(context.Background(), "a plan")
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

	_, err = store.Create(ctx, "a plan")
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

			title, err := store.Create(context.Background(), tc.message)
			assert.NilError(t, err)
			assert.Check(t, cmp.Equal(title, tc.want))

			p, ok := cci.FactoryPlan(store.ID())
			assert.Assert(t, ok)
			assert.Check(t, cmp.Equal(p.Title, title))
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

	title, err := store.Create(context.Background(), "add a --dry-run flag")
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(title, "add-a-dry-run-flag"))

	p, ok := cci.FactoryPlan(store.ID())
	assert.Assert(t, ok)
	assert.Check(t, cmp.Equal(p.Title, "add-a-dry-run-flag"))
}

// A plan factory will not create has no title and no ID.
func TestStoreCreateFailsWithoutATitle(t *testing.T) {
	client, cci := testClient(t)
	cci.FactoryStatusCode = http.StatusInternalServerError
	store := newStore(client, "")

	title, err := store.Create(context.Background(), "add a --dry-run flag")
	assert.Check(t, cmp.ErrorContains(err, "create the plan in factory"))
	assert.Check(t, cmp.Equal(title, ""))
	assert.Check(t, cmp.Equal(store.ID(), ""))
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

// TestStoreAnswerOpenQuestionsSendsTheEvent checks that AnswerOpenQuestions
// sends the plan's flow factory's answer-open-questions event, and fails for a
// plan with no flow to send it.
func TestStoreAnswerOpenQuestionsSendsTheEvent(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()
	cci.DropFactoryFlow(store.ID())

	err := store.AnswerOpenQuestions(ctx)
	assert.Check(t, cmp.ErrorContains(err, "send the answer-open-questions event"))

	store, _, cci = createdStore(t)
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), "Building - Human")
	cci.SetFactoryApplicableEvents(store.ID(), "answer-open-questions", "abandon")
	err = store.AnswerOpenQuestions(ctx)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"answer-open-questions"}))
}

// TestStoreSendsTheHumansActions checks that StartWork, Abandon and Delete
// each fail before Create, and after it do what they say to the store's plan
// in factory.
func TestStoreSendsTheHumansActions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		call  func(context.Context, *Store) error
		check func(t *testing.T, cci *fakes.FakeCircleCI, id string)
	}{
		{
			name:  "StartWork sends prioritize",
			state: "Unprioritized",
			call:  func(ctx context.Context, s *Store) error { return s.StartWork(ctx) },
			check: func(t *testing.T, cci *fakes.FakeCircleCI, id string) {
				p, _ := cci.FactoryPlan(id)
				assert.Check(t, cmp.DeepEqual(p.Events, []string{"prioritize"}))
			},
		},
		{
			name:  "Abandon sends abandon with its reason",
			state: "Planning - Human",
			call:  func(ctx context.Context, s *Store) error { return s.Abandon(ctx, "wont-do") },
			check: func(t *testing.T, cci *fakes.FakeCircleCI, id string) {
				p, _ := cci.FactoryPlan(id)
				assert.Check(t, cmp.DeepEqual(p.Events, []string{"abandon"}))
				assert.Check(t, cmp.DeepEqual(p.Reasons, []string{"wont-do"}))
			},
		},
		{
			name:  "Delete deletes the plan",
			state: "Done",
			call:  func(ctx context.Context, s *Store) error { return s.Delete(ctx) },
			check: func(t *testing.T, cci *fakes.FakeCircleCI, id string) {
				_, ok := cci.FactoryPlan(id)
				assert.Check(t, !ok, "the plan is still there")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := testClient(t)
			ctx := context.Background()
			err := tc.call(ctx, newStore(client, ""))
			assert.Check(t, cmp.ErrorIs(err, errNotCreated))

			store, _, cci := createdStore(t)
			cci.SetFactoryPlanState(store.ID(), tc.state)
			cci.SetFactoryApplicableEvents(store.ID(), "prioritize", "abandon")
			err = tc.call(ctx, store)
			assert.Check(t, err)
			tc.check(t, cci, store.ID())
		})
	}
}

// TestStoreFlowClaimed checks that Flow says whether a factory session claims
// the plan.
func TestStoreFlowClaimed(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()

	cci.ClaimFactoryPlan(store.ID(), "s-1")
	f, err := store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, f.Claimed, "a claimed plan's flow is not claimed")

	cci.ReleaseFactoryPlan(store.ID())
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, !f.Claimed, "a released plan's flow is still claimed")
}

// TestStoreApproveDesignSendsTheEvent checks that ApproveDesign sends the
// plan's flow factory's approve-for-units event.
func TestStoreApproveDesignSendsTheEvent(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), "Planning - Human")
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
			want:   planapprove.ErrQuestionsOpen,
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
			cci.SetFactoryPlanState(store.ID(), "Planning - Human")
			cci.SetFactoryApplicableEvents(store.ID(), tc.events...)

			err := store.ApproveDesign(ctx)
			assert.Check(t, cmp.ErrorIs(err, tc.want))
			p, _ := cci.FactoryPlan(store.ID())
			assert.Check(t, cmp.Len(p.Events, 0))
			assert.Check(t, cmp.Equal(p.Session, "s-1"))
		})
	}
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

	_, err = store.Create(ctx, "a plan")
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

	cci.SetFactoryTask(store.ID(), "build-unit")
	cci.SetFactoryFlowStatus(store.ID(), fakes.FactoryFlowStatus{
		PR:         "https://github.com/o/r/pull/7",
		Deviations: []string{"lint fails"},
		Next: []fakes.FactoryTransition{
			{
				Actor:        "chunk",
				TaskCategory: "build-unit",
				IsDispatched: true,
			},
		},
		TaskUnit: "Unit 2: parser",
	})
	cci.SetFactoryPlanState(store.ID(), "Building - Agent")
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	// Its events are the human events of its next, which has none.
	assert.Check(t, cmp.DeepEqual(f, plan.Flow{
		State:  "Building - Agent",
		Events: []string{},
		Task:   "build-unit",
		Unit:   "Unit 2: parser",
		Next: []plan.Step{
			{
				Actor:      "chunk",
				Task:       "build-unit",
				Dispatched: true,
			},
		},
		PR:         "https://github.com/o/r/pull/7",
		Deviations: []string{"lint fails"},
		Claimed:    true,
	}))

	cci.SetFactoryTask(store.ID(), "")
	cci.SetFactoryFlowStatus(store.ID(), fakes.FactoryFlowStatus{
		Outcome: "merged",
		Next:    []fakes.FactoryTransition{},
	})
	cci.SetFactoryPlanState(store.ID(), "Done")
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(f, plan.Flow{
		State:      "Done",
		Events:     []string{},
		Deviations: []string{},
		Ended:      true,
		Outcome:    "merged",
		Claimed:    true,
	}))

	cci.SetFactoryPlanState(store.ID(), "ended")
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, f.Ended, "an ended flow is not ended")
	assert.Check(t, cmp.Equal(f.Outcome, "merged"))

	cci.DropFactoryFlow(store.ID())
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(f.Events, 0))
	assert.Check(t, cmp.Equal(f.Task, ""))
}

// TestStoreFamily checks that Family is the plan's parent, open sub-plans and
// blockers, each titled and in the state its flow names, and empty before
// Create.
func TestStoreFamily(t *testing.T) {
	client, cci := testClient(t)
	store := newStore(client, "")
	ctx := context.Background()

	fam, err := store.Family(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(fam, plan.Family{}))

	_, err = store.Create(ctx, "a plan")
	assert.NilError(t, err)
	id := store.ID()
	newPlan := func(title, state string) string {
		t.Helper()
		p, err := client.CreatePlan(ctx, title, "# "+title+"\n")
		assert.NilError(t, err)
		cci.SetFactoryPlanState(p.ID, state)
		return p.ID
	}
	parent := newPlan("parent", "Building - Agent")
	c1 := newPlan("child one", "Planning - Human")
	done := newPlan("child done", "Done")
	c2 := newPlan("child two", "Building - Agent")
	blocker := newPlan("blocker", "Integrating - Human")
	cci.SetFactoryParent(id, parent)
	for _, c := range []string{c1, done, c2} {
		cci.SetFactoryParent(c, id)
	}
	ended := newPlan("ended blocker", "ended")
	cci.SetFactoryFlowStatus(ended, fakes.FactoryFlowStatus{Outcome: "canceled"})
	cci.BlockFactoryPlan(id, blocker, ended)

	want := plan.Family{
		Parent: &plan.Relative{
			ID:    parent,
			Title: "parent",
			State: "Building - Agent",
		},
		Children: []plan.Relative{
			{
				ID:    c1,
				Title: "child one",
				State: "Planning - Human",
			},
			{
				ID:    c2,
				Title: "child two",
				State: "Building - Agent",
			},
		},
		Blockers: []plan.Relative{
			{
				ID:    blocker,
				Title: "blocker",
				State: "Integrating - Human",
			},
			{
				ID:      ended,
				Title:   "ended blocker",
				State:   "ended",
				Outcome: "canceled",
			},
		},
	}
	fam, err = store.Family(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(fam, want))

	// The parent is read once: a later change is not seen.
	cci.SetFactoryParent(id, "")
	fam, err = store.Family(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(fam, want))

	// A child with no flow keeps its plan's title and has no state.
	cci.DropFactoryFlow(c2)
	fam, err = store.Family(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(fam.Children[1], plan.Relative{
		ID:    c2,
		Title: "child two",
	}))
}

// TestStoreFamilyRereadsAFailedParent checks that a failed read of the
// plan's parent is not kept: the next Family reads it again.
func TestStoreFamilyRereadsAFailedParent(t *testing.T) {
	store, client, cci := createdStore(t)
	ctx := context.Background()
	p, err := client.CreatePlan(ctx, "parent", "# parent\n")
	assert.NilError(t, err)
	cci.SetFactoryParent(store.ID(), p.ID)

	cci.FactoryStatusCode = http.StatusInternalServerError
	_, err = store.Family(ctx)
	assert.Check(t, cmp.ErrorContains(err, "parent"))

	cci.FactoryStatusCode = 0
	fam, err := store.Family(ctx)
	assert.NilError(t, err)
	assert.Assert(t, fam.Parent != nil)
	assert.Check(t, cmp.Equal(fam.Parent.ID, p.ID))
}

// TestStoreFamilyFails checks that Family fails, saying which read failed,
// when factory fails any of the reads after the parent's.
func TestStoreFamilyFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(r *http.Request) bool
		want string
	}{
		{
			name: "plans",
			fail: func(r *http.Request) bool { return r.URL.Path == "/api/v3/factory/plans" },
			want: "sub-plans",
		},
		{
			name: "flows",
			fail: func(r *http.Request) bool { return r.URL.Path == "/api/v3/factory/flows" },
			want: "flows",
		},
		{
			name: "blockers",
			fail: func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/blockers") },
			want: "blockers",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cci := fakes.NewFakeCircleCI()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && tc.fail(r) {
					http.Error(w, `{"error":{"title":"API error"}}`, http.StatusBadRequest)
					return
				}
				cci.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)
			client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
			assert.NilError(t, err)
			store := newStore(client, "")
			ctx := context.Background()
			_, err = store.Create(ctx, "a plan")
			assert.NilError(t, err)

			_, err = store.Family(ctx)
			assert.Check(t, cmp.ErrorContains(err, tc.want))
		})
	}
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

	_, err = store.Create(ctx, "a plan")
	assert.NilError(t, err)
	err = store.SetText(ctx, "# A plan\n\n## 1. Outcome\n\nEdited by hand.\n")
	assert.NilError(t, err)
	text, err := store.Text(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(text, "# A plan\n\n## 1. Outcome\n\nEdited by hand.\n"))
}
