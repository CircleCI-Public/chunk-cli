package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// gradeResult is claude's result event carrying answer as its structured
// output.
func gradeResult(t *testing.T, answer any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"type": "result", "subtype": "success", "structured_output": answer})
	assert.NilError(t, err)
	return string(data)
}

type verdict struct {
	ID          string `json:"id"`
	Verdict     string `json:"verdict"`
	DuplicateOf string `json:"duplicate_of,omitempty"`
	Reason      string `json:"reason"`
}

func TestGraderPromptHidesWhichPromptReportedEachFinding(t *testing.T) {
	a := finding("b.go", 9, review.SeverityHigh)
	a.Prompt = "injection-review"
	low := finding("a.go", 1, review.SeverityLow)
	low.Prompt = "naming-review"
	b := finding("a.go", 3, review.SeverityMedium)
	b.Prompt = "coverage-review"

	blocking := blockingInOrder([]review.Finding{a, low, b})
	prompt := graderPrompt("add a flag", blocking)

	// Only blocking findings, in file order, numbered for the answer.
	assert.Equal(t, len(blocking), 2)
	assert.Equal(t, blocking[0].File, "a.go")
	assert.Assert(t, strings.Contains(prompt, "### F1 (medium, a.go:3)"), prompt)
	assert.Assert(t, strings.Contains(prompt, "### F2 (high, b.go:9)"), prompt)
	assert.Assert(t, strings.Contains(prompt, "add a flag"), prompt)
	// The bar reviews were held to, so the grader holds them to no other.
	assert.Assert(t, strings.Contains(prompt, "What blocks a change"), prompt)
	for _, name := range []string{"injection-review", "naming-review", "coverage-review"} {
		assert.Assert(t, !strings.Contains(prompt, name), "prompt names %s", name)
	}
}

func TestGraderPromptAsksForMissesWhenNothingWasReported(t *testing.T) {
	prompt := graderPrompt("add a flag", nil)
	assert.Assert(t, strings.Contains(prompt, "reported no blocking findings"), prompt)
}

func TestParseGrade(t *testing.T) {
	blocking := []review.Finding{
		finding("a.go", 1, review.SeverityHigh),
		finding("a.go", 2, review.SeverityHigh),
		finding("a.go", 3, review.SeverityMedium),
		finding("b.go", 1, review.SeverityMedium),
	}
	out := gradeResult(t, map[string]any{
		"findings": []verdict{
			{ID: "F1", Verdict: VerdictConfirmed, Reason: "divides by zero"},
			{ID: "F2", Verdict: VerdictConfirmed, DuplicateOf: "F1"},
			// A chain resolves to its first finding.
			{ID: "F3", Verdict: VerdictConfirmed, DuplicateOf: "F2"},
			// Only the first verdict on a finding counts.
			{ID: "F1", Verdict: VerdictWrong},
			// Ids it was not given, and verdicts that are not ones, are
			// dropped; F4 is left ungraded.
			{ID: "F9", Verdict: VerdictWrong},
			{ID: "F4", Verdict: "maybe"},
		},
		"missed": []Missed{
			{File: "c.go", Line: 4, Severity: "high", About: "nil map write"},
			{File: "", Severity: "high", About: "nowhere"},
		},
		"summary": " sound but for one bug ",
	})

	g, err := parseGrade(out, blocking)
	assert.NilError(t, err)

	var got []string
	for _, f := range g.Findings {
		got = append(got, fmt.Sprintf("%s %s %d", f.Finding.Location(), f.Verdict, f.DuplicateOf))
	}
	assert.DeepEqual(t, got, []string{"a.go:1 confirmed -1", "a.go:2 confirmed 0", "a.go:3 confirmed 0", "b.go:1  -1"})
	assert.Equal(t, g.Ungraded, 1)
	assert.DeepEqual(t, g.Problems(), []int{0, 3})
	assert.DeepEqual(t, g.Missed, []Missed{{File: "c.go", Line: 4, Severity: "high", About: "nil map write"}})
	assert.Equal(t, g.Summary, "sound but for one bug")
}

// A finding that names a later one, or itself, as what it duplicates stands
// for its own problem; so does one whose original was left ungraded, or that
// problem would go uncounted.
func TestParseGradeIgnoresDuplicatesThatPointNowhere(t *testing.T) {
	blocking := []review.Finding{
		finding("a.go", 1, review.SeverityHigh),
		finding("a.go", 2, review.SeverityHigh),
		finding("a.go", 3, review.SeverityHigh),
	}
	out := gradeResult(t, map[string]any{
		"findings": []verdict{
			{ID: "F1", Verdict: VerdictWrong, DuplicateOf: "F2"},
			{ID: "F3", Verdict: VerdictConfirmed, DuplicateOf: "F2"},
		},
		"missed": []Missed{}, "summary": "",
	})

	g, err := parseGrade(out, blocking)
	assert.NilError(t, err)
	assert.Equal(t, g.Findings[0].DuplicateOf, -1)
	assert.Equal(t, g.Findings[2].DuplicateOf, -1)
	assert.Equal(t, g.Ungraded, 1)
}

