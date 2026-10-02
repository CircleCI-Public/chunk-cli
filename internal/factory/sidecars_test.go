package factory

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

func TestReviewPromptsShowEachReviewItsOwnEarlierFindings(t *testing.T) {
	prompts := []review.Prompt{{Name: "tests", Body: "Review the tests."}, {Name: "style", Body: "Review the style."}}
	history := []Exchange{{
		Round:  1,
		Checks: []Check{fail, broken},
		Reply:  "Fixed a.go. Left the nil deref alone: the input is never empty.",
	}}

	got := reviewPrompts(prompts, history)

	tests := got[0].Body
	assert.Assert(t, strings.HasPrefix(tests, reviewScope+"Review the tests."), tests)
	assert.Assert(t, strings.Contains(tests, "## Earlier rounds"), tests)
	assert.Assert(t, strings.Contains(tests, "### Round 1: you raised\n\n- [high] a.go:3: nil deref"), tests)
	assert.Assert(t, strings.Contains(tests, "the input is never empty"), tests)
	assert.Assert(t, !strings.Contains(tests, "Exited 1."), "validation failures are not a review's to weigh")

	// A review that raised nothing is not shown another's findings.
	assert.Equal(t, got[1].Body, reviewScope+"Review the style.")
}

func TestReviewPromptsWithoutHistoryAreScopedOnly(t *testing.T) {
	got := reviewPrompts([]review.Prompt{{Name: "tests", Body: "Review."}}, nil)
	assert.Equal(t, got[0].Body, reviewScope+"Review.")
}

func TestEarlierRoundsMarksAMissingReply(t *testing.T) {
	got := earlierRounds("tests", []Exchange{{Round: 2, Checks: []Check{fail}}})
	assert.Assert(t, strings.Contains(got, "### The implementer replied\n\n(no reply)"), got)
}
