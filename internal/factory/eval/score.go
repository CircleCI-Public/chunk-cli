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
	// Clean is whether the case is labeled clean, and Labeled whether it is
	// labeled at all: only labeled cases count towards recall and precision
	// against labels.
	Clean   bool `json:"clean"`
	Labeled bool `json:"labeled"`
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
	// Grade is the grader's judgment of the run, and GradeError why there is
	// none when grading was asked for and failed.
	Grade      *Grade `json:"grade,omitempty"`
	GradeError string `json:"grade_error,omitempty"`
}

// Problem is one problem the blocking findings on a case were about.
type Problem struct {
	// Expected is the case's problem this is; nil when the case does not list
	// it, or lists none.
	Expected *Expected `json:"expected,omitempty"`
	// Findings are the blocking findings about it: for an expected problem,
	// the one paired with it first, and otherwise the first first. More than
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
	out := CaseResult{Case: c.Name, Run: run, Clean: c.Clean, Labeled: c.Labeled(), Findings: []review.Finding{}}
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

	// Each expected problem first takes a finding about it of its own, so two
	// problems in one file are caught by two findings rather than both by one.
	owner := matchExpected(c.Expect, blocking)
	var members [][]int // per problem, its findings' indexes in blocking
	for i := range c.Expect {
		j := slices.Index(owner, i)
		if j < 0 {
			out.Missed = append(out.Missed, c.Expect[i])
			continue
		}
		out.Problems = append(out.Problems, Problem{Expected: &c.Expect[i]})
		members = append(members, []int{j})
	}
	// The rest join the expected problem they are about, or the unlisted
	// problem an earlier finding near them reported, or report a new one.
	for j, f := range blocking {
		if owner[j] >= 0 {
			continue
		}
		i := -1
		for k, p := range out.Problems {
			if p.Expected != nil && matches(*p.Expected, f) || p.Expected == nil && sameProblem(blocking[members[k][0]], f) {
				i = k
				break
			}
		}
		if i < 0 {
			out.Problems = append(out.Problems, Problem{})
			members = append(members, nil)
			i = len(members) - 1
		}
		members[i] = append(members[i], j)
	}
	for i, m := range members {
		// An expected problem's paired finding stays first: it is the one
		// the problem was matched by.
		if out.Problems[i].Expected != nil {
			slices.Sort(m[1:])
		} else {
			slices.Sort(m)
		}
		for _, j := range m {
			out.Problems[i].Findings = append(out.Problems[i].Findings, blocking[j])
		}
	}
	return out
}

