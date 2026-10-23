package circleci

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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
		assert.Check(t, cmp.Equal(p.State, "Unprioritized"))
		assert.Check(t, p.Branch != "")

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

		_, err := client.GetPlan(ctx, "no-such-plan")
		assertStatus(t, err, http.StatusNotFound)
		_, err = client.PlanText(ctx, "no-such-plan")
		assertStatus(t, err, http.StatusNotFound)
		assertStatus(t, client.SetPlanText(ctx, "no-such-plan", "x"), http.StatusNotFound)
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

		_, err := client.CreateComment(ctx, "no-such-plan", "", "a comment")
		assertStatus(t, err, http.StatusNotFound)
		_, err = client.ListComments(ctx, "no-such-plan", false)
		assertStatus(t, err, http.StatusNotFound)
		_, err = client.ResolveComment(ctx, "no-such-comment")
		assertStatus(t, err, http.StatusNotFound)
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
		_, err = client.ListComments(ctx, "plan-1", true)
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
