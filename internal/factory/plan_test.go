package factory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/review"
	"github.com/smeltdev/smelt/internal/sidecar"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// promptOf is the prompt a harness script pipes in, which it base64-encodes.
func promptOf(t *testing.T, script string) string {
	t.Helper()
	m := regexp.MustCompile(`echo (\S+) \| base64 -d`).FindStringSubmatch(script)
	assert.Assert(t, m != nil, "no prompt in script:\n%s", script)
	b, err := base64.StdEncoding.DecodeString(m[1])
	assert.NilError(t, err)
	return string(b)
}

const planJSON = `{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{
  "summary":"Add the flag in cmd.",
  "files":[{"path":"cmd/flag.go","change":"add","why":"the flag"}],
  "acceptance":[{"item":"A flag exists","how":"cmd/flag.go","verifiable_in_sandbox":true}],
  "risks":[],"questions":[],"cannot_verify_here":["needs Docker on the host"]}}`

func planSidecars(run func(script string) (int, string, error)) (*Sidecars, *string) {
	entry := &sidecar.PoolEntry{ID: "impl", RepoPath: "/repo"}
	var script string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, s string, _ map[string]string, out iostream.OutputFn, _ func(string)) (int, error) {
		script = s
		code, stdout, err := run(s)
		out(iostream.StreamStdout, []byte(stdout))
		return code, err
	}
	return &Sidecars{Exec: exec, Implementer: &Implementer{Entry: entry}}, &script
}

func TestPlanAsksForTheSchemaWithReadOnlyTools(t *testing.T) {
	s, script := planSidecars(func(string) (int, string, error) { return 0, planJSON, nil })
	plan, err := s.Plan(context.Background(), PlanInput{Request: "Add a flag.\n- [ ] A flag exists"})
	assert.NilError(t, err)
	assert.Equal(t, plan.Summary, "Add the flag in cmd.")
	assert.Equal(t, plan.Files[0].Path, "cmd/flag.go")
	assert.Assert(t, plan.Acceptance[0].VerifiableInSandbox)
	assert.DeepEqual(t, plan.CannotVerifyHere, []string{"needs Docker on the host"})

	assert.Assert(t, contains(*script, "--json-schema"))
	assert.Assert(t, contains(*script, "Read,Grep,Glob,Bash(git log:*),Bash(git diff:*)"))
	for _, writer := range []string{"Edit", "Write"} {
		assert.Assert(t, !contains(*script, writer), writer)
	}
	prompt := promptOf(t, *script)
	assert.Assert(t, contains(prompt, "Add a flag.\n- [ ] A flag exists"), prompt)
	assert.Assert(t, !contains(prompt, "previous plan"), prompt)
}

func TestPlanRevisionCarriesThePlanAndItsFindings(t *testing.T) {
	s, script := planSidecars(func(string) (int, string, error) { return 0, planJSON, nil })
	prev := Plan{Summary: "Old approach."}
	_, err := s.Plan(context.Background(), PlanInput{Request: "r", Previous: &prev,
		Findings: []review.Finding{{File: "plan", Severity: "high", Body: "acceptance item B is not covered"}}})
	assert.NilError(t, err)
	prompt := promptOf(t, *script)
	assert.Assert(t, contains(prompt, "Old approach."), prompt)
	assert.Assert(t, contains(prompt, "[high] plan: acceptance item B is not covered"), prompt)
}

func TestPlanFailures(t *testing.T) {
	for name, run := range map[string]func(string) (int, string, error){
		"exit":    func(string) (int, string, error) { return 1, "", nil },
		"garbage": func(string) (int, string, error) { return 0, "nope", nil },
		"no summary": func(string) (int, string, error) {
			return 0, `{"type":"result","is_error":false,"structured_output":{"summary":" ","files":[],"acceptance":[],"risks":[],"questions":[],"cannot_verify_here":[]}}`, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := planSidecars(run)
			_, err := s.Plan(context.Background(), PlanInput{Request: "r"})
			assert.Assert(t, err != nil)
		})
	}
}

