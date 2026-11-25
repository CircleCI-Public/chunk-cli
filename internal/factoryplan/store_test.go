package factoryplan

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
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

// newStore is a Store of plan id, or of a plan Create makes, in the fake
// factory client.
func newStore(client *circleci.Client, id, repo string) *Store {
	return NewStore(client, id, repo)
}

// createdStore is a Store whose plan has been created in a fake factory.
func createdStore(t *testing.T) (*Store, *circleci.Client, *fakes.FakeCircleCI) {
	t.Helper()
	client, cci := testClient(t)
	store := newStore(client, "", "")
	err := store.Create(context.Background(), "A plan", "a plan", "")
	assert.NilError(t, err)
	return store, client, cci
}

// TestStoreTextReadsThePlanFromFactory checks that Text reads the plan's text
// from factory, where the agent writes it with plan_set, and is empty before
// the plan exists.
func TestStoreTextReadsThePlanFromFactory(t *testing.T) {
	client, _ := testClient(t)
	store := newStore(client, "", "")
	ctx := context.Background()

	text, err := store.Text(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(text, ""))

	err = store.Create(ctx, "A plan", "a plan", "")
	assert.NilError(t, err)
	err = client.SetPlanText(ctx, store.ID(), "# A plan\n")
	assert.NilError(t, err)
	text, err = store.Text(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(text, "# A plan\n"))
}

// TestStoreStructureReadsItFromFactory checks that Structure reads the plan's
// units and open questions from factory's structured content, and is empty
// before the plan exists.
func TestStoreStructureReadsItFromFactory(t *testing.T) {
	client, cci := testClient(t)
	store := newStore(client, "", "")
	ctx := context.Background()

	st, err := store.Structure(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(st, plan.Structure{}))

	err = store.Create(ctx, "A plan", "a plan", "")
	assert.NilError(t, err)
	err = client.SetPlanText(ctx, store.ID(), "# A plan\n\n## 1. Outcome\n\nWhy.\n")
	assert.NilError(t, err)
	cci.SetFactoryPlanUnits(store.ID(), fakes.FactoryUnit{Name: "Unit 1: lexer", Done: true, Body: "\nLex.\n"})
	st, err = store.Structure(ctx)
	assert.NilError(t, err)
	unit := plan.Unit{Number: 1, Name: "lexer", Done: true, Blocks: []plan.Block{{Type: "paragraph", Text: "Lex."}}}
	outcome := plan.Section{Heading: "1. Outcome", Blocks: []plan.Block{{Type: "paragraph", Text: "Why."}}}
	assert.Check(t, cmp.DeepEqual(st, plan.Structure{
		Sections: []plan.Section{outcome},
		Units:    []plan.Unit{unit},
	}))

	cci.SetFactoryPlanOpenQuestionObjects(store.ID(), "Which store?", "Which TTL?")
	st, err = store.Structure(ctx)
	assert.NilError(t, err)
	which := plan.Block{Type: "paragraph", Text: "Which store?"}
	ttl := plan.Block{Type: "paragraph", Text: "Which TTL?"}
	assert.Check(t, cmp.DeepEqual(st, plan.Structure{
		Sections: []plan.Section{outcome, {Heading: "3. Open Questions", Blocks: []plan.Block{which, ttl}}},
		Units:    []plan.Unit{unit},
		OpenQuestions: []plan.Question{
			{Text: "Which store?", Blocks: []plan.Block{which}},
			{Text: "Which TTL?", Blocks: []plan.Block{ttl}},
		},
	}))

	cci.FailFactoryStructuredContent(store.ID())
	_, err = store.Structure(ctx)
	assert.Check(t, cmp.ErrorContains(err, "get the plan's units"))
}

// A new plan is the skeleton as the web UI makes it: the typed title as its
// heading, the request under 1. Outcome, and the other sections empty, with
// the repository the store was made with.
func TestStoreCreate(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	client, cci := testClient(t)
	store := NewStore(client, "", "CircleCI-Public/chunk-cli")

	const factoryID = "9b1e0c2a-0000-4000-8000-000000000001"

	err := store.Create(context.Background(), "Retry flaky uploads", "Retry uploads that fail with a 5xx", factoryID)
	assert.NilError(t, err)
	assert.Check(t, store.ID() != "")

	p, ok := cci.FactoryPlan(store.ID())
	assert.Assert(t, ok)
	assert.Check(t, cmp.Equal(p.Title, "Retry flaky uploads"))
	assert.Check(t, cmp.Equal(p.Text, "# Retry flaky uploads\n\n"+
		"## 1. Outcome\n\nRetry uploads that fail with a 5xx\n\n"+
		"## 2. Design\n\n## 3. Open Questions\n\n## 4. Worked example\n\n## 5. Units\n"))
	assert.Check(t, cmp.Equal(p.State, "init"))
	assert.Check(t, cmp.DeepEqual(p.Repos, []string{"CircleCI-Public/chunk-cli"}))
	assert.Check(t, cmp.Equal(p.Factory, factoryID))
}

