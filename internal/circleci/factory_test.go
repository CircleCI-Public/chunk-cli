package circleci

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestPlans(t *testing.T) {
	ctx := context.Background()

	t.Run("creates a plan, then reads and replaces its text", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)

		p, err := client.CreatePlan(ctx, "version-quiet-flag", "# version-quiet-flag\n")
		assert.NilError(t, err)
		assert.Check(t, p.ID != "")
		assert.Check(t, cmp.Equal(p.Title, "version-quiet-flag"))
		assert.Check(t, p.Branch != "")

		f, err := client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(f.State, "Unprioritized"))

		got, err := client.GetPlan(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(got, p))

		text, err := client.PlanText(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(text, "# version-quiet-flag\n"))

		assert.NilError(t, client.SetPlanText(ctx, p.ID, "# version-quiet-flag\n\n## 1. Outcome\n"))
		text, err = client.PlanText(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(text, "# version-quiet-flag\n\n## 1. Outcome\n"))
	})

	t.Run("sends the plan as a V3 entity with the token", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)

		_, err := client.CreatePlan(ctx, "a plan", "text")
		assert.NilError(t, err)

		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Check(t, cmp.Equal(last.Method, http.MethodPost))
		assert.Check(t, cmp.Equal(last.URL.Path, "/api/v3/factory/plans"))
		assert.Check(t, cmp.Equal(last.Header.Get("Circle-Token"), "test-token"))
		var body map[string]any
		assert.NilError(t, json.Unmarshal(last.Body, &body))
		assert.Check(t, cmp.DeepEqual(body, map[string]any{
			"data": map[string]any{"attributes": map[string]any{"title": "a plan", "text": "text"}},
		}))
	})

	t.Run("an unknown plan is a 404", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)
		id := uuid.NewString()

		_, err := client.GetPlan(ctx, id)
		assertStatus(t, err, http.StatusNotFound)
		_, err = client.PlanText(ctx, id)
		assertStatus(t, err, http.StatusNotFound)
		assertStatus(t, client.SetPlanText(ctx, id, "x"), http.StatusNotFound)
	})

	t.Run("a plan id that is not a UUID is a 400", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)

		_, err := client.GetPlan(ctx, "no-such-plan")
		assertStatus(t, err, http.StatusBadRequest)
		_, err = client.PlanText(ctx, "no-such-plan")
		assertStatus(t, err, http.StatusBadRequest)
		assertStatus(t, client.SetPlanText(ctx, "no-such-plan", "x"), http.StatusBadRequest)
	})

	t.Run("reports when a plan was made and changed, and its open comments", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		assert.Check(t, !p.CreatedAt.IsZero())
		assert.Check(t, cmp.Equal(p.UpdatedAt, p.CreatedAt))

		open, err := client.CreateComment(ctx, p.ID, "", "one")
		assert.NilError(t, err)
		_, err = client.CreateComment(ctx, p.ID, "", "two")
		assert.NilError(t, err)
		_, err = client.ResolveComment(ctx, open.ID)
		assert.NilError(t, err)
		assert.NilError(t, client.SetPlanText(ctx, p.ID, "# a-plan\n"))

		got, err := client.GetPlan(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(got.OpenComments, 1))
		assert.Check(t, got.UpdatedAt.After(p.CreatedAt))
	})

	t.Run("lists the caller's open plans, oldest first, every page", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.FactoryPageSize = 2
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)
		var want []string
		for _, title := range []string{"one", "two", "done", "three", "four", "five"} {
			p, err := client.CreatePlan(ctx, title, "")
			assert.NilError(t, err)
			if title == "done" {
				fake.SetFactoryPlanState(p.ID, "Done")
				continue
			}
			want = append(want, p.ID)
		}
		_, err := client.CreateComment(ctx, want[1], "", "a comment")
		assert.NilError(t, err)

		plans, err := client.ListPlans(ctx)
		assert.NilError(t, err)
		ids := make([]string, 0, len(plans))
		for _, p := range plans {
			ids = append(ids, p.ID)
		}
		assert.Check(t, cmp.DeepEqual(ids, want))
		assert.Check(t, cmp.Equal(plans[0].Title, "one"))
		assert.Check(t, !plans[0].CreatedAt.IsZero())
		assert.Check(t, cmp.Equal(plans[1].OpenComments, 1))

		var queries []string
		for _, r := range fake.Recorder.AllRequests() {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v3/factory/plans" {
				queries = append(queries, r.URL.Query().Get("filter[user_id]")+" "+r.URL.Query().Get("page[cursor]"))
			}
		}
		assert.Check(t, cmp.DeepEqual(queries, []string{"me ", "me 2", "me 4"}))
	})

}