// matchExpected pairs expected problems with blocking findings about them, one
// finding each, pairing as many as can be. Each takes the first finding no
// other has; only when none is left does an earlier problem give up its
// finding for another it can take. It returns, for each
// finding, the index of the expected problem it is paired with, or -1.
func matchExpected(expect []Expected, findings []review.Finding) []int {
	owner := make([]int, len(findings))
	for k := range owner {
		owner[k] = -1
	}
	var assign func(i int, seen []bool) bool
	assign = func(i int, seen []bool) bool {
		for k, f := range findings {
			if owner[k] < 0 && !seen[k] && matches(expect[i], f) {
				seen[k] = true
				owner[k] = i
				return true
			}
		}
		for k, f := range findings {
			if seen[k] || !matches(expect[i], f) {
				continue
			}
			seen[k] = true
			if assign(owner[k], seen) {
				owner[k] = i
				return true
			}
		}
		return false
	}
	for i := range expect {
		assign(i, make([]bool, len(findings)))
	}
	return owner
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

// regroup rebuilds a graded run's problems from the grader's judgment of which
// findings are about the same problem, which reads the findings where nearby
// lines only place them. Two problems three lines apart stay two, and one
// problem reported at two distant lines is one.
//
// An expected problem keeps the finding paired with it and gains every
// finding the grader put with that one. The grader's other groups are
// problems the case does not list. Which expected problems were caught or
// missed does not change: that is the labels' pairing, not the grader's.
func regroup(r *CaseResult) {
	g := r.Grade
	if g == nil {
		return
	}
	// Each blocking finding's group, as the index in g.Findings of the
	// finding that stands for it.
	group := map[findingKey]int{}
	for i, f := range g.Findings {
		gi := i
		if f.DuplicateOf >= 0 {
			gi = f.DuplicateOf
		}
		group[keyOf(f.Finding)] = gi
	}
	var out []Problem
	byGroup := map[int]int{} // a group's problem's index in out
	owners := map[findingKey]bool{}
	for _, p := range r.Problems {
		if p.Expected == nil {
			continue
		}
		owner := p.Findings[0]
		owners[keyOf(owner)] = true
		gi, ok := group[keyOf(owner)]
		if _, taken := byGroup[gi]; ok && !taken {
			byGroup[gi] = len(out)
		}
		out = append(out, Problem{Expected: p.Expected, Findings: []review.Finding{owner}})
	}
	for _, f := range r.Findings {
		if !f.WorthChanging() || owners[keyOf(f)] {
			continue
		}
		gi, graded := group[keyOf(f)]
		i, ok := byGroup[gi]
		// A finding the grader was not shown is a problem of its own.
		if !graded || !ok {
			i = len(out)
			out = append(out, Problem{})
			if graded {
				byGroup[gi] = i
			}
		}
		out[i].Findings = append(out[i].Findings, f)
	}
	r.Problems = out
}

// Summary is how the reviews did across a suite.
type Summary struct {
	// Runs counts case runs, Scored those every review finished, and Errored
	// the rest, which the other counts leave out.
	Runs    int `json:"runs"`
	Scored  int `json:"scored"`
	Errored int `json:"errored"`

	// Blocking counts high and medium findings in scored runs, and Reported
	// the problems they were about, each once, by file and nearby lines.
	// Repeats are the findings beyond the first about each problem.
	Blocking int `json:"blocking"`
	Reported int `json:"reported"`
	Repeats  int `json:"repeats"`
	// Shared counts reported problems more than one prompt reported: where
	// the prompts overlap.
	Shared int `json:"shared"`

	// Labeled counts scored runs of labeled cases; the counts below up to
	// Graded are over those alone.
	Labeled int `json:"labeled"`
	// Expected counts the problems the cases list, and Caught those a
	// blocking finding was about.
	Expected int `json:"expected"`
	Caught   int `json:"caught"`
	// Recall is Caught over Expected; absent when no scored run expected a
	// problem.
	Recall *float64 `json:"recall,omitempty"`
	// ReportedLabeled counts the problems reported on labeled cases, and
	// Unexpected those the cases do not list.
	ReportedLabeled int `json:"reported_labeled"`
	Unexpected      int `json:"unexpected"`
	// Precision is the share of problems reported on labeled cases that the
	// cases list; absent when nothing was reported. It is a floor: an
	// unexpected problem may be a real one the suite does not list.
	Precision *float64 `json:"precision,omitempty"`
	// Clean counts scored runs of clean cases, and CleanPassed those with no
	// blocking finding at all: sound work factory would have let through.
	Clean       int `json:"clean"`
	CleanPassed int `json:"clean_passed"`

	// Graded is the grader's view of the scored runs; nil when none was
	// graded or grading was not asked for.
	Graded *GradedSummary `json:"graded,omitempty"`

	// Prompts breaks the counts down by review prompt, so a noisy, a blind or
	// a redundant one can be told from the rest.
	Prompts map[string]*PromptSummary `json:"prompts"`
}

// GradedSummary is what the grader made of the scored runs it graded. Its
// problems are the grader's: findings it marked as about the same problem
// count once, at the verdict of the first.
type GradedSummary struct {
	// Runs counts graded runs, and Errors scored runs whose grading failed,
	// which the other counts leave out.
	Runs   int `json:"runs"`
	Errors int `json:"errors"`
	// Problems counts the distinct problems reported, by verdict.
	Problems    int `json:"problems"`
	Confirmed   int `json:"confirmed"`
	NotBlocking int `json:"not_blocking"`
	Wrong       int `json:"wrong"`
	// Duplicates counts findings the grader judged about a problem an
	// earlier one reported, and Ungraded those it gave no verdict.
	Duplicates int `json:"duplicates"`
	Ungraded   int `json:"ungraded"`
	// Missed counts problems the grader found that no finding was about.
	Missed int `json:"missed"`
	// Precision is Confirmed over the problems with a verdict, and Coverage
	// Confirmed over Confirmed and Missed: coverage of what the grader could
	// find, not of every problem there is.
	Precision *float64 `json:"precision,omitempty"`
	Coverage  *float64 `json:"coverage,omitempty"`
	CostUSD   float64  `json:"cost_usd"`
	// Calibration checks the grader against the labeled cases.
	Calibration Calibration `json:"calibration"`
}

// Calibration is how the grader's judgment agrees with the labels, where there
// are any. A grader that confirms what is known to be wrong, or rejects what is
// known to be right, says less about the unlabeled cases.
type Calibration struct {
	// OnExpected counts graded findings about an expected problem, and
	// OnExpectedConfirmed those the grader confirmed: agreement.
	OnExpected          int `json:"on_expected"`
	OnExpectedConfirmed int `json:"on_expected_confirmed"`
	// OnClean counts graded findings on clean cases, and OnCleanConfirmed
	// those the grader confirmed: disagreement.
	OnClean          int `json:"on_clean"`
	OnCleanConfirmed int `json:"on_clean_confirmed"`
	// MissedOnClean counts problems the grader said the reviews missed on
	// clean cases: disagreement.
	MissedOnClean int `json:"missed_on_clean"`
	// ExpectedMissed counts expected problems no finding was about, and
	// ExpectedFound those the grader found itself: agreement.
	ExpectedMissed int `json:"expected_missed"`
	ExpectedFound  int `json:"expected_found"`
}

// PromptSummary is how one review prompt did across a suite's scored runs.
// A problem counts for every prompt that reported it, not only the first.
type PromptSummary struct {
	// Blocking counts its high and medium findings.
	Blocking int `json:"blocking"`
	// Caught counts the expected problems it reported, and Unexpected the
	// unlisted ones on labeled cases.
	Caught     int `json:"caught"`
	Unexpected int `json:"unexpected"`
	// OnClean counts the problems it reported on clean cases, each of which
	// would have sent sound work back.
	OnClean int `json:"on_clean"`
	// Shared counts the problems it reported that another prompt reported
	// too.
	Shared int `json:"shared"`
	// Confirmed, NotBlocking and Wrong count its graded findings by the
	// grader's verdict, duplicates included.
	Confirmed   int `json:"confirmed"`
	NotBlocking int `json:"not_blocking"`
	Wrong       int `json:"wrong"`
	Errors      int `json:"errors"`
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

	var graded GradedSummary
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
		s.Reported += len(r.Problems)
		s.Repeats += r.Repeats()
		for _, f := range r.Findings {
			if f.WorthChanging() {
				s.Blocking++
				prompt(f.Prompt).Blocking++
			}
		}
		if r.Labeled {
			s.Labeled++
			s.Caught += r.Caught()
			s.Expected += r.Caught() + len(r.Missed)
			s.ReportedLabeled += len(r.Problems)
			s.Unexpected += r.Unexpected()
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
				switch {
				case p.Expected != nil:
					ps.Caught++
				case r.Labeled:
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
		switch {
		case r.GradeError != "":
			graded.Errors++
		case r.Grade != nil:
			addGrade(&graded, r, prompt)
		}
	}
	s.Recall = ratio(s.Caught, s.Expected)
	s.Precision = ratio(s.Caught, s.ReportedLabeled)
	if graded.Runs > 0 || graded.Errors > 0 {
		graded.Precision = ratio(graded.Confirmed, graded.Confirmed+graded.NotBlocking+graded.Wrong)
		graded.Coverage = ratio(graded.Confirmed, graded.Confirmed+graded.Missed)
		s.Graded = &graded
	}
	return s
}

// addGrade adds one graded run to the graded summary and the prompts' rows.
func addGrade(gs *GradedSummary, r CaseResult, prompt func(string) *PromptSummary) {
	g := r.Grade
	gs.Runs++
	gs.Missed += len(g.Missed)
	gs.Ungraded += g.Ungraded
	gs.CostUSD += g.CostUSD
	for _, f := range g.Findings {
		if f.Verdict == "" {
			continue
		}
		ps := prompt(f.Finding.Prompt)
		switch f.Verdict {
		case VerdictConfirmed:
			ps.Confirmed++
		case VerdictNotBlocking:
			ps.NotBlocking++
		case VerdictWrong:
			ps.Wrong++
		}
		if f.DuplicateOf >= 0 {
			gs.Duplicates++
			continue
		}
		gs.Problems++
		switch f.Verdict {
		case VerdictConfirmed:
			gs.Confirmed++
		case VerdictNotBlocking:
			gs.NotBlocking++
		case VerdictWrong:
			gs.Wrong++
		}
	}
	calibrate(&gs.Calibration, r)
}

// calibrate adds how r's grade agrees with r's labels.
func calibrate(c *Calibration, r CaseResult) {
	if !r.Labeled {
		return
	}
	g := r.Grade
	onExpected := map[findingKey]bool{}
	for _, p := range r.Problems {
		if p.Expected == nil {
			continue
		}
		for _, f := range p.Findings {
			onExpected[keyOf(f)] = true
		}
	}
	for _, f := range g.Findings {
		if f.Verdict == "" {
			continue
		}
		confirmed := f.Verdict == VerdictConfirmed
		switch {
		case r.Clean:
			c.OnClean++
			if confirmed {
				c.OnCleanConfirmed++
			}
		case onExpected[keyOf(f.Finding)]:
			c.OnExpected++
			if confirmed {
				c.OnExpectedConfirmed++
			}
		}
	}
	if r.Clean {
		c.MissedOnClean += len(g.Missed)
	}
	for _, e := range r.Missed {
		c.ExpectedMissed++
		if slices.ContainsFunc(g.Missed, func(m Missed) bool {
			return matches(e, review.Finding{File: m.File, Line: m.Line})
		}) {
			c.ExpectedFound++
		}
	}
}

// findingKey identifies a finding within one case run, to match the grader's
// copy of it with the scorer's.
type findingKey struct {
	prompt, file, body string
	line               int
}

func keyOf(f review.Finding) findingKey {
	return findingKey{prompt: f.Prompt, file: f.File, body: f.Body, line: f.Line}
}

func ratio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}