// A new plan records the store's GitHub repository in factory, and none when
// the store has none; a plan factory refuses for its repository is not made.
func TestStoreCreateRecordsTheRepository(t *testing.T) {
	for _, tc := range []struct {
		name   string
		repo   string
		status int
		want   []string
	}{
		{
			name: "a GitHub repository",
			repo: "CircleCI-Public/chunk-cli",
			want: []string{"CircleCI-Public/chunk-cli"},
		},
		{
			name: "none",
			repo: "",
			want: nil,
		},
		{
			name:   "factory refuses it",
			repo:   "CircleCI-Public/chunk-cli",
			status: http.StatusBadRequest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cci := testClient(t)
			store := newStore(client, "", tc.repo)
			if tc.status != 0 {
				cci.FactoryStatusCode = tc.status
				err := store.Create(context.Background(), "A plan", "a plan", "")
				assert.Check(t, cmp.ErrorContains(err, "create the plan in factory"))
				assert.Check(t, cmp.Equal(store.ID(), ""))
				return
			}

			err := store.Create(context.Background(), "A plan", "a plan", "")
			assert.NilError(t, err)
			p, ok := cci.FactoryPlan(store.ID())
			assert.Assert(t, ok)
			assert.Check(t, cmp.DeepEqual(p.Repos, tc.want))
			assert.Check(t, cmp.Equal(p.Factory, ""))
		})
	}
}

// A plan factory will not create has no ID.
func TestStoreCreateFailsWithoutAnID(t *testing.T) {
	client, cci := testClient(t)
	cci.FactoryStatusCode = http.StatusInternalServerError
	store := newStore(client, "", "")

	err := store.Create(context.Background(), "A plan", "add a --dry-run flag", "")
	assert.Check(t, cmp.ErrorContains(err, "create the plan in factory"))
	assert.Check(t, cmp.Equal(store.ID(), ""))
}

// Factories are the factories factory lists, as the screen offers them, and a
// failed list is an error, never a part of one.
func TestStoreFactories(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		want    []plan.Factory
		wantErr string
	}{
		{
			name: "two factories",
			want: []plan.Factory{
				{ID: "f-1", OrgID: "org-1", Name: "mobile"},
				{ID: "f-2", OrgID: "org-1", Name: "default"},
			},
		},
		{
			name:    "factory fails",
			status:  http.StatusInternalServerError,
			wantErr: "list the factories",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cci := testClient(t)
			cci.SetFactories(
				fakes.FactoryFactory{ID: "f-1", OrgID: "org-1", Name: "mobile"},
				fakes.FactoryFactory{ID: "f-2", OrgID: "org-1", Name: "default"},
			)
			cci.FactoryStatusCode = tc.status
			store := newStore(client, "", "")

			got, err := store.Factories(context.Background())
			if tc.wantErr != "" {
				assert.Check(t, cmp.ErrorContains(err, tc.wantErr))
				return
			}
			assert.NilError(t, err)
			assert.Check(t, cmp.DeepEqual(got, tc.want), "in the order factory lists them; the screen sorts")
		})
	}
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

// TestStoreAddressCommentsHandsOffTheUnresolvedComments checks that
// AddressComments sends the plan's flow the address-comments input carrying
// the IDs of the comments unresolved on the plan, whoever wrote them, and
// fails for a plan with no flow to send it.
func TestStoreAddressCommentsHandsOffTheUnresolvedComments(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()
	cci.DropFactoryFlow(store.ID())

	err := store.AddressComments(ctx)
	assert.Check(t, err != nil, "a plan with no flow took the event")

	store, client, cci := createdStore(t)
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), "refine-proposal")
	// c1 and c2 saved through the store; c3 saved, then resolved; c4 written
	// straight through the API, as another client would.
	c1, err := store.AddComment(ctx, "", "one")
	assert.NilError(t, err)
	c2, err := store.AddComment(ctx, "", "two")
	assert.NilError(t, err)
	c3, err := store.AddComment(ctx, "", "three")
	assert.NilError(t, err)
	err = store.ResolveComment(ctx, c3.ID)
	assert.NilError(t, err)
	c4, err := client.CreateComment(ctx, store.ID(), "", "four")
	assert.NilError(t, err)

	err = store.AddressComments(ctx)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"address-comments"}))
	assert.Check(t, cmp.DeepEqual(p.Inputs, []any{[]string{c1.ID, c2.ID, c4.ID}}), "the resolved comment was handed off, or an open one left out")

	// A retry lists again and sends the same IDs.
	err = store.AddressComments(ctx)
	assert.NilError(t, err)
	p, _ = cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Inputs, []any{[]string{c1.ID, c2.ID, c4.ID}, []string{c1.ID, c2.ID, c4.ID}}))
}

// TestStoreHandOffWithNoCommentsSendsAnEmptyList checks that a hand-off with
// no unresolved comments sends an empty list, not null.
func TestStoreHandOffWithNoCommentsSendsAnEmptyList(t *testing.T) {
	store, _, cci := createdStore(t)
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), "build")

	err := store.AnswerOpenQuestions(context.Background())
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Inputs, []any{[]string{}}))
}

// TestStoreHandOffSendsNothingWhenTheCommentsCannotBeListed checks that a
// hand-off whose comment list fails is an error and sends no event.
func TestStoreHandOffSendsNothingWhenTheCommentsCannotBeListed(t *testing.T) {
	store, _, cci := createdStore(t)
	cci.DeleteFactoryPlan(store.ID())

	err := store.AddressComments(context.Background())
	assert.Check(t, cmp.ErrorContains(err, "list the plan's open comments"))
	for _, r := range cci.Recorder.AllRequests() {
		assert.Check(t, r.Method != http.MethodPost || r.URL.Path != "/api/v3/factory/events", "an event was sent: %s", r.URL.Path)
	}
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
	cci.SetFactoryPlanState(store.ID(), "build")
	cci.SetFactoryApplicableEvents(store.ID(), "answer-open-questions", "end")
	err = store.AnswerOpenQuestions(ctx)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"answer-open-questions"}))
	assert.Check(t, cmp.DeepEqual(p.Inputs, []any{[]string{}}))
}

