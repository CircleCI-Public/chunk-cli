package watchd

import (
	"encoding/json"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// findingsOutput renders a review output carrying the given findings.
func findingsOutput(t *testing.T, findings ...review.Finding) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"findings": findings})
	assert.NilError(t, err)
	return "Prose review.\n```json\n" + string(raw) + "\n```\n"
}

func TestSessionCountsDistinctFindingsAndThoseWorthChanging(t *testing.T) {
	out := findingsOutput(t,
		review.Finding{File: "a.go", Line: 1, Severity: "high", Body: "nil deref"},
		review.Finding{File: "a.go", Line: 2, Severity: "medium", Body: "unchecked error"},
		review.Finding{File: "a.go", Line: 3, Severity: "low", Body: "naming"},
		review.Finding{File: "a.go", Line: 4, Severity: "info", Body: "note"},
	)
	d, root := newSessionDaemon(t, &fakeBackend{respond: func(string) (string, int) { return out, 0 }})

	sess, err := d.startSession(SessionRequest{ProjectRoot: root})
	assert.NilError(t, err)
	detail := waitForSession(t, d, sess.ID)

	// Both prompts report the same four findings; the round counts them once.
	round := detail.Rounds[0]
	assert.Equal(t, round.Findings, 4)
	assert.Equal(t, round.Worth, 2, "only high and medium are worth changing")
	for _, p := range round.Reviews {
		assert.Equal(t, p.Findings, 4)
	}
	res := detail.Details[0].Results[0]
	assert.Assert(t, res.FindingsParsed)
	assert.Equal(t, res.Output, "Prose review.", "the JSON block is not repeated in the prose")
	assert.Equal(t, res.Findings[0].ID, res.Prompt+"-1")
}

// Structured output is best effort: a review that ignores the instructions, or
// whose JSON is broken, is still a review.
func TestSessionFallsBackToProseWhenFindingsAreMissingOrMalformed(t *testing.T) {
	for name, out := range map[string]string{
		"no block":  "Just prose, nothing structured.",
		"malformed": "Prose.\n```json\n{\"findings\":[{\"file\":\n```",
	} {
		t.Run(name, func(t *testing.T) {
			d, root := newSessionDaemon(t, &fakeBackend{respond: func(string) (string, int) { return out, 0 }})
			sess, err := d.startSession(SessionRequest{ProjectRoot: root})
			assert.NilError(t, err)
			detail := waitForSession(t, d, sess.ID)

			assert.Equal(t, detail.State, SessionDone)
			assert.Equal(t, detail.Rounds[0].Findings, 0)
			assert.Assert(t, !detail.Details[0].Results[0].FindingsParsed)
			assert.Equal(t, detail.Details[0].Results[0].Output, out)
		})
	}
}
