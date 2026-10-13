package factory

import (
	"context"
	"errors"
	"fmt"

	"github.com/smeltdev/smelt/internal/review"
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
	// ResultBlocked means the implementer finished what it could and ended its
	// summary with a BLOCKED: line saying what it could not do.
	ResultBlocked Result = "blocked"
)

// Outcome is the end state of a loop.
type Outcome struct {
	Result Result
	// Rounds is how many rounds were checked.
	Rounds int
	Change Change
	// Checks are the last round's checks.
	Checks []Check
	// Blocked is the reason the implementer's last summary gave after
	// BLOCKED:, or "".
	Blocked string
	// Plan is the plan the implementer was sent with, nil when the run did
	// not plan.
	Plan *Plan
}

// Incomplete is the findings of the last round's checks that say requested
// work is missing.
func (o Outcome) Incomplete() []review.Finding {
	return MissingWork(o.Checks)
}

// EventKind identifies a step of the loop for display.
type EventKind int

// Event kinds, in the order a round emits them.
const (
	EventPlanning     EventKind = iota // the plan was asked for
	EventPlanReviewed                  // the plan is settled; Plan, Findings
	EventImplementing                  // a turn started; Round, Prompt
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
	// Plan and Findings are the settled plan and what its review still found
	// wrong with it.
	Plan     *Plan
	Findings []review.Finding
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
	// Planner, when set, has the implementer plan, and the plan reviewed,
	// before its first turn. A loop that checks first does not plan: there is
	// work to describe already.
	Planner Planner
	OnEvent func(Event)
}

// Run implements prompt, then checks the work and feeds failures back until
// the checks pass, the implementer stops changing anything, or attempts run
// out. The returned error is for a step that could not run, not for checks
// that failed; the outcome so far is returned with it.
func (l Loop) Run(ctx context.Context, steps Steps, prompt string) (Outcome, error) {
	out, err := l.run(ctx, steps, prompt)
	// A block outranks why the loop stopped: the implementer said what it
	// cannot do, which no further round changes.
	if out.Blocked != "" && out.Result != "" && out.Result != ResultNoChange {
		out.Result = ResultBlocked
	}
	return out, err
}

func (l Loop) run(ctx context.Context, steps Steps, prompt string) (Outcome, error) {
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
	if l.Planner != nil && !l.CheckFirst {
		plan, preamble, err := l.plan(ctx, emit, prompt)
		if err != nil {
			return out, fmt.Errorf("plan: %w", err)
		}
		out.Plan, prompt = &plan, preamble+prompt
	}
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
			out.Blocked = BlockedReason(turn.Summary)

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

// plan has the implementer write a plan and has it reviewed. A plan whose
// review found something worth changing is revised once; if the revision is
// still found wanting the run goes ahead anyway, with what the review said in
// front of the implementer, so a disagreement cannot stall it. preamble is
// what goes before the request in the implementer's first prompt.
func (l Loop) plan(ctx context.Context, emit func(Event), request string) (plan Plan, preamble string, err error) {
	emit(Event{Kind: EventPlanning})
	in := PlanInput{Request: request}
	var worth []review.Finding
	// The first pass writes the plan; the second revises it.
	for range 2 {
		if plan, err = l.Planner.Plan(ctx, in); err != nil {
			return Plan{}, "", err
		}
		var found []review.Finding
		if found, err = l.Planner.ReviewPlan(ctx, request, plan); err != nil {
			return Plan{}, "", err
		}
		if worth = worthChanging(found); len(worth) == 0 {
			break
		}
		prev := plan
		in = PlanInput{Request: request, Previous: &prev, Findings: worth}
	}
	emit(Event{Kind: EventPlanReviewed, Plan: &plan, Findings: worth})
	return plan, planPreamble(plan, worth), nil
}

// worthChanging is the findings that send work back.
func worthChanging(findings []review.Finding) []review.Finding {
	var out []review.Finding
	for _, f := range findings {
		if f.WorthChanging() {
			out = append(out, f)
		}
	}
	return out
}
