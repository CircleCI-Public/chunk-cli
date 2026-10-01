// Package factory drives an intent to done: a worker agent implements it, then
// validation and review agents check the result, and their feedback goes back
// to the worker until the checks pass or the attempts run out.
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

// maxValidateFeedbackBytes caps how much validation output is fed back to the
// worker. Failures are reported at the end of a run's output, so the tail is
// what is kept.
const maxValidateFeedbackBytes = 32 * 1024

// ErrNotConverged is returned when every attempt ran and the last still failed
// its checks.
var ErrNotConverged = errors.New("checks still failing after the last attempt")

// ReviewResult is one reviewer's answer for one attempt. Err is set when the
// review could not produce a verdict at all.
type ReviewResult struct {
	Name     string
	Verdict  Verdict
	Feedback string
	Err      string
}

// Check is everything the checkers said about one attempt.
type Check struct {
	ValidatePassed bool
	ValidateOutput string
	Reviews        []ReviewResult
}

// Attempt records one round of work and its check.
type Attempt struct {
	N      int
	Check  Check
	Passed bool
}

// Options configures a run. Work and Check do the remote work, so the loop
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
	// Check validates and reviews the local tree.
	Check  func(ctx context.Context, attempt int) (Check, error)
	Status iostream.StatusFunc
}

// Run loops work and check until an attempt passes or MaxAttempts is reached.
// It returns every attempt made, and ErrNotConverged when none passed.
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

		status(iostream.LevelStep, fmt.Sprintf("Attempt %d of %d: checking...", n, opts.MaxAttempts))
		check, err := opts.Check(ctx, n)
		if err != nil {
			return attempts, fmt.Errorf("attempt %d: check: %w", n, err)
		}
		// A check cut short by cancellation says nothing about the work.
		if err := ctx.Err(); err != nil {
			return attempts, err
		}
		a := Attempt{N: n, Check: check, Passed: Passed(check, opts.FailOn)}
		attempts = append(attempts, a)
		if a.Passed {
			status(iostream.LevelDone, fmt.Sprintf("Attempt %d passed", n))
			return attempts, nil
		}
		status(iostream.LevelWarn, fmt.Sprintf("Attempt %d did not pass", n))
		feedback = Feedback(check, opts.FailOn)
	}
	return attempts, ErrNotConverged
}

// Passed reports whether an attempt's check is good enough to stop the loop:
// validation passed and no review failed to run or returned a verdict at or
// above failOn. A review that never produced a verdict fails the attempt, so a
// broken reviewer cannot wave the work through.
func Passed(check Check, failOn Verdict) bool {
	if !check.ValidatePassed {
		return false
	}
	for _, r := range check.Reviews {
		if r.Err != "" || fails(r.Verdict, failOn) {
			return false
		}
	}
	return true
}

// fails reports whether a verdict is at least as severe as failOn.
func fails(v, failOn Verdict) bool {
	return severity(v) >= severity(failOn)
}

func severity(v Verdict) int {
	switch v {
	case VerdictBlocked:
		return 2
	case VerdictWarn:
		return 1
	case VerdictApproved:
		return 0
	}
	return 0
}

// Feedback turns a failed check into what the worker is told to address:
// validation output when it failed, and every review whose verdict fails. A
// review that could not run is left out: it is nothing the worker can fix.
func Feedback(check Check, failOn Verdict) string {
	var b strings.Builder
	if !check.ValidatePassed {
		out := check.ValidateOutput
		if len(out) > maxValidateFeedbackBytes {
			out = "…" + out[len(out)-maxValidateFeedbackBytes:]
		}
		fmt.Fprintf(&b, "## Validation failed\n\n```\n%s\n```\n\n", strings.TrimSpace(out))
	}
	for _, r := range check.Reviews {
		if r.Err == "" && fails(r.Verdict, failOn) {
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

Your changes so far are already in this repository. Validation and review
found the issues below. Address every one of them, then stop.

%s`, intent, feedback)
}
