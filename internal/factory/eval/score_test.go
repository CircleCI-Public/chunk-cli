package eval

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

func finding(file string, line int, severity string) review.Finding {
	return review.Finding{File: file, Line: line, Severity: severity, Body: "problem in " + file}
}

func reviewed(prompt string, findings ...review.Finding) review.Result {
	return review.Result{Prompt: prompt, Parsed: review.Parsed{Findings: findings}}
}

// where is each problem's first finding, as file:line, with the prompts that
// reported it.
func where(ps []Problem) []string {
	var out []string
	for _, p := range ps {
		s := p.Findings[0].Location()
		if p.Expected != nil {
			s = "expected " + s
		}
		for _, name := range p.Prompts() {
			s += " " + name
		}
		out = append(out, s)
	}
	return out
}

func TestScoreGroupsBlockingFindingsIntoProblems(t *testing.T) {
	c := Case{Name: "flag", Expect: []Expected{
		{File: "cmd/list.go", Lines: []int{40, 45}, About: "filter ignores --json"},
		{File: "docs/CLI.md", About: "flag not documented"},
		{File: "cmd/list_test.go", Lines: []int{10, 12}, About: "no test for the empty list"},
	}}
	got := Score(c, 1, []review.Result{
		reviewed("correctness",
			finding("cmd/list.go", 48, review.SeverityHigh),   // within slack of 45
			finding("cmd/list.go", 41, review.SeverityMedium), // the same problem again
			finding("cmd/other.go", 3, review.SeverityMedium), // a problem the case does not list
		),
		reviewed("docs", finding("docs/CLI.md", 0, review.SeverityMedium)),
		reviewed("testing",
			finding("cmd/other.go", 5, review.SeverityHigh),      // the unlisted problem, from another prompt
			finding("cmd/list_test.go", 30, review.SeverityHigh), // too far from 10-12: another problem
			finding("cmd/list_test.go", 11, review.SeverityLow),  // right place, but does not block
		),
	})

	assert.Assert(t, !got.Errored())
	assert.DeepEqual(t, where(got.Problems), []string{
		"expected cmd/list.go:48 correctness",
		"expected docs/CLI.md docs",
		"cmd/other.go:3 correctness testing",
		"cmd/list_test.go:30 testing",
	})
	assert.Equal(t, got.Problems[0].Expected.About, "filter ignores --json")
	assert.Equal(t, len(got.Problems[0].Findings), 2)
	assert.DeepEqual(t, got.Missed, []Expected{c.Expect[2]})
	assert.Equal(t, got.Caught(), 2)
	assert.Equal(t, got.Unexpected(), 2)
	assert.Equal(t, got.Repeats(), 2)
	assert.Equal(t, len(got.Findings), 7)
}

func TestScoreGivesEachProblemItsOwnFinding(t *testing.T) {
	// Two problems in one file, anywhere in it: two findings catch both, not
	// one finding both.
	c := Case{Name: "two", Expect: []Expected{{File: "a.go", About: "one"}, {File: "a.go", About: "two"}}}
	got := Score(c, 1, []review.Result{
		reviewed("x", finding("a.go", 3, review.SeverityHigh)),
		reviewed("y", finding("a.go", 9, review.SeverityMedium)),
	})
	assert.DeepEqual(t, where(got.Problems), []string{"expected a.go:3 x", "expected a.go:9 y"})
	assert.Equal(t, got.Repeats(), 0)
}

func TestScorePairsAsManyProblemsAsItCan(t *testing.T) {
	// Taking the first finding about each problem in turn would leave one
	// problem missed in every case here, though each has a finding of its own.
	for _, tc := range []struct {
		name   string
		expect []Expected
		lines  []int
		want   []string
	}{
		{
			name:   "a finding about the whole file",
			expect: []Expected{{File: "a.go", Lines: []int{105, 105}}, {File: "a.go", Lines: []int{61, 68}}},
			lines:  []int{0, 105},
			want:   []string{"expected a.go:105 x", "expected a.go x"},
		},
		{
			name:   "a problem anywhere in the file",
			expect: []Expected{{File: "a.go"}, {File: "a.go", Lines: []int{61, 68}}},
			lines:  []int{64, 200},
			want:   []string{"expected a.go:200 x", "expected a.go:64 x"},
		},
		{
			name:   "overlapping lines",
			expect: []Expected{{File: "a.go", Lines: []int{10, 20}}, {File: "a.go", Lines: []int{18, 25}}},
			lines:  []int{19, 12},
			want:   []string{"expected a.go:12 x", "expected a.go:19 x"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fs []review.Finding
			for _, l := range tc.lines {
				fs = append(fs, finding("a.go", l, review.SeverityHigh))
			}
			got := Score(Case{Name: "c", Expect: tc.expect}, 1, []review.Result{reviewed("x", fs...)})
			assert.DeepEqual(t, where(got.Problems), tc.want)
			assert.Equal(t, got.Caught(), 2)
			assert.Equal(t, len(got.Missed), 0)
		})
	}
}

