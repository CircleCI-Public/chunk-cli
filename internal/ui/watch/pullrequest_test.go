package watch

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
	lines, _ := sessModel().runOutcomeLines(st, run.s)
	for _, l := range lines {
		if line := strings.TrimSpace(stripANSI(l)); strings.HasPrefix(line, "Pull request:") {
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
		"Pull request: pushing the branch to origin...")
	assert.Equal(t, prRow(st, prRun(chunkd.SessionRunning, &chunkd.PullRequest{State: chunkd.PROpening})),
		"Pull request: opening on GitHub...")
	assert.Equal(t, prRow(st, prRun(chunkd.SessionDone, &chunkd.PullRequest{
		State: chunkd.PROpened, Number: 7, URL: "https://github.com/acme/widgets/pull/7",
	})), "Pull request: https://github.com/acme/widgets/pull/7")
	assert.Equal(t, prRow(st, prRun(chunkd.SessionDone, &chunkd.PullRequest{
		State: chunkd.PROpened, Number: 7, URL: "https://github.com/acme/widgets/pull/7", Draft: true,
	})), "Pull request: https://github.com/acme/widgets/pull/7 draft")
	assert.Equal(t, prRow(st, prRun(chunkd.SessionDone, &chunkd.PullRequest{State: chunkd.PRFailed, Message: "403 Forbidden"})),
		"Pull request: could not be opened: 403 Forbidden")
	assert.Equal(t, prRow(st, prRun(chunkd.SessionDone, &chunkd.PullRequest{State: chunkd.PRSkipped, Message: "no GitHub token"})),
		"Pull request: none opened: no GitHub token")
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

const prURL = "https://github.com/acme/widgets/pull/7"

// openPRModel is a dashboard on an ended run with its pull request open, the
// cursor on its last row, and an opener that records what it was asked to open.
func openPRModel(t *testing.T, pr *chunkd.PullRequest, openErr error) (Model, *[]string) {
	t.Helper()
	var opened []string
	m := sessModel(prRun(chunkd.SessionDone, pr)).WithOpener(func(url string) error {
		opened = append(opened, url)
		return openErr
	})
	m.selRun = "abc"
	m, _ = press(m, tea.KeyRight)
	for range m.runItems(m.selectedRun().s) {
		m, _ = press(m, tea.KeyDown)
	}
	return m, &opened
}

func openedPR() *chunkd.PullRequest {
	return &chunkd.PullRequest{State: chunkd.PROpened, Number: 7, URL: prURL}
}

// msgs runs cmd and what it batches, leaving out anything slower than a
// moment, such as the timer that clears a note.
func msgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	out := make(chan tea.Msg, 8)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		go func() {
			if msg := c(); msg != nil {
				if batch, ok := msg.(tea.BatchMsg); ok {
					for _, b := range batch {
						run(b)
					}
					return
				}
				out <- msg
			}
		}()
	}
	run(cmd)
	var got []tea.Msg
	for {
		select {
		case msg := <-out:
			got = append(got, msg)
		case <-time.After(200 * time.Millisecond):
			return got
		}
	}
}

// deliverMsgs feeds each message back to the dashboard, as the runtime would.
func deliverMsgs(m Model, in []tea.Msg) Model {
	for _, msg := range in {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func TestOutcomeRowsReadBranchAndPullRequest(t *testing.T) {
	m, _ := openPRModel(t, openedPR(), nil)
	out := stripANSI(m.render())
	assert.Assert(t, strings.Contains(out, "Branch: chunk/factory/abc"), out)
	assert.Assert(t, strings.Contains(out, "Pull request: "+prURL), out)
	assert.Assert(t, !strings.Contains(out, "pr  ") && !strings.Contains(out, "branch  "), out)
}

func TestPullRequestIsAHyperlink(t *testing.T) {
	m, _ := openPRModel(t, openedPR(), nil)
	assert.Assert(t, strings.Contains(m.render(), "\x1b]8;;"+prURL), "the URL is wrapped in an OSC 8 link")
}

func TestEnterAndOOpenThePullRequest(t *testing.T) {
	for _, code := range []rune{tea.KeyEnter, 'o'} {
		m, opened := openPRModel(t, openedPR(), nil)
		m, cmd := press(m, code)
		m = deliverMsgs(m, msgs(cmd))
		assert.DeepEqual(t, *opened, []string{prURL})
		assert.Assert(t, strings.Contains(stripANSI(m.render()), "opened in your browser"))
	}
}

func TestOOpensThePullRequestFromAnyRow(t *testing.T) {
	m, opened := openPRModel(t, openedPR(), nil)
	m, _ = press(m, tea.KeyUp, tea.KeyUp)
	_, cmd := press(m, 'o')
	deliverMsgs(m, msgs(cmd))
	assert.DeepEqual(t, *opened, []string{prURL})
}

func TestCmdEnterOpensThePullRequest(t *testing.T) {
	m, opened := openPRModel(t, openedPR(), nil)
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModSuper})
	deliverMsgs(next.(Model), msgs(cmd))
	assert.DeepEqual(t, *opened, []string{prURL})
}

