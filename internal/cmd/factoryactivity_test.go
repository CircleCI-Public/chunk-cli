package cmd

import (
	"context"
	"errors"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// fakeSteps runs a loop with no sidecars: every turn returns turn and err.
type fakeSteps struct {
	turn factory.Turn
	err  error
}

func (f fakeSteps) Implement(context.Context, string) (factory.Turn, error) { return f.turn, f.err }
func (fakeSteps) Collect(context.Context) (factory.Change, error) {
	return factory.Change{}, nil
}
func (fakeSteps) Check(context.Context, int) ([]factory.Check, error) { return nil, nil }

func TestFactoryActivityFilesARoundOnItsSidecars(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv("CHUNK_WATCHD_DIR", t.TempDir())
	root := t.TempDir()
	ctx := context.Background()

	activity := newFactoryActivity(ctx, root, "chunk/factory/run-1", "sb-impl")
	assert.Assert(t, activity != nil)
	steps := recordedSteps{Steps: fakeSteps{turn: factory.Turn{Duration: 90 * time.Second, CostUSD: 1.5}}, activity: activity}

	_, err := steps.Implement(ctx, "add a flag")
	assert.NilError(t, err)
	activity.commandSubmitted(config.Command{Name: "test", Run: "go test ./..."}, "cmd-1")
	activity.reviewProgress(review.ProgressEvent{Prompt: "naming", SidecarID: "sb-rev", State: review.StateRunning})
	activity.checked(factory.Check{Name: "test", Kind: factory.KindValidate, Status: factory.StatusFailed, Duration: 3 * time.Second})
	activity.reviewProgress(review.ProgressEvent{Prompt: "naming", SidecarID: "sb-rev", State: review.StateDone, Duration: time.Second})

	events := reviewEvents(t, root)
	for _, e := range events {
		// The work is on the run's branch, not the one checked out.
		assert.Equal(t, e.Branch, "chunk/factory/run-1")
	}

	impl := eventsFor(events, "sb-impl")
	assert.Equal(t, len(impl), 4)
	assert.Equal(t, impl[0].Op, eventlog.OpImplement)
	assert.Assert(t, !impl[0].Final, "the opening event must leave the turn open")
	assert.Equal(t, impl[1].Op, eventlog.OpImplement)
	assert.Equal(t, impl[1].Msg, "implemented in 1m30s ($1.50)")
	passed, total, ok := impl[1].Outcome()
	assert.Assert(t, ok, "a turn must close or the sidecar reads as busy")
	assert.Equal(t, passed, 1)
	assert.Equal(t, total, 1)

	assert.Equal(t, impl[2].Op, eventlog.OpValidate)
	assert.Equal(t, impl[2].Msg, "$ go test ./...")
	assert.Equal(t, impl[3].Msg, "test failed in 3.0s")
	assert.Equal(t, impl[3].CommandID, "cmd-1", "the closing event carries the command so its output can be replayed")
	passed, _, ok = impl[3].Outcome()
	assert.Assert(t, ok, "each validation command closes as a run of its own")
	assert.Equal(t, passed, 0)

	rev := eventsFor(events, "sb-rev")
	assert.Equal(t, len(rev), 2)
	assert.Equal(t, rev[0].Op, eventlog.OpReview)
}

// The loop reports no event for a turn that fails, so the steps have to close
// it themselves.
func TestFactoryActivityClosesAFailedTurn(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()
	ctx := context.Background()

	activity := newFactoryActivity(ctx, root, "chunk/factory/run-1", "sb-impl")
	steps := recordedSteps{Steps: fakeSteps{err: errors.New("claude exited 1")}, activity: activity}
	_, err := factory.Loop{Attempts: 1}.Run(ctx, steps, "add a flag")
	assert.ErrorContains(t, err, "claude exited 1")

	impl := eventsFor(reviewEvents(t, root), "sb-impl")
	assert.Equal(t, len(impl), 3)
	assert.Equal(t, impl[1].Msg, "implementer failed: claude exited 1")
	assert.Assert(t, !impl[1].Final, "the reason must not close the turn on its own")
	passed, _, ok := impl[2].Outcome()
	assert.Assert(t, ok)
	assert.Equal(t, passed, 0)
}

func TestFactoryActivityThrottlesToolUse(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()

	activity := newFactoryActivity(context.Background(), root, "chunk/factory/run-1", "sb-impl")
	activity.implementing()
	activity.toolUsed(factory.Activity{Tool: "Edit", Detail: "main.go"})
	activity.toolUsed(factory.Activity{Tool: "Bash", Detail: "go test ./..."})
	activity.toolUsed(factory.Activity{Detail: "text the implementer wrote"})

	impl := eventsFor(reviewEvents(t, root), "sb-impl")
	assert.Equal(t, len(impl), 2, "a burst of tool uses files only the first")
	assert.Equal(t, impl[1].Msg, "Edit main.go")

	// The next turn files its first tool use straight away.
	activity.implementing()
	activity.toolUsed(factory.Activity{Tool: "Read", Detail: "go.mod"})
	assert.Equal(t, len(eventsFor(reviewEvents(t, root), "sb-impl")), 4)
}

func TestFactoryActivityRegistersValidationForOutputReplay(t *testing.T) {
	regs := captureRegistrations(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()

	activity := newFactoryActivity(context.Background(), root, "chunk/factory/run-1", "sb-impl")
	activity.commandSubmitted(config.Command{Name: "lint", Run: "task lint"}, "cmd-9")

	select {
	case reg := <-regs:
		assert.Equal(t, reg.CommandID, "cmd-9")
		assert.Equal(t, reg.SidecarID, "sb-impl")
		assert.Equal(t, reg.Op, string(eventlog.OpValidate))
		assert.Equal(t, reg.Name, "lint")
		assert.Equal(t, config.CanonicalProjectRoot(reg.ProjectRoot), config.CanonicalProjectRoot(root))
	case <-time.After(5 * time.Second):
		t.Fatal("validation command was not registered")
	}
}

// Recording is best-effort: with no log, a run goes ahead recording nothing.
func TestFactoryActivityToleratesNoLog(t *testing.T) {
	var activity *factoryActivity
	steps := recordedSteps{Steps: fakeSteps{}, activity: activity}
	_, err := steps.Implement(context.Background(), "add a flag")
	assert.NilError(t, err)
	activity.toolUsed(factory.Activity{Tool: "Edit"})
	activity.commandSubmitted(config.Command{Name: "test"}, "cmd-1")
	activity.checked(factory.Check{Status: factory.StatusPassed})
	activity.reviewProgress(review.ProgressEvent{SidecarID: "sb-rev", State: review.StateRunning})
	activity.reviewSubmitted(&sidecar.PoolEntry{ID: "sb-rev"}, "naming", "cmd-2")
	activity.finish(errors.New("stopped"))
}