func TestParseGradeFailsWithoutAGrade(t *testing.T) {
	_, err := parseGrade(`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"ran out"}`, nil)
	assert.ErrorContains(t, err, "the grader gave no grade (error_max_turns): ran out")
}

// graded returns r with a grade giving each blocking finding the verdict in
// verdicts, by location, and the misses given.
func graded(r CaseResult, verdicts map[string]string, missed ...Missed) CaseResult {
	g := &Grade{}
	for _, f := range blockingInOrder(r.Findings) {
		g.Findings = append(g.Findings, GradedFinding{Finding: f, Verdict: verdicts[f.Location()], DuplicateOf: -1})
	}
	g.Missed = missed
	g.CostUSD = 0.5
	r.Grade = g
	return r
}

func TestSummarizeGraded(t *testing.T) {
	flag := Case{Name: "flag", Expect: []Expected{{File: "a.go", Lines: []int{1, 1}, About: "bug"}, {File: "b.go", Lines: []int{7, 7}, About: "untested"}}}
	clean := Case{Name: "clean", Clean: true}
	unlabeled := Case{Name: "unlabeled"}

	onFlag := graded(Score(flag, 1, []review.Result{
		reviewed("bugs", finding("a.go", 1, review.SeverityHigh), finding("z.go", 5, review.SeverityMedium)),
		reviewed("tests", finding("a.go", 2, review.SeverityMedium)),
	}), map[string]string{"a.go:1": VerdictConfirmed, "a.go:2": VerdictConfirmed, "z.go:5": VerdictWrong},
		// The grader found the expected problem the reviews missed.
		Missed{File: "b.go", Line: 8, Severity: "medium", About: "untested"})
	// The grader marks the second a.go finding as the first's problem.
	onFlag.Grade.Findings[1].DuplicateOf = 0

	onClean := graded(Score(clean, 1, []review.Result{
		reviewed("bugs", finding("c.go", 2, review.SeverityHigh)),
		reviewed("tests"),
	}), map[string]string{"c.go:2": VerdictConfirmed}, Missed{File: "c.go", Line: 9, Severity: "high", About: "leak"})

	onUnlabeled := graded(Score(unlabeled, 1, []review.Result{
		reviewed("bugs"),
		reviewed("tests", finding("d.go", 1, review.SeverityMedium)),
	}), map[string]string{"d.go:1": VerdictNotBlocking})

	failed := Score(unlabeled, 2, []review.Result{reviewed("bugs"), reviewed("tests")})
	failed.GradeError = "timed out"

	s := Summarize([]string{"bugs", "tests"}, []CaseResult{onFlag, onClean, onUnlabeled, failed})

	assert.Assert(t, s.Graded != nil)
	g := *s.Graded
	assert.Equal(t, g.Runs, 3)
	assert.Equal(t, g.Errors, 1)
	// Five blocking findings, one a duplicate: four problems.
	assert.Equal(t, g.Problems, 4)
	assert.Equal(t, g.Duplicates, 1)
	assert.Equal(t, g.Confirmed, 2)
	assert.Equal(t, g.NotBlocking, 1)
	assert.Equal(t, g.Wrong, 1)
	assert.Equal(t, g.Missed, 2)
	assert.Equal(t, *g.Precision, 0.5)
	assert.Equal(t, *g.Coverage, 0.5)
	assert.Equal(t, g.CostUSD, 1.5)
	assert.DeepEqual(t, g.Calibration, Calibration{
		// Both a.go findings are about the expected bug, and both confirmed.
		OnExpected: 2, OnExpectedConfirmed: 2,
		// The grader confirmed a problem on a case labeled clean, and found
		// one more there.
		OnClean: 1, OnCleanConfirmed: 1, MissedOnClean: 1,
		ExpectedMissed: 1, ExpectedFound: 1,
	})
	assert.DeepEqual(t, *s.Prompts["bugs"], PromptSummary{Blocking: 3, Caught: 1, Unexpected: 2, OnClean: 1, Shared: 1, Confirmed: 2, Wrong: 1})
	assert.DeepEqual(t, *s.Prompts["tests"], PromptSummary{Blocking: 2, Caught: 1, Shared: 1, Confirmed: 1, NotBlocking: 1})
}

