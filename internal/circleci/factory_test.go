package circleci

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
		assert.Check(t, cmp.Equal(f.State, "init"))

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

	t.Run("reads a plan's structured content", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "# a plan\n")
		assert.NilError(t, err)
		para := func(s string) PlanBlock { return PlanBlock{Type: "paragraph", Text: s} }

		sc, err := client.PlanStructuredContent(ctx, p.ID)
		assert.NilError(t, err)
		assertItems(t, sc, PlanStructuredContent{})

		fake.SetFactoryPlanUnits(p.ID,
			fakes.FactoryUnit{Name: "Unit 1: lexer", Done: true, Body: "\nLex.\n"},
			fakes.FactoryUnit{Name: "Unit 2: parser"},
		)
		sc, err = client.PlanStructuredContent(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(sc.Units, []PlanUnit{
			{Number: 1, Name: "lexer", Done: true, Text: "\nLex.\n", Blocks: []PlanBlock{para("Lex.")}},
			{Number: 2, Name: "parser", Blocks: []PlanBlock{}},
		}))
		assert.Check(t, cmp.Equal(sc.Markdown(), "## 5. Units\n\n### Unit 1: lexer (done)\n\nLex.\n### Unit 2: parser\n"))
		assert.Check(t, sc.Outcome == nil)
		assert.Check(t, sc.OpenQuestions == nil, "unset decoded as %#v", sc.OpenQuestions)

		fake.SetFactoryPlanOpenQuestionObjects(p.ID, "Which store?", "Which TTL?")
		sc, err = client.PlanStructuredContent(ctx, p.ID)
		assert.NilError(t, err)
		assert.Assert(t, sc.OpenQuestions != nil)
		assert.Check(t, cmp.DeepEqual(sc.OpenQuestions.Questions, []PlanQuestion{
			{Text: "Which store?", Blocks: []PlanBlock{para("Which store?")}},
			{Text: "Which TTL?", Blocks: []PlanBlock{para("Which TTL?")}},
		}))
		assert.Check(t, cmp.Len(sc.Units, 2), "setting the questions kept the units")

		fake.SetFactoryPlanUnits(p.ID) // serves "units": []
		sc, err = client.PlanStructuredContent(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, sc.Units != nil && len(sc.Units) == 0, "[] decoded as %#v", sc.Units)

		p2, err := client.CreatePlan(ctx, "parsed", "# parsed\n\n## 1. Outcome\n\nIt works.\n\n## 2. Design\n\n### Context\n\n- x\n\n## 5. Units\n\n### Unit 1: a (done)\n\nDo a.\n")
		assert.NilError(t, err)
		sc, err = client.PlanStructuredContent(ctx, p2.ID)
		assert.NilError(t, err)
		assertItems(t, sc, PlanStructuredContent{
			Outcome: &PlanSection{Text: "\nIt works.\n\n", Blocks: []PlanBlock{para("It works.")}},
			Design: &PlanSection{Text: "\n### Context\n\n- x\n\n", Blocks: []PlanBlock{
				{Type: "heading", Text: "### Context"},
				{Type: "list", Text: "- x", Blocks: []PlanBlock{{Type: "list_item", Text: "- x", Blocks: []PlanBlock{para("x")}}}},
			}},
			Units: []PlanUnit{{Number: 1, Name: "a", Done: true, Text: "\nDo a.\n", Blocks: []PlanBlock{para("Do a.")}}},
		})
		assert.Check(t, cmp.Equal(sc.Markdown(), "## 1. Outcome\n\nIt works.\n\n## 2. Design\n\n### Context\n\n- x\n\n## 5. Units\n\n### Unit 1: a (done)\n\nDo a.\n"))

		p3, err := client.CreatePlan(ctx, "dup", "# dup\n\n## 1. Outcome\n\nA.\n\n## 1. Outcome\n\nB.\n\n## 2. Design\n\nD.\n")
		assert.NilError(t, err)
		sc, err = client.PlanStructuredContent(ctx, p3.ID)
		assert.NilError(t, err)
		assert.Check(t, sc.Outcome == nil, "a twice-headed section was served")
		assert.Assert(t, sc.Design != nil)
		assert.Check(t, cmp.Equal(sc.Design.Text, "\nD.\n"))

		p4, err := client.CreatePlan(ctx, "fenced", "# fenced\n\n## 2. Design\n\n```md\n## 5. Units\n```\n\n## 5. Units\n\n### Unit 1: a\n")
		assert.NilError(t, err)
		sc, err = client.PlanStructuredContent(ctx, p4.ID)
		assert.NilError(t, err)
		assert.Assert(t, sc.Design != nil)
		assert.Check(t, cmp.Equal(sc.Design.Text, "\n```md\n## 5. Units\n```\n\n"), "a fenced heading ended 2. Design")
		assert.Check(t, cmp.Len(sc.Units, 1), "a fenced heading made a second 5. Units")
	})

	t.Run("decodes factory's structured content", func(t *testing.T) {
		body, err := os.ReadFile("testdata/structured-content.json")
		assert.NilError(t, err)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}))
		t.Cleanup(srv.Close)
		sc, err := newTestClient(t, srv.URL).PlanStructuredContent(t.Context(), "7b1e0000-0000-4000-8000-000000000000")
		assert.NilError(t, err)
		para := func(s string) PlanBlock { return PlanBlock{Type: "paragraph", Text: s} }
		item := func(s, p string) PlanBlock {
			return PlanBlock{Type: "list_item", Text: s, Blocks: []PlanBlock{para(p)}}
		}
		assertItems(t, sc, PlanStructuredContent{
			Outcome: &PlanSection{Text: "\nOne parser.\n\n", Blocks: []PlanBlock{para("One parser.")}},
			Design: &PlanSection{Text: "\n### Context\n\n> quoted\n> more\n\n---\n\n1. first\n2. second\n\n", Blocks: []PlanBlock{
				{Type: "heading", Text: "### Context"},
				{Type: "blockquote", Text: "> quoted\n> more", Blocks: []PlanBlock{para("quoted\n> more")}},
				{Type: "thematic_break", Text: "---"},
				{Type: "list", Text: "1. first\n2. second", Blocks: []PlanBlock{item("1. first", "first"), item("2. second", "second")}},
			}},
			OpenQuestions: &PlanSection{Text: "\n- Which store?\n", Questions: []PlanQuestion{{Text: "Which store?", Blocks: []PlanBlock{para("Which store?")}}}},
			Units: []PlanUnit{
				{Name: "parser", Done: true, Text: "\n- Add `Parse`.\n- Test it.\n\n", Blocks: []PlanBlock{
					{Type: "list", Text: "- Add `Parse`.\n- Test it.", Blocks: []PlanBlock{item("- Add `Parse`.", "Add `Parse`."), item("- Test it.", "Test it.")}}}},
				{Name: "api", Text: "\nWire it.\n\n```go\nfunc route() {}\n```\n", Blocks: []PlanBlock{
					para("Wire it."), {Type: "code_block", Text: "```go\nfunc route() {}\n```"}}},
			},
		})
		assert.Check(t, cmp.Equal(sc.Markdown(), "## 1. Outcome\n\nOne parser.\n\n## 2. Design\n\n### Context\n\n> quoted\n> more\n\n---\n\n1. first\n2. second\n\n## 3. Open Questions\n\n- Which store?\n## 5. Units\n\n### Unit 1: parser (done)\n\n- Add `Parse`.\n- Test it.\n\n### Unit 2: api\n\nWire it.\n\n```go\nfunc route() {}\n```\n"))
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

		p, err := client.CreatePlan(ctx, "b plan", "", "CircleCI-Public/chunk-cli")
		assert.NilError(t, err)
		reqs = fake.Recorder.AllRequests()
		last = reqs[len(reqs)-1]
		body = nil
		err = json.Unmarshal(last.Body, &body)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(body, map[string]any{"data": map[string]any{"attributes": map[string]any{
			"title": "b plan",
			"vcs":   []any{map[string]any{"provider": "github", "repo_full_name": "CircleCI-Public/chunk-cli"}},
		}}}))
		got, ok := fake.FactoryPlan(p.ID)
		assert.Assert(t, ok)
		assert.Check(t, cmp.DeepEqual(got.Repos, []string{"CircleCI-Public/chunk-cli"}))
		assert.Check(t, cmp.Equal(got.Text,
			"# b plan\n\n## 1. Outcome\n\n## 2. Design\n\n## 3. Open Questions\n\n## 4. Worked example\n\n## 5. Units\n"))
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
		_, err = client.PlanStructuredContent(ctx, id)
		assertStatus(t, err, http.StatusNotFound)
		assertStatus(t, client.SetPlanText(ctx, id, "x"), http.StatusNotFound)
		err = client.DeletePlan(ctx, id)
		assertStatus(t, err, http.StatusNotFound)
	})

	t.Run("deletes a plan, its comments and its flow", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		_, err = client.CreateComment(ctx, p.ID, "", "a comment")
		assert.NilError(t, err)

		err = client.DeletePlan(ctx, p.ID)
		assert.NilError(t, err)

		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Check(t, cmp.Equal(last.Method, http.MethodDelete))
		assert.Check(t, cmp.Equal(last.URL.Path, "/api/v3/factory/plans/"+p.ID))
		_, err = client.GetPlan(ctx, p.ID)
		assertStatus(t, err, http.StatusNotFound)
		_, err = client.GetFlow(ctx, p.ID)
		assertStatus(t, err, http.StatusNotFound)
		_, err = client.ListComments(ctx, p.ID, false)
		assertStatus(t, err, http.StatusNotFound)
	})

	t.Run("refuses to delete a plan with sub-plans or a claim", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func(t *testing.T, fake *fakes.FakeCircleCI, client *Client, planID string)
			want  string
		}{
			{
				name: "sub-plans",
				setup: func(t *testing.T, fake *fakes.FakeCircleCI, client *Client, planID string) {
					child, err := client.CreatePlan(ctx, "a sub-plan", "")
					assert.NilError(t, err)
					fake.SetFactoryParent(child.ID, planID)
				},
				want: "sub-plans",
			},
			{
				name: "a claim",
				setup: func(_ *testing.T, fake *fakes.FakeCircleCI, _ *Client, planID string) {
					fake.ClaimFactoryPlan(planID, "s-1")
				},
				want: "claimed",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fake := fakes.NewFakeCircleCI()
				srv := httptest.NewServer(fake)
				t.Cleanup(srv.Close)
				client := newTestClient(t, srv.URL)
				p, err := client.CreatePlan(ctx, "a plan", "")
				assert.NilError(t, err)
				tc.setup(t, fake, client, p.ID)

				err = client.DeletePlan(ctx, p.ID)
				var se *StatusError
				assert.Assert(t, errors.As(err, &se), "got %v", err)
				assert.Check(t, cmp.Equal(se.StatusCode, http.StatusConflict))
				assert.Check(t, cmp.Contains(se.ServerMessage, tc.want))

				_, err = client.GetPlan(ctx, p.ID)
				assert.NilError(t, err, "a refused delete keeps the plan")
			})
		}
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

	t.Run("reads a plan's parent on get and list", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		parent, err := client.CreatePlan(ctx, "retry-policy", "")
		assert.NilError(t, err)
		child, err := client.CreatePlan(ctx, "retry-config", "")
		assert.NilError(t, err)
		fake.SetFactoryParent(child.ID, parent.ID)

		got, err := client.GetPlan(ctx, child.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(got.Parent, parent.ID))
		got, err = client.GetPlan(ctx, parent.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(got.Parent, ""), "a top-level plan has no parent")

		plans, err := client.ListPlans(ctx)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(parentsOf(plans), map[string]string{parent.ID: "", child.ID: parent.ID}))

		flows, err := client.ListFlows(ctx)
		assert.NilError(t, err)
		flowParents := map[string]string{}
		for _, f := range flows {
			flowParents[f.ID] = f.Parent
		}
		assert.Check(t, cmp.DeepEqual(flowParents, map[string]string{parent.ID: "", child.ID: parent.ID}))
		flow, err := client.GetFlow(ctx, child.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(flow.Parent, parent.ID))
		flow, err = client.GetFlow(ctx, parent.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(flow.Parent, ""), "a top-level plan's flow has no parent")
	})

	t.Run("lists open plans across pages", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.FactoryPageSize = 2
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		for _, title := range []string{"a", "b", "c", "d", "e"} {
			p, err := client.CreatePlan(ctx, title, "")
			assert.NilError(t, err)
			if title == "c" {
				fake.SetFactoryPlanState(p.ID, "ended")
			}
		}

		plans, err := client.ListPlans(ctx)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(titles(plans), []string{"a", "b", "d", "e"}), "oldest first, ended left out")
		last := fake.Recorder.AllRequests()[len(fake.Recorder.AllRequests())-1]
		assert.Check(t, cmp.Equal(last.URL.Query().Get("filter[user_id]"), "me"))
	})

	t.Run("reads references as conductor may serve them", func(t *testing.T) {
		for _, tc := range []struct {
			name, refs string
			want       string
		}{
			{
				name: "no references",
			},
			{
				name: "empty references",
				refs: `,"references":{}`,
			},
			{
				name: "no plans",
				refs: `,"references":{"factory_plans":[]}`,
			},
			{
				name: "a plan it blocks",
				refs: `,"references":{"factory_plans":[{"id":"x","rel":"blocks"}]}`,
			},
			{
				name: "a parent",
				refs: `,"references":{"factory_plans":[{"id":"p1","rel":"parent"}]}`,
				want: "p1",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				id := uuid.NewString()
				item := `{"id":"` + id + `","attributes":{"title":"a plan"}` + tc.refs + `}`
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/api/v3/factory/plans" {
						_, _ = w.Write([]byte(`{"data":[` + item + `]}`))
						return
					}
					_, _ = w.Write([]byte(`{"data":` + item + `}`))
				}))
				t.Cleanup(srv.Close)
				client := newTestClient(t, srv.URL)

				got, err := client.GetPlan(ctx, id)
				assert.NilError(t, err)
				assert.Check(t, cmp.Equal(got.Parent, tc.want))
				plans, err := client.ListPlans(ctx)
				assert.NilError(t, err)
				assert.Assert(t, cmp.Len(plans, 1))
				assert.Check(t, cmp.Equal(plans[0].Parent, tc.want))
			})
		}
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
		assert.Check(t, cmp.Equal(f.State, "init"))
		assert.Check(t, cmp.Equal(f.SessionID, ""))
		assert.Check(t, cmp.Equal(f.Title, "a plan"))
		assert.Check(t, !f.UpdatedAt.IsZero())
		assert.Check(t, cmp.Equal(f.Phase, PhaseReceived))

		fake.SetFactoryPlanState(p.ID, "refine-proposal")
		f, err = client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(f.Phase, PhaseStarted))

		fake.SetFactoryPlanState(p.ID, "ended")
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

	t.Run("an ended plan keeps its flow, with its outcome", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)

		fake.SetFactoryPlanState(p.ID, "ended")
		fake.SetFactoryFlowStatus(p.ID, fakes.FactoryFlowStatus{Outcome: "rejected"})
		f, err := client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(f.Phase, PhaseEnded))
		assert.Check(t, cmp.Equal(f.Outcome, "rejected"))
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

		fake.SetFactoryTask(p.ID, "rebase")
		fake.SetFactoryPlanState(p.ID, "integrate")
		fake.SetFactoryRequiredEvents(p.ID, "answer-open-questions")
		fake.SetFactoryApplicableEvents(p.ID, "address-comments", "abandon-integrate", "abandon")
		f, err = client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(f.ApplicableEvents,
			[]FlowEvent{AnswerOpenQuestions, AddressComments, "abandon-integrate", "abandon"}),
			"required events first, after the task")
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

	t.Run("abandons a flow for each reason", func(t *testing.T) {
		for _, tc := range []struct {
			reason  AbandonReason
			outcome string
		}{
			{
				reason:  AbandonCanceled,
				outcome: "canceled",
			},
			{
				reason:  AbandonWontDo,
				outcome: "rejected",
			},
			{
				reason:  AbandonDuplicate,
				outcome: "duplicated",
			},
		} {
			t.Run(string(tc.reason), func(t *testing.T) {
				fake := fakes.NewFakeCircleCI()
				srv := httptest.NewServer(fake)
				t.Cleanup(srv.Close)
				client := newTestClient(t, srv.URL)
				p, err := client.CreatePlan(ctx, "a plan", "")
				assert.NilError(t, err)
				fake.SetFactoryPlanState(p.ID, "refine-proposal")

				err = client.AbandonFlow(ctx, p.ID, tc.reason)
				assert.NilError(t, err)

				reqs := fake.Recorder.AllRequests()
				last := reqs[len(reqs)-1]
				var body map[string]any
				err = json.Unmarshal(last.Body, &body)
				assert.NilError(t, err)
				assert.Check(t, cmp.DeepEqual(body, map[string]any{"data": map[string]any{
					"attributes": map[string]any{"event": "abandon", "reason": string(tc.reason)},
					"references": map[string]any{"factory_flow": map[string]any{"id": p.ID}},
				}}))
				got, _ := fake.FactoryPlan(p.ID)
				assert.Assert(t, len(got.Events) > 0)
				assert.Check(t, cmp.Equal(got.Events[len(got.Events)-1], "abandon"))
				assert.Assert(t, cmp.Len(got.Reasons, len(got.Events)))
				assert.Check(t, cmp.Equal(got.Reasons[len(got.Reasons)-1], string(tc.reason)))

				fake.SetFactoryPlanState(p.ID, "ended")
				fake.SetFactoryFlowStatus(p.ID, fakes.FactoryFlowStatus{Outcome: tc.outcome})
				f, err := client.GetFlow(ctx, p.ID)
				assert.NilError(t, err)
				assert.Check(t, cmp.Equal(f.Phase, PhaseEnded))
				assert.Check(t, cmp.Equal(f.Outcome, tc.outcome))
			})
		}
	})

	t.Run("abandoning a plan with no flow is a 404", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		fake.DropFactoryFlow(p.ID)

		err = client.AbandonFlow(ctx, p.ID, AbandonCanceled)
		var se *StatusError
		assert.Assert(t, errors.As(err, &se), "got %v", err)
		assert.Check(t, cmp.Equal(se.StatusCode, http.StatusNotFound))
	})

	t.Run("the fake refuses an abandon without a reason, and a reason on another event", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)

		for _, tc := range []struct {
			name  string
			attrs string
			want  string
		}{
			{
				name:  "an abandon with no reason",
				attrs: `{"event":"abandon"}`,
				want:  "an abandon needs a reason",
			},
			{
				name:  "an abandon with an unknown reason",
				attrs: `{"event":"abandon","reason":"bored"}`,
				want:  "an abandon needs a reason",
			},
			{
				name:  "a reason on another event",
				attrs: `{"event":"address-comments","reason":"canceled"}`,
				want:  "only abandon takes a reason",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				body := `{"data":{"attributes":` + tc.attrs + `,"references":{"factory_flow":{"id":"` + p.ID + `"}}}}`
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/v3/factory/events", strings.NewReader(body))
				assert.NilError(t, err)
				req.Header.Set("Circle-Token", "test-token")
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				assert.NilError(t, err)
				t.Cleanup(func() { _ = resp.Body.Close() })
				msg, err := io.ReadAll(resp.Body)
				assert.NilError(t, err)
				assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusBadRequest))
				assert.Check(t, cmp.Contains(string(msg), tc.want))
			})
		}
		got, _ := fake.FactoryPlan(p.ID)
		assert.Check(t, cmp.Len(got.Events, 0), "a refused event is not stored")
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
		fake.SetFactoryPlanState(b.ID, "ended")
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
				ID: a.ID, Title: "a", State: "init", Phase: PhaseReceived,
				SessionID: "session-1", ApplicableEvents: []FlowEvent{},
				Deviations: []string{}, Next: []Transition{},
			},
			{
				ID: b.ID, Title: "b", State: "ended", Phase: PhaseEnded, Outcome: "merged", ApplicableEvents: []FlowEvent{},
				Deviations: []string{}, Next: []Transition{},
			},
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
		two, err := client.CreatePlan(ctx, "two", "")
		assert.NilError(t, err)
		fake.SetFactoryFlowStatus(two.ID, fakes.FactoryFlowStatus{
			WaitingOn: "session", WaitingOnWhy: "the session reviews the plan",
			Progress: map[string]any{"summary": "Reviewing the plan"},
		})

		flows, err := client.ListFlows(ctx)
		assert.NilError(t, err)
		var titles, phases []string
		for _, f := range flows {
			titles = append(titles, f.Title)
			phases = append(phases, f.Phase)
		}
		assert.Check(t, cmp.DeepEqual(titles, []string{"one", "two"}))
		assert.Check(t, cmp.DeepEqual(phases, []string{PhaseReceived, PhaseReceived}))
		assert.Assert(t, cmp.Len(flows, 2))
		assert.Check(t, cmp.Nil(flows[0].Progress))
		assert.Assert(t, flows[1].Progress != nil)
		assert.Check(t, cmp.Equal(flows[1].Progress.Summary, "Reviewing the plan"))
		assert.Check(t, cmp.Nil(flows[1].Progress.Units))
		assert.Check(t, cmp.Equal(flows[1].WaitingOnWhy, "the session reviews the plan"))
	})

	t.Run("reads a flow from a conductor that serves no title or phase", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"a","attributes":{"state":"ended"}}]}`))
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

	t.Run("reads a flow's events from next alone", func(t *testing.T) {
		for _, tc := range []struct {
			name, attrs string
			want        []FlowEvent
		}{
			{
				name:  "from a conductor that serves applicable_events and next",
				attrs: `"applicable_events":["abandon"],"next":[{"actor":"human","event":"address-comments"},{"actor":"human","event":"abandon"}]`,
				want:  []FlowEvent{AddressComments, "abandon"},
			},
			{
				name:  "from a conductor that serves no next",
				attrs: `"applicable_events":["abandon"]`,
				want:  nil,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				id := uuid.NewString()
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"data":{"id":"` + id + `","attributes":{"state":"refine-proposal",` + tc.attrs + `}}}`))
				}))
				t.Cleanup(srv.Close)
				client := newTestClient(t, srv.URL)

				f, err := client.GetFlow(ctx, id)
				assert.NilError(t, err)
				assert.Check(t, cmp.DeepEqual(f.ApplicableEvents, tc.want))
			})
		}
	})

	t.Run("reads the flow's status and next", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		p, err := client.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		fake.SetFactoryPlanState(p.ID, "build")
		fake.SetFactoryTask(p.ID, "build-unit")
		fake.SetFactoryFlowStatus(p.ID, fakes.FactoryFlowStatus{
			PR: "https://github.com/o/r/pull/1", Deviations: []string{"lint"}, Outcome: "merged",
			Next: []fakes.FactoryTransition{
				{Actor: "session", TaskCategory: "build-unit", IsDispatched: true},
				{Actor: "human", Event: "answer-open-questions", IsRequired: true},
				{Actor: "human", Action: "merge-pr"},
				{Actor: "robot"},
			},
			TaskUnit:     "Unit 2: parser",
			WaitingOn:    "session",
			WaitingOnWhy: "the session builds Unit 2: parser",
			Progress: map[string]any{
				"summary": "Building unit 2 of 5", "current_unit": "Unit 2: parser",
				"done_unit_count": 1, "unit_count": 5, "deviation_count": 0,
				"fixup_count": 0, "finding_count": 3, "revision_count": 1,
			},
		})

		f, err := client.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Assert(t, f.Task != nil)
		assert.Check(t, f.Task.ID != "")
		f.UpdatedAt = time.Time{}
		assert.Check(t, cmp.DeepEqual(f, &Flow{
			ID: p.ID, Title: "a plan", State: "build", Phase: PhaseStarted,
			ApplicableEvents: []FlowEvent{AnswerOpenQuestions},
			Task:             &FlowTask{ID: f.Task.ID, Category: "build-unit", Unit: "Unit 2: parser"},
			Outcome:          "merged", PR: "https://github.com/o/r/pull/1",
			Deviations: []string{"lint"},
			Next: []Transition{
				{Actor: "session", TaskCategory: "build-unit", IsDispatched: true},
				{Actor: "human", Event: AnswerOpenQuestions, IsRequired: true},
				{Actor: "human", Action: "merge-pr"},
				{Actor: "robot"},
			},
			WaitingOn:    "session",
			WaitingOnWhy: "the session builds Unit 2: parser",
			Progress: &FlowProgress{
				Summary: "Building unit 2 of 5", CurrentUnit: "Unit 2: parser",
				DoneUnits: ptr(1), Units: ptr(5), Deviations: ptr(0),
				Findings: 3, Revisions: 1,
			},
		}))
	})

	t.Run("an older conductor serves no status", func(t *testing.T) {
		id := uuid.NewString()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":"` + id + `","attributes":{"state":"refine-proposal","applicable_events":["abandon"]}}}`))
		}))
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)

		f, err := client.GetFlow(ctx, id)
		assert.NilError(t, err)
		assert.Check(t, cmp.Equal(f.PR, ""))
		assert.Check(t, cmp.Nil(f.Deviations))
		assert.Check(t, cmp.Nil(f.Next))
		assert.Check(t, cmp.Equal(f.WaitingOn, ""))
		assert.Check(t, cmp.Equal(f.WaitingOnWhy, ""))
		assert.Check(t, cmp.Nil(f.Progress))

		fake := fakes.NewFakeCircleCI()
		fsrv := httptest.NewServer(fake)
		t.Cleanup(fsrv.Close)
		fclient := newTestClient(t, fsrv.URL)
		p, err := fclient.CreatePlan(ctx, "a plan", "")
		assert.NilError(t, err)
		f, err = fclient.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.Len(f.Deviations, 0))
		assert.Check(t, cmp.Len(f.Next, 0))

		fake.SetFactoryTaskIDOnly(p.ID)
		f, err = fclient.GetFlow(ctx, p.ID)
		assert.NilError(t, err)
		assert.Assert(t, f.Task != nil)
		assert.Check(t, cmp.Equal(f.Task.Unit, ""))
	})

	t.Run("lists a flow's blockers", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)
		c1, err := client.CreatePlan(ctx, "retry-config", "")
		assert.NilError(t, err)
		c2, err := client.CreatePlan(ctx, "retry-wiring", "")
		assert.NilError(t, err)
		o1, err := client.CreatePlan(ctx, "other", "")
		assert.NilError(t, err)
		fake.SetFactoryPlanState(c1.ID, "build")

		got, err := client.ListFlowBlockers(ctx, c2.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(got, []Blocker{}))
		assert.Check(t, cmp.Len(got, 0))

		fake.BlockFactoryPlanUntil(c2.ID, "build", c1.ID)
		fake.BlockFactoryPlan(c2.ID, o1.ID)
		got, err = client.ListFlowBlockers(ctx, c2.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(got, []Blocker{
			{ID: c1.ID, Title: "retry-config", State: "build", Active: false},
			{ID: o1.ID, Title: "other", State: "init", Active: true},
		}), "in the order blocked; one past its until")
		last := fake.Recorder.AllRequests()[len(fake.Recorder.AllRequests())-1]
		assert.Check(t, cmp.Equal(last.URL.Path, "/api/v3/factory/flows/"+c2.ID+"/blockers"))

		fake.DropFactoryFlow(o1.ID)
		got, err = client.ListFlowBlockers(ctx, c2.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(got, []Blocker{
			{ID: c1.ID, Title: "retry-config", State: "build", Active: false},
			{ID: o1.ID, Title: "other", State: "", Active: true},
		}), "a blocker with no flow has no state and still blocks")

		e1, err := client.CreatePlan(ctx, "canceled", "")
		assert.NilError(t, err)
		e2, err := client.CreatePlan(ctx, "merged", "")
		assert.NilError(t, err)
		e3, err := client.CreatePlan(ctx, "ended-blocked", "")
		assert.NilError(t, err)
		fake.SetFactoryPlanState(e1.ID, "ended")
		fake.SetFactoryFlowStatus(e1.ID, fakes.FactoryFlowStatus{Outcome: "canceled"})
		fake.SetFactoryPlanState(e2.ID, "ended")
		fake.SetFactoryFlowStatus(e2.ID, fakes.FactoryFlowStatus{Outcome: "merged"})
		fake.BlockFactoryPlan(e3.ID, e1.ID, e2.ID)
		got, err = client.ListFlowBlockers(ctx, e3.ID)
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(got, []Blocker{
			{ID: e1.ID, Title: "canceled", State: "ended", Outcome: "canceled", Active: true},
			{ID: e2.ID, Title: "merged", State: "ended", Outcome: "merged", Active: false},
		}), "an ended blocker still blocks unless it merged")

		_, err = client.ListFlowBlockers(ctx, uuid.NewString())
		assertStatus(t, err, http.StatusNotFound)
	})

	t.Run("reads a blocker from a conductor that serves no is_active", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"b1","attributes":{"title":"a","state":"build"}}]}`))
		}))
		t.Cleanup(srv.Close)
		client := newTestClient(t, srv.URL)

		got, err := client.ListFlowBlockers(ctx, uuid.NewString())
		assert.NilError(t, err)
		assert.Check(t, cmp.DeepEqual(got, []Blocker{{ID: "b1", Title: "a", State: "build", Active: true}}))
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
		_, err = client.ListFlows(ctx)
		assert.Check(t, cmp.ErrorIs(err, ErrNotAuthorized), "status %d", code)
		_, err = client.ListComments(ctx, uuid.NewString(), true)
		assert.Check(t, cmp.ErrorIs(err, ErrNotAuthorized), "status %d", code)
		_, err = client.ListPlans(ctx)
		assert.Check(t, cmp.ErrorIs(err, ErrNotAuthorized), "status %d", code)
		_, err = client.ListFlowBlockers(ctx, uuid.NewString())
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

func titles(ps []Plan) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Title)
	}
	return out
}

// parentsOf maps each plan's ID to its parent's.
func parentsOf(ps []Plan) map[string]string {
	out := map[string]string{}
	for _, p := range ps {
		out[p.ID] = p.Parent
	}
	return out
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

	_, err := client.ListComments(ctx, "plan-1", false)
	assert.ErrorContains(t, err, "does not advance")
	_, err = client.ListPlans(ctx)
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

func ptr[T any](v T) *T { return &v }

// assertItems checks the sections and units sc decoded from items. The
// top-level attributes it also holds are read through its Markdown.
func assertItems(t *testing.T, sc *PlanStructuredContent, want PlanStructuredContent) {
	t.Helper()
	assert.Check(t, cmp.DeepEqual(sc.Outcome, want.Outcome))
	assert.Check(t, cmp.DeepEqual(sc.Design, want.Design))
	assert.Check(t, cmp.DeepEqual(sc.OpenQuestions, want.OpenQuestions))
	assert.Check(t, cmp.DeepEqual(sc.WorkedExample, want.WorkedExample))
	assert.Check(t, cmp.DeepEqual(sc.Units, want.Units))
}

func TestPlanStructuredContentMarkdown(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, tc := range []struct {
		name string
		in   PlanStructuredContent
		want string
	}{
		{
			name: "nothing served",
			in:   PlanStructuredContent{},
			want: "",
		},
		{
			name: "empty units section",
			in:   PlanStructuredContent{legacy: planLegacy{Units: []planLegacyUnit{}}},
			want: "## 5. Units\n\n",
		},
		{
			name: "sections in skeleton order, units with done",
			in: PlanStructuredContent{legacy: planLegacy{
				WorkedExample: s("\nEx.\n\n"),
				Outcome:       s("\nOut.\n\n"),
				Units: []planLegacyUnit{
					{
						Name: "Unit 1: a",
						Done: true,
						Body: "\nA.\n\n",
					},
					{
						Name: "Unit 2: b",
						Body: "\nB.\n",
					},
				},
			}},
			want: "## 1. Outcome\n\nOut.\n\n## 4. Worked example\n\nEx.\n\n## 5. Units\n\n### Unit 1: a (done)\n\nA.\n\n### Unit 2: b\n\nB.\n",
		},
		{
			name: "empty body",
			in:   PlanStructuredContent{legacy: planLegacy{OpenQuestions: s("")}},
			want: "## 3. Open Questions\n",
		},
		{
			name: "body without a trailing newline",
			in: PlanStructuredContent{legacy: planLegacy{
				Design: s("\nD."),
				Units: []planLegacyUnit{
					{
						Name: "Unit 1: a",
						Body: "\nA.",
					},
				},
			}},
			want: "## 2. Design\n\nD.\n## 5. Units\n\n### Unit 1: a\n\nA.\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.Equal(tc.in.Markdown(), tc.want))
		})
	}
}
