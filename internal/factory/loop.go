// Package factory drives an intent to done: a worker agent implements it,
// review agents judge the result, and their feedback goes back to the worker
// until the reviews pass or the attempts run out.
package factory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// DefaultMaxAttempts is how many work rounds a run gets when none is given.
const DefaultMaxAttempts = 3

// ErrNotConverged is returned when every attempt ran and the last still had
// failing reviews.
var ErrNotConverged = errors.New("reviews still failing after the last attempt")

// ReviewResult is one reviewer's answer for one attempt.
type ReviewResult struct {
	Name     string
	Verdict  Verdict
	Feedback string
}

// Attempt records one round of work and the reviews of it.
type Attempt struct {
	N       int
	Reviews []ReviewResult
	Passed  bool
}

// Options configures a run. Work and Review do the remote work, so the loop
// itself holds no sidecar logic.
type Options struct {
	Intent      string
	MaxAttempts int
	// FailOn is the least severe verdict that fails an attempt: VerdictBlocked
	// lets warnings through, VerdictWarn does not.
	FailOn Verdict
	// Work runs the worker agent on prompt and returns once its changes are in
	// the local tree.
	Work func(ctx context.Context, attempt int, prompt string) error
	// Review reviews the local tree.
	Review func(ctx context.Context, attempt int) ([]ReviewResult, error)
	Status iostream.StatusFunc
}

// Run loops work and review until an attempt passes or MaxAttempts is
// reached. It returns every attempt made, and ErrNotConverged when none passed.
func Run(ctx context.Context, opts Options) ([]Attempt, error) {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultMaxAttempts
	}
	if opts.FailOn == "" {
		opts.FailOn = VerdictBlocked
	}
	status := opts.Status
	if status == nil {
		status = func(iostream.Level, string) {}
	}

	var attempts []Attempt
	feedback := ""
	for n := 1; n <= opts.MaxAttempts; n++ {
		status(iostream.LevelStep, fmt.Sprintf("Attempt %d of %d: working...", n, opts.MaxAttempts))
		if err := opts.Work(ctx, n, WorkPrompt(opts.Intent, feedback)); err != nil {
			return attempts, fmt.Errorf("attempt %d: work: %w", n, err)
		}

		status(iostream.LevelStep, fmt.Sprintf("Attempt %d of %d: reviewing...", n, opts.MaxAttempts))
		reviews, err := opts.Review(ctx, n)
		if err != nil {
			return attempts, fmt.Errorf("attempt %d: review: %w", n, err)
		}
		a := Attempt{N: n, Reviews: reviews, Passed: Passed(reviews, opts.FailOn)}
		attempts = append(attempts, a)
		if a.Passed {
			status(iostream.LevelDone, fmt.Sprintf("Attempt %d passed", n))
			return attempts, nil
		}
		status(iostream.LevelWarn, fmt.Sprintf("Attempt %d did not pass", n))
		feedback = Feedback(reviews, opts.FailOn)
	}
	return attempts, ErrNotConverged
}

// Passed reports whether no review returned a verdict that fails the attempt.
func Passed(reviews []ReviewResult, failOn Verdict) bool {
	for _, r := range reviews {
		if fails(r.Verdict, failOn) {
			return false
		}
	}
	return true
}

// fails reports whether a verdict fails an attempt: only blocked does under
// VerdictBlocked, anything but approved does under VerdictWarn.
func fails(v, failOn Verdict) bool {
	if failOn == VerdictWarn {
		return v != VerdictApproved
	}
	return v == VerdictBlocked
}

// Feedback is what the worker is told to address: every review whose verdict
// fails the attempt.
func Feedback(reviews []ReviewResult, failOn Verdict) string {
	var b strings.Builder
	for _, r := range reviews {
		if fails(r.Verdict, failOn) {
			fmt.Fprintf(&b, "## Review: %s (%s)\n\n%s\n\n", r.Name, r.Verdict, r.Feedback)
		}
	}
	return strings.TrimSpace(b.String())
}

// WorkPrompt is what the worker is asked to do. The intent is repeated on every
// attempt rather than relying on the agent's session, so a replaced worker
// sidecar picks up where the last one left off.
func WorkPrompt(intent, feedback string) string {
	if feedback == "" {
		return intent
	}
	return fmt.Sprintf(`%s

## Feedback on your previous attempt

Your changes so far are already in this repository. Reviewers found the
issues below. Address every one of them, then stop.

%s`, intent, feedback)
}