// TestStoreSendsTheHumansActions checks that StartWork, End and Delete
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
			state: "init",
			call:  func(ctx context.Context, s *Store) error { return s.StartWork(ctx) },
			check: func(t *testing.T, cci *fakes.FakeCircleCI, id string) {
				p, _ := cci.FactoryPlan(id)
				assert.Check(t, cmp.DeepEqual(p.Events, []string{"prioritize"}))
				assert.Check(t, cmp.DeepEqual(p.Inputs, []any{true}))
			},
		},
		{
			name:  "End sends the end event, with its reason",
			state: "refine-proposal",
			call:  func(ctx context.Context, s *Store) error { return s.End(ctx, "wont-do") },
			check: func(t *testing.T, cci *fakes.FakeCircleCI, id string) {
				p, _ := cci.FactoryPlan(id)
				assert.Check(t, cmp.DeepEqual(p.Events, []string{"end"}))
				assert.Check(t, cmp.DeepEqual(p.Reasons, []string{"wont-do"}))
			},
		},
		{
			name:  "Delete deletes the plan",
			state: "ended",
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
			err := tc.call(ctx, newStore(client, "", ""))
			assert.Check(t, cmp.ErrorIs(err, errNotCreated))

			store, _, cci := createdStore(t)
			cci.SetFactoryPlanState(store.ID(), tc.state)
			cci.SetFactoryApplicableEvents(store.ID(), "prioritize", "end")
			err = tc.call(ctx, store)
			assert.Check(t, err)
			tc.check(t, cci, store.ID())
		})
	}
}

// TestStoreEnd checks that End posts end to every flow that has not ended,
// held flows included, and posts nothing to one that has.
func TestStoreEnd(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      string
		applicable []string
		reason     string // "" means "duplicate"
		status     int    // the fake's FactoryStatusCode, failing every factory call
		noFlow     bool
		wantEvents []string
		wantErr    string
	}{
		{
			name:       "a live flow posts end",
			state:      "build",
			applicable: []string{"address-comments", "end"},
			wantEvents: []string{"end"},
		},
		{
			name:       "a live flow ends as completed",
			state:      "build",
			applicable: []string{"end"},
			reason:     "completed",
			wantEvents: []string{"end"},
		},
		{
			name:       "a held flow, with an empty next, posts end",
			state:      "build",
			wantEvents: []string{"end"},
		},
		{
			name:    "an ended flow posts nothing",
			state:   "ended",
			wantErr: ErrEnded.Error(),
		},
		{
			name:       "a 400 is an error",
			state:      "build",
			applicable: []string{"end"},
			reason:     "bored",
			wantErr:    "send the end event",
		},
		{
			name:       "a failed flow read posts nothing",
			state:      "build",
			applicable: []string{"end"},
			status:     http.StatusInternalServerError,
			wantErr:    "get the flow",
		},
		{
			name:    "a plan with no flow posts end and reports the 404",
			state:   "build",
			noFlow:  true,
			wantErr: "send the end event",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, cci := createdStore(t)
			ctx := context.Background()
			cci.SetFactoryPlanState(store.ID(), tc.state)
			cci.SetFactoryApplicableEvents(store.ID(), tc.applicable...)
			cci.FactoryStatusCode = tc.status
			if tc.noFlow {
				cci.DropFactoryFlow(store.ID())
			}
			reason := tc.reason
			if reason == "" {
				reason = "duplicate"
			}

			err := store.End(ctx, reason)

			cci.FactoryStatusCode = 0
			if tc.wantErr == "" {
				assert.Check(t, err)
			} else {
				assert.Check(t, cmp.ErrorContains(err, tc.wantErr))
			}
			if tc.wantErr == ErrEnded.Error() {
				assert.Check(t, cmp.ErrorIs(err, ErrEnded))
			}
			p, _ := cci.FactoryPlan(store.ID())
			assert.Check(t, cmp.DeepEqual(p.Events, tc.wantEvents))
			if len(tc.wantEvents) == 0 {
				return
			}
			assert.Check(t, cmp.DeepEqual(p.Reasons, []string{reason}))
			cci.SetFactoryPlanState(store.ID(), "ended")
			f, err := store.Flow(ctx)
			assert.NilError(t, err)
			assert.Check(t, f.Ended, "the plan did not end")
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
	assert.Check(t, cmp.Equal(f.Holder, "s-1"))

	cci.ReleaseFactoryPlan(store.ID())
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, !f.Claimed, "a released plan's flow is still claimed")
	assert.Check(t, cmp.Equal(f.Holder, ""))
}

// TestStoreFlowClaimedByAWorker checks that a flow a worker claims with no
// session working it is claimed, with no holder and the worker named.
func TestStoreFlowClaimedByAWorker(t *testing.T) {
	store, _, cci := createdStore(t)

	cci.ClaimFactoryPlanForWorker(store.ID(), "w-1")
	f, err := store.Flow(context.Background())
	assert.NilError(t, err)
	assert.Check(t, f.Claimed, "a worker-claimed plan's flow is not claimed")
	assert.Check(t, cmp.Equal(f.Holder, ""))
	assert.Assert(t, f.Worker != nil)
	assert.Check(t, cmp.Equal(f.Worker.ID, "w-1"))
}