func TestScoreKeepsUnlistedProblemsApartByLine(t *testing.T) {
	got := Score(Case{Name: "clean"}, 1, []review.Result{
		reviewed("x", finding("a.go", 10, review.SeverityHigh), finding("a.go", 20, review.SeverityHigh)),
		reviewed("y", finding("a.go", 12, review.SeverityMedium), finding("a.go", 0, review.SeverityMedium)),
	})
	// 12 is near 10; a finding on the whole file is about the first problem
	// in it.
	assert.DeepEqual(t, where(got.Problems), []string{"a.go:10 x y", "a.go:20 x"})
	assert.Equal(t, got.Repeats(), 2)
}

func TestScoreRecordsReviewsThatFailed(t *testing.T) {
	got := Score(Case{Name: "c"}, 2, []review.Result{
		reviewed("ok"),
		{Prompt: "broken", Error: "claude gave no structured findings"},
	})
	assert.Assert(t, got.Errored())
	assert.Equal(t, got.Run, 2)
	assert.DeepEqual(t, got.Errors, []PromptError{{Prompt: "broken", Error: "claude gave no structured findings"}})
}

func TestSummarize(t *testing.T) {
	flag := Case{Name: "flag", Expect: []Expected{{File: "a.go", About: "bug"}, {File: "b.go", About: "untested"}}}
	clean := Case{Name: "clean", Clean: true}
	unlabeled := Case{Name: "unlabeled"}
	results := []CaseResult{
		Score(flag, 1, []review.Result{
			reviewed("bugs", finding("a.go", 1, review.SeverityHigh), finding("a.go", 2, review.SeverityHigh)),
			reviewed("tests", finding("a.go", 1, review.SeverityMedium), finding("c.go", 1, review.SeverityLow)),
		}),
		Score(clean, 1, []review.Result{
			reviewed("bugs", finding("c.go", 2, review.SeverityHigh)),
			reviewed("tests", finding("c.go", 1, review.SeverityMedium)),
		}),
		Score(clean, 2, []review.Result{reviewed("bugs"), reviewed("tests", finding("c.go", 1, review.SeverityLow))}),
		Score(clean, 3, []review.Result{reviewed("bugs"), {Prompt: "tests", Error: "timed out"}}),
		// Unlabeled, its problem counts as reported but neither for nor
		// against the labels.
		Score(unlabeled, 1, []review.Result{reviewed("bugs", finding("d.go", 1, review.SeverityHigh)), reviewed("tests")}),
	}

	s := Summarize([]string{"bugs", "tests", "quiet"}, results)

	assert.Equal(t, s.Runs, 5)
	assert.Equal(t, s.Scored, 4)
	assert.Equal(t, s.Labeled, 3)
	assert.Equal(t, s.Errored, 1)
	assert.Equal(t, s.Expected, 2)
	assert.Equal(t, s.Caught, 1)
	assert.Equal(t, *s.Recall, 0.5)
	assert.Equal(t, s.Blocking, 6)
	assert.Equal(t, s.Reported, 3)
	assert.Equal(t, s.ReportedLabeled, 2)
	assert.Equal(t, s.Unexpected, 1)
	assert.Equal(t, s.Repeats, 3)
	assert.Equal(t, s.Shared, 2)
	assert.Equal(t, *s.Precision, 0.5)
	assert.Equal(t, s.Clean, 2)
	assert.Equal(t, s.CleanPassed, 1)
	assert.DeepEqual(t, *s.Prompts["bugs"], PromptSummary{Blocking: 4, Caught: 1, Unexpected: 1, OnClean: 1, Shared: 2})
	assert.DeepEqual(t, *s.Prompts["tests"], PromptSummary{Blocking: 2, Caught: 1, Unexpected: 1, OnClean: 1, Shared: 2, Errors: 1})
	assert.DeepEqual(t, *s.Prompts["quiet"], PromptSummary{})
	assert.Assert(t, s.Graded == nil)
}

func TestSummarizeLeavesRatiosOutWithNothingToDivideBy(t *testing.T) {
	s := Summarize([]string{"bugs"}, []CaseResult{Score(Case{Name: "clean", Clean: true}, 1, []review.Result{reviewed("bugs")})})
	assert.Assert(t, s.Recall == nil)
	assert.Assert(t, s.Precision == nil)
	assert.Equal(t, s.CleanPassed, 1)
}
