package server

import (
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/changeset"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// Score weights. Each is the most its fact can contribute, so a reader can work
// out where a total came from without running the code.
const (
	sizeWeight  = 60 // a change at the line limit
	filesWeight = 20 // a change touching filesBreadth files or more
	debtWeight  = 25 // the last run failed
	filesSpread = 10 // files at which breadth is maxed out
	// Both of these come from the project's own history, and both only ever add
	// — see riskHistory for why history is allowed to tighten and never loosen.
	unusualWeight = 10 // large for this repo, whatever it measures in lines
	historyWeight = 20 // changes this size usually fail here
)

// scoreChange summarises how much caution a change deserves.
//
// The score is a report, not the decision. decideRisk still answers "who waits"
// from the facts themselves, because a threshold on lines can be argued with in
// review and a threshold on a composite cannot: nobody can tell you whether 47
// should have been 52. What the score is for is the questions one bit cannot
// answer — which of two changes is riskier, whether a change is unusual for this
// repo, and whether there is anything worth advising about it.
func scoreChange(limit int, rules inertRules, owesBlockingRun bool, ch changeset.Changes, chErr error, hist historyEvidence) watchd.RiskSummary {
	if chErr != nil {
		// Nothing is known about this change. The top of the scale is the only
		// honest answer, and it is the same direction decideRisk takes.
		return watchd.RiskSummary{
			Score: 100,
			Band:  watchd.BandHigh,
			Parts: []string{fmt.Sprintf("change size unavailable: %v (100)", chErr)},
		}
	}
	if ch.Empty() {
		return watchd.RiskSummary{Score: 0, Band: watchd.BandLow, Parts: []string{"no changes (0)"}}
	}

	var (
		score int
		parts []string
	)
	inert := rules.allInert(ch.Paths)

	// A docs change is not a small source change, it is a change nothing should
	// fail on: no check reads these files, so how many lines of them moved is
	// not evidence about anything. Size and breadth are scored at zero rather
	// than discounted or capped, because a discount and a ceiling both still
	// let a thousand lines of README out-score three lines of Go — they only
	// move the point where it happens.
	if inert {
		parts = append(parts, fmt.Sprintf("%s across %s, docs and text only (0)",
			lineCount(ch.Lines), fileCount(len(ch.Paths))))
	} else {
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
	}

	if owesBlockingRun {
		score += debtWeight
		parts = append(parts, fmt.Sprintf("last run failed (%d)", debtWeight))
	}

	// What this repo's own history says. It can only raise the score, never
	// lower one: a codebase where every change is enormous must not thereby
	// teach the daemon that enormous is fine.
	//
	// Skipped for an inert change for the same reason its own line count is:
	// both of these are functions of a size no check reads, so "larger than 90%
	// of changes here" says no more about a changelog than the lines it was
	// derived from. A failed last run is not — that debt is owed whatever moved.
	if !inert {
		if hist.unusual() {
			score += unusualWeight
			parts = append(parts, fmt.Sprintf("larger than %d%% of recent changes here (%d)", hist.Percentile, unusualWeight))
		}
		if hist.suggestsCaution() {
			score += historyWeight
			parts = append(parts, fmt.Sprintf("%d of the last %d changes this size failed here (%d)", hist.Failed, hist.Similar, historyWeight))
		}
	}

	if score > 100 {
		score = 100
	}
	return watchd.RiskSummary{
		Score:  score,
		Lines:  ch.Lines,
		Files:  len(ch.Paths),
		Inert:  inert,
		Band:   band(score),
		Parts:  parts,
		Advice: advise(limit, rules, ch, chErr),
	}
}

func band(score int) string {
	switch {
	case score >= 80:
		return watchd.BandHigh
	case score >= 50:
		return watchd.BandMedium
	default:
		return watchd.BandLow
	}
}

// advise says what could be done about a change, or nothing.
//
// Only size and breadth produce advice, and only together. "Commit in parts" is
// actionable for 2,000 lines across nine files and useless for 2,000 lines in
// one generated file, so a single-file change gets no suggestion rather than an
// impossible one. Nothing is advised about a failing run or an unmeasurable
// tree either: the run itself is about to say more than any advice could.
func advise(limit int, rules inertRules, ch changeset.Changes, chErr error) string {
	if chErr != nil || ch.Lines < limit || len(ch.Paths) < 2 || rules.allInert(ch.Paths) {
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