// TestStoreApproveDesignSendsTheEvent checks that ApproveDesign sends the
// plan's flow factory's approve-for-units event.
func TestStoreApproveDesignSendsTheEvent(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), "refine-proposal")
	cci.SetFactoryRequiredEvents(store.ID(), "approve-for-units")
	cci.SetFactoryApplicableEvents(store.ID(), "address-comments", "end")

	err := store.ApproveDesign(ctx)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-units"}))
	assert.Check(t, cmp.DeepEqual(p.Inputs, []any{true}))
}

// TestStoreApproveDesignRefusesWhatFactoryDoesNotOffer checks that
// ApproveDesign sends nothing for a plan whose flow does not offer design
// approval, and keeps the planning session's claim.
func TestStoreApproveDesignRefusesWhatFactoryDoesNotOffer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      string
		required   []string
		applicable []string
		want       error
	}{
		{
			name:       "building is what is required",
			state:      "refine-units",
			required:   []string{"approve-for-building"},
			applicable: []string{"end"},
			want:       ErrDesignApproved,
		},
		{
			name:       "both are required",
			state:      "refine-proposal",
			required:   []string{"approve-for-units", "approve-for-building"},
			applicable: []string{"end"},
			want:       ErrDesignApproved,
		},
		{
			name:       "open questions",
			state:      "refine-proposal",
			required:   []string{"answer-open-questions"},
			applicable: []string{"approve-for-units", "approve-for-building", "end"},
			want:       planapprove.ErrNoApproval,
		},
		{
			name:       "nothing listed",
			state:      "stand-by",
			applicable: []string{"end"},
			want:       planapprove.ErrNoApproval,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, cci := createdStore(t)
			ctx := context.Background()
			cci.ClaimFactoryPlan(store.ID(), "s-1")
			cci.SetFactoryPlanState(store.ID(), tc.state)
			cci.SetFactoryRequiredEvents(store.ID(), tc.required...)
			cci.SetFactoryApplicableEvents(store.ID(), tc.applicable...)

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
	store := newStore(client, "", "")
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

// TestStoreFlowReadsTheFlow checks that Flow is the events the plan's flow
// lists as applicable and the category of the task it has out, and empty
// before the plan exists or has a flow.
func TestStoreFlowReadsTheFlow(t *testing.T) {
	client, cci := testClient(t)
	store := newStore(client, "", "")
	ctx := context.Background()

	f, err := store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(f.Events, 0))
	assert.Check(t, cmp.Equal(f.Task, ""))

	err = store.Create(ctx, "A plan", "a plan", "")
	assert.NilError(t, err)
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(f.Events, 0))
	assert.Check(t, cmp.Equal(f.Task, ""))
	assert.Check(t, cmp.Nil(f.Progress))

	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryApplicableEvents(store.ID(), "address-comments", "end")
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(f.Events, []string{"address-comments", "end"}))
	assert.Check(t, cmp.Equal(f.Task, ""), "a flow with no task out")

	cci.SetFactoryFlowStatus(store.ID(), fakes.FactoryFlowStatus{
		Next: []fakes.FactoryTransition{
			{Actor: "human", Input: "answer-open-questions", IsRequired: true},
			{Actor: "human", Event: "end"},
		},
	})
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(f.Events, []string{"answer-open-questions", "end"}))
	assert.Assert(t, len(f.Next) > 0)
	assert.Check(t, cmp.Equal(f.Next[0].Event, "answer-open-questions"))
	cci.SetFactoryFlowStatus(store.ID(), fakes.FactoryFlowStatus{})

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
		TaskUnit:     "Unit 2: parser",
		WaitingOn:    "session",
		WaitingOnWhy: "the session builds Unit 2: parser",
		Progress: map[string]any{
			"summary": "Building unit 2 of 3", "current_unit": "Unit 2: parser",
			"deviation_count": 2, "binding_deviation_count": 1,
			"fixup_count": 1, "finding_count": 3, "revision_count": 0,
		},
	})
	cci.SetFactoryPlanState(store.ID(), "build")
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	// Its events are the human events of its next, which has none.
	assert.Check(t, cmp.DeepEqual(f, plan.Flow{
		State:  "build",
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
		PR:           "https://github.com/o/r/pull/7",
		Deviations:   []string{"lint fails"},
		Claimed:      true,
		Holder:       "s-1",
		Worker:       &plan.Worker{ID: fakes.FactoryWorkerID, Unread: "no worker answers for this plan"},
		WaitingOn:    "session",
		WaitingOnWhy: "the session builds Unit 2: parser",
		Progress: &plan.Progress{
			Summary: "Building unit 2 of 3", CurrentUnit: "Unit 2: parser",
			Findings: 3, Fixups: 1, Deviations: ptr(2), BindingDeviations: ptr(1),
		},
	}))

	cci.SetFactoryTask(store.ID(), "")
	cci.SetFactoryFlowStatus(store.ID(), fakes.FactoryFlowStatus{
		Outcome: "merged",
		Next:    []fakes.FactoryTransition{},
	})
	cci.SetFactoryPlanState(store.ID(), "ended")
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(f, plan.Flow{
		State:      "ended",
		Events:     []string{},
		Deviations: []string{},
		Ended:      true,
		Outcome:    "merged",
		Claimed:    true,
		Holder:     "s-1",
		Worker:     &plan.Worker{ID: fakes.FactoryWorkerID, Unread: "no worker answers for this plan"},
	}))

	cci.SetFactoryPlanState(store.ID(), "ended")
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, f.Ended, "an ended flow is not ended")
	assert.Check(t, cmp.Equal(f.Outcome, "merged"))

	// A 404 is a plan with no flow, not one that ended.
	cci.DropFactoryFlow(store.ID())
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(f, plan.Flow{}))
}