func TestFlows(t *testing.T) {
	ctx := context.Background()

	t.Run("reads a plan's flow and the session claiming it", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)

		f, err := client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(f.ID, p.ID))
		assert.Check(t, cmp.Equal(f.State, "Unprioritized"))
		assert.Check(t, cmp.Equal(f.SessionID, ""))
		assert.Check(t, cmp.Equal(f.Title, "a plan"))
		assert.Check(t, !f.UpdatedAt.IsZero())
		assert.Check(t, cmp.Equal(f.Phase, PhaseReceived))

		fake.SetFactoryPlanState(p.ID, "Planning - Human")
		f, err = client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(f.Phase, PhaseStarted))

		fake.SetFactoryPlanState(p.ID, "Done")
		f, err = client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(f.Phase, PhaseEnded))

		fake.ClaimFactoryPlan(p.ID, "session-1")
		f, err = client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(f.SessionID, "session-1"))

		fake.ReleaseFactoryPlan(p.ID)
		f, err = client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(f.SessionID, ""))
	})

	t.Run("reads the events the flow applies now", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		fake.ClaimFactoryPlan(p.ID, "session-1")

		f, err := client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Len(f.ApplicableEvents, 0))

		fake.SetFactoryApplicableEvents(p.ID, "address-comments", "abandon")
		f, err = client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(f.ApplicableEvents, []FlowEvent{AddressComments, "abandon"}))
	})

	t.Run("reads the task the flow has out", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		fake.ClaimFactoryPlan(p.ID, "session-1")

		f, err := client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, f.Task == nil, "a flow with no task out: %+v", f.Task)

		fake.SetFactoryTask(p.ID, "early-plan")
		f, err = client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Assert(t, f.Task != nil)
		assert.Check(t, cmp.Equal(f.Task.Category, "early-plan"))
		assert.Check(t, f.Task.ID != "")

		fake.SetFactoryTaskIDOnly(p.ID)
		f, err = client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Assert(t, f.Task != nil, "an older factory's id-only reference is still a task")
		assert.Check(t, cmp.Equal(f.Task.Category, ""))
		assert.Check(t, f.Task.ID != "")

		fake.SetFactoryTask(p.ID, "")
		f, err = client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, f.Task == nil, "the task outlived its submit: %+v", f.Task)
	})

	t.Run("sends a flow an event", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		fake.ClaimFactoryPlan(p.ID, "session-1")

		err = client.CreateFlowEvent(ctx, p.ID, AddressComments)
		assert.NilError(t, err)

		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Check(t, cmp.Equal(last.URL.Path, "/api/v3/factory/events"))
		var body map[string]any
		err = json.Unmarshal(last.Body, &body)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(body, map[string]any{"data": map[string]any{
			"attributes": map[string]any{"event": "address-comments"},
			"references": map[string]any{"factory_flow": map[string]any{"id": p.ID}},
		}}))
		got, _ := fake.FactoryPlan(p.ID)
		assert.Check(t, cmp.DeepEqual(got.Events, []string{"address-comments"}))
	})

	t.Run("an event for a plan with no flow is a 404", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)

		assertStatus(t, client.CreateFlowEvent(ctx, uuid.NewString(), AddressComments), http.StatusNotFound)
	})

	t.Run("a plan whose flow was dropped has none", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		fake.DropFactoryFlow(p.ID)

		_, err = client.GetFlow(ctx, p.ID)
		assertStatus(t, err, http.StatusNotFound)
		assertStatus(t, client.CreateFlowEvent(ctx, p.ID, AddressComments), http.StatusNotFound)
	})

	t.Run("lists the caller's flows", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)

		flows, err := client.ListFlows(ctx)
		assert.NilError(t, err)
		assert.Check(t, cmp.Len(flows, 0))

		a, err := client.CreatePlan(ctx, "a", "")
		assert.NilError(t, err)
		b, err := client.CreatePlan(ctx, "b", "")
		assert.NilError(t, err)
		gone, err := client.CreatePlan(ctx, "gone", "")
		assert.NilError(t, err)
		fake.SetFactoryPlanState(b.ID, "Done")
		fake.ClaimFactoryPlan(a.ID, "session-1")
		fake.DropFactoryFlow(gone.ID)

		flows, err = client.ListFlows(ctx)
		assert.NilError(t, err)
		for i := range flows {
			assert.Check(t, !flows[i].UpdatedAt.IsZero())
			flows[i].UpdatedAt = time.Time{}
		}
		assert.Check(t, cmp.DeepEqual(flows, []Flow{
			{
				ID: a.ID, Title: "a", State: "Unprioritized", Phase: PhaseReceived,
				SessionID: "session-1", ApplicableEvents: []FlowEvent{},
			},
			{ID: b.ID, Title: "b", State: "Done", Phase: PhaseEnded, ApplicableEvents: []FlowEvent{}},
		}))
		last := fake.Recorder.AllRequests()[len(fake.Recorder.AllRequests())-1]
		assert.Check(t, cmp.Equal(last.URL.Path, "/api/v3/factory/flows"))
		assert.Check(t, cmp.Equal(last.URL.Query().Get("filter[user_id]"), "me"))
	})

	t.Run("lists flows with their titles", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		_, err := client.CreatePlan(ctx, "one", "")
		assert.NilError(t, err)
		_, err = client.CreatePlan(ctx, "two", "")
		assert.NilError(t, err)

		flows, err := client.ListFlows(ctx)
		assert.NilError(t, err)
		var titles, phases []string
		for _, f := range flows {
			titles = append(titles, f.Title)
			phases = append(phases, f.Phase)
		}
		assert.Check(t, cmp.DeepEqual(titles, []string{"one", "two"}))
		assert.Check(t, cmp.DeepEqual(phases, []string{PhaseReceived, PhaseReceived}))
	})

	t.Run("reads a flow from a conductor that serves no title or phase", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"a","attributes":{"state":"Done"}}]}`))
		}))
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)

		flows, err := client.ListFlows(ctx)
		assert.NilError(t, err)
		assert.Assert(t, cmp.Len(flows, 1))
		assert.Check(t, cmp.Equal(flows[0].Title, ""))
		assert.Check(t, flows[0].UpdatedAt.IsZero())
		assert.Check(t, cmp.Equal(flows[0].Phase, ""))
	})
}

func TestReleaseClaim(t *testing.T) {
	ctx := context.Background()

	t.Run("releases the session's claim and keeps the flow", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		fake.ClaimFactoryPlan(p.ID, "session-1")

		err = client.ReleaseClaim(ctx, "session-1", p.ID)
		assert.NilError(t, err)

		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Check(t, cmp.Equal(last.URL.Path, "/api/v3/factory/sessions/session-1/release"))
		var body map[string]any
		err = json.Unmarshal(last.Body, &body)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(body, map[string]any{"flow_id": p.ID}))
		f, err := client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(f.SessionID, ""))
	})

	t.Run("a claim the session does not hold is a 404", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		fake.ClaimFactoryPlan(p.ID, "session-1")

		assertStatus(t, client.ReleaseClaim(ctx, "session-2", p.ID), http.StatusNotFound)
		got, _ := fake.FactoryPlan(p.ID)
		assert.Check(t, cmp.Equal(got.Session, "session-1"))
	})
}

func TestPlanComments(t *testing.T) {
	ctx := context.Background()

	t.Run("creates, lists and resolves comments", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)

		quoted, err := client.CreateComment(ctx, p.ID, "## 1. Outcome", "too vague")
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(quoted.PlanID, p.ID))
		assert.Check(t, cmp.Equal(quoted.QuotedText, "## 1. Outcome"))
		assert.Check(t, cmp.Equal(quoted.Body, "too vague"))
		assert.Check(t, !quoted.CreatedAt.IsZero())
		assert.Check(t, cmp.Nil(quoted.ResolvedAt))
		whole, err := client.CreateComment(ctx, p.ID, "", "looks good overall")
		assert.NilError(t, err)

		all, err := client.ListComments(ctx, p.ID, false)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(commentIDs(all), []string{quoted.ID, whole.ID}))

		resolved, err := client.ResolveComment(ctx, quoted.ID)
		assert.NilError(t, err)
		assert.Assert(t, resolved.ResolvedAt != nil)

		open, err := client.ListComments(ctx, p.ID, true)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(commentIDs(open), []string{whole.ID}))

		all, err = client.ListComments(ctx, p.ID, false)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(commentIDs(all), []string{quoted.ID, whole.ID}))
	})

	t.Run("replaces a comment's body and deletes comments", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		edited, err := client.CreateComment(ctx, p.ID, "## 1. Outcome", "too vague")
		assert.NilError(t, err)
		kept, err := client.CreateComment(ctx, p.ID, "", "looks good")
		assert.NilError(t, err)

		assert.NilError(t, client.SetCommentBody(ctx, edited.ID, "say who it is for"))
		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Check(t, cmp.Equal(last.Method, http.MethodPut))
		assert.Check(t, cmp.Equal(last.URL.Path, "/api/v3/factory/comments/"+edited.ID+"/body"))
		var body map[string]any
		assert.NilError(t, json.Unmarshal(last.Body, &body))
		assert.Check(t, cmp.DeepEqual(body, map[string]any{"body": "say who it is for"}))

		all, err := client.ListComments(ctx, p.ID, false)
		assert.NilError(t, err)
		assert.Assert(t, cmp.Len(all, 2))
		assert.Check(t, cmp.Equal(all[0].Body, "say who it is for"))
		assert.Check(t, cmp.Equal(all[0].QuotedText, "## 1. Outcome"))

		_, err = client.ResolveComment(ctx, edited.ID)
		assert.NilError(t, err)
		assert.NilError(t, client.DeleteComment(ctx, edited.ID))
		all, err = client.ListComments(ctx, p.ID, false)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(commentIDs(all), []string{kept.ID}))

		assertStatus(t, client.DeleteComment(ctx, edited.ID), http.StatusNotFound)
		assertStatus(t, client.SetCommentBody(ctx, edited.ID, "x"), http.StatusNotFound)
	})

	t.Run("resolving again keeps the first resolved time", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		cm, err := client.CreateComment(ctx, p.ID, "", "a comment")
		assert.NilError(t, err)

		first, err := client.ResolveComment(ctx, cm.ID)
		assert.NilError(t, err)
		again, err := client.ResolveComment(ctx, cm.ID)
		assert.NilError(t, err)
		assert.Assert(t, first.ResolvedAt != nil && again.ResolvedAt != nil)
		assert.Check(t, again.ResolvedAt.Equal(*first.ResolvedAt))
	})

	t.Run("reads every page of a plan's comments", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.FactoryPageSize = 2
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		var want []string
		for _, body := range []string{"one", "two", "three", "four", "five"} {
			cm, err := client.CreateComment(ctx, p.ID, "", body)
			assert.NilError(t, err)
			want = append(want, cm.ID)
		}

		got, err := client.ListComments(ctx, p.ID, false)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(commentIDs(got), want))

		var cursors []string
		for _, r := range fake.Recorder.AllRequests() {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v3/factory/comments" {
				cursors = append(cursors, r.URL.Query().Get("page[cursor]"))
			}
		}
		assert.Check(t, cmp.DeepEqual(cursors, []string{"", "2", "4"}))
	})

	t.Run("comments on an unknown plan are a 404", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)

		_, err := client.CreateComment(ctx, uuid.NewString(), "", "a comment")
		assertStatus(t, err, http.StatusNotFound)
		_, err = client.ListComments(ctx, uuid.NewString(), false)
		assertStatus(t, err, http.StatusNotFound)
		_, err = client.ResolveComment(ctx, uuid.NewString())
		assertStatus(t, err, http.StatusNotFound)
	})

	t.Run("ids that are not UUIDs are a 400", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()
		client := newTestClient(t, srv.URL)

		_, err := client.CreateComment(ctx, "no-such-plan", "", "a comment")
		assertStatus(t, err, http.StatusBadRequest)
		_, err = client.ListComments(ctx, "no-such-plan", false)
		assertStatus(t, err, http.StatusBadRequest)
		_, err = client.ResolveComment(ctx, "no-such-comment")
		assertStatus(t, err, http.StatusBadRequest)
		assertStatus(t, client.SetCommentBody(ctx, "no-such-comment", "x"), http.StatusBadRequest)
		assertStatus(t, client.DeleteComment(ctx, "no-such-comment"), http.StatusBadRequest)
	})
}

func TestFactoryNotAuthorized(t *testing.T) {
	ctx := context.Background()
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		fake := fakes.NewFakeCircleCI()
		fake.FactoryStatusCode = code
		srv := httptest.NewServer(fake)
		client := newTestClient(t, srv.URL)

		_, err := client.CreatePlan(ctx, "a plan", "")
		assert.Check(t, cmp.ErrorIs(err, ErrNotAuthorized), "status %d", code)
		_, err = client.ListPlans(ctx)
		assert.Check(t, cmp.ErrorIs(err, ErrNotAuthorized), "status %d", code)
		_, err = client.ListComments(ctx, uuid.NewString(), true)
		assert.Check(t, cmp.ErrorIs(err, ErrNotAuthorized), "status %d", code)
		srv.Close()
	}
}

func commentIDs(cs []PlanComment) []string {
	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	return ids
}

func assertStatus(t *testing.T, err error, code int) {
	t.Helper()
	var se *StatusError
	assert.Assert(t, errors.As(err, &se), "got %v", err)
	assert.Check(t, cmp.Equal(se.StatusCode, code))
}

// A server that keeps naming a next page must not hold the client forever.
func TestListsStopWhenTheServerNeverEndsThem(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[],"page":{"next":"same"}}`))
	}))
	defer srv.Close()
	client := newTestClient(t, srv.URL)

	_, err := client.ListPlans(ctx)
	assert.ErrorContains(t, err, "does not advance")

	_, err = client.ListComments(ctx, "plan-1", false)
	assert.ErrorContains(t, err, "does not advance")
}

func TestNextCursor(t *testing.T) {
	next := func(s string) v3Page { return v3Page{Next: &s} }

	got, err := nextCursor("", next("b"), 1)
	assert.NilError(t, err)
	assert.Equal(t, got, "b")

	got, err = nextCursor("b", v3Page{}, 2)
	assert.NilError(t, err)
	assert.Equal(t, got, "")

	_, err = nextCursor("b", next("c"), maxPages)
	assert.ErrorContains(t, err, "pages")
}
