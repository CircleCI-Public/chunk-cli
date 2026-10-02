package watch

import (
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
)

// A review run is filed as a step event, an optional error event carrying why
// it failed, and a closing event. The pane hides the closing event behind the
// header, so the failure reason has to be visible on the lines above it.
func TestReviewRunRendersAsAReview(t *testing.T) {
	now := time.Now()
	events := []eventlog.Event{
		{Ts: now, Op: eventlog.OpReview, SidecarID: "sb-1", Level: "step", Msg: "$ claude -p naming"},
		{Ts: now.Add(time.Second), Op: eventlog.OpReview, SidecarID: "sb-1", Level: "error", Msg: "naming failed: rate limited"},
		{Ts: now.Add(time.Second), Op: eventlog.OpReview, SidecarID: "sb-1", Level: "error", Msg: "naming failed after 1.0s", Final: true, Total: 1},
	}
	m := New(nil, false)
	lines, _ := m.buildCollapsibleLines(newWatchStyles(false), groupByInvocation(events), 0, 20)
	out := strings.Join(lines, "\n")

	assert.Assert(t, !strings.Contains(out, "validate"), "a review run must not be headed as a validate run:\n%s", out)
	assert.Assert(t, strings.Contains(out, "review"), "the header should name the op:\n%s", out)
	assert.Assert(t, strings.Contains(out, "rate limited"), "the failure reason should be visible:\n%s", out)
}

func TestInvocationHeaderNamesItsOp(t *testing.T) {
	st := newWatchStyles(false)
	run := func(op eventlog.Op) string {
		g := invocationGroup{events: []eventlog.Event{
			{Op: op, Level: "done", Final: true, Passed: 1, Total: 1, Ts: time.Now()},
		}}
		return renderInvocationHeader(st, g, true, false, false)
	}
	assert.Assert(t, strings.Contains(run(eventlog.OpValidate), "validate"))
	assert.Assert(t, strings.Contains(run(eventlog.OpReview), "review"))
	assert.Assert(t, !strings.Contains(run(eventlog.OpReview), "validate"))
	assert.Assert(t, strings.Contains(run(eventlog.OpImplement), "implement"))
	assert.Assert(t, !strings.Contains(run(eventlog.OpImplement), "validate"))
}

func TestLastRunResultCountsReviews(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpValidate, SidecarID: "sb-1", Level: "done", Final: true, Passed: 1, Total: 1},
		{Op: eventlog.OpReview, SidecarID: "sb-1", Level: "error", Final: true, Total: 1},
		{Op: eventlog.OpSync, SidecarID: "sb-1", Level: "done", Msg: "synced"},
		{Op: eventlog.OpReview, SidecarID: "sb-2", Level: "done", Final: true, Passed: 1, Total: 1},
		{Op: eventlog.OpValidate, SidecarID: "sb-4", Level: "done", Final: true, Passed: 1, Total: 1},
		{Op: eventlog.OpImplement, SidecarID: "sb-4", Level: "error", Final: true, Total: 1},
	}
	assert.Equal(t, lastRunResult(events, "sb-1"), levelError, "a failed review newer than a passed validate")
	assert.Equal(t, lastRunResult(events, "sb-2"), levelDone)
	assert.Equal(t, lastRunResult(events, "sb-3"), "")
	assert.Equal(t, lastRunResult(events, "sb-4"), levelError, "a failed implementer turn newer than a passed validate")
}
