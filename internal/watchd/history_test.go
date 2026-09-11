package watchd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// historyProject returns a fresh history store and a project root whose data
// directory is inside the test's temp dir.
func historyProject(t *testing.T) (*riskHistory, string) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	return newRiskHistory(), t.TempDir()
}

// fill records n runs of the given size, failing the first failed of them.
func fill(h *riskHistory, root string, n, lines, failed int) {
	for i := 0; i < n; i++ {
		h.record(root, RiskSummary{Lines: lines, Files: 1}, i >= failed)
	}
}

// A repo with almost no history has no opinion, whatever that history says.
// One unlucky afternoon is not a pattern.
func TestHistoryStaysQuietUntilThereIsEnoughOfIt(t *testing.T) {
	h, root := historyProject(t)
	fill(h, root, minSimilar-1, 100, minSimilar-1)

	ev := h.evidence(root, RiskSummary{Lines: 100})
	assert.DeepEqual(t, ev, historyEvidence{})
	assert.Equal(t, ev.suggestsCaution(), false)
}

func TestHistoryReportsFailuresAmongComparableChanges(t *testing.T) {
	h, root := historyProject(t)
	fill(h, root, 6, 100, 4)

	ev := h.evidence(root, RiskSummary{Lines: 100})
	assert.Equal(t, ev.Similar, 6)
	assert.Equal(t, ev.Failed, 4)
	assert.Equal(t, ev.suggestsCaution(), true)
}

// Comparable means within a factor of two. Without a band no two changes would
// ever be the same size and the rule would never fire; with an unbounded one a
// three-line change would be judged by a rewrite.
func TestHistoryOnlyComparesChangesOfComparableSize(t *testing.T) {
	h, root := historyProject(t)
	fill(h, root, 8, 1000, 8)

	ev := h.evidence(root, RiskSummary{Lines: 10})
	assert.Equal(t, ev.Similar, 0, "a 10-line change was judged by 1000-line ones")
	assert.Equal(t, ev.suggestsCaution(), false)
}

func TestHistoryReportsWhereAChangeSitsForThisRepo(t *testing.T) {
	h, root := historyProject(t)
	fill(h, root, 9, 10, 0)

	assert.Equal(t, h.evidence(root, RiskSummary{Lines: 500}).Percentile, 100)
	assert.Equal(t, h.evidence(root, RiskSummary{Lines: 1}).Percentile, 0)
}

// The record survives the daemon that wrote it: a restart forgets the debt and
// the baseline, but what a repo's runs have historically done is worth keeping.
func TestHistoryIsReadBackFromDisk(t *testing.T) {
	h, root := historyProject(t)
	fill(h, root, 6, 100, 4)

	fresh := newRiskHistory()
	ev := fresh.evidence(root, RiskSummary{Lines: 100})
	assert.Equal(t, ev.Similar, 6)
	assert.Equal(t, ev.Failed, 4)
}

// A daemon killed mid-write leaves a half-line. One bad line costs one record,
// not the whole history.
func TestHistorySkipsATruncatedLine(t *testing.T) {
	h, root := historyProject(t)
	fill(h, root, 6, 100, 4)

	dir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	path := filepath.Join(dir, historyFile)
	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(path, []byte(string(data)+`{"at":"2026-`), 0o600))

	ev := newRiskHistory().evidence(root, RiskSummary{Lines: 100})
	assert.Equal(t, ev.Similar, 6)
}

// It records sizes and a verdict, never paths or content: the file sits in the
// project's data directory and has no business becoming a copy of the code.
func TestHistoryRecordsNoCode(t *testing.T) {
	h, root := historyProject(t)
	h.record(root, RiskSummary{Lines: 12, Files: 2, Inert: true}, true)

	dir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	data, err := os.ReadFile(filepath.Join(dir, historyFile))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), `"lines":12`), "got %s", data)
	assert.Assert(t, !strings.Contains(string(data), "/"), "a path leaked into history: %s", data)
}

// History tightens and never loosens. A repo where every change is enormous
// must not teach the daemon that enormous is fine.
func TestHistoryCannotReleaseALargeChange(t *testing.T) {
	ev := historyEvidence{Similar: 20, Failed: 0, Percentile: 5}
	d := decideRisk(auto, false, sourceChange(5000), nil, ev)
	assert.Equal(t, d.async, false, "history released a change the rules held")
}

// And it does hold one the rules would have released.
func TestHistoryHoldsASmallChangeThatUsuallyFailsHere(t *testing.T) {
	ev := historyEvidence{Similar: 8, Failed: 6, Percentile: 50}
	d := decideRisk(auto, false, sourceChange(20), nil, ev)
	assert.Equal(t, d.async, false)
	assert.Assert(t, strings.Contains(d.reason, "6 of the last 8"), "reason was %q", d.reason)
}

// An explicit setting still wins: history is a heuristic and "always" is an
// instruction.
func TestHistoryDoesNotOverrideModeAlways(t *testing.T) {
	p := asyncPolicy{mode: config.AsyncValidateAlways, maxLines: DefaultAsyncMaxLines}
	ev := historyEvidence{Similar: 8, Failed: 8, Percentile: 99}
	assert.Equal(t, decideRisk(p, false, sourceChange(20), nil, ev).async, true)
}

// Being unusual for this repo raises the score without deciding anything.
func TestAnUnusuallyLargeChangeScoresHigher(t *testing.T) {
	normal := scoreChange(500, false, change(100, "a.go"), nil, historyEvidence{Similar: 6, Percentile: 40})
	unusual := scoreChange(500, false, change(100, "a.go"), nil, historyEvidence{Similar: 6, Percentile: 95})

	assert.Equal(t, unusual.Score, normal.Score+unusualWeight)
	assert.Assert(t, strings.Contains(strings.Join(unusual.Parts, ", "), "larger than 95%"),
		"parts were %v", unusual.Parts)
}