// fakePlanner scripts the plan turns and their reviews.
type fakePlanner struct {
	reviews [][]review.Finding
	inputs  []PlanInput
	plans   []Plan
}

func (f *fakePlanner) Plan(_ context.Context, in PlanInput) (Plan, error) {
	f.inputs = append(f.inputs, in)
	p := Plan{Summary: fmt.Sprintf("plan %d", len(f.inputs)), CannotVerifyHere: []string{"needs Docker on the host"}}
	f.plans = append(f.plans, p)
	return p, nil
}

func (f *fakePlanner) ReviewPlan(context.Context, string, Plan) ([]review.Finding, error) {
	found := f.reviews[0]
	f.reviews = f.reviews[1:]
	return found, nil
}

var (
	planHigh = review.Finding{File: "plan", Severity: "high", Body: "acceptance item B is not covered"}
	planLow  = review.Finding{File: "plan", Severity: "low", Body: "wording"}
)

func TestLoopPlansReviewsAndRevisesOnce(t *testing.T) {
	tests := []struct {
		name       string
		reviews    [][]review.Finding
		wantPlans  int
		wantIn     []string
		wantNotIn  []string
		wantPlanOf string
	}{
		{"approved at once", [][]review.Finding{{planLow}}, 1, []string{"Your plan, reviewed and approved", "plan 1"}, []string{"still disagreed"}, "plan 1"},
		{"revised once and approved", [][]review.Finding{{planHigh}, nil}, 2, []string{"plan 2"}, []string{"still disagreed", "plan 1"}, "plan 2"},
		{"still wrong after the revision goes ahead", [][]review.Finding{{planHigh}, {planHigh}}, 2, []string{"plan 2", "still disagreed", "[high] plan: acceptance item B is not covered"}, nil, "plan 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			planner := &fakePlanner{reviews: tt.reviews}
			steps := &fakeSteps{changes: []Change{change(1)}, checks: [][]Check{{pass}}}
			var kinds []EventKind
			out, err := Loop{Attempts: 2, Planner: planner, OnEvent: func(e Event) { kinds = append(kinds, e.Kind) }}.Run(context.Background(), steps, "add a flag")
			assert.NilError(t, err)
			assert.Equal(t, out.Result, ResultPassed)
			assert.Equal(t, len(planner.plans), tt.wantPlans)
			assert.Equal(t, out.Plan.Summary, tt.wantPlanOf)

			prompt := steps.prompts[0]
			for _, want := range tt.wantIn {
				assert.Assert(t, contains(prompt, want), "missing %q in:\n%s", want, prompt)
			}
			for _, bad := range tt.wantNotIn {
				assert.Assert(t, !contains(prompt, bad), "unexpected %q in:\n%s", bad, prompt)
			}
			// The request follows the plan, and what the plan cannot verify is
			// the seed of the BLOCKED list.
			assert.Assert(t, strings.HasSuffix(prompt, "add a flag"), prompt)
			assert.Assert(t, contains(prompt, "needs Docker on the host"), prompt)
			assert.Assert(t, contains(prompt, "under a BLOCKED: line"), prompt)

			// The plan is settled before the implementer is prompted.
			assert.DeepEqual(t, kinds[:3], []EventKind{EventPlanning, EventPlanReviewed, EventImplementing})
			if tt.wantPlans == 2 {
				assert.Assert(t, planner.inputs[1].Previous != nil)
				assert.Equal(t, planner.inputs[1].Previous.Summary, "plan 1")
				assert.Equal(t, planner.inputs[1].Findings[0].Body, planHigh.Body)
			}
		})
	}
}

