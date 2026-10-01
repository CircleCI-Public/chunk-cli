package factory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// fakeSteps scripts a loop: each Implement call takes the next change, each
// Check call the next round of checks.
type fakeSteps struct {
	changes []Change
	checks  [][]Check
	prompts []string
	checked int
	current Change
	implErr error
}

func (f *fakeSteps) Implement(_ context.Context, prompt string) (Turn, error) {
	f.prompts = append(f.prompts, prompt)
	if f.implErr != nil {
		return Turn{}, f.implErr
	}
	f.current = f.changes[0]
	f.changes = f.changes[1:]
	return Turn{Summary: "done"}, nil
}

func (f *fakeSteps) Collect(context.Context) (Change, error) { return f.current, nil }

func (f *fakeSteps) Check(context.Context, int) ([]Check, error) {
	c := f.checks[f.checked]
	f.checked++
	return c, nil
}

func change(n int) Change {
	return Change{Fingerprint: fmt.Sprintf("fp%d", n), Stat: fmt.Sprintf("%d files changed", n)}
}

var (
	pass    = Check{Name: "lint", Kind: KindValidate, Status: StatusPassed}
	fail    = Check{Name: "tests", Kind: KindReview, Status: StatusFailed, Feedback: "- [high] a.go:3: nil deref"}
	errored = Check{Name: "style", Kind: KindReview, Status: StatusErrored, Error: "timed out"}
)

func TestLoopPassesFirstRound(t *testing.T) {
	steps := &fakeSteps{changes: []Change{change(1)}, checks: [][]Check{{pass}}}
	out, err := Loop{Attempts: 3}.Run(context.Background(), steps, "add a flag")
	assert.NilError(t, err)
	assert.Equal(t, out.Result, ResultPassed)
	assert.Equal(t, out.Rounds, 1)
	assert.DeepEqual(t, steps.prompts, []string{"add a flag"})
}

func TestLoopFeedsFailuresBackUntilPass(t *testing.T) {
	steps := &fakeSteps{changes: []Change{change(1), change(2)}, checks: [][]Check{{pass, fail}, {pass, pass}}}
	out, err := Loop{Attempts: 3}.Run(context.Background(), steps, "add a flag")
	assert.NilError(t, err)
	assert.Equal(t, out.Result, ResultPassed)
	assert.Equal(t, out.Rounds, 2)
	assert.Equal(t, len(steps.prompts), 2)
	assert.Assert(t, strings.Contains(steps.prompts[1], "a.go:3: nil deref"), steps.prompts[1])
	assert.Assert(t, !strings.Contains(steps.prompts[1], "lint"), "passing checks are not fed back")
}

func TestLoopStopsWhenAttemptsRunOut(t *testing.T) {
	steps := &fakeSteps{changes: []Change{change(1), change(2)}, checks: [][]Check{{fail}, {fail}}}
	out, err := Loop{Attempts: 2}.Run(context.Background(), steps, "p")
	assert.NilError(t, err)
	assert.Equal(t, out.Result, ResultExhausted)
	assert.Equal(t, out.Rounds, 2)
	// No turn is spent on fixes that would never be checked.
	assert.Equal(t, len(steps.prompts), 2)
}

func TestLoopStopsWhenImplementerChangesNothing(t *testing.T) {
	steps := &fakeSteps{changes: []Change{change(1), change(1)}, checks: [][]Check{{fail}}}
	out, err := Loop{Attempts: 5}.Run(context.Background(), steps, "p")
	assert.NilError(t, err)
	assert.Equal(t, out.Result, ResultStuck)
	assert.Equal(t, steps.checked, 1, "the same code is not reviewed twice")
}

func TestLoopReportsNoChangeOnFirstRound(t *testing.T) {
	steps := &fakeSteps{changes: []Change{{Fingerprint: "empty"}}}
	out, err := Loop{Attempts: 3}.Run(context.Background(), steps, "p")
	assert.NilError(t, err)
	assert.Equal(t, out.Result, ResultNoChange)
	assert.Equal(t, steps.checked, 0)
}

// TestLoopRechecksWithoutImplementingAfterOnlyErrors guards against prompting
// the implementer with nothing to fix: it would change nothing, and the loop
// would then call it stuck instead of retrying the checks that could not run.
func TestLoopRechecksWithoutImplementingAfterOnlyErrors(t *testing.T) {
	steps := &fakeSteps{changes: []Change{change(1)}, checks: [][]Check{{pass, errored}, {pass, pass}}}
	out, err := Loop{Attempts: 3}.Run(context.Background(), steps, "p")
	assert.NilError(t, err)
	assert.Equal(t, out.Result, ResultPassed)
	assert.Equal(t, out.Rounds, 2)
	assert.Equal(t, len(steps.prompts), 1)
}

func TestLoopReturnsStepErrorsWithOutcomeSoFar(t *testing.T) {
	boom := errors.New("sidecar gone")
	steps := &fakeSteps{implErr: boom}
	_, err := Loop{Attempts: 3}.Run(context.Background(), steps, "p")
	assert.Assert(t, errors.Is(err, boom))
	assert.ErrorContains(t, err, "round 1: implement")
}

func TestLoopRejectsBadInput(t *testing.T) {
	_, err := Loop{Attempts: 0}.Run(context.Background(), &fakeSteps{}, "p")
	assert.ErrorContains(t, err, "attempts")
	_, err = Loop{Attempts: 1}.Run(context.Background(), &fakeSteps{}, "")
	assert.ErrorContains(t, err, "prompt is empty")
}

func TestLoopEmitsEventsInOrder(t *testing.T) {
	var kinds []EventKind
	steps := &fakeSteps{changes: []Change{change(1)}, checks: [][]Check{{pass}}}
	_, err := Loop{Attempts: 1, OnEvent: func(e Event) { kinds = append(kinds, e.Kind) }}.Run(context.Background(), steps, "p")
	assert.NilError(t, err)
	assert.DeepEqual(t, kinds, []EventKind{EventImplementing, EventImplemented, EventCollected, EventChecking, EventChecked})
}
