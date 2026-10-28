package watch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// brokenRun is a run that failed part way, such as on a reviewer sidecar that
// could not be made, after its implementer's work was committed. It continues
// a chain that began with run first-run, which its branch is named for.
func brokenRun(id string) sessionInfo {
	r := endedRun(id)
	r.s.State = chunkd.SessionFailed
	r.s.ProjectRoot = "/repo"
	r.s.Factory.Branch = "chunk/factory/add-verbose/first-run"
	r.s.Factory.Committed = true
	r.s.Factory.Stat = "1 file changed, 2 insertions(+)"
	return r
}

type continued struct{ root, runID string }

// continueModel is a dashboard with run selected whose continues are recorded
// in calls, answered with err.
func continueModel(run sessionInfo, err error) (Model, *[]continued) {
	var calls []continued
	m := sessModel(run).WithContinue(func(root, runID string) (string, error) {
		calls = append(calls, continued{root, runID})
		if err != nil {
			return "", err
		}
		return "new-run", nil
	})
	m.selRun = run.s.ID
	return m, &calls
}

func TestRContinuesAFailedRunWithCommittedWork(t *testing.T) {
	m, calls := continueModel(brokenRun("abc123"), nil)
	m, cmd := press(m, 'r')
	assert.Assert(t, cmd != nil)
	assert.Equal(t, m.runLine(), "starting a run to continue this one…")

	next, _ := m.Update(cmd())
	m = next.(Model)
	assert.Equal(t, len(*calls), 1)
	assert.Equal(t, (*calls)[0], continued{root: "/repo", runID: "first-run"})
	assert.Equal(t, m.pendingRun, "new-run")
	assert.Equal(t, m.runLine(), "continuing in a new run; it appears in the list in a moment")
}

// A second r before the factory answers would only be refused, as the
// project already has a run going, and say so over the one that started.
func TestRIsOffWhileAContinueIsStarting(t *testing.T) {
	m, calls := continueModel(brokenRun("abc123"), nil)
	m, cmd := press(m, 'r')
	assert.Assert(t, cmd != nil)
	m, again := press(m, 'r')
	assert.Assert(t, again == nil)

	next, _ := m.Update(cmd())
	m = next.(Model)
	assert.Equal(t, len(*calls), 1)
	assert.Assert(t, !m.continuing)
}

func TestContinueSaysWhyItCouldNotStart(t *testing.T) {
	m, _ := continueModel(brokenRun("abc123"), errors.New("the chunk daemon is not reachable"))
	m, cmd := press(m, 'r')
	next, _ := m.Update(cmd())
	m = next.(Model)
	assert.Equal(t, m.pendingRun, "")
	assert.Equal(t, m.runLine(), "could not continue the run: the chunk daemon is not reachable")
}

func TestRDoesNothingForARunWithNothingToContinue(t *testing.T) {
	passed := brokenRun("passed")
	passed.s.State = chunkd.SessionDone
	passed.s.Factory.Result = chunkd.ResultPassed

	uncommitted := brokenRun("uncommitted")
	uncommitted.s.Factory.Committed = false

	running := factoryRun("running", chunkd.SessionRunning, time.Now())
	running.s.Factory.Branch = "chunk/factory/add-verbose/running"

	for name, run := range map[string]sessionInfo{"passed": passed, "uncommitted": uncommitted, "running": running} {
		t.Run(name, func(t *testing.T) {
			m, calls := continueModel(run, nil)
			_, cmd := press(m, 'r')
			assert.Assert(t, cmd == nil)
			assert.Equal(t, len(*calls), 0)
		})
	}
}

func TestContinueIsOfferedOnlyWhereItCanRun(t *testing.T) {
	m, _ := continueModel(brokenRun("abc123"), nil)
	// Wide enough that the hint fits on one line of the run pane.
	m.width, m.focusedPane = 114, paneRight
	out := m.View().Content
	assert.Assert(t, strings.Contains(out, "press r to continue it, or chunk factory build --continue first-run"), out)
	assert.Assert(t, strings.Contains(out, "continue run"), out)

	// A dashboard with no way to start runs, such as one on a remote daemon,
	// shows the command alone.
	bare := sessModel(brokenRun("abc123"))
	bare.width, bare.selRun, bare.focusedPane = 114, "abc123", paneRight
	out = bare.View().Content
	assert.Assert(t, strings.Contains(out, "chunk factory build --continue first-run"), out)
	assert.Assert(t, !strings.Contains(out, "press r"), out)
	_, cmd := press(bare, 'r')
	assert.Assert(t, cmd == nil)
}