func TestLoopSkipsThePlan(t *testing.T) {
	t.Run("no planner", func(t *testing.T) {
		steps := &fakeSteps{changes: []Change{change(1)}, checks: [][]Check{{pass}}}
		out, err := Loop{Attempts: 2}.Run(context.Background(), steps, "add a flag")
		assert.NilError(t, err)
		assert.Assert(t, out.Plan == nil)
		assert.DeepEqual(t, steps.prompts, []string{"add a flag"})
	})
	t.Run("a loop that checks first", func(t *testing.T) {
		planner := &fakePlanner{}
		steps := &fakeSteps{changes: []Change{change(2)}, current: change(1), checks: [][]Check{{fail}, {pass}}}
		out, err := Loop{Attempts: 3, CheckFirst: true, Planner: planner}.Run(context.Background(), steps, "add a flag")
		assert.NilError(t, err)
		assert.Equal(t, out.Result, ResultPassed)
		assert.Equal(t, len(planner.plans), 0)
		assert.Assert(t, out.Plan == nil)
	})
}

func TestPlanReviewPromptGolden(t *testing.T) {
	plan := Plan{
		Summary:    "Add the flag.",
		Files:      []PlanFile{{Path: "cmd/flag.go", Change: "add", Why: "the flag"}},
		Acceptance: []PlanItem{{Item: "A flag exists", How: "cmd/flag.go", VerifiableInSandbox: true}, {Item: "Docker readiness", How: "probe", VerifiableInSandbox: false}},
		Risks:      []string{"flag name clash"},
	}
	got := planReviews("Add a flag.", plan, []review.Prompt{{Name: "requirements", Body: "diff-based text"}, {Name: "style", Body: "x"}, {Name: "adversarial", Body: "y"}})
	assert.Equal(t, len(got), 2)
	assert.Equal(t, got[0].Name, "requirements")
	assert.Equal(t, got[1].Name, "adversarial")
	assert.Assert(t, !contains(got[0].Body, "diff-based text"))

	const want = "Review the plan below against the requested change. No code has been written, so there is no diff: do not look for one, and do not implement anything.\n\n" +
		"## Requested change\n\nAdd a flag.\n\n" +
		"## Plan\n\nAdd the flag.\n\nFiles:\n- cmd/flag.go (add): the flag\n\nAcceptance:\n- A flag exists: cmd/flag.go\n- Docker readiness: probe [not verifiable in the sandbox]\n\nRisks:\n- flag name clash\n\n" +
		"## Review instructions\n\nAnswer only these questions:\n\n" +
		"1. Does the plan cover every requirement and acceptance item the request states?\n" +
		"2. Does any file choice contradict the request?\n" +
		"3. Is anything in the plan out of scope?\n\n" +
		"Report a high or medium finding only for a requirement or acceptance item the plan leaves out, or a choice that contradicts the request. " +
		"Use `plan` as the file for a finding about the plan as a whole, or the file the finding is about. Anything else is low.\n" +
		"Do not judge how the code will be written; judge only what the plan commits to."
	assert.Equal(t, got[0].Body, want)

	assert.Equal(t, len(planReviews("r", plan, []review.Prompt{{Name: "style"}})), 0)
}

// reviewerSidecars is a Sidecars whose reviewer reports a high finding for each
// "- [ ] item" line of the request that the plan under review does not mention.
func reviewerSidecars(t *testing.T, names ...string) (*Sidecars, *int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	exec := func(_ context.Context, _ *sidecar.PoolEntry, script string, _ map[string]string, out iostream.OutputFn, _ func(string)) (int, error) {
		prompt := promptOf(t, script)
		_, afterHead, _ := strings.Cut(prompt, "## Requested change\n\n")
		request, plan, _ := strings.Cut(afterHead, "## Plan")
		var findings []string
		for _, line := range strings.Split(request, "\n") {
			if item, ok := strings.CutPrefix(line, "- [ ] "); ok && !strings.Contains(plan, item) {
				body, _ := json.Marshal(fmt.Sprintf("the plan does not cover %q", item))
				findings = append(findings, fmt.Sprintf(`{"file":"plan","line":0,"severity":"high","body":%s,"patch":""}`, body))
			}
		}
		mu.Lock()
		calls++
		mu.Unlock()
		out(iostream.StreamStdout, []byte(`{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"review":"ok","findings":[`+strings.Join(findings, ",")+`]}}`))
		return 0, nil
	}
	prompts := make([]review.Prompt, len(names))
	for i, n := range names {
		prompts[i] = review.Prompt{Name: n, Body: "body"}
	}
	entry := &sidecar.PoolEntry{ID: "rev", RepoPath: "/repo"}
	return &Sidecars{
		Exec:        exec,
		Implementer: &Implementer{Entry: &sidecar.PoolEntry{ID: "impl"}},
		Acquire:     func(context.Context) (*sidecar.PoolEntry, error) { return entry, nil },
		Release:     func(*sidecar.PoolEntry) {},
		Prompts:     prompts,
		Review:      review.Options{StructuredFindings: true},
	}, &calls
}