// TestStoreFlowBeforeCreate checks that Flow before the plan exists is an
// empty flow, not an ended one, read without asking factory.
func TestStoreFlowBeforeCreate(t *testing.T) {
	client, cci := testClient(t)
	f, err := newStore(client, "", "").Flow(context.Background())
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(f, plan.Flow{}))
	for _, r := range cci.Recorder.AllRequests() {
		asked := r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v3/factory/flows")
		assert.Check(t, !asked, "asked factory for %s", r.URL.Path)
	}
}

// TestStoreFlowFailure checks that a flow read factory fails is an error,
// never read as a plan that ended.
func TestStoreFlowFailure(t *testing.T) {
	store, _, cci := createdStore(t)
	cci.FactoryStatusCode = http.StatusInternalServerError
	f, err := store.Flow(context.Background())
	assert.Check(t, cmp.ErrorContains(err, "get the plan's flow"))
	assert.Check(t, !f.Ended, "a failed read is read as ended")
}

// TestStoreFamily checks that Family is the plan's parent, open sub-plans and
// blockers, each titled and in the state its flow names, and empty before
// Create.
func TestStoreFamily(t *testing.T) {
	client, cci := testClient(t)
	store := newStore(client, "", "")
	ctx := context.Background()

	fam, err := store.Family(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(fam, plan.Family{}))

	err = store.Create(ctx, "A plan", "a plan", "")
	assert.NilError(t, err)
	id := store.ID()
	newPlan := func(title, state string) string {
		t.Helper()
		p, err := client.CreatePlan(ctx, title, "# "+title+"\n", "")
		assert.NilError(t, err)
		cci.SetFactoryPlanState(p.ID, state)
		return p.ID
	}
	parent := newPlan("parent", "build")
	c1 := newPlan("child one", "refine-proposal")
	done := newPlan("child done", "ended")
	c2 := newPlan("child two", "build")
	blocker := newPlan("blocker", "integrate")
	cci.SetFactoryParent(id, parent)
	for _, c := range []string{c1, done, c2} {
		cci.SetFactoryParent(c, id)
	}
	ended := newPlan("ended blocker", "ended")
	cci.SetFactoryFlowStatus(ended, fakes.FactoryFlowStatus{Outcome: "canceled"})
	merged := newPlan("merged blocker", "ended")
	cci.SetFactoryFlowStatus(merged, fakes.FactoryFlowStatus{Outcome: "merged"})
	cci.BlockFactoryPlan(id, blocker, ended, merged)

	want := plan.Family{
		Parent: &plan.Relative{
			ID:    parent,
			Title: "parent",
			State: "build",
		},
		Children: []plan.Relative{
			{
				ID:    c1,
				Title: "child one",
				State: "refine-proposal",
			},
			{
				ID:    c2,
				Title: "child two",
				State: "build",
			},
		},
		Blockers: []plan.Relative{
			{
				ID:     blocker,
				Title:  "blocker",
				State:  "integrate",
				Active: true,
			},
			{
				ID:      ended,
				Title:   "ended blocker",
				State:   "ended",
				Outcome: "canceled",
				Active:  true,
			},
			{
				ID:      merged,
				Title:   "merged blocker",
				State:   "ended",
				Outcome: "merged",
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

	// A child whose flow is gone has ended: it is not listed.
	cci.DropFactoryFlow(c2)
	fam, err = store.Family(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(fam.Children, want.Children[:1]))

	// A parent with no flow has an empty title. The parent is read once per
	// store, so a fresh one sees it again.
	cci.SetFactoryParent(id, parent)
	cci.DropFactoryFlow(parent)
	fam, err = newStore(client, id, "").Family(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(fam.Parent, &plan.Relative{ID: parent}))
}

// TestStoreFamilyRereadsAFailedParent checks that a failed read of the
// plan's parent is not kept: the next Family reads it again.
func TestStoreFamilyRereadsAFailedParent(t *testing.T) {
	store, client, cci := createdStore(t)
	ctx := context.Background()
	p, err := client.CreatePlan(ctx, "parent", "# parent\n", "")
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
			store := newStore(client, "", "")
			ctx := context.Background()
			err = store.Create(ctx, "A plan", "a plan", "")
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

	cci.SetFactoryPlanState(store.ID(), "refine-proposal")
	st, err := store.State(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(st, "refine-proposal"))

	cci.DropFactoryFlow(store.ID())
	st, err = store.State(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(st, ""))

	// The fake answers 400 for an ID that is no UUID: an error that is not a 404.
	_, err = NewStore(client, "not-a-uuid", "").State(ctx)
	assert.Check(t, err != nil, "a failed flow read is not read as no state")
}

func TestStoreSetTextReplacesThePlanInFactory(t *testing.T) {
	client, _ := testClient(t)
	store := newStore(client, "", "")
	ctx := context.Background()

	err := store.SetText(ctx, "# A plan\n")
	assert.Check(t, cmp.ErrorIs(err, errNotCreated))

	err = store.Create(ctx, "A plan", "a plan", "")
	assert.NilError(t, err)
	err = store.SetText(ctx, "# A plan\n\n## 1. Outcome\n\nEdited by hand.\n")
	assert.NilError(t, err)
	text, err := store.Text(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(text, "# A plan\n\n## 1. Outcome\n\nEdited by hand.\n"))
}

func ptr[T any](v T) *T { return &v }

// workersFake is a fake factory behind a server that counts GET /workers
// requests and, when workersStatus is not 0, answers them with it.
type workersFake struct {
	client        *circleci.Client
	cci           *fakes.FakeCircleCI
	workersStatus atomic.Int32
	// workersEmpty answers GET /workers with an empty list.
	workersEmpty atomic.Bool
	// workersOther answers GET /workers with a list of only another worker.
	workersOther atomic.Bool
	workersReads atomic.Int32
}

func newWorkersFake(t *testing.T) *workersFake {
	t.Helper()
	w := &workersFake{cci: fakes.NewFakeCircleCI()}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v3/factory/workers" {
			w.workersReads.Add(1)
			if status := int(w.workersStatus.Load()); status != 0 {
				rw.WriteHeader(status)
				return
			}
			if w.workersEmpty.Load() {
				rw.Header().Set("Content-Type", "application/json")
				_, _ = rw.Write([]byte(`{"data":[]}`))
				return
			}
			if w.workersOther.Load() {
				rw.Header().Set("Content-Type", "application/json")
				_, _ = rw.Write([]byte(`{"data":[{"id":"w2","attributes":{"hostname":"another-host","phase":"started","health":"ok"}}]}`))
				return
			}
		}
		w.cci.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	w.client = client
	return w
}

// created is a store whose plan has been created in the fake.
func (w *workersFake) created(t *testing.T) *Store {
	t.Helper()
	store := newStore(w.client, "", "")
	err := store.Create(context.Background(), "A plan", "a plan", "")
	assert.NilError(t, err)
	return store
}

// TestStoreFlowWorker checks that Flow reads the worker claiming the plan
// with what it reports of the plan, and reads none for an unclaimed plan.
func TestStoreFlowWorker(t *testing.T) {
	ctx := context.Background()
	w := newWorkersFake(t)
	store := w.created(t)

	f, err := store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Nil(f.Worker))
	assert.Check(t, cmp.Equal(w.workersReads.Load(), int32(0)), "workers were read for an unclaimed plan")

	w.cci.PutFactoryWorker(fakes.FactoryWorker{
		ID: "w1", Hostname: "scotts-mbp", Phase: "started", Health: "ok",
		Headless: []fakes.FactoryHeadless{
			{FlowID: store.ID(), Role: "architect", State: "running", Health: "ok"},
			{FlowID: store.ID(), Role: "reviewer", State: "idle", Health: "unresponsive", HealthReason: "no heartbeat for 3m"},
			{FlowID: "another-plan", Role: "architect", State: "running", Health: "ok"},
		},
		FlowErrors: []fakes.FactoryFlowError{
			{FlowID: "another-plan", Problem: "not this plan's", Fix: "ignore"},
			{FlowID: store.ID(), Problem: "no clone", Fix: "run git fetch"},
		},
	})
	w.cci.ClaimFactoryPlanForWorker(store.ID(), "w1")
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(w.workersReads.Load(), int32(1)))
	assert.Check(t, cmp.DeepEqual(f.Worker, &plan.Worker{
		ID: "w1", Hostname: "scotts-mbp", Live: true,
		Headless: []plan.Headless{
			{Role: "architect", State: "running", Health: "ok"},
			{Role: "reviewer", State: "idle", Health: "unresponsive", HealthReason: "no heartbeat for 3m"},
		},
		Problem: "no clone", Fix: "run git fetch",
	}))

	w.cci.PutFactoryWorker(fakes.FactoryWorker{ID: "w1", Hostname: "scotts-mbp", Phase: "ended"})
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Assert(t, f.Worker != nil)
	assert.Check(t, !f.Worker.Live, "an ended worker is live")
}

