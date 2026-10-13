package eval

import (
	"slices"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// lineSlack is how far apart two findings, or a finding and an expected
// problem's lines, may point and still be about the same problem. Reviewers
// often name the line a problem shows on rather than the one it starts on.
const lineSlack = 3

// CaseResult is how one run of the reviews did on one case.
type CaseResult struct {
	Case string `json:"case"`
	Run  int    `json:"run"`
	// Clean is whether the case expects no problems.
	Clean bool `json:"clean"`
	// Errors are the reviews that could not run. A case with any is left out
	// of the summary rather than scored on the reviews that did, which would
	// make a broken prompt look like a quiet one.
	Errors []PromptError `json:"errors,omitempty"`
	// Problems are what the blocking findings were about, each once, with
	// every finding about it: an expected problem one caught, or one the case
	// does not list. An unlisted problem on a clean case sends sound work
	// back. On another it may be real, so it is worth reading rather than
	// counting blindly.
	Problems []Problem `json:"problems,omitempty"`
	// Missed are the expected problems no blocking finding was about, in the
	// case's order.
	Missed []Expected `json:"missed,omitempty"`
	// Findings is everything the reviews reported, blocking or not.
	Findings []review.Finding `json:"findings"`
}

// Problem is one problem the blocking findings on a case were about.
type Problem struct {
	// Expected is the case's problem this is; nil when the case does not list
	// it.
	Expected *Expected `json:"expected,omitempty"`
	// Findings are the blocking findings about it, the first first. More than
	// one is the same problem reported again, by the same prompt or another.
	Findings []review.Finding `json:"findings"`
}

// Prompts are the prompts that reported the problem, each once, in the order
// they first did.
func (p Problem) Prompts() []string {
	var out []string
	for _, f := range p.Findings {
		if !slices.Contains(out, f.Prompt) {
			out = append(out, f.Prompt)
		}
	}
	return out
}

// PromptError is a review that failed on a case.
type PromptError struct {
	Prompt string `json:"prompt"`
	Error  string `json:"error"`
}

// Errored reports whether any review failed on the case.
func (r CaseResult) Errored() bool { return len(r.Errors) > 0 }

// Caught counts the expected problems a blocking finding was about.
func (r CaseResult) Caught() int {
	return len(r.Problems) - r.Unexpected()
}

// Unexpected counts the problems the case does not list.
func (r CaseResult) Unexpected() int {
	n := 0
	for _, p := range r.Problems {
		if p.Expected == nil {
			n++
		}
	}
	return n
}

// Repeats counts blocking findings about a problem an earlier one already
// reported.
func (r CaseResult) Repeats() int {
	n := 0
	for _, p := range r.Problems {
		n += len(p.Findings) - 1
	}
	return n
}

// Score groups the reviews' blocking findings on c into problems and matches
// them against what c expects. Only high and medium findings count: they are
// what factory sends back.
func Score(c Case, run int, results []review.Result) CaseResult {
	out := CaseResult{Case: c.Name, Run: run, Clean: c.Clean(), Findings: []review.Finding{}}
	var blocking []review.Finding
	for _, r := range results {
		if r.Error != "" {
			out.Errors = append(out.Errors, PromptError{Prompt: r.Prompt, Error: r.Error})
			continue
		}
		for _, f := range r.Parsed.Findings {
			f.Prompt = r.Prompt
			out.Findings = append(out.Findings, f)
			if f.WorthChanging() {
				blocking = append(blocking, f)
			}
		}
	}

	// Each expected problem first takes the first finding about it that no
	// other has taken, so two problems in one file are caught by two findings
	// rather than both by one.
	used := make([]bool, len(blocking))
	for i := range c.Expect {
		e := &c.Expect[i]
		j := -1
		for k, f := range blocking {
			if !used[k] && matches(*e, f) {
				j = k
				break
			}
		}
		if j < 0 {
			out.Missed = append(out.Missed, *e)
			continue
		}
		used[j] = true
		out.Problems = append(out.Problems, Problem{Expected: e, Findings: []review.Finding{blocking[j]}})
	}
	// The rest join the expected problem they are about, or the unlisted
	// problem an earlier finding near them reported, or report a new one.
	for j, f := range blocking {
		if used[j] {
			continue
		}
		i := slices.IndexFunc(out.Problems, func(p Problem) bool {
			if p.Expected != nil {
				return matches(*p.Expected, f)
			}
			return sameProblem(p.Findings[0], f)
		})
		if i < 0 {
			out.Problems = append(out.Problems, Problem{Findings: []review.Finding{f}})
			continue
		}
		out.Problems[i].Findings = append(out.Problems[i].Findings, f)
	}
	return out
}

// matches reports whether f is about e: the same file, and a line within e's
// range give or take lineSlack. A finding about the file as a whole, or an
// expected problem anywhere in it, matches on the file alone.
func matches(e Expected, f review.Finding) bool {
	if f.File != e.File {
		return false
	}
	if len(e.Lines) == 0 || f.Line == 0 {
		return true
	}
	return f.Line >= e.Lines[0]-lineSlack && f.Line <= e.Lines[1]+lineSlack
}

// sameProblem reports whether two findings the case does not expect are about
// one problem, as far as their locations tell: the same file, and lines
// within lineSlack, or either about the file as a whole. Two problems that
// close together read as one.
func sameProblem(a, b review.Finding) bool {
	if a.File != b.File {
		return false
	}
	if a.Line == 0 || b.Line == 0 {
		return true
	}
	return max(a.Line-b.Line, b.Line-a.Line) <= lineSlack
}

// Summary is how the reviews did across a suite.
type Summary struct {
	// Runs counts case runs, Scored those every review finished, and Errored
	// the rest, which the other counts leave out.
	Runs    int `json:"runs"`
	Scored  int `json:"scored"`
	Errored int `json:"errored"`

	// Expected counts the problems in scored runs, and Caught those a
	// blocking finding was about.
	Expected int `json:"expected"`
	Caught   int `json:"caught"`
	// Recall is Caught over Expected; absent when no scored run expected a
	// problem.
	Recall *float64 `json:"recall,omitempty"`

	// Blocking counts high and medium findings in scored runs, and Reported
	// the problems they were about, each once. Unexpected are the reported
	// problems the suite does not list, and Repeats the findings beyond the
	// first about each problem.
	Blocking   int `json:"blocking"`
	Reported   int `json:"reported"`
	Unexpected int `json:"unexpected"`
	Repeats    int `json:"repeats"`
	// Shared counts reported problems more than one prompt reported: where
	// the prompts overlap.
	Shared int `json:"shared"`
	// Precision is the share of reported problems the suite lists; absent
	// when nothing was reported. It is a floor: an unexpected problem may be
	// a real one the suite does not list.
	Precision *float64 `json:"precision,omitempty"`

	// Clean counts scored runs of clean cases, and CleanPassed those with no
	// blocking finding at all: sound work factory would have let through.
	Clean       int `json:"clean"`
	CleanPassed int `json:"clean_passed"`

	// Prompts breaks the counts down by review prompt, so a noisy, a blind or
	// a redundant one can be told from the rest.
	Prompts map[string]*PromptSummary `json:"prompts"`
}

// PromptSummary is how one review prompt did across a suite's scored runs.
// A problem counts for every prompt that reported it, not only the first.
type PromptSummary struct {
	// Blocking counts its high and medium findings.
	Blocking int `json:"blocking"`
	// Caught counts the expected problems it reported, and Unexpected the
	// unlisted ones.
	Caught     int `json:"caught"`
	Unexpected int `json:"unexpected"`
	// OnClean counts the problems it reported on clean cases, each of which
	// would have sent sound work back.
	OnClean int `json:"on_clean"`
	// Shared counts the problems it reported that another prompt reported
	// too.
	Shared int `json:"shared"`
	Errors int `json:"errors"`
}

// Summarize totals results. prompts names every review prompt that ran, so
// one that never reported anything still has a row.
func Summarize(prompts []string, results []CaseResult) Summary {
	s := Summary{Prompts: map[string]*PromptSummary{}}
	for _, p := range prompts {
		s.Prompts[p] = &PromptSummary{}
	}
	prompt := func(name string) *PromptSummary {
		if s.Prompts[name] == nil {
			s.Prompts[name] = &PromptSummary{}
		}
		return s.Prompts[name]
	}

	for _, r := range results {
		s.Runs++
		if r.Errored() {
			s.Errored++
			for _, e := range r.Errors {
				prompt(e.Prompt).Errors++
			}
			continue
		}
		s.Scored++
		s.Caught += r.Caught()
		s.Expected += r.Caught() + len(r.Missed)
		s.Reported += len(r.Problems)
		s.Unexpected += r.Unexpected()
		s.Repeats += r.Repeats()
		for _, f := range r.Findings {
			if f.WorthChanging() {
				s.Blocking++
				prompt(f.Prompt).Blocking++
			}
		}
		if r.Clean {
			s.Clean++
			if len(r.Problems) == 0 {
				s.CleanPassed++
			}
		}
		for _, p := range r.Problems {
			reporters := p.Prompts()
			if len(reporters) > 1 {
				s.Shared++
			}
			for _, name := range reporters {
				ps := prompt(name)
				if p.Expected != nil {
					ps.Caught++
				} else {
					ps.Unexpected++
				}
				if r.Clean {
					ps.OnClean++
				}
				if len(reporters) > 1 {
					ps.Shared++
				}
			}
		}
	}
	s.Recall = ratio(s.Caught, s.Expected)
	s.Precision = ratio(s.Caught, s.Reported)
	return s
}

func ratio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}