func TestNoBrowserFallsBackToCopyingTheURL(t *testing.T) {
	m, _ := openPRModel(t, openedPR(), errors.New("no browser"))
	m, cmd := press(m, tea.KeyEnter)
	got := msgs(cmd)
	assert.Assert(t, strings.Contains(fmt.Sprint(got), prURL), "the URL is put on the clipboard: %v", got)
	m = deliverMsgs(m, got)
	assert.Assert(t, strings.Contains(stripANSI(m.render()), "could not open a browser, URL copied"))
}

func TestCopyTakesWhatHasTheCursor(t *testing.T) {
	m, _ := openPRModel(t, openedPR(), nil)
	m1, cmd := press(m, 'c')
	assert.Assert(t, strings.Contains(fmt.Sprint(msgs(cmd)), prURL))
	assert.Assert(t, strings.Contains(stripANSI(m1.render()), "copied"))

	m2, _ := press(m, tea.KeyUp)
	m2, cmd = press(m2, 'c')
	assert.Assert(t, strings.Contains(fmt.Sprint(msgs(cmd)), "chunk/factory/abc"))
	assert.Assert(t, strings.Contains(stripANSI(m2.render()), "copied"))
}

func TestBAndPCopyTheBranchAndPullRequestFromAnyRow(t *testing.T) {
	m, _ := openPRModel(t, openedPR(), nil)
	for range m.runItems(m.selectedRun().s) {
		m, _ = press(m, tea.KeyUp)
	}
	m1, cmd := press(m, 'b')
	assert.Assert(t, strings.Contains(fmt.Sprint(msgs(cmd)), "chunk/factory/abc"))
	assert.Assert(t, strings.Contains(stripANSI(m1.render()), "copied"))

	m2, cmd := press(m, 'p')
	assert.Assert(t, strings.Contains(fmt.Sprint(msgs(cmd)), prURL))
	assert.Assert(t, strings.Contains(stripANSI(m2.render()), "copied"))
}

func TestPCopiesNothingWithoutAnOpenPullRequest(t *testing.T) {
	m, _ := openPRModel(t, nil, nil)
	_, cmd := press(m, 'p')
	assert.Equal(t, len(msgs(cmd)), 0)
}

func TestNoteClearsWhenItsTimerFires(t *testing.T) {
	m, _ := openPRModel(t, openedPR(), nil)
	m, _ = press(m, 'c')
	next, _ := m.Update(rowNoteExpiredMsg{seq: m.rowNote.seq})
	assert.Assert(t, !strings.Contains(stripANSI(next.(Model).render()), "copied"))
}

func TestBranchRowIsSelectableButEnterDoesNothing(t *testing.T) {
	m, opened := openPRModel(t, openedPR(), nil)
	m, _ = press(m, tea.KeyUp)
	assert.Equal(t, m.selectedRunItem().kind, itemBranch)
	_, cmd := press(m, tea.KeyEnter)
	assert.Equal(t, len(msgs(cmd)), 0)
	assert.Equal(t, len(*opened), 0)
}

func TestFooterHintsFollowTheCursor(t *testing.T) {
	m, _ := openPRModel(t, openedPR(), nil)
	foot := func(m Model) string { return stripANSI(m.renderFooter(m.styles())) }
	assert.Assert(t, strings.Contains(foot(m), "Enter/o open"), foot(m))
	assert.Assert(t, strings.Contains(foot(m), "c copy URL"), foot(m))
	assert.Assert(t, !strings.Contains(foot(m), "⌘"), "a terminal that cannot send Cmd+Enter does not advertise it")

	next, _ := m.Update(tea.KeyboardEnhancementsMsg{Flags: 1})
	assert.Assert(t, strings.Contains(foot(next.(Model)), "Enter/⌘Enter/o open"), foot(next.(Model)))

	m, _ = press(m, tea.KeyUp)
	assert.Assert(t, !strings.Contains(foot(m), "open"), "the branch row has nothing to open: %s", foot(m))
	assert.Assert(t, strings.Contains(foot(m), "c copy branch"), foot(m))
}

