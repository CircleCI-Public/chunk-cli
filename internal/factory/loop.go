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
	// Check reviews and validates the implementer's work so far.
	Check(ctx context.Context, round int) ([]Check, error)
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
	// feedback, so another round would review the same code again.
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
	// CheckFirst checks the work already there before the implementer's first
	// turn, which is then sent the prompt with what failed. Work that passes
	// as it is ends the loop without a turn. The first round counts as one of
	// the attempts.
	CheckFirst bool
	OnEvent    func(Event)
}

// Run implements prompt, then checks the work and feeds failures back until
// the checks pass, the implementer stops changing anything, or attempts run
// out. The returned error is for a step that could not run, not for checks
// that failed; the outcome so far is returned with it.
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

	var out Outcome
	// lead is held back from the implementer until there is feedback to send
	// with it, when the loop checks first.
	var lead string
	if l.CheckFirst {
		change, err := steps.Collect(ctx)
		if err != nil {
			return out, fmt.Errorf("round 1: %w", err)
		}
		emit(Event{Kind: EventCollected, Round: 1, Change: change})
		if change.Empty() {
			out.Result, out.Change = ResultNoChange, change
			return out, nil
		}
		out.Change = change
		lead, prompt = prompt, ""
	}
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

			change, err := steps.Collect(ctx)
			if err != nil {
				return out, fmt.Errorf("round %d: %w", round, err)
			}
			emit(Event{Kind: EventCollected, Round: round, Change: change})
			switch {
			case change.Empty():
				out.Result, out.Change = ResultNoChange, change
				return out, nil
			case round > 1 && change.Fingerprint == out.Change.Fingerprint:
				// The last round's checks still describe this code.
				out.Result = ResultStuck
				return out, nil
			}
			out.Change = change
		}

		emit(Event{Kind: EventChecking, Round: round})
		checks, err := steps.Check(ctx, round)
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
		if lead != "" && prompt != "" {
			prompt, lead = lead+"\n\n"+prompt, ""
		}
	}
}