// TestStoreFlowWorkerUnreadable checks that a claiming worker that cannot be
// read, because its list fails or lists nothing, is the flow's worker with
// only its ID and why it is unread, and does not fail Flow.
func TestStoreFlowWorkerUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int32
		empty  bool
		other  bool
		unread string
	}{
		{
			name:   "the list answers 500",
			status: http.StatusInternalServerError,
			unread: "500",
		},
		{
			name:   "the list is empty",
			empty:  true,
			unread: "no worker answers for this plan",
		},
		{
			name:   "the list holds only another worker",
			other:  true,
			unread: "no worker answers for this plan",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			w := newWorkersFake(t)
			store := w.created(t)
			w.cci.PutFactoryWorker(fakes.FactoryWorker{
				ID: "w1", Hostname: "scotts-mbp", Phase: "started", Health: "ok",
				Headless:   []fakes.FactoryHeadless{{FlowID: store.ID(), Role: "architect", State: "running", Health: "ok"}},
				FlowErrors: []fakes.FactoryFlowError{{FlowID: store.ID(), Problem: "p", Fix: "f"}},
			})
			w.cci.ClaimFactoryPlanForWorker(store.ID(), "w1")
			w.cci.SetFactoryPlanState(store.ID(), "refine-proposal")
			w.workersEmpty.Store(tc.empty)
			w.workersOther.Store(tc.other)
			w.workersStatus.Store(tc.status)

			f, err := store.Flow(ctx)

			assert.NilError(t, err)
			assert.Check(t, cmp.Equal(f.State, "refine-proposal"), "the rest of the flow is filled")
			assert.Check(t, f.Claimed)
			assert.Assert(t, f.Worker != nil)
			assert.Check(t, cmp.Equal(f.Worker.ID, "w1"))
			assert.Check(t, cmp.Contains(f.Worker.Unread, tc.unread))
			assert.Check(t, cmp.Equal(f.Worker.Hostname, ""))
			assert.Check(t, cmp.Len(f.Worker.Headless, 0))
			assert.Check(t, cmp.Equal(f.Worker.Problem, ""))
			assert.Check(t, cmp.Equal(f.Worker.Fix, ""))
			assert.Check(t, !f.Worker.Live)
		})
	}
}

