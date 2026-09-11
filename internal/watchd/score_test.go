package watchd

import (
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

func change(lines int, paths ...string) gitutil.Changes {
	return gitutil.Changes{Paths: paths, Lines: lines, Baseline: gitutil.BaselineHead}
}

func TestScoreRisesWithSize(t *testing.T) {
	small := scoreChange(500, false, change(10, "main.go"), nil, historyEvidence{})
	big := scoreChange(500, false, change(400, "main.go"), nil, historyEvidence{})

	assert.Assert(t, small.Score < big.Score, "%d is not below %d", small.Score, big.Score)
	assert.Equal(t, small.Band, BandLow)
}

// Size is capped, so a change ten times the limit does not swamp everything
// else the score is made of.
func TestScoreSizeIsCapped(t *testing.T) {
	at := scoreChange(500, false, change(500, "main.go"), nil, historyEvidence{})
	way := scoreChange(500, false, change(50000, "main.go"), nil, historyEvidence{})
	assert.Equal(t, at.Score, way.Score)
}

func TestScoreRisesWithBreadth(t *testing.T) {
	narrow := scoreChange(500, false, change(100, "a.go"), nil, historyEvidence{})
	wide := scoreChange(500, false, change(100, "a.go", "b.go", "c.go", "d.go", "e.go"), nil, historyEvidence{})
	assert.Assert(t, wide.Score > narrow.Score, "%d is not above %d", wide.Score, narrow.Score)
}

// A docs change is not a small source change, it is a change nothing should
// fail on. A thousand lines of it must not out-score three lines of Go.
func TestADocsChangeIsCappedBelowASmallSourceChange(t *testing.T) {
	docs := scoreChange(500, false, change(5000, "README.md", "docs/CLI.md"), nil, historyEvidence{})
	source := scoreChange(500, false, change(3, "main.go"), nil, historyEvidence{})

	assert.Equal(t, docs.Score, inertCeil)
	assert.Equal(t, docs.Band, BandLow)
	assert.Assert(t, docs.Score >= source.Score, "docs %d, source %d", docs.Score, source.Score)
}

func TestAFailedLastRunAddsToTheScore(t *testing.T) {
	clean := scoreChange(500, false, change(100, "main.go"), nil, historyEvidence{})
	owing := scoreChange(500, true, change(100, "main.go"), nil, historyEvidence{})

	assert.Equal(t, owing.Score, clean.Score+debtWeight)
	assert.Assert(t, strings.Contains(strings.Join(owing.Parts, " "), "last run failed"),
		"parts were %v", owing.Parts)
}

// Nothing known means the top of the scale. It is the same direction the
// release decision takes, for the same reason.
func TestAnUnmeasurableChangeScoresHighest(t *testing.T) {
	s := scoreChange(500, false, gitutil.Changes{}, errors.New("not a repo"), historyEvidence{})
	assert.Equal(t, s.Score, 100)
	assert.Equal(t, s.Band, BandHigh)
}

func TestAnEmptyChangeScoresZero(t *testing.T) {
	s := scoreChange(500, false, gitutil.Changes{Baseline: gitutil.BaselineHead}, nil, historyEvidence{})
	assert.Equal(t, s.Score, 0)
	assert.Equal(t, s.Band, BandLow)
}

func TestScoreIsNeverOverAHundred(t *testing.T) {
	s := scoreChange(500, true, change(100000,
		"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go", "j.go", "k.go"),
		nil, historyEvidence{Similar: 10, Failed: 9, Percentile: 99})
	assert.Equal(t, s.Score, 100)
}

// The parts are the point: a score you cannot take apart is a score nobody can
// argue with.
func TestScorePartsNameTheFactsBehindIt(t *testing.T) {
	s := scoreChange(500, false, change(912, "a.go", "b.go"), nil, historyEvidence{})
	joined := strings.Join(s.Parts, ", ")
	assert.Assert(t, strings.Contains(joined, "912 lines"), "got %q", joined)
	assert.Assert(t, strings.Contains(joined, "2 files"), "got %q", joined)
	assert.Assert(t, strings.Contains(s.String(), "risk "), "got %q", s.String())
}

// Advice has to be actionable. Splitting up a change makes sense across several
// files and is impossible in one, so a single-file change is told nothing
// rather than something it cannot do.
func TestAdviceOnlyWhereThereIsSomethingToDo(t *testing.T) {
	across := scoreChange(500, false, change(2000, "a.go", "b.go", "c.go"), nil, historyEvidence{})
	assert.Assert(t, strings.Contains(across.Advice, "committing it in parts"), "got %q", across.Advice)

	for name, s := range map[string]RiskSummary{
		"one file":      scoreChange(500, false, change(2000, "generated.go"), nil, historyEvidence{}),
		"under limit":   scoreChange(500, false, change(100, "a.go", "b.go"), nil, historyEvidence{}),
		"docs and text": scoreChange(500, false, change(5000, "a.md", "b.md"), nil, historyEvidence{}),
		"unmeasurable":  scoreChange(500, false, gitutil.Changes{}, errors.New("nope"), historyEvidence{}),
	} {
		assert.Equal(t, s.Advice, "", "advice was given for %s", name)
	}
}

// The score reports, it does not decide. Release still turns on the facts,
// because a line threshold can be argued with in review and a threshold on a
// composite cannot.
func TestTheScoreDoesNotDecideWhoWaits(t *testing.T) {
	// Medium score, still released: 400 lines across 5 files is under the limit.
	d := decideRisk(auto, false, change(400, "a.go", "b.go", "c.go", "d.go", "e.go"), nil, historyEvidence{})
	assert.Equal(t, d.async, true)
	assert.Equal(t, d.risk.Band, BandMedium)

	// Low score, still held: nothing about three lines is risky, but the last
	// run failed and that is what decides.
	held := decideRisk(auto, true, change(3, "main.go"), nil, historyEvidence{})
	assert.Equal(t, held.async, false)
}