func TestPullRequestRowsWithoutALinkAreNotSelectable(t *testing.T) {
	for _, pr := range []*chunkd.PullRequest{
		{State: chunkd.PRPending},
		{State: chunkd.PRPushing},
		{State: chunkd.PRFailed, Message: "403"},
		{State: chunkd.PRSkipped, Message: "no token"},
		nil,
	} {
		m, opened := openPRModel(t, pr, nil)
		assert.Equal(t, m.selectedRunItem().kind, itemBranch, "the last row is the branch")
		_, cmd := press(m, 'o')
		assert.Equal(t, len(msgs(cmd)), 0)
		assert.Equal(t, len(*opened), 0)
		assert.Assert(t, !strings.Contains(stripANSI(m.renderFooter(m.styles())), "open"))
	}
}

func TestDraftPullRequestOpensToo(t *testing.T) {
	pr := openedPR()
	pr.Draft = true
	m, opened := openPRModel(t, pr, nil)
	_, cmd := press(m, tea.KeyEnter)
	deliverMsgs(m, msgs(cmd))
	assert.DeepEqual(t, *opened, []string{prURL})
}

// Once the daemon has looked up where an ended run's pull request stands, the
// run list's chip says so instead of that the run passed.
func TestRunChipFollowsThePullRequest(t *testing.T) {
	st := newWatchStyles(false)
	opened := func(status string) sessionInfo {
		return prRun(chunkd.SessionDone, &chunkd.PullRequest{
			State: chunkd.PROpened, Number: 7, URL: "https://github.com/acme/widgets/pull/7", Status: status,
		})
	}
	chip := func(run sessionInfo) string { return strings.TrimSpace(stripANSI(runChip(st, run.s))) }

	for status, want := range map[string]string{
		"":                           "passed", // not looked up yet
		chunkd.PRStatusUp:            "PR up",
		chunkd.PRStatusInReview:      "in review",
		chunkd.PRStatusFeedback:      "feedback",
		chunkd.PRStatusChecksFailing: "checks failing",
		chunkd.PRStatusMerged:        "merged",
		chunkd.PRStatusClosed:        "closed",
	} {
		assert.Equal(t, chip(opened(status)), want, "status %q", status)
	}

	// The run list can be searched by it.
	assert.Assert(t, strings.Contains(runStatus(opened(chunkd.PRStatusFeedback).s), "feedback"))

	// A pull request that never opened has no status to show, whatever it holds.
	run := prRun(chunkd.SessionDone, &chunkd.PullRequest{State: chunkd.PRFailed, Status: chunkd.PRStatusMerged})
	assert.Equal(t, chip(run), "passed")
	// A run that did not pass is not rescued by its pull request.
	run = opened(chunkd.PRStatusMerged)
	run.s.Stages = []chunkd.Stage{{ID: chunkd.StageFactoryLoop, State: chunkd.StageFailed}}
	assert.Assert(t, chip(run) != "merged", "the loop failed, so the pull request does not name the run")
}

func TestRunPaneSaysWhereThePullRequestStands(t *testing.T) {
	st := newWatchStyles(false)
	say := func(status string) string {
		return stripANSI(nextStepLine(st, prRun(chunkd.SessionDone, &chunkd.PullRequest{
			State: chunkd.PROpened, Number: 7, URL: "https://github.com/acme/widgets/pull/7", Status: status,
		}).s))
	}
	assert.Equal(t, say(chunkd.PRStatusMerged), ui.IconOK+" The pull request was merged.")
	assert.Equal(t, say(chunkd.PRStatusClosed), ui.IconWarn+" The pull request was closed without being merged.")
	assert.Equal(t, say(chunkd.PRStatusFeedback), ui.IconWarn+" Reviewers left feedback on the pull request. It is waiting on you.")
	assert.Equal(t, say(chunkd.PRStatusChecksFailing), ui.IconFail+" The pull request's checks on GitHub are failing.")
	assert.Equal(t, say(chunkd.PRStatusInReview), ui.IconOK+" All checks passed. The pull request is in review.")

	assert.Equal(t, prRow(st, prRun(chunkd.SessionDone, &chunkd.PullRequest{
		State: chunkd.PROpened, Number: 7, URL: "https://github.com/acme/widgets/pull/7", Status: chunkd.PRStatusMerged,
	})), "pr https://github.com/acme/widgets/pull/7 · merged")
}