// TestStoreClaimPicksTheDefaultWorker checks which of the workers that can
// claim the plan Claim claims it for.
func TestStoreClaimPicksTheDefaultWorker(t *testing.T) {
	live := func(id, host string) fakes.FactoryWorker {
		return fakes.FactoryWorker{ID: id, Hostname: host, Phase: "started", Health: "ok"}
	}
	notLive := func(id, host string) fakes.FactoryWorker {
		return fakes.FactoryWorker{ID: id, Hostname: host, Phase: "started", Health: "unresponsive"}
	}
	for _, tc := range []struct {
		name    string
		workers []fakes.FactoryWorker
		host    func() (string, error)
		want    string // the claimed worker's ID, "" for ErrNoWorker
	}{
		{
			name: "no worker",
			host: func() (string, error) { return "scotts-mbp", nil },
		},
		{
			name:    "no live worker",
			workers: []fakes.FactoryWorker{notLive("w1", "scotts-mbp")},
			host:    func() (string, error) { return "scotts-mbp", nil },
		},
		{
			name:    "this machine's worker when it is not the first",
			workers: []fakes.FactoryWorker{live("w1", "other"), live("w2", "scotts-mbp")},
			host:    func() (string, error) { return "scotts-mbp", nil },
			want:    "w2",
		},
		{
			name:    "this machine's worker is not live",
			workers: []fakes.FactoryWorker{notLive("w1", "scotts-mbp"), live("w2", "other"), live("w3", "another")},
			host:    func() (string, error) { return "scotts-mbp", nil },
			want:    "w2",
		},
		{
			name:    "the hostnames differ in case and domain",
			workers: []fakes.FactoryWorker{live("w1", "other"), live("w2", "Scotts-MBP")},
			host:    func() (string, error) { return "scotts-mbp.local", nil },
			want:    "w2",
		},
		{
			name:    "the worker's hostname has the domain",
			workers: []fakes.FactoryWorker{live("w1", "other"), live("w2", "scotts-mbp.local")},
			host:    func() (string, error) { return "SCOTTS-MBP", nil },
			want:    "w2",
		},
		{
			name:    "no worker matches this machine",
			workers: []fakes.FactoryWorker{notLive("w1", "x"), live("w2", "other"), live("w3", "another")},
			host:    func() (string, error) { return "scotts-mbp", nil },
			want:    "w2",
		},
		{
			name:    "the hostname cannot be read",
			workers: []fakes.FactoryWorker{live("w1", "other"), live("w2", "scotts-mbp")},
			host:    func() (string, error) { return "", errors.New("no hostname") },
			want:    "w1",
		},
		{
			name:    "the hostname is empty",
			workers: []fakes.FactoryWorker{live("w1", "other"), live("w2", "")},
			host:    func() (string, error) { return "", nil },
			want:    "w1",
		},
		{
			name:    "a bound worker is not listed",
			workers: []fakes.FactoryWorker{{ID: "w1", Hostname: "scotts-mbp", Phase: "started", Health: "ok", Bound: true}, live("w2", "other")},
			host:    func() (string, error) { return "scotts-mbp", nil },
			want:    "w2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, cci := createdStore(t)
			store.hostname = tc.host
			for _, w := range tc.workers {
				cci.PutFactoryWorker(w)
			}

			host, err := store.Claim(context.Background())

			p, _ := cci.FactoryPlan(store.ID())
			if tc.want == "" {
				assert.Check(t, cmp.ErrorIs(err, ErrNoWorker))
				assert.Check(t, cmp.Equal(p.Worker, ""))
				return
			}
			assert.NilError(t, err)
			assert.Check(t, cmp.Equal(p.Worker, tc.want))
			i := slices.IndexFunc(tc.workers, func(w fakes.FactoryWorker) bool { return w.ID == tc.want })
			assert.Check(t, cmp.Equal(host, tc.workers[i].Hostname))
		})
	}
}