// graderAndReviewer is a fake claude that grades when its prompt is the
// grader's, confirming every finding and missing nothing, and reviews as
// fakeReviewer otherwise. It records which sidecar each grading ran on.
const graderAndReviewer = `input=$(cat)
case "$input" in
*"independent grader"*)
  mkdir -p "$HOME/graded"
  pwd >> "$HOME/graded/where"
  printf '{"type":"result","subtype":"success","is_error":false,"result":"","total_cost_usd":0.25,"structured_output":{"findings":[{"id":"F1","verdict":"confirmed","reason":"checked"},{"id":"F2","verdict":"wrong","duplicate_of":"F1","reason":"same"}],"missed":[],"summary":"ok"}}\n'
  exit 0;;
esac
` + fakeReviewer

func installFakeClaudeReadingStdin(t *testing.T, home, body string) {
	t.Helper()
	bin := filepath.Join(home, ".local", "bin")
	assert.NilError(t, os.MkdirAll(bin, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
}

func TestRunGradesEachCaseRunOnTheFirstSidecar(t *testing.T) {
	f := newEvalFixture(t)
	installFakeClaudeReadingStdin(t, f.home, graderAndReviewer)
	f.opts.Grade = &GradeOptions{Credential: f.opts.Review.Credential}

	results, err := Run(context.Background(), f.opts)
	assert.NilError(t, err)

	assert.Equal(t, len(results), 2)
	for _, r := range results {
		assert.Assert(t, !r.Errored(), "%s: %+v", r.Case, r.Errors)
		assert.Equal(t, r.GradeError, "", r.Case)
		assert.Assert(t, r.Grade != nil, r.Case)
		assert.Equal(t, len(r.Grade.Findings), 2, r.Case)
		assert.Equal(t, r.Grade.Findings[0].Verdict, VerdictConfirmed)
		assert.Equal(t, r.Grade.Findings[1].DuplicateOf, 0)
		assert.Equal(t, r.Grade.CostUSD, 0.25)
	}
	where, err := os.ReadFile(filepath.Join(f.home, "graded", "where"))
	assert.NilError(t, err)
	want := f.entries[0].RepoPath + "\n" + f.entries[0].RepoPath + "\n"
	got := strings.ReplaceAll(string(where), "/private/var/", "/var/")
	assert.Equal(t, got, strings.ReplaceAll(want, "/private/var/", "/var/"))
}

// A grading that fails leaves its case scored on its labels, and the suite
// goes on.
func TestRunRecordsAFailedGradingAndGoesOn(t *testing.T) {
	f := newEvalFixture(t)
	installFakeClaudeReadingStdin(t, f.home, `input=$(cat)
case "$input" in
*"independent grader"*) echo '{"type":"result","subtype":"error_max_turns","is_error":true,"result":"ran out"}'; exit 0;;
esac
`+fakeReviewer)
	f.opts.Grade = &GradeOptions{Credential: f.opts.Review.Credential}

	results, err := Run(context.Background(), f.opts)
	assert.NilError(t, err)

	assert.Equal(t, len(results), 2)
	for _, r := range results {
		assert.Assert(t, !r.Errored())
		assert.Assert(t, r.Grade == nil)
		assert.Assert(t, strings.Contains(r.GradeError, "ran out"), r.GradeError)
	}
	assert.Equal(t, results[0].Caught(), 1)
}

func TestRunStopsWhenTheGraderCredentialIsRejected(t *testing.T) {
	f := newEvalFixture(t)
	installFakeClaudeReadingStdin(t, f.home, `input=$(cat)
case "$input" in
*"independent grader"*) echo 'Failed to authenticate. API Error: 401' >&2; exit 1;;
esac
`+fakeReviewer)
	f.opts.Grade = &GradeOptions{Credential: f.opts.Review.Credential}

	results, err := Run(context.Background(), f.opts)
	assert.Assert(t, errors.Is(err, claudecode.ErrCredentialRejected), "%v", err)
	assert.Equal(t, len(results), 0)
}

func at(prompt, file string, line int, severity, body string) review.Finding {
	return review.Finding{Prompt: prompt, File: file, Line: line, Severity: severity, Body: body}
}

// The case from chunk-cli's suite that grouping by nearby lines got wrong: a
// remark on the line above the expected leak is another problem, and the
// grader says so.
func TestRegroupSplitsNearbyFindingsTheGraderKeepsApart(t *testing.T) {
	c := Case{Name: "telemetry", Expect: []Expected{{File: "record.go", Lines: []int{105, 105}, About: "leaks error text"}}}
	r := Score(c, 1, []review.Result{
		{Prompt: "adversarial", Parsed: review.Parsed{Findings: []review.Finding{
			at("", "record.go", 105, review.SeverityHigh, "error_message leaks paths"),
		}}},
		{Prompt: "correctness", Parsed: review.Parsed{Findings: []review.Finding{
			at("", "record.go", 104, review.SeverityMedium, "error_type is the wrapper's type"),
			at("", "record.go", 105, review.SeverityMedium, "error text goes to Segment"),
		}}},
	})
	// By nearby lines, all three are the leak.
	assert.DeepEqual(t, where(r.Problems), []string{"expected record.go:105 adversarial correctness"})
	assert.Equal(t, len(r.Problems[0].Findings), 3)

	// Shown by place: F1 the error_type remark, F2 and F3 the leak.
	r = graded(r, map[string]string{"record.go:104": VerdictNotBlocking, "record.go:105": VerdictConfirmed})
	r.Grade.Findings[2].DuplicateOf = 1
	regroup(&r)

	assert.DeepEqual(t, where(r.Problems), []string{
		"expected record.go:105 adversarial correctness",
		"record.go:104 correctness",
	})
	assert.Equal(t, r.Problems[0].Findings[0].Prompt, "adversarial")
	assert.Equal(t, r.Caught(), 1)
	assert.Equal(t, r.Unexpected(), 1)

	s := Summarize([]string{"adversarial", "correctness"}, []CaseResult{r})
	assert.Equal(t, s.Reported, 2)
	assert.Equal(t, s.Unexpected, 1)
	// Only the two findings about the leak count for it.
	assert.DeepEqual(t, s.Graded.Calibration, Calibration{OnExpected: 2, OnExpectedConfirmed: 2})
	assert.Equal(t, s.Prompts["correctness"].Caught, 1)
	assert.Equal(t, s.Prompts["correctness"].Unexpected, 1)
}

// One problem reported far from where it is expected is still that problem
// when the grader says so.
func TestRegroupJoinsDistantFindingsTheGraderPutsTogether(t *testing.T) {
	c := Case{Name: "flag", Expect: []Expected{{File: "a.go", Lines: []int{10, 10}, About: "bug"}}}
	r := Score(c, 1, []review.Result{
		reviewed("bugs", at("", "a.go", 10, review.SeverityHigh, "nil deref")),
		reviewed("tests", at("", "a.go", 50, review.SeverityMedium, "the nil deref at line 10 is reachable from here")),
	})
	assert.Equal(t, r.Unexpected(), 1)

	r = graded(r, map[string]string{"a.go:10": VerdictConfirmed, "a.go:50": VerdictConfirmed})
	r.Grade.Findings[1].DuplicateOf = 0
	regroup(&r)

	assert.DeepEqual(t, where(r.Problems), []string{"expected a.go:10 bugs tests"})
	assert.Equal(t, r.Unexpected(), 0)
}

// Two expected problems the grader judges one stay two: which were caught is
// the labels' to say. A finding the grader was not shown is a problem of its
// own, not part of whichever problem came first.
func TestRegroupKeepsTheLabelsPairing(t *testing.T) {
	c := Case{Name: "two", Expect: []Expected{
		{File: "x.go", Lines: []int{1, 1}, About: "first"},
		{File: "x.go", Lines: []int{20, 20}, About: "second"},
	}}
	r := Score(c, 1, []review.Result{
		reviewed("bugs", at("", "x.go", 1, review.SeverityHigh, "one"), at("", "x.go", 20, review.SeverityHigh, "two")),
		reviewed("tests", at("", "y.go", 3, review.SeverityHigh, "unseen")),
	})
	r = graded(r, map[string]string{"x.go:1": VerdictConfirmed, "x.go:20": VerdictConfirmed})
	r.Grade.Findings[1].DuplicateOf = 0
	// The grader was shown only the x.go findings.
	r.Grade.Findings = r.Grade.Findings[:2]
	regroup(&r)

	assert.DeepEqual(t, where(r.Problems), []string{"expected x.go:1 bugs", "expected x.go:20 bugs", "y.go:3 tests"})
	assert.Equal(t, r.Caught(), 2)
	assert.Equal(t, len(r.Missed), 0)
}

func TestRegroupLeavesAnUngradedRunAlone(t *testing.T) {
	c := Case{Name: "flag", Expect: []Expected{{File: "a.go", About: "bug"}}}
	r := Score(c, 1, []review.Result{reviewed("bugs", finding("a.go", 1, review.SeverityHigh), finding("b.go", 1, review.SeverityHigh))})
	before := where(r.Problems)
	regroup(&r)
	assert.DeepEqual(t, where(r.Problems), before)
}