func TestReviewPlanFindsAMissingAcceptanceItem(t *testing.T) {
	const request = "Do it.\n- [ ] Plan turn\n- [ ] Gates first\n- [ ] Prove it"
	tests := []struct {
		name string
		plan Plan
		want []string
	}{
		{"covers everything", Plan{Summary: "s", Acceptance: []PlanItem{{Item: "Plan turn"}, {Item: "Gates first"}, {Item: "Prove it"}}}, nil},
		{"misses one", Plan{Summary: "s", Acceptance: []PlanItem{{Item: "Plan turn"}, {Item: "Prove it"}}}, []string{`the plan does not cover "Gates first"`}},
		{"misses all", Plan{Summary: "s"}, []string{`the plan does not cover "Plan turn"`, `the plan does not cover "Gates first"`, `the plan does not cover "Prove it"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, calls := reviewerSidecars(t, "requirements")
			found, err := s.ReviewPlan(context.Background(), request, tt.plan)
			assert.NilError(t, err)
			assert.Equal(t, *calls, 1)
			var got []string
			for _, f := range found {
				assert.Equal(t, f.Prompt, "requirements")
				assert.Assert(t, f.WorthChanging())
				got = append(got, f.Body)
			}
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func TestReviewPlanRunsOnlyTheCompletenessReviews(t *testing.T) {
	s, calls := reviewerSidecars(t, "style", "requirements", "adversarial", "security")
	_, err := s.ReviewPlan(context.Background(), "r", Plan{Summary: "s"})
	assert.NilError(t, err)
	assert.Equal(t, *calls, 2)

	s, calls = reviewerSidecars(t, "style")
	found, err := s.ReviewPlan(context.Background(), "r", Plan{Summary: "s"})
	assert.NilError(t, err)
	assert.Equal(t, *calls, 0)
	assert.Equal(t, len(found), 0)
}

func TestPlanIsInTheNotesPromptAndTheRecord(t *testing.T) {
	plan := Plan{Summary: "Add the flag in cmd."}
	prompt := notesPrompt(NotesInput{Request: "r", Plan: &plan})
	assert.Assert(t, contains(prompt, "Add the flag in cmd."), prompt)
	assert.Assert(t, !contains(notesPrompt(NotesInput{Request: "r"}), "plan the implementer"))

	dir := t.TempDir()
	keepEndedRecord(dir, Record{RunID: "run-1"}, Report{Outcome: Outcome{Result: ResultPassed, Rounds: 1, Plan: &plan}},
		func(iostream.Level, string) {})
	b, err := os.ReadFile(recordPath(dir, "run-1"))
	assert.NilError(t, err)
	assert.Assert(t, contains(string(b), `"plan"`) && contains(string(b), "Add the flag in cmd."), string(b))
}

func TestPlanForSkipsContinuedRuns(t *testing.T) {
	p := &fakePlanner{}
	withGuidance := &Continuation{From: Record{RunID: "run-1"}, Guidance: "also add tests"}
	assert.Assert(t, !withGuidance.checkFirst(), "guidance makes the continuation implement first, so CheckFirst does not skip the plan")

	assert.Equal(t, planFor(true, nil, p), Planner(p))
	assert.Assert(t, planFor(false, nil, p) == nil, "factory.plan false or --no-plan")
	assert.Assert(t, planFor(true, withGuidance, p) == nil)
	assert.Assert(t, planFor(true, &Continuation{From: Record{RunID: "run-1"}}, p) == nil)
}