// TestStoreClaim checks what Claim sends and returns, and its failures.
func TestStoreClaim(t *testing.T) {
	ctx := context.Background()
	live := fakes.FactoryWorker{ID: "w1", Hostname: "scotts-mbp", Phase: "started", Health: "ok"}

	t.Run("the picked worker's ID is sent", func(t *testing.T) {
		store, _, cci := createdStore(t)
		cci.PutFactoryWorker(live)
		host, err := store.Claim(ctx)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(host, "scotts-mbp"))
		var body string
		for _, r := range cci.Recorder.AllRequests() {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/claim") {
				body = string(r.Body)
			}
		}
		assert.Check(t, cmp.Contains(body, `"worker_id":"w1"`))
	})

	for _, tc := range []struct {
		name   string
		status int
	}{
		{
			name:   "the claim is refused as claimed",
			status: http.StatusConflict,
		},
		{
			name:   "the plan is not found",
			status: http.StatusNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, cci := createdStore(t)
			cci.PutFactoryWorker(live)
			cci.FailFactoryClaim(store.ID(), tc.status)

			_, err := store.Claim(ctx)

			assert.Check(t, !errors.Is(err, ErrNoWorker))
			var se *circleci.StatusError
			assert.Assert(t, errors.As(err, &se), "err = %v", err)
			assert.Check(t, cmp.Equal(se.StatusCode, tc.status))
			assert.Check(t, cmp.ErrorContains(err, "claim the plan"))
		})
	}

	t.Run("a flow already claimed answers 409 flow_claimed", func(t *testing.T) {
		store, _, cci := createdStore(t)
		cci.PutFactoryWorker(live)
		cci.PutFactoryWorker(fakes.FactoryWorker{ID: "w0", Hostname: "aaa", Phase: "started", Health: "ok"})
		cci.ClaimFactoryPlanForWorker(store.ID(), "w0")
		store.hostname = func() (string, error) { return "scotts-mbp", nil }

		_, err := store.Claim(ctx)

		assert.Check(t, !errors.Is(err, ErrNoWorker))
		var se *circleci.StatusError
		assert.Assert(t, errors.As(err, &se), "err = %v", err)
		assert.Check(t, cmp.Equal(se.StatusCode, http.StatusConflict))
		assert.Check(t, cmp.Contains(se.ServerMessage, "is claimed by another worker"), "factory's reason is kept")
	})

	t.Run("listing the workers fails", func(t *testing.T) {
		w := newWorkersFake(t)
		store := w.created(t)
		w.workersStatus.Store(http.StatusInternalServerError)

		_, err := store.Claim(ctx)

		assert.Check(t, !errors.Is(err, ErrNoWorker))
		assert.Check(t, cmp.ErrorContains(err, "list the workers that can claim the plan"))
		var se *circleci.StatusError
		assert.Check(t, errors.As(err, &se), "err = %v", err)
	})

	t.Run("before Create", func(t *testing.T) {
		client, _ := testClient(t)
		_, err := newStore(client, "", "").Claim(ctx)

		assert.Check(t, cmp.ErrorIs(err, errNotCreated))
	})
}

// TestStoreRelease checks that Release frees the plan's claim and returns
// factory's refusal when it has one.
func TestStoreRelease(t *testing.T) {
	ctx := context.Background()

	t.Run("it frees the claim", func(t *testing.T) {
		store, _, cci := createdStore(t)
		cci.ClaimFactoryPlanForWorker(store.ID(), "w1")

		err := store.Release(ctx)

		assert.NilError(t, err)
		p, _ := cci.FactoryPlan(store.ID())
		assert.Check(t, cmp.Equal(p.Worker, ""))
	})

	t.Run("the claim changed", func(t *testing.T) {
		store, _, cci := createdStore(t)
		cci.ClaimFactoryPlanForWorker(store.ID(), "w1")
		cci.FailFactoryRelease(store.ID(), http.StatusConflict)

		err := store.Release(ctx)

		var se *circleci.StatusError
		assert.Assert(t, errors.As(err, &se), "err = %v", err)
		assert.Check(t, cmp.Equal(se.StatusCode, http.StatusConflict))
		p, _ := cci.FactoryPlan(store.ID())
		assert.Check(t, cmp.Equal(p.Worker, "w1"))
	})

	t.Run("before Create", func(t *testing.T) {
		client, cci := testClient(t)

		err := newStore(client, "", "").Release(ctx)

		assert.Check(t, cmp.ErrorIs(err, errNotCreated))
		assert.Check(t, cmp.Len(cci.Recorder.AllRequests(), 0), "a request reached factory")
	})
}

// TestStoreApprove checks that Approve sends approve-for-building for a plan
// waiting on the developer for it, releases no claim, and refuses a plan that
// is not ready.
func TestStoreApprove(t *testing.T) {
	ctx := context.Background()

	t.Run("ready", func(t *testing.T) {
		store, _, cci := createdStore(t)
		cci.ClaimFactoryPlanForWorker(store.ID(), "w1")
		cci.SetFactoryFlowStatus(store.ID(), fakes.FactoryFlowStatus{
			WaitingOn: "human",
			Next:      []fakes.FactoryTransition{{Actor: "human", Event: "approve-for-building", IsRequired: true}},
		})

		err := store.Approve(ctx)

		assert.NilError(t, err)
		p, _ := cci.FactoryPlan(store.ID())
		assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
		assert.Check(t, cmp.Equal(p.Worker, "w1"), "the claim is kept")
		for _, r := range cci.Recorder.AllRequests() {
			assert.Check(t, !strings.HasSuffix(r.URL.Path, "/release"), "a claim was released")
		}
	})

	t.Run("not ready", func(t *testing.T) {
		store, _, cci := createdStore(t)
		cci.SetFactoryFlowStatus(store.ID(), fakes.FactoryFlowStatus{WaitingOn: "daemon"})

		err := store.Approve(ctx)

		assert.Check(t, cmp.ErrorIs(err, planapprove.ErrNotReady))
		p, _ := cci.FactoryPlan(store.ID())
		assert.Check(t, cmp.Len(p.Events, 0))
	})

	t.Run("before Create", func(t *testing.T) {
		client, cci := testClient(t)

		err := newStore(client, "", "").Approve(ctx)

		assert.Check(t, cmp.ErrorIs(err, errNotCreated))
		assert.Check(t, cmp.Len(cci.Recorder.AllRequests(), 0), "a request reached factory")
	})
}
