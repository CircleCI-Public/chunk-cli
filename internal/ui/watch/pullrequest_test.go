package watch

import (
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// prRun is a factory run whose pull request has got as far as pr.
func prRun(state chunkd.SessionState, pr *chunkd.PullRequest) sessionInfo {
	started := time.Now().Add(-time.Minute)
	r := factoryRun("abc", state, started)
	r.s.Rounds = []chunkd.Round{liveRound(1, "passed")}
	r.s.Factory.Committed, r.s.Factory.Stat = true, "1 file changed"
	r.s.Factory.Branch, r.s.Factory.Result = "chunk/factory/abc", chunkd.ResultPassed
	r.s.Factory.PullRequest = pr
	if state.Finished() {
		ended := started.Add(time.Minute)
		r.s.EndedAt = &ended
	}
	return r
}

// prRow is the pr row of the run pane's outcome block.
func prRow(st watchStyles, run sessionInfo) string {
	for _, l := range sessModel().runOutcomeLines(st, run.s) {
		if line := strings.TrimSpace(stripANSI(l)); strings.HasPrefix(line, "pr ") {
			return strings.Join(strings.Fields(line), " ")
		}
	}
	return ""
}

// The run pane carries the pull request every stage of the way, so a person
// watching sees it being opened and ends up with its address.
func TestRunPaneShowsThePullRequest(t *testing.T) {
	st := newWatchStyles(false)

	assert.Equal(t, prRow(st, prRun(chunkd.SessionRunning, &chunkd.PullRequest{State: chunkd.PRPending})), "",
		"a run with its rounds still to go has nothing to say about a pull request yet")
	assert.Equal(t, prRow(st, prRun(chunkd.SessionRunning, &chunkd.PullRequest{State: chunkd.PRPushing})),
		"pr pushing the branch to origin...")
	assert.Equal(t, prRow(st, prRun(chunkd.SessionRunning, &chunkd.PullRequest{State: chunkd.PROpening})),
		"pr opening on GitHub...")
	assert.Equal(t, prRow(st, prRun(chunkd.SessionDone, &chunkd.PullRequest{
		State: chunkd.PROpened, Number: 7, URL: "https://github.com/acme/widgets/pull/7",
	})), "pr https://github.com/acme/widgets/pull/7")
	assert.Equal(t, prRow(st, prRun(chunkd.SessionDone, &chunkd.PullRequest{
		State: chunkd.PROpened, Number: 7, URL: "https://github.com/acme/widgets/pull/7", Draft: true,
	})), "pr https://github.com/acme/widgets/pull/7 draft")
	assert.Equal(t, prRow(st, prRun(chunkd.SessionDone, &chunkd.PullRequest{State: chunkd.PRFailed, Message: "403 Forbidden"})),
		"pr could not be opened: 403 Forbidden")
	assert.Equal(t, prRow(st, prRun(chunkd.SessionDone, &chunkd.PullRequest{State: chunkd.PRSkipped, Message: "no GitHub token"})),
		"pr none opened: no GitHub token")
	// A run told to open none says nothing about one either way.
	assert.Equal(t, prRow(st, prRun(chunkd.SessionDone, nil)), "")
}

// What to do next is the pull request once there is one, not the branch.
func TestNextStepLineLeadsWithThePullRequest(t *testing.T) {
	st := newWatchStyles(false)
	say := func(run sessionInfo) string { return stripANSI(nextStepLine(st, run.s)) }

	assert.Equal(t, say(prRun(chunkd.SessionRunning, &chunkd.PullRequest{State: chunkd.PRPushing})),
		"Now: the checks passed and the pull request is being opened. Nothing to do yet.")
	assert.Equal(t, say(prRun(chunkd.SessionDone, &chunkd.PullRequest{
		State: chunkd.PROpened, Number: 7, URL: "https://github.com/acme/widgets/pull/7",
	})), ui.IconOK+" All checks passed. The pull request is open and ready for review.")
	assert.Equal(t, say(prRun(chunkd.SessionDone, &chunkd.PullRequest{State: chunkd.PRFailed, Message: "403"})),
		ui.IconFail+" All checks passed, but the pull request could not be opened. The work is on the branch.")
	// With no pull request to point at, the change itself is what to look at.
	assert.Equal(t, say(prRun(chunkd.SessionDone, &chunkd.PullRequest{State: chunkd.PRSkipped, Message: "no GitHub token"})),
		ui.IconOK+" All checks passed. The change is ready for you to look at.")
}

// The run list names the pull request an ended run opened, so it can be found
// without opening the run.
func TestRunListNamesAnOpenPullRequest(t *testing.T) {
	run := prRun(chunkd.SessionDone, &chunkd.PullRequest{State: chunkd.PROpened, Number: 7, URL: "https://github.com/acme/widgets/pull/7"})

	assert.Assert(t, strings.HasPrefix(runDetail(run.s), "PR #7 · "), runDetail(run.s))

	run.s.Factory.PullRequest = &chunkd.PullRequest{State: chunkd.PRSkipped, Message: "no GitHub token"}
	assert.Assert(t, !strings.Contains(runDetail(run.s), "PR #"), runDetail(run.s))
}
