package watchd

import (
	"fmt"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// Risk score bands. They exist so a caller can act on the score without
// hard-coding a number, and so the number itself stays arguable: a band is a
// claim about caution, not a probability of failure.
const (
	BandLow    = "low"
	BandMedium = "medium"
	BandHigh   = "high"
)

// Score weights. Each is the most its fact can contribute, so a reader can work
// out where a total came from without running the code.
const (
	sizeWeight  = 60 // a change at the line limit
	filesWeight = 20 // a change touching filesBreadth files or more
	debtWeight  = 25 // the last run failed
	inertCeil   = 10 // the most a docs-and-text change can score
	filesSpread = 10 // files at which breadth is maxed out
	// Both of these come from the project's own history, and both only ever add
	// — see riskHistory for why history is allowed to tighten and never loosen.
	unusualWeight = 10 // large for this repo, whatever it measures in lines
	historyWeight = 20 // changes this size usually fail here
)

// RiskSummary is what the daemon made of a change, as reported to a caller.
//
// It is deliberately not just a number. A score on its own cannot be argued
// with — "risk 62" invites either trust or dismissal and supports neither — so
// the facts it was built from travel with it, and the advice that follows from
// them is spelled out rather than left to be inferred.
type RiskSummary struct {
	// Score is 0–100. Higher means more caution: bigger, broader, or already
	// failing. It does not decide anything on its own; see decideRisk.
	Score int `json:"score"`
	// Lines and Files are what was measured, kept apart from the score so a
	// caller can compare two changes without unpicking one.
	Lines int `json:"lines"`
	Files int `json:"files"`
	// Inert reports that every changed path was docs or text.
	Inert bool `json:"inert,omitempty"`
	// Band is Score bucketed into BandLow, BandMedium or BandHigh.
	Band string `json:"band"`
	// Parts are the facts behind the score, each with what it contributed.
	Parts []string `json:"parts,omitempty"`
	// Advice is what a developer or agent could do about it, or "" when there is
	// nothing useful to say.
	Advice string `json:"advice,omitempty"`
}

// scoreChange summarises how much caution a change deserves.
//
// The score is a report, not the decision. decideRisk still answers "who waits"
// from the facts themselves, because a threshold on lines can be argued with in
// review and a threshold on a composite cannot: nobody can tell you whether 47
// should have been 52. What the score is for is the questions one bit cannot
// answer — which of two changes is riskier, whether a change is unusual for this
// repo, and whether there is anything worth advising about it.
func scoreChange(limit int, owesBlockingRun bool, ch gitutil.Changes, chErr error, hist historyEvidence) RiskSummary {
	if chErr != nil {
		// Nothing is known about this change. The top of the scale is the only
		// honest answer, and it is the same direction decideRisk takes.
		return RiskSummary{
			Score: 100,
			Band:  BandHigh,
			Parts: []string{fmt.Sprintf("change size unavailable: %v (100)", chErr)},
		}
	}
	if ch.Empty() {
		return RiskSummary{Score: 0, Band: BandLow, Parts: []string{"no changes (0)"}}
	}

	var (
		score int
		parts []string
	)
	inert := allInert(ch.Paths)

	size := ch.Lines * sizeWeight / max(limit, 1)
	if size > sizeWeight {
		size = sizeWeight
	}
	score += size
	parts = append(parts, fmt.Sprintf("%s (%d)", lineCount(ch.Lines), size))

	breadth := len(ch.Paths) * filesWeight / filesSpread
	if breadth > filesWeight {
		breadth = filesWeight
	}
	score += breadth
	parts = append(parts, fmt.Sprintf("%s (%d)", fileCount(len(ch.Paths)), breadth))

	// Applied as a ceiling rather than a discount: a docs change is not a small
	// source change, it is a change nothing should fail on, and a thousand lines
	// of it should not out-score three lines of Go.
	if inert && score > inertCeil {
		score = inertCeil
		parts = append(parts, fmt.Sprintf("docs and text only (ceiling %d)", inertCeil))
	}

	if owesBlockingRun {
		score += debtWeight
		parts = append(parts, fmt.Sprintf("last run failed (%d)", debtWeight))
	}

	// What this repo's own history says. It can only raise the score, never
	// lower one: a codebase where every change is enormous must not thereby
	// teach the daemon that enormous is fine.
	if hist.unusual() {
		score += unusualWeight
		parts = append(parts, fmt.Sprintf("larger than %d%% of recent changes here (%d)", hist.Percentile, unusualWeight))
	}
	if hist.suggestsCaution() {
		score += historyWeight
		parts = append(parts, fmt.Sprintf("%d of the last %d changes this size failed here (%d)", hist.Failed, hist.Similar, historyWeight))
	}

	if score > 100 {
		score = 100
	}
	return RiskSummary{
		Score:  score,
		Lines:  ch.Lines,
		Files:  len(ch.Paths),
		Inert:  inert,
		Band:   band(score),
		Parts:  parts,
		Advice: advise(limit, ch, chErr),
	}
}

func band(score int) string {
	switch {
	case score >= 80:
		return BandHigh
	case score >= 50:
		return BandMedium
	default:
		return BandLow
	}
}

// advise says what could be done about a change, or nothing.
//
// Only size and breadth produce advice, and only together. "Commit in parts" is
// actionable for 2,000 lines across nine files and useless for 2,000 lines in
// one generated file, so a single-file change gets no suggestion rather than an
// impossible one. Nothing is advised about a failing run or an unmeasurable
// tree either: the run itself is about to say more than any advice could.
func advise(limit int, ch gitutil.Changes, chErr error) string {
	if chErr != nil || ch.Lines < limit || len(ch.Paths) < 2 || allInert(ch.Paths) {
		return ""
	}
	return fmt.Sprintf(
		"%s across %s is past the point where checks can run in the background; "+
			"committing it in parts would get each piece checked sooner",
		lineCount(ch.Lines), fileCount(len(ch.Paths)))
}

// fileCount renders a file total for a human, singular included.
func fileCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// String renders a summary as one line of terminal output.
func (r RiskSummary) String() string {
	if len(r.Parts) == 0 {
		return fmt.Sprintf("risk %d/100 %s", r.Score, r.Band)
	}
	return fmt.Sprintf("risk %d/100 %s — %s", r.Score, r.Band, strings.Join(r.Parts, ", "))
}
