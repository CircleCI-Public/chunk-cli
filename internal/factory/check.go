package factory

import (
	"fmt"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// Kind is what produced a check: a review prompt or a validation command.
type Kind string

// Check kinds.
const (
	KindReview   Kind = "review"
	KindValidate Kind = "validate"
)

// Status is how a check came out.
type Status string

// Check statuses.
const (
	// StatusPassed means the check ran and found nothing to fix.
	StatusPassed Status = "passed"
	// StatusFailed means the check ran and found something the implementer
	// must fix: review findings, or a validation command exiting non-zero.
	StatusFailed Status = "failed"
	// StatusErrored means the check could not run, such as a review that timed
	// out. It says nothing about the code, so it is not fed back.
	StatusErrored Status = "errored"
)

// Check is the outcome of one review or validation command in one round. A
// review is treated like any other command run on a sidecar: it passes or
// fails, and a failure carries feedback for the implementer.
type Check struct {
	Name      string
	Kind      Kind
	Status    Status
	SidecarID string
	Duration  time.Duration
	// Feedback is what the implementer is told when the check failed.
	Feedback string
	// Error is why the check could not run, for StatusErrored.
	Error string
	// Findings are a review's findings, kept as data for display.
	Findings []review.Finding
}

// outputTail bounds how much of a failed command's output is fed back. The end
// is kept: that is where test runners and compilers summarise a failure.
const outputTail = 4000

// FromReview converts one review result, run with structured findings, into a
// check. A review fails only on findings worth changing (high or medium), the
// same bar the session loop uses, so the implementer is not sent round after
// round to polish style remarks. Lower findings are kept for display. A review
// that could not run errored.
func FromReview(r review.Result) Check {
	c := Check{Name: r.Prompt, Kind: KindReview, SidecarID: r.SidecarID, Duration: r.Duration, Findings: r.Parsed.Findings}
	if r.Error != "" {
		c.Status, c.Error = StatusErrored, r.Error
		return c
	}
	var b strings.Builder
	for _, f := range r.Parsed.Findings {
		if !f.WorthChanging() {
			continue
		}
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(&b, "- [%s] %s: %s\n", f.Severity, loc, f.Body)
	}
	if b.Len() == 0 {
		c.Status = StatusPassed
		return c
	}
	c.Status, c.Feedback = StatusFailed, strings.TrimRight(b.String(), "\n")
	return c
}

// FromCommand converts one validation command's run into a check. runErr is a
// failure to run the command at all, as opposed to it exiting non-zero.
func FromCommand(name, sidecarID string, exitCode int, output string, d time.Duration, runErr error) Check {
	c := Check{Name: name, Kind: KindValidate, SidecarID: sidecarID, Duration: d}
	switch {
	case runErr != nil:
		c.Status, c.Error = StatusErrored, runErr.Error()
	case exitCode == 0:
		c.Status = StatusPassed
	default:
		c.Status = StatusFailed
		c.Feedback = fmt.Sprintf("Exited %d.\n```\n%s\n```", exitCode, tailText(strings.TrimSpace(output), outputTail))
	}
	return c
}

func tailText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// Feedback renders the failed checks of a round as the implementer's next
// prompt, or "" when nothing failed. Errored checks are left out: they say
// nothing about the code, and asking the implementer to fix them would send it
// chasing infrastructure.
func Feedback(checks []Check) string {
	var b strings.Builder
	for _, c := range checks {
		if c.Status != StatusFailed {
			continue
		}
		fmt.Fprintf(&b, "\n## %s: %s\n\n%s\n", c.Kind, c.Name, c.Feedback)
	}
	if b.Len() == 0 {
		return ""
	}
	return "Your changes were reviewed and validated, and these checks failed. " +
		"Fix the problems they describe in the code. A review finding you judge to be wrong " +
		"may be left alone; say why in your final message.\n" + b.String()
}

// Passed reports whether every check passed. An errored check is not a pass:
// a round where a review could not run has not shown the code is clean.
func Passed(checks []Check) bool {
	for _, c := range checks {
		if c.Status != StatusPassed {
			return false
		}
	}
	return true
}
