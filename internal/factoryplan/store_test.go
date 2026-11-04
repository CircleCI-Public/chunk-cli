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
func newStore(client *circleci.Client, id, repo string) *Store {
	s := NewStore(client, id, repo)
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
	store := newStore(client, "", "")
	_, err := store.Create(context.Background(), "a plan")
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

	_, err = store.Create(ctx, "a plan")
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

	_, err = store.Create(ctx, "a plan")
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
			store := NewStore(client, "", "")
			store.name = namedAs(tc.reply, tc.err)

			title, err := store.Create(context.Background(), tc.message)
			assert.NilError(t, err)
			assert.Check(t, cmp.Equal(title, tc.want))

			p, ok := cci.FactoryPlan(store.ID())
			assert.Assert(t, ok)
			assert.Check(t, cmp.Equal(p.Title, title))
			assert.Check(t, cmp.Equal(p.Text,
				"# "+tc.want+"\n\n## 1. Outcome\n\n## 2. Design\n\n## 3. Open Questions\n\n## 4. Worked example\n\n## 5. Units\n"))
			assert.Check(t, cmp.Equal(p.State, "init"))
		})
	}
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
				_, err := store.Create(context.Background(), "a plan")
				assert.Check(t, cmp.ErrorContains(err, "create the plan in factory"))
				assert.Check(t, cmp.Equal(store.ID(), ""))
				return
			}

			_, err := store.Create(context.Background(), "a plan")
			assert.NilError(t, err)
			p, ok := cci.FactoryPlan(store.ID())
			assert.Assert(t, ok)
			assert.Check(t, cmp.DeepEqual(p.Repos, tc.want))
		})
	}
}

// With no claude installed, the real namer fails and the plan is still made,
// titled with the slug.
func TestStoreCreateWithoutClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	client, cci := testClient(t)
	store := NewStore(client, "", "")

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
	store := newStore(client, "", "")

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
	cci.SetFactoryPlanState(store.ID(), "refine-proposal")
	err = store.AddressComments(ctx)
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"address-comments"}))
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
			state: "init",
			call:  func(ctx context.Context, s *Store) error { return s.StartWork(ctx) },
			check: func(t *testing.T, cci *fakes.FakeCircleCI, id string) {
				p, _ := cci.FactoryPlan(id)
				assert.Check(t, cmp.DeepEqual(p.Events, []string{"prioritize"}))
			},
		},
		{
			name:  "Abandon sends abandon with its reason",
			state: "refine-proposal",
			call:  func(ctx context.Context, s *Store) error { return s.Abandon(ctx, "wont-do") },
			check: func(t *testing.T, cci *fakes.FakeCircleCI, id string) {
				p, _ := cci.FactoryPlan(id)
				assert.Check(t, cmp.DeepEqual(p.Events, []string{"abandon"}))
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
	assert.Check(t, cmp.Equal(f.Holder, "s-1"))

	cci.ReleaseFactoryPlan(store.ID())
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, !f.Claimed, "a released plan's flow is still claimed")
	assert.Check(t, cmp.Equal(f.Holder, ""))
}

// TestStoreApproveDesignSendsTheEvent checks that ApproveDesign sends the
// plan's flow factory's approve-for-units event.
func TestStoreApproveDesignSendsTheEvent(t *testing.T) {
	store, _, cci := createdStore(t)
	ctx := context.Background()
	cci.ClaimFactoryPlan(store.ID(), "s-1")
	cci.SetFactoryPlanState(store.ID(), "refine-proposal")
	cci.SetFactoryRequiredEvents(store.ID(), "approve-for-units")
	cci.SetFactoryApplicableEvents(store.ID(), "address-comments", "abandon")

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
			applicable: []string{"abandon"},
			want:       ErrDesignApproved,
		},
		{
			name:       "both are required",
			state:      "refine-proposal",
			required:   []string{"approve-for-units", "approve-for-building"},
			applicable: []string{"abandon"},
			want:       ErrDesignApproved,
		},
		{
			name:       "open questions",
			state:      "refine-proposal",
			required:   []string{"answer-open-questions"},
			applicable: []string{"approve-for-units", "approve-for-building", "abandon"},
			want:       planapprove.ErrNoApproval,
		},
		{
			name:       "nothing listed",
			state:      "stand-by",
			applicable: []string{"abandon"},
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

	_, err = store.Create(ctx, "a plan")
	assert.NilError(t, err)
	f, err = store.Flow(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(f.Events, 0))
	assert.Check(t, cmp.Equal(f.Task, ""))
	assert.Check(t, cmp.Nil(f.Progress))

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

	_, err = store.Create(ctx, "a plan")
	assert.NilError(t, err)
	err = store.SetText(ctx, "# A plan\n\n## 1. Outcome\n\nEdited by hand.\n")
	assert.NilError(t, err)
	text, err := store.Text(ctx)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(text, "# A plan\n\n## 1. Outcome\n\nEdited by hand.\n"))
}

func ptr[T any](v T) *T { return &v }
