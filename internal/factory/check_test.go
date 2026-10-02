package factory

import (
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

func TestFromReview(t *testing.T) {
	clean := FromReview(review.Result{Prompt: "tests", Output: "Nothing found."})
	assert.Equal(t, clean.Status, StatusPassed)

	broken := FromReview(review.Result{Prompt: "tests", Error: "timed out after 15m0s"})
	assert.Equal(t, broken.Status, StatusErrored)
	assert.Equal(t, broken.Feedback, "")

	found := FromReview(review.Result{Prompt: "tests", Parsed: review.Parsed{Findings: []review.Finding{
		{File: "a.go", Line: 3, Severity: review.SeverityHigh, Body: "nil deref on empty input"},
		{File: "b.go", Severity: review.SeverityMedium, Body: "error dropped"},
		{File: "c.go", Line: 9, Severity: review.SeverityLow, Body: "naming"},
	}}})
	assert.Equal(t, found.Status, StatusFailed)
	// Low findings are kept for display but not fed back.
	assert.Equal(t, found.Feedback, "- [high] a.go:3: nil deref on empty input\n- [medium] b.go: error dropped")
	assert.Equal(t, len(found.Findings), 3)
}

// TestFromReviewPassesOnMinorFindings guards the churn the live run showed:
// reviewers find something smaller every round, so only findings worth
// changing may fail a review.
func TestFromReviewPassesOnMinorFindings(t *testing.T) {
	c := FromReview(review.Result{Prompt: "style", Parsed: review.Parsed{Findings: []review.Finding{
		{File: "a.go", Severity: review.SeverityLow, Body: "naming"},
		{File: "a.go", Severity: review.SeverityInfo, Body: "fyi"},
	}}})
	assert.Equal(t, c.Status, StatusPassed)
	assert.Equal(t, c.Feedback, "")
	assert.Equal(t, len(c.Findings), 2)
}

func TestFromCommand(t *testing.T) {
	assert.Equal(t, FromCommand("lint", "sc", 0, "ok", 0, nil).Status, StatusPassed)

	failed := FromCommand("test", "sc", 2, strings.Repeat("x", outputTail)+"FAIL pkg", 0, nil)
	assert.Equal(t, failed.Status, StatusFailed)
	// The tail is kept, where a test runner reports what failed.
	assert.Assert(t, strings.Contains(failed.Feedback, "FAIL pkg"))
	assert.Assert(t, strings.HasPrefix(failed.Feedback, "Exited 2."))
	// The exit code and output are kept for the run's summary too.
	assert.Equal(t, failed.ExitCode, 2)
	assert.Assert(t, strings.HasSuffix(failed.Output, "FAIL pkg"))
	assert.Assert(t, len(failed.Output) <= outputTail+len("…"))

	errored := FromCommand("test", "sc", 0, "", 0, errors.New("exec: connection reset"))
	assert.Equal(t, errored.Status, StatusErrored)
}

func TestFeedbackLeavesOutPassedAndErrored(t *testing.T) {
	assert.Equal(t, Feedback([]Check{pass, errored}), "")

	fb := Feedback([]Check{pass, fail, errored})
	assert.Assert(t, strings.Contains(fb, "## review: tests"))
	assert.Assert(t, !strings.Contains(fb, "style"))
	assert.Assert(t, !strings.Contains(fb, "lint"))
}

func TestPassedTreatsErroredAsNotPassed(t *testing.T) {
	assert.Assert(t, Passed([]Check{pass}))
	assert.Assert(t, !Passed([]Check{pass, errored}))
	assert.Assert(t, !Passed([]Check{fail}))
}

func TestValidationCommands(t *testing.T) {
	got := ValidationCommands([]config.Command{
		{Name: "test", Run: "go test ./...", Role: "gate"},
		{Name: "test-changed", Run: "go test {{CHANGED_PACKAGES}}"},
		{Name: "format", Run: "gofmt -w .", Role: "autofix"},
		{Name: "local-only", Run: "make thing", Local: true},
		{Name: "lint", Run: "golangci-lint run"},
	})
	var names []string
	for _, c := range got {
		names = append(names, c.Name)
	}
	assert.DeepEqual(t, names, []string{"test", "lint"})
}
