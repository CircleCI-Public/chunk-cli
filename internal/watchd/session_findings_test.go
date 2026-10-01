package watchd

import (
	"encoding/json"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// findingsOutput renders claude's JSON result for a review carrying the given
// findings.
func findingsOutput(t *testing.T, findings ...review.Finding) string {
	t.Helper()
	return reviewOutput(t, "Prose review.", findings...)
}

// reviewOutput renders claude's JSON result for a review with the given prose
// and findings.
func reviewOutput(t *testing.T, prose string, findings ...review.Finding) string {
	t.Helper()
	if findings == nil {
		findings = []review.Finding{}
	}
	structured := map[string]any{"review": prose, "findings": findings}
	text, err := json.Marshal(structured)
	assert.NilError(t, err)
	raw, err := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": string(text), "structured_output": structured,
	})
	assert.NilError(t, err)
	return string(raw)
}

func TestSessionCountsDistinctFindingsAndThoseWorthChanging(t *testing.T) {
	out := findingsOutput(t,
		review.Finding{File: "a.go", Line: 1, Severity: "high", Body: "nil deref"},
		review.Finding{File: "a.go", Line: 2, Severity: "medium", Body: "unchecked error"},
		review.Finding{File: "a.go", Line: 3, Severity: "low", Body: "naming"},
		review.Finding{File: "a.go", Line: 4, Severity: "info", Body: "note"},
	)
	// The agent that is asked to fix them changes nothing, which ends the loop.
	r := newLoopRig(t, func(string) string { return out }, func(string) {})
	detail := r.start(SessionRequest{})

	// Both prompts report the same four findings; the round counts them once.
	round := detail.Rounds[0]
	assert.Equal(t, round.Findings, 4)
	assert.Equal(t, round.Worth, 2, "only high and medium are worth changing")
	for _, p := range round.Reviews {
		assert.Equal(t, p.Findings, 4)
	}
	res := detail.Details[0].Results[0]
	assert.Equal(t, res.Output, "Prose review.", "the output is the prose, not claude's JSON")
	assert.Equal(t, res.Findings[0].ID, res.Prompt+"-1")
}

// A review whose answer has no structured output is a failed review, not a
// prose-only one: nothing it said can be acted on.
func TestSessionFailsAReviewWithoutStructuredOutput(t *testing.T) {
	d, root := newSessionDaemon(t, &fakeBackend{respond: func(string) (string, int) { return "Just prose, nothing structured.", 0 }})
	sess, err := d.startSession(SessionRequest{ProjectRoot: root})
	assert.NilError(t, err)
	detail := waitForSession(t, d, sess.ID)

	assert.Equal(t, detail.Rounds[0].Findings, 0)
	res := detail.Details[0].Results[0]
	assert.Assert(t, strings.Contains(res.Error, "read claude's result"), res.Error)
	assert.Equal(t, len(res.Findings), 0)
}
