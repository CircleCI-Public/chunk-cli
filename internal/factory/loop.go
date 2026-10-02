package factory

import (
	"context"
	"errors"
	"fmt"
)

// Steps are the operations one round of the loop is made of. Sidecars
// implements them against real sidecars; tests implement them in memory.
type Steps interface {
	// Implement runs one implementer turn with prompt.
	Implement(ctx context.Context, prompt string) (Turn, error)
	// Collect describes the implementer's work so far.
	Collect(ctx context.Context) (Change, error)
	// Check reviews and validates the implementer's work so far. history is
	// every earlier round that failed, with the implementer's reply to it.
	Check(ctx context.Context, round int, history []Exchange) ([]Check, error)
}

// Exchange is one round's failed checks and the implementer's reply to them.
// Reviews are shown the exchanges for what they raised, so a finding the
// implementer declined with a reason is weighed against that reason rather
// than raised afresh by a reviewer that has forgotten it.
type Exchange struct {
	Round int
	// Checks are the round's failed checks, as fed back.
	Checks []Check
	// Reply is the implementer's summary of the turn that answered them.
	Reply string
}

// Result is why the loop stopped.
type Result string

// Loop results.
const (
	// ResultPassed means every check passed.
	ResultPassed Result = "passed"
	// ResultExhausted means checks still failed when attempts ran out.
	ResultExhausted Result = "exhausted"
	// ResultStuck means the implementer changed nothing in response to
	// feedback that its reply cannot settle: a validation command failed, or
	// the reviewers already weighed its reply to the same code.
	ResultStuck Result = "stuck"
	// ResultNoChange means the implementer's work is empty: it changed
	// nothing, or reverted everything it had changed.
	ResultNoChange Result = "no_change"
)

// Outcome is the end state of a loop.
type Outcome struct {
	Result Result
	// Rounds is how many rounds were checked.
	Rounds int
	Change Change
	// Checks are the last round's checks.
	Checks []Check
}

// EventKind identifies a step of the loop for display.
type EventKind int

// Event kinds, in the order a round emits them.
const (
	EventImplementing EventKind = iota // a turn started; Round, Prompt
	EventImplemented                   // a turn ended; Round, Turn
	EventCollected                     // the work was collected; Round, Change
	EventChecking                      // checks started; Round
	EventChecked                       // checks ended; Round, Checks
)

// Event reports progress through the loop.
type Event struct {
	Kind   EventKind
	Round  int
	Prompt string
	Turn   Turn
	Change Change
	Checks []Check
}

// Loop drives the implementer through rounds of review and validation.
type Loop struct {
	// Attempts is the most rounds to check. Each round after the first starts
	// with an implementer turn fixing the previous round's failures.
	Attempts int
	OnEvent  func(Event)
}

// Run implements prompt, then checks the work and feeds failures back until
// the checks pass, the implementer stops changing anything, or attempts run
// out. The returned error is for a step that could not run, not for checks
// that failed; the outcome so far is returned with it.
//
// An implementer that changes nothing may be declining review findings it
// judges wrong. The same code is then checked once more, so the reviewers can
// weigh its reply; it is stuck only if it changes nothing again, or if a
// validation command failed, which no reply can answer.
func (l Loop) Run(ctx context.Context, steps Steps, prompt string) (Outcome, error) {
	if l.Attempts < 1 {
		return Outcome{}, errors.New("attempts must be at least 1")
	}
	if prompt == "" {
		return Outcome{}, errors.New("prompt is empty")
	}
	emit := l.OnEvent
	if emit == nil {
		emit = func(Event) {}
	}

	var (
		out     Outcome
		history []Exchange
		// rechecked is set when the last round checked unchanged code.
		rechecked bool
	)
	for round := 1; ; round++ {
		// An empty prompt means the last round failed only on checks that could
		// not run: there is nothing to fix, so the same code is checked again.
		if prompt != "" {
			emit(Event{Kind: EventImplementing, Round: round, Prompt: prompt})
			turn, err := steps.Implement(ctx, prompt)
			if err != nil {
				return out, fmt.Errorf("round %d: implement: %w", round, err)
			}
			emit(Event{Kind: EventImplemented, Round: round, Turn: turn})
			if round > 1 {
				history = append(history, Exchange{Round: round - 1, Checks: failed(out.Checks), Reply: turn.Summary})
			}

			change, err := steps.Collect(ctx)
			if err != nil {
				return out, fmt.Errorf("round %d: %w", round, err)
			}
			emit(Event{Kind: EventCollected, Round: round, Change: change})
			unchanged := round > 1 && change.Fingerprint == out.Change.Fingerprint
			switch {
			case change.Empty():
				out.Result, out.Change = ResultNoChange, change
				return out, nil
			case unchanged && (rechecked || validationFailed(out.Checks)):
				// The last round's checks still describe this code.
				out.Result = ResultStuck
				return out, nil
			}
			rechecked = unchanged
			out.Change = change
		}

		emit(Event{Kind: EventChecking, Round: round})
		checks, err := steps.Check(ctx, round, history)
		if err != nil {
			return out, fmt.Errorf("round %d: check: %w", round, err)
		}
		out.Rounds, out.Checks = round, checks
		emit(Event{Kind: EventChecked, Round: round, Checks: checks})

		if Passed(checks) {
			out.Result = ResultPassed
			return out, nil
		}
		if round == l.Attempts {
			out.Result = ResultExhausted
			return out, nil
		}
		prompt = Feedback(checks)
	}
}

// failed keeps the checks that failed, the ones fed back.
func failed(checks []Check) []Check {
	var out []Check
	for _, c := range checks {
		if c.Status == StatusFailed {
			out = append(out, c)
		}
	}
	return out
}

// validationFailed reports whether a validation command failed: unchanged
// code would fail it again whatever the implementer replied.
func validationFailed(checks []Check) bool {
	for _, c := range checks {
		if c.Kind == KindValidate && c.Status == StatusFailed {
			return true
		}
	}
	return false
}
