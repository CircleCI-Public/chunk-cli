package factoryplan

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
// the plan with fallbackTitle and runs no claude, and a location that fails,
// so a test that looks a session up without saying where it works fails
// loudly rather than asking a real factory daemon.
func newStore(client *circleci.Client, id string) *Store {
	s := NewStore(client, id)
	s.name = namedAs("", errors.New("no claude in tests"))
	s.location = locatedAt(nil, errors.New("no factory daemon in tests"))
	return s
}

// locatedAt is a location that answers err when it is set, and otherwise
// each session's worktree from dirs, or factory.ErrUnknownSession for a
// session not in it.
func locatedAt(dirs map[string]string, err error) func(context.Context, string) (string, error) {
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
	err = store.Approve(ctx, "s-1", "/data/plans/"+store.ID())
	assert.Check(t, cmp.ErrorIs(err, ErrNotReady))
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.Len(p.Events, 0))
	assert.Check(t, cmp.Equal(p.Session, "s-1"))

	cci.DropFactoryFlow(store.ID())
	err = store.Approve(ctx, "s-1", "/data/plans/"+store.ID())
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

	err = store.Approve(ctx, "s-1", "/data/plans/"+store.ID())
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

// approvableStore is a created Store whose plan factory offers to approve for
// building, claimed by holder unless that is "".
func approvableStore(t *testing.T, holder string) (*Store, *fakes.FakeCircleCI) {
	t.Helper()
	store, client, cci := createdStore(t)
	err := client.SetPlanText(context.Background(), store.ID(), "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
	assert.NilError(t, err)
	if holder != "" {
		cci.ClaimFactoryPlan(store.ID(), holder)
	}
	cci.SetFactoryPlanState(store.ID(), plan.ReadyState)
	cci.SetFactoryApplicableEvents(store.ID(), "approve-for-building")
	return store, cci
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

// TestStoreApproveWithAnotherSessionsClaim checks what approving does with a
// claim another session holds, by where that session works.
func TestStoreApproveWithAnotherSessionsClaim(t *testing.T) {
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
			store, cci := approvableStore(t, holder)
			ctx := context.Background()
			worktree := "/no/such/data/plans/" + store.ID()
			dirs := map[string]string{}
			if !tc.unknown {
				dirs[holder] = strings.Replace(tc.dir, "PLAN", worktree, 1)
			}
			store.location = locatedAt(dirs, tc.lookupErr)

			err := store.Approve(ctx, "s-1", worktree)
			p, _ := cci.FactoryPlan(store.ID())
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

// TestStoreApproveFollowsSymlinks checks that a holder whose worktree is a
// symlink to the plan's is read as working in the plan's worktree.
func TestStoreApproveFollowsSymlinks(t *testing.T) {
	store, cci := approvableStore(t, "builder")
	worktree := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	err := os.Symlink(worktree, link)
	assert.NilError(t, err)
	store.location = locatedAt(map[string]string{"builder": link}, nil)

	err = store.Approve(context.Background(), "s-1", worktree)
	assert.Check(t, cmp.ErrorIs(err, ErrBeingBuilt))
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.Len(p.Events, 0))
	assert.Check(t, cmp.Equal(p.Session, "builder"))
}

// TestStoreApproveWithNoClaim checks that a plan no session claims is
// approved with nothing released.
func TestStoreApproveWithNoClaim(t *testing.T) {
	store, cci := approvableStore(t, "")

	err := store.Approve(context.Background(), "s-1", "/data/plans/"+store.ID())
	assert.NilError(t, err)
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
	for _, path := range posts(cci) {
		assert.Check(t, !strings.HasPrefix(path, "/api/v3/factory/sessions/"), path)
	}
}

// TestStoreApproveStopsWhenTheReleaseFails checks that a claim released
// between reading the holder and releasing it, so the release 404s, stops the
// approval before the event is sent.
func TestStoreApproveStopsWhenTheReleaseFails(t *testing.T) {
	store, cci := approvableStore(t, "parent")
	store.location = func(context.Context, string) (string, error) {
		cci.ReleaseFactoryPlan(store.ID())
		return "/data/plans/parent-1", nil
	}

	err := store.Approve(context.Background(), "s-1", "/data/plans/"+store.ID())
	var se *circleci.StatusError
	isStatus := errors.As(err, &se)
	assert.Assert(t, isStatus)
	assert.Check(t, cmp.Equal(se.StatusCode, http.StatusNotFound))
	assert.Check(t, cmp.ErrorContains(err, "release session parent's claim"))
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.Len(p.Events, 0))
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

	err = store.Approve(ctx, "s-1", "/data/plans/"+store.ID())
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

	err = store.Approve(ctx, "s-1", "/data/plans/"+store.ID())
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
			err := client.SetPlanText(ctx, store.ID(), tc.text)
			assert.NilError(t, err)
			cci.SetFactoryApplicableEvents(store.ID(), tc.events...)
			err = store.Approve(ctx, "s-1", "/data/plans/"+store.ID())
			assert.Check(t, cmp.ErrorIs(err, ErrNoUnits))
		})
	}
	p, _ := cci.FactoryPlan(store.ID())
	assert.Check(t, cmp.Len(p.Events, 0))
}

// TestStoreApproveCountsDoneUnits checks that a plan whose only unit is done
// has units: with both approvals offered, Approve sends the plan rather than
// refusing it with ErrNoUnits.
func TestStoreApproveCountsDoneUnits(t *testing.T) {
	store, client, cci := createdStore(t)
	ctx := context.Background()
	cci.SetFactoryPlanState(store.ID(), plan.ReadyState)
	err := client.SetPlanText(ctx, store.ID(), "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag (done)\n")
	assert.NilError(t, err)
	cci.SetFactoryApplicableEvents(store.ID(), "approve-for-units", "approve-for-building")

	err = store.Approve(ctx, "s-1", "/data/plans/"+store.ID())
	assert.Check(t, !errors.Is(err, ErrNoUnits), "a done unit was not counted: %v", err)
	assert.NilError(t, err)
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

	_, err = store.Create(ctx, "a plan")
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
	cci.BlockFactoryPlan(id, blocker)

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
