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
		Round: 1,
		// A validation command can share a review's name; its failure is
		// not the review's to weigh.
		Checks: []Check{fail, {Name: "tests", Kind: KindValidate, Status: StatusFailed, Feedback: "Exited 1."}},
		Reply:  "Fixed a.go. Left the nil deref alone: the input is never empty.",
	}}

	got := reviewPrompts(prompts, history)

	tests := got[0].Body
	assert.Assert(t, strings.HasPrefix(tests, reviewScope+"Review the tests."), tests)
	assert.Assert(t, strings.Contains(tests, "## Earlier rounds"), tests)
	assert.Assert(t, strings.Contains(tests, "### Round 1: you raised\n\n- [high] a.go:3: nil deref"), tests)
	assert.Assert(t, strings.Contains(tests, "the input is never empty"), tests)
	assert.Assert(t, !strings.Contains(tests, "Exited 1."), tests)

	// A review that raised nothing is not shown another's findings.
	assert.Equal(t, got[1].Body, reviewScope+"Review the style.")
}
