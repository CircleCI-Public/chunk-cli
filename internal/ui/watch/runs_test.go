package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/chunkd/server"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func liveSession(id string, state chunkd.SessionState, started time.Time) sessionInfo {
	return sessionInfo{label: "repo", s: chunkd.Session{
		ID: id, State: state, StartedAt: started, Branch: "feature", HeadSHA: "0123456789abcdef",
		Stages: []chunkd.Stage{
			{ID: chunkd.StageFactoryLoop, State: chunkd.StageRunning},
		},
	}}
}

func sessModel(sessions ...sessionInfo) Model {
	m := New(nil, true).WithConnection(chunkd.Connection{})
	m.width, m.height = 110, 40
	m.sessions, m.allRuns = sessions, sessions
	return m
}

func keyPress(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func press(m Model, codes ...rune) (Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, c := range codes {
		var next tea.Model
		next, cmd = m.Update(keyPress(c))
		m = next.(Model)
	}
	return m, cmd
}

func TestCollectSessionsPutsLiveOnesFirstThenNewest(t *testing.T) {
	now := time.Now()
	ended := now
	done := chunkd.Session{ID: "done-new", State: chunkd.SessionDone, StartedAt: now, EndedAt: &ended}
	old := chunkd.Session{ID: "done-old", State: chunkd.SessionDone, StartedAt: now.Add(-time.Hour), EndedAt: &ended}
	live := chunkd.Session{ID: "live", State: chunkd.SessionRunning, StartedAt: now.Add(-2 * time.Hour)}

	got := collectSessions([]chunkd.ProjectSnapshot{
		{Root: "/a", RepoName: "a", Sessions: []chunkd.Session{old, done}},
		{Root: "/b", Sessions: []chunkd.Session{live}},
	})

	assert.Equal(t, got[0].s.ID, "live", "a live session comes first, however old")
	assert.Equal(t, got[1].s.ID, "done-new")
	assert.Equal(t, got[2].s.ID, "done-old")
	assert.Equal(t, got[0].label, "b", "a project with no repo name falls back to its directory")
}

func factoryRun(id string, state chunkd.SessionState, started time.Time) sessionInfo {
	r := liveSession(id, state, started)
	r.s.Branch = "chunk/factory/" + id
	r.s.Factory = &chunkd.FactoryRun{Prompt: "# Add a --verbose flag\n\nto chunk validate", Attempts: 3, RunID: id, Worktree: "/tmp/wt/" + id}
	return r
}

func TestVisibleRunsKeepsLiveOnesAndCapsRecentEndedOnes(t *testing.T) {
	now := time.Now()
	var all []sessionInfo
	all = append(all, liveSession("live", chunkd.SessionRunning, now))
	for i := range maxRuns + 2 {
		ended := now.Add(-time.Duration(i) * time.Minute)
		s := liveSession(fmt.Sprintf("done-%d", i), chunkd.SessionDone, ended)
		s.s.EndedAt = &ended
		all = append(all, s)
	}
	old := now.Add(-2 * staleAfter)
	stale := liveSession("stale", chunkd.SessionDone, old)
	stale.s.EndedAt = &old

	got := visibleRuns(append(all[:2:2], stale), now)
	assert.Equal(t, len(got), 2, "a run that ended long ago is not listed")

	got = visibleRuns(all, now)
	assert.Equal(t, len(got), maxRuns)
	assert.Equal(t, got[0].s.ID, "live")
}

func TestSplitRunSidecarsAttachesAFactoryRunsPoolToTheRun(t *testing.T) {
	run := factoryRun("abc", chunkd.SessionRunning, time.Now())
	run.s.Factory.SidecarIDs = []string{"1", "2", "3"}
	sidecars := []sidecarInfo{
		{id: "1", name: "anything"},
		{id: "2", name: "factory-abc-rebuilt-123"},
		{id: "3", name: "factory-abc"},
		{id: "4", name: "factory-other-1"},
		{id: "5", name: "my-sidecar"},
	}
	rest, byRun := splitRunSidecars(sidecars, []sessionInfo{run})
	assert.Equal(t, len(byRun["abc"]), 3)
	assert.Equal(t, len(rest), 2, "a pool of a run that is not listed keeps its rows: it may still be running")
}

func TestSplitRunSidecarsDoesNotMixRunsWithTheSameFactoryID(t *testing.T) {
	a := factoryRun("same", chunkd.SessionRunning, time.Now())
	b := factoryRun("same", chunkd.SessionRunning, time.Now())
	a.s.ID, a.s.Factory.SidecarIDs = "session-a", []string{"sidecar-a"}
	b.s.ID, b.s.Factory.SidecarIDs = "session-b", []string{"sidecar-b"}

	rest, byRun := splitRunSidecars([]sidecarInfo{{id: "sidecar-a"}, {id: "sidecar-b"}}, []sessionInfo{a, b})
	assert.Equal(t, len(rest), 0)
	assert.Equal(t, byRun["session-a"][0].id, "sidecar-a")
	assert.Equal(t, byRun["session-b"][0].id, "sidecar-b")
}

// The factory run's pane says where the run stands, shows the round that
// matters in full and the finished ones in a line, keeps the infrastructure
// folded until asked for, and fits the screen however short it is.
func TestFactoryRunPaneShowsImplementerChecksAndReviews(t *testing.T) {
	r := factoryRun("abc", chunkd.SessionRunning, time.Now().Add(-time.Minute))
	ended := time.Now()
	r.s.Rounds = []chunkd.Round{{
		Number: 1, State: chunkd.RoundDone, Note: "1 of 3 checks passed", Findings: 2, Worth: 1,
		StartedAt: ended.Add(-time.Minute), EndedAt: &ended,
		Implement: &chunkd.RoundImplement{State: chunkd.ImplementApplied, DurationMS: 4000, CostUSD: 0.41, Stat: "3 files changed"},
		Checks: []chunkd.RoundCheck{
			{Name: "test", Status: "passed", DurationMS: 1000},
			{Name: "lint", Status: "failed", DurationMS: 500, Output: "app.go:3: unused variable"},
		},
		Reviews: []chunkd.ReviewPrompt{{Name: "bugs", State: chunkd.PromptDone, Findings: 2, CommandID: "cmd-r"}},
	}, {
		Number: 2, State: chunkd.RoundImplementing,
		Implement: &chunkd.RoundImplement{State: chunkd.ImplementRunning},
	}}
	r.s.Factory.Progress = chunkd.Feed{Lines: []chunkd.FeedLine{{Level: chunkd.FeedStep, Text: "syncing the implementer"}}, Total: 1}

	for _, height := range []int{12, 24, 50} {
		m := sessModel(r)
		m.selRun = "abc"
		m.height = height
		out := m.render()
		assert.Assert(t, strings.Count(out, "\n") <= height, "height %d: %d lines", height, strings.Count(out, "\n"))
		if height < 50 {
			continue
		}
		for _, want := range []string{
			"Add a --verbose flag", "Round 2 (max 3) · implementing", "$0.41",
			"Round 1", "1 of 3 checks passed", "2 issues, 1 worth changing", "Round 2", "implement", "▸ infra",
		} {
			assert.Assert(t, strings.Contains(out, want), "missing %q:\n%s", want, out)
		}
		// The finished round is a line; infrastructure is folded.
		for _, absent := range []string{"review: bugs", "lint", "/tmp/wt/abc", "syncing the implementer", "not built yet"} {
			assert.Assert(t, !strings.Contains(out, absent), "unexpected %q:\n%s", absent, out)
		}

		m, _ = press(m, 'i')
		out = m.render()
		for _, want := range []string{"▾ infra", "/tmp/wt/abc", "syncing the implementer"} {
			assert.Assert(t, strings.Contains(out, want), "missing %q:\n%s", want, out)
		}
	}
}

// While the implementer works, its row says what it last did and how much it
// has done, with what it last said under it.
func TestRunningImplementerRowShowsItsProgress(t *testing.T) {
	r := factoryRun("abc", chunkd.SessionRunning, time.Now().Add(-time.Minute))
	r.s.Rounds = []chunkd.Round{{
		Number: 1, State: chunkd.RoundImplementing, StartedAt: time.Now().Add(-2 * time.Minute),
		Implement: &chunkd.RoundImplement{State: chunkd.ImplementRunning, Progress: chunkd.ImplementProgress{
			Doing: "Run the tests", Said: "The flag is in; now its test.", Actions: 3, At: time.Now(),
		}},
	}}
	m := sessModel(r)
	m.selRun = "abc"
	m.width, m.height = 160, 40

	out := stripANSI(m.render())
	for _, want := range []string{"implement", "Run the tests · 3 actions", "The flag is in; now its test."} {
		assert.Assert(t, strings.Contains(out, want), "missing %q:\n%s", want, out)
	}
	assert.Assert(t, !strings.Contains(out, "quiet"), out)
}

// A finished round opens and closes on Space, from its header or from a row
// inside it, and the selection stays on the header.
func TestRoundsOpenAndCloseAndKeepTheSelectionOnTheHeader(t *testing.T) {
	r := failedRun()
	m := sessModel(r)
	m.selRun = "abc"
	m, _ = press(m, tea.KeyRight)
	run := m.selectedRun()
	// The ended run opens its failing round, 2, and no other.
	assert.Assert(t, !m.roundOpen(run.s, 0) && m.roundOpen(run.s, 1) && !m.roundOpen(run.s, 2))
	assert.Equal(t, len(m.runItems(run.s)), 3+3+1, "three headers, round 2's implementer and two checks, and the branch")

	m, _ = press(m, ' ')
	assert.Assert(t, m.roundOpen(m.selectedRun().s, 0), "space on the first header opens it")
	out := stripANSI(m.render())
	assert.Assert(t, strings.Contains(out, "▾ Round 1") && strings.Contains(out, "unit tests"), out)

	// From a row inside round 2: space closes the round and selects its header.
	m, _ = press(m, tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyDown) // header 1 → implement, lint(round 1), test, header 2... walk to a row of round 2
	items := m.runItems(m.selectedRun().s)
	for items[m.runItem].round != 1 || items[m.runItem].kind != itemCheck {
		m, _ = press(m, tea.KeyDown)
	}
	m, _ = press(m, ' ')
	items = m.runItems(m.selectedRun().s)
	assert.Assert(t, !m.roundOpen(m.selectedRun().s, 1))
	assert.Equal(t, items[m.runItem].kind, itemRound)
	assert.Equal(t, items[m.runItem].round, 1)

	// Enter on a header toggles too.
	m, _ = press(m, tea.KeyEnter)
	assert.Assert(t, m.roundOpen(m.selectedRun().s, 1))
}

func failedRun() sessionInfo {
	r := factoryRun("abc", chunkd.SessionDone, time.Now().Add(-time.Hour))
	ended := time.Now()
	r.s.EndedAt = &ended
	r.s.Stages[0].State, r.s.Stages[0].Note = chunkd.StageFailed, "checks still failed after 3 round(s)"
	r.s.Factory.Result, r.s.Factory.Committed = "exhausted", true
	r.s.Factory.Branch, r.s.Factory.Log = "chunk/factory/abc", "/tmp/run.log"
	round := func(n int, lint string) chunkd.Round {
		return chunkd.Round{
			Number: n, State: chunkd.RoundDone,
			Implement: &chunkd.RoundImplement{State: chunkd.ImplementApplied, CostUSD: 0.5},
			Checks:    []chunkd.RoundCheck{{Name: "lint", Status: lint}, {Name: "unit tests", Status: "passed"}},
		}
	}
	r.s.Rounds = []chunkd.Round{round(1, "passed"), round(2, "failed"), round(3, "passed")}
	return r
}

// An ended run that did not pass says what stopped it, opens the round that
// failed rather than the last, and says how to carry on.
func TestEndedFailedRunNamesTheBlockerAndTheNextStep(t *testing.T) {
	m := sessModel(failedRun())
	m.selRun = "abc"
	out := stripANSI(m.render())
	for _, want := range []string{
		"checks still failed after 3 round(s) · blocked by lint", "$1.50",
		"▾ Round 2", "▸ Round 1", "▸ Round 3",
		"Branch: chunk/factory/abc", "next", "chunk factory build --continue abc",
	} {
		assert.Assert(t, strings.Contains(out, want), "missing %q:\n%s", want, out)
	}
	assert.Assert(t, !strings.Contains(out, "/tmp/run.log"), "the log is infrastructure:\n%s", out)
}

// The title, status and outcome hold their place on a short terminal while the
// rounds between them give way.
func TestShortTerminalKeepsStatusAndOutcomeInView(t *testing.T) {
	m := sessModel(failedRun())
	m.selRun = "abc"
	m.height = 16
	out := m.render()
	assert.Assert(t, strings.Count(out, "\n") <= 16, out)
	for _, want := range []string{"blocked by lint", "chunk factory build --continue abc"} {
		assert.Assert(t, strings.Contains(out, want), "missing %q:\n%s", want, out)
	}
}

// A long prompt wraps onto a few lines instead of being cut to one, and no line
// of the pane runs past the terminal.
func TestLongPromptWrapsAndPaneStaysWithinTheTerminal(t *testing.T) {
	r := failedRun()
	r.s.Factory.Prompt = strings.Repeat("refactor the helper so that ", 12)
	for _, width := range []int{70, 110, 160} {
		m := sessModel(r)
		m.selRun = "abc"
		m.width = width
		out := m.render()
		assert.Assert(t, strings.Count(out, "refactor the helper") > 1, "the prompt is wrapped over lines:\n%s", out)
		assert.Assert(t, strings.Contains(out, "…"), "a prompt past three lines is cut with an ellipsis:\n%s", out)
		for i, line := range strings.Split(out, "\n") {
			assert.Assert(t, lipgloss.Width(line) <= width, "width %d line %d is %d columns", width, i, lipgloss.Width(line))
		}
	}
}

func TestEndedFactoryRunSaysWhereItsWorkIs(t *testing.T) {
	r := factoryRun("abc", chunkd.SessionDone, time.Now().Add(-time.Hour))
	ended := time.Now()
	r.s.EndedAt = &ended
	r.s.Stages[0].State, r.s.Stages[0].Note = chunkd.StageFailed, "checks still failed after 3 round(s)"
	r.s.Factory.Result, r.s.Factory.Committed = "exhausted", true
	r.s.Factory.Branch, r.s.Factory.Log = "chunk/factory/abc", "/tmp/run.log"
	r.s.Factory.Stat = "2 files changed, 10 insertions(+)"

	m := sessModel(r)
	m.selRun = "abc"
	out := m.render()
	for _, want := range []string{"still failing", "checks still failed after 3 round(s)", "chunk/factory/abc", "committed · 2 files changed, 10 insertions(+)", "chunk factory build --continue abc"} {
		assert.Assert(t, strings.Contains(out, want), "missing %q:\n%s", want, out)
	}
	m, _ = press(m, 'i')
	assert.Assert(t, strings.Contains(m.render(), "/tmp/run.log"), "the log is under infra:\n%s", m.render())
}

func TestNoRunsLeavesTheSidecarPaneAsItWas(t *testing.T) {
	m := sessModel()
	m.selRun = ""
	m.sidecars = []sidecarInfo{{id: "sc-1", name: "box", repoName: "repo", branch: "main", verified: true}}
	out := strings.Join(m.renderLeftPane(m.styles(), 30), "\n")
	assert.Assert(t, !strings.Contains(out, "runs"), out)
	assert.Assert(t, strings.HasPrefix(out, m.styles().emphasis("sidecars")), out)
}

func TestRunsWithoutLooseSidecarsDrawNoSidecarSection(t *testing.T) {
	m := sessModel(factoryRun("a", chunkd.SessionRunning, time.Now()))
	m.sidecars = nil
	out := strings.Join(m.renderLeftPane(m.styles(), 30), "\n")
	assert.Assert(t, !strings.Contains(out, "sidecars"), out)
}

func TestSearchFindsARunByItsSidecar(t *testing.T) {
	m := sessModel(factoryRun("a", chunkd.SessionRunning, time.Now()), factoryRun("b", chunkd.SessionDone, time.Now()))
	m.allRuns = m.sessions
	m.runSidecars = map[string][]sidecarInfo{"b": {{id: "sc-9", name: "needle-box"}}}
	m = m.setQuery("needle")
	assert.Equal(t, len(m.sessions), 1)
	assert.Equal(t, m.sessions[0].s.ID, "b")
}

func TestHiddenRunsSidecarsStayOutOfTheSidecarList(t *testing.T) {
	old := factoryRun("old", chunkd.SessionDone, time.Now().Add(-30*24*time.Hour))
	ended := time.Now().Add(-30 * 24 * time.Hour)
	old.s.EndedAt = &ended
	old.s.Factory.SidecarIDs = []string{"sc-run"}
	m := sessModel()
	next, _ := m.Update(dataMsg{
		sessions: []sessionInfo{old},
		sidecars: []sidecarInfo{{id: "sc-run", lastActivity: time.Now(), verified: true}, {id: "sc-loose", lastActivity: time.Now(), verified: true}},
	})
	m = next.(Model)
	assert.Equal(t, len(m.sessions), 0, "the old run is not listed")
	assert.Equal(t, len(m.sidecars), 1)
	assert.Equal(t, m.sidecars[0].id, "sc-loose")
}

func TestSidecarsInARunsWorktreeBelongToTheRun(t *testing.T) {
	run := factoryRun("a", chunkd.SessionRunning, time.Now())
	run.s.Factory.Worktree = "/data/wt/a"
	rest, by := splitRunSidecars([]sidecarInfo{
		{id: "in-wt", projectPath: "/data/wt/a"},
		{id: "elsewhere", projectPath: "/repo"},
	}, []sessionInfo{run})
	assert.Equal(t, len(rest), 1)
	assert.Equal(t, rest[0].id, "elsewhere")
	assert.Equal(t, by["a"][0].id, "in-wt")
}

func TestOneSessionsPoolIsOneRow(t *testing.T) {
	now := time.Now()
	got := collapsePools([]sidecarInfo{
		{id: "a", sessionID: "s1", projectIdx: 0, lastActivity: now.Add(-time.Minute), lastResult: levelError},
		{id: "b", sessionID: "s1", projectIdx: 0, lastActivity: now, lastResult: levelDone, verified: true},
		{id: "c", sessionID: "s2", projectIdx: 0, lastActivity: now},
		{id: "d", sessionID: "s1", projectIdx: 1, lastActivity: now},
		{id: "e", lastActivity: now},
	})
	assert.Equal(t, len(got), 4)
	assert.Equal(t, got[0].id, "a")
	assert.DeepEqual(t, got[0].members, []string{"a", "b"})
	assert.Equal(t, got[0].lastResult, levelDone, "the newest member's result")
	assert.Assert(t, got[0].verified, "the newest member's confirmation")
	assert.Assert(t, got[0].owns("b"))
	assert.Equal(t, len(got[1].members), 0, "a lone sidecar is not a pool")
}

func TestSearchFiltersSidecarsToo(t *testing.T) {
	m := sessModel(factoryRun("a", chunkd.SessionRunning, time.Now()))
	m.looseSidecars = []sidecarInfo{{id: "sc-1", name: "box", repoName: "alpha"}, {id: "sc-2", name: "box2", repoName: "beta"}}
	m = m.setQuery("beta")
	assert.Equal(t, len(m.sidecars), 1)
	assert.Equal(t, m.sidecars[0].id, "sc-2")
	m = m.setQuery("")
	assert.Equal(t, len(m.sidecars), 2)
}

// Runs and sidecars are one list to the selection: down off the last run
// lands on the first sidecar, and up off it goes back.
func TestSelectionMovesFromRunsToSidecarsAndBack(t *testing.T) {
	m := sessModel(factoryRun("a", chunkd.SessionRunning, time.Now()), factoryRun("b", chunkd.SessionDone, time.Now()))
	m.sidecars = []sidecarInfo{{id: "sc-1", name: "box"}, {id: "sc-2", name: "box2"}}
	m = m.reselectRun()
	assert.Equal(t, m.selRun, "a", "the first poll lands on the newest run")

	m, _ = press(m, tea.KeyDown)
	assert.Equal(t, m.selRun, "b")
	m, _ = press(m, tea.KeyDown)
	assert.Equal(t, m.selRun, "")
	assert.Equal(t, m.selectedID, "sc-1")
	m, _ = press(m, tea.KeyDown)
	assert.Equal(t, m.selectedID, "sc-2")
	m, _ = press(m, tea.KeyUp, tea.KeyUp)
	assert.Equal(t, m.selRun, "b")
	m, _ = press(m, tea.KeyUp, tea.KeyUp)
	assert.Equal(t, m.selRun, "a", "up stops at the first run")
}

func TestShortDashboardKeepsItsSelectionVisible(t *testing.T) {
	var runs []sessionInfo
	for i := range maxRuns {
		run := factoryRun(fmt.Sprintf("run-%d", i), chunkd.SessionRunning, time.Now())
		run.s.Factory.Prompt = fmt.Sprintf("run %d", i)
		runs = append(runs, run)
	}
	m := sessModel(runs...)
	m.height = 8
	m.selRun = runs[len(runs)-1].s.ID
	out := m.render()
	assert.Assert(t, strings.Count(out, "\n") <= m.height, out)
	assert.Assert(t, strings.Contains(out, runTitle(runs[len(runs)-1].s)), "selected run is not visible:\n%s", out)

	m.selRun = ""
	m.sidecars = []sidecarInfo{{id: "selected", name: "selected", repoName: "repo", verified: true}}
	m.selectedID = "selected"
	out = m.render()
	assert.Assert(t, strings.Contains(out, "selected"), "selected sidecar is not visible:\n%s", out)
}

func TestRunItemsOpenTheirOutput(t *testing.T) {
	r := factoryRun("abc", chunkd.SessionRunning, time.Now())
	r.s.Rounds = []chunkd.Round{{
		Number: 1, State: chunkd.RoundDone,
		Implement: &chunkd.RoundImplement{State: chunkd.ImplementApplied, Summary: "Added the flag."},
		Checks:    []chunkd.RoundCheck{{Name: "lint", Status: "failed", Output: "app.go:3: unused"}, {Name: "test", Status: "passed"}},
		Reviews:   []chunkd.ReviewPrompt{{Name: "bugs", State: chunkd.PromptDone, CommandID: "cmd-r"}},
	}}
	m := sessModel(r)
	m.selRun = "abc"
	m, _ = press(m, tea.KeyRight, tea.KeyDown) // past the round's header

	// The implementer's turn and a failed check open what the snapshot holds.
	m, cmd := press(m, tea.KeyEnter)
	assert.Assert(t, cmd == nil && m.output != nil)
	assert.Equal(t, m.output.text, "Added the flag.")
	m, _ = press(m, tea.KeyEscape, tea.KeyDown, tea.KeyEnter)
	assert.Assert(t, m.output != nil)
	assert.Equal(t, m.output.text, "app.go:3: unused")

	// A passing check kept nothing, and says so rather than opening empty.
	m, _ = press(m, tea.KeyEscape, tea.KeyDown, tea.KeyEnter)
	assert.Assert(t, m.output == nil)
	assert.Assert(t, strings.Contains(m.runNote, "no output kept"), m.runNote)

	// A finished review whose findings are not recorded yet opens its
	// buffered Claude run.
	m.detailFn = func(string) (chunkd.SessionDetail, error) { return chunkd.SessionDetail{}, nil }
	m, cmd = press(m, tea.KeyDown, tea.KeyEnter)
	assert.Assert(t, cmd != nil && m.output == nil)
	m = update(m, cmd())
	assert.Assert(t, m.output != nil)
	assert.Equal(t, m.output.commandID, "cmd-r")
}

// A finished review's Claude run printed only a JSON result, so its pane shows
// the findings the daemon parsed from it.
func TestFinishedReviewOpensItsFindings(t *testing.T) {
	r := factoryRun("abc", chunkd.SessionDone, time.Now())
	r.s.Rounds = []chunkd.Round{{
		Number: 1, State: chunkd.RoundDone,
		Reviews: []chunkd.ReviewPrompt{{Name: "bugs", State: chunkd.PromptDone, CommandID: "cmd-r"}},
	}}
	m := sessModel(r)
	m.selRun = "abc"
	var asked string
	m.detailFn = func(id string) (chunkd.SessionDetail, error) {
		asked = id
		return chunkd.SessionDetail{Details: []chunkd.RoundDetail{{Number: 1, Results: []chunkd.ReviewResult{{
			Prompt: "bugs", Status: chunkd.CheckFailed,
			Findings: []chunkd.Finding{{File: "app.go", Line: 3, Severity: "high", Body: "nil map write"}},
		}}}}}, nil
	}
	m, _ = press(m, tea.KeyRight, tea.KeyDown)
	assert.Equal(t, *m.selectedRunItem(), runItem{kind: itemReview, round: 0})

	m, cmd := press(m, tea.KeyEnter)
	m = update(m, cmd())
	assert.Equal(t, asked, "abc")
	assert.Assert(t, m.output != nil)
	assert.Equal(t, m.output.text, "1 finding\n\n[high] app.go:3\nnil map write")

	// An answer for a pane since replaced is dropped.
	m, _ = press(m, tea.KeyEscape)
	m, cmd = press(m, tea.KeyEnter)
	msg := cmd()
	m.outputSeq++
	m = update(m, msg)
	assert.Assert(t, m.output == nil)
}

func TestReviewTextSaysWhenThereIsNothing(t *testing.T) {
	assert.Equal(t, reviewText(chunkd.ReviewResult{Status: chunkd.CheckPassed}), "No findings.")
	assert.Equal(t, reviewText(chunkd.ReviewResult{Error: "timed out"}), "Could not run: timed out")
}

// update delivers msg to m.
func update(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func TestRunKeysQuitDetachesCancelNeedsConfirmation(t *testing.T) {
	m := sessModel(
		liveSession("run-1", chunkd.SessionRunning, time.Now()),
		liveSession("run-2", chunkd.SessionRunning, time.Now().Add(-time.Minute)),
	)
	m = m.reselectRun()

	// Cancel: the first x only asks, another key or another run withdraws it,
	// and only a second x on the same run sends the request.
	m, cmd := press(m, 'x')
	assert.Assert(t, cmd == nil)
	assert.Equal(t, m.runConfirm, "run-1")
	assert.Assert(t, strings.Contains(m.render(), "press x again"))
	m, cmd = press(m, tea.KeyDown, 'x')
	assert.Assert(t, cmd == nil)
	assert.Equal(t, m.runConfirm, "run-2")
	_, cmd = press(m, 'x')
	assert.Assert(t, cmd != nil, "the second x on the same run sends the cancel")

	// q leaves the dashboard; Esc in the details only goes back to the list.
	_, cmd = press(m, 'q')
	_, isQuit := cmd().(tea.QuitMsg)
	assert.Assert(t, isQuit)
	m, _ = press(m, tea.KeyRight)
	m, cmd = press(m, tea.KeyEscape)
	assert.Assert(t, m.focusedPane == paneLeft && cmd == nil)
}

func TestCancelResultOnlyUpdatesTheRunItCameFrom(t *testing.T) {
	m := sessModel(
		liveSession("run-1", chunkd.SessionRunning, time.Now()),
		liveSession("run-2", chunkd.SessionRunning, time.Now().Add(-time.Minute)),
	).reselectRun()
	m, _ = press(m, tea.KeyDown)
	assert.Equal(t, m.selRun, "run-2")

	m = m.withSessionAction(sessionActionMsg{id: "run-1", note: "cancel requested"})
	assert.Equal(t, m.runNote, "")
	m = m.withSessionAction(sessionActionMsg{id: "run-2", note: "cancel requested"})
	assert.Equal(t, m.runNote, "cancel requested")
}

// Against a real daemon: the dashboard sees a run started elsewhere, quitting
// the dashboard leaves it running, and only the confirmed cancel key stops it.
func TestQuittingTheDashboardDetachesAndOnlyConfirmedCancelStopsTheSession(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	sockDir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	t.Setenv("CHUNK_DAEMON_DIR", sockDir)

	project := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = project
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@t")
		out, runErr := c.CombinedOutput()
		assert.NilError(t, runErr, string(out))
	}
	run("init", "-b", "main")
	prompts := filepath.Join(project, ".chunk", "reviews")
	assert.NilError(t, os.MkdirAll(prompts, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(prompts, "bugs.md"), []byte("find bugs"), 0o644))
	projectCfg := `{"orgID":"org-1","commands":[{"name":"test","run":"go test ./..."}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(project, ".chunk", "config.json"), []byte(projectCfg), 0o644))
	run("add", ".")
	run("commit", "-m", "init")
	dataDir, err := config.ProjectDataDir(project)
	assert.NilError(t, err)
	assert.NilError(t, sidecar.RegisterProjectRoot(dataDir, project))
	root := config.CanonicalProjectRoot(project)

	cfg := server.ReviewConfig{
		Credential: claudecode.Credential{EnvVar: config.EnvAnthropicAPIKey, Value: "sk-test"},
		// A run that never finishes on its own.
		RunFactory: func(ctx context.Context, _ factory.RunOptions) (factory.Report, error) {
			<-ctx.Done()
			return factory.Report{}, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- server.RunDaemon(ctx, nil, "", nil, nil, server.WithReview(cfg)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not shut down")
		}
	})
	waitForCond(t, "daemon", chunkd.IsDaemonRunning)

	id, err := chunkd.StartFactory(chunkd.FactoryRequest{ProjectRoot: root, Prompt: "add a flag"})
	assert.NilError(t, err)
	state := func() chunkd.SessionState {
		d, fetchErr := chunkd.FetchSession(id)
		assert.NilError(t, fetchErr)
		return d.State
	}

	m := sessModel()
	m.loadFn = loadFromDaemon
	var msg tea.Msg
	waitForCond(t, "session in snapshot", func() bool {
		msg = m.loadData()
		dm, ok := msg.(dataMsg)
		return ok && len(dm.sessions) == 1
	})
	next, _ := m.Update(msg)
	m = next.(Model)
	assert.Assert(t, m.selectedRun() != nil, "the first poll selects the run")

	// Quitting detaches: the session is untouched.
	_, quitCmd := press(m, 'q')
	_, isQuit := quitCmd().(tea.QuitMsg)
	assert.Assert(t, isQuit)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, state(), chunkd.SessionRunning)

	// The confirmed cancel key stops it.
	m, cmd := press(m, 'x', 'x')
	assert.Assert(t, cmd != nil)
	res, ok := cmd().(sessionActionMsg)
	assert.Assert(t, ok)
	assert.NilError(t, res.err)
	waitForCond(t, "session cancelled", func() bool { return state() == chunkd.SessionCancelled })
}

func waitForCond(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The footer is drawn from the same bindings the handlers match, so a key it
// does not offer does nothing, and one it offers works.
func TestFooterOffersOnlyTheKeysThatWork(t *testing.T) {
	ended := time.Now()
	done := liveSession("done", chunkd.SessionDone, ended.Add(-time.Minute))
	done.s.EndedAt = &ended

	for _, tc := range []struct {
		run    sessionInfo
		cancel bool
	}{
		{liveSession("live", chunkd.SessionRunning, time.Now()), true},
		{done, false},
	} {
		m := sessModel(tc.run).reselectRun()
		footer := m.renderFooter(m.styles())
		assert.Equal(t, strings.Contains(footer, "cancel run"), tc.cancel, "%s:\n%s", tc.run.s.ID, footer)

		_, cmd := press(m, 'x', 'x')
		assert.Equal(t, cmd != nil, tc.cancel, "%s: x x", tc.run.s.ID)
	}
}

// An implementer's summary is prose: it is wrapped to the pane rather than
// clipped, holds the dashboard's height, and scrolls.
func TestTextPaneWrapsAndScrollsWithinTheScreen(t *testing.T) {
	var words []string
	for i := range 400 {
		words = append(words, fmt.Sprintf("word%d", i))
	}
	m := sessModel()
	m.width, m.height = 60, 20
	m = m.openText("round 1 implementer", strings.Join(words, " "))

	out := m.render()
	assert.Equal(t, strings.Count(out, "\n"), 20, out)
	for i, line := range strings.Split(out, "\n") {
		assert.Assert(t, lipgloss.Width(line) <= 60, "line %d is %d columns", i, lipgloss.Width(line))
	}
	assert.Assert(t, strings.Contains(out, "word20"), "the text past the first line's width is wrapped onto the next:\n%s", out)
	assert.Assert(t, strings.Contains(out, "↓ more"), out)

	m, _ = press(m, 'G')
	out = m.render()
	assert.Assert(t, strings.Contains(out, "word399"), "G goes to the end:\n%s", out)
	assert.Assert(t, !strings.Contains(out, "↓ more"), out)
	m, _ = press(m, 'g')
	assert.Assert(t, strings.Contains(m.render(), "word0 "), "g goes back to the top")

	// A narrower terminal wraps the text again rather than clipping it.
	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	m = next.(Model)
	for i, line := range strings.Split(m.render(), "\n") {
		assert.Assert(t, lipgloss.Width(line) <= 40, "line %d is %d columns", i, lipgloss.Width(line))
	}
	assert.Assert(t, strings.Contains(m.render(), "word3"), "the start of the text is still shown:\n%s", m.render())
}

// A factory review passes as a check unless it found something worth
// changing, so its row says whether it failed the round, not only how many
// findings it had.
func TestFactoryReviewRowsSayWhetherTheReviewPassed(t *testing.T) {
	st := newWatchStyles(false)
	m := sessModel()
	row := func(p chunkd.ReviewPrompt) string {
		p.Name, p.State = "bugs", chunkd.PromptDone
		r := m.reviewRow(st, p)
		return stripANSI(r.icon + " " + r.detail)
	}

	assert.Equal(t, row(chunkd.ReviewPrompt{Status: "passed"}), ui.IconOK+" 0ms")
	assert.Equal(t, row(chunkd.ReviewPrompt{Status: "passed", Findings: 2}), ui.IconOK+" 0ms  2 issues, none worth changing")
	assert.Equal(t, row(chunkd.ReviewPrompt{Status: "failed", Findings: 3, Worth: 1}), ui.IconFail+" 0ms  3 issues, 1 worth changing")
	assert.Equal(t, row(chunkd.ReviewPrompt{Status: "errored", Error: "timed out"}), ui.IconFail+" 0ms  timed out")
	// Until its status is known, any finding is flagged.
	assert.Equal(t, row(chunkd.ReviewPrompt{Findings: 2}), ui.IconWarn+" 0ms  2 issues found")
}

// The run pane says what is happening and whether there is anything to do,
// without making the person read the rounds to work it out.
func TestNextStepLineSaysWhatToDo(t *testing.T) {
	st := newWatchStyles(false)
	say := func(state chunkd.SessionState, r chunkd.Round) string {
		s := chunkd.Session{State: state, Factory: &chunkd.FactoryRun{}, Rounds: []chunkd.Round{r}}
		return stripANSI(nextStepLine(st, s))
	}

	assert.Equal(t, say(chunkd.SessionRunning, chunkd.Round{State: chunkd.RoundImplementing}),
		"Now: the implementer is making the change. Nothing to do yet.")
	assert.Equal(t, say(chunkd.SessionRunning, chunkd.Round{State: chunkd.RoundChecking}),
		"Now: checks and reviewers are looking at the change. Nothing to do yet.")
	assert.Equal(t, say(chunkd.SessionDone, chunkd.Round{State: chunkd.RoundDone}),
		ui.IconOK+" All checks passed. The change is ready for you to look at.")
	assert.Equal(t, say(chunkd.SessionDone, chunkd.Round{State: chunkd.RoundDone, Checks: []chunkd.RoundCheck{{Name: "lint", Status: chunkd.CheckFailed}}}),
		ui.IconFail+" Still failing after the last round: lint")
	// A run that made no change has nothing for the person to look at.
	noChange := chunkd.Session{State: chunkd.SessionDone, Factory: &chunkd.FactoryRun{Result: chunkd.ResultNoChange}, Rounds: []chunkd.Round{{State: chunkd.RoundDone}}}
	assert.Equal(t, stripANSI(nextStepLine(st, noChange)), "The implementer made no change, so there is nothing to look at.")
	// A loop that failed without a failing row in its last round is not a pass.
	failedLoop := chunkd.Session{State: chunkd.SessionDone, Factory: &chunkd.FactoryRun{}, Rounds: []chunkd.Round{{State: chunkd.RoundDone}},
		Stages: []chunkd.Stage{{State: chunkd.StageFailed}}}
	assert.Equal(t, stripANSI(nextStepLine(st, failedLoop)), ui.IconFail+" The run ended without its checks passing.")
	assert.Equal(t, nextStepLine(st, chunkd.Session{}), "")
}

func stripANSI(s string) string {
	return regexp.MustCompile("\x1b\\[[0-9;]*m|\x1b\\]8;[^\x07\x1b]*(\x07|\x1b\\\\)").ReplaceAllString(s, "")
}

// poll delivers the daemon's runs as the dashboard's poll does.
func poll(m Model, runs ...sessionInfo) Model {
	next, _ := m.Update(dataMsg{sessions: runs})
	return next.(Model)
}

// liveRound is a finished round with an implementer turn, lint and test, and a
// review.
func liveRound(n int, lint string) chunkd.Round {
	ended := time.Now()
	return chunkd.Round{
		Number: n, State: chunkd.RoundDone, EndedAt: &ended,
		Implement: &chunkd.RoundImplement{State: chunkd.ImplementApplied},
		Checks:    []chunkd.RoundCheck{{Name: "lint", Status: lint}, {Name: "test", Status: "passed"}},
		Reviews:   []chunkd.ReviewPrompt{{Name: "bugs", State: chunkd.PromptDone, CommandID: fmt.Sprintf("cmd-r%d", n)}},
	}
}

// A new round folds the one before it; a selection inside the folded round
// moves to its header rather than onto whatever row slid into its place.
func TestSelectionInARoundThatFoldsMovesToItsHeader(t *testing.T) {
	r := factoryRun("abc", chunkd.SessionRunning, time.Now().Add(-time.Minute))
	r.s.Rounds = []chunkd.Round{liveRound(1, "failed")}
	m := sessModel(r)
	m.selRun = "abc"
	m, _ = press(m, tea.KeyRight, tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyDown)
	assert.Equal(t, *m.selectedRunItem(), runItem{kind: itemReview, round: 0})

	r.s.Rounds = append(r.s.Rounds, liveRound(2, "passed"))
	m = poll(m, r)
	assert.Equal(t, *m.selectedRunItem(), runItem{kind: itemRound, round: 0})
}

// When the run ends failing, its failing round opens above the selection; the
// selection stays on the row it was on.
func TestSelectionKeepsItsRowWhenRowsOpenAboveIt(t *testing.T) {
	r := factoryRun("abc", chunkd.SessionRunning, time.Now().Add(-time.Minute))
	r.s.Rounds = []chunkd.Round{liveRound(1, "failed"), liveRound(2, "failed")}
	r.s.Rounds[1].Checks[0].Status = "passed"
	m := sessModel(r)
	m.selRun = "abc"
	// Pin round 2 open (close, then open), then walk to its review.
	m, _ = press(m, tea.KeyRight, tea.KeyDown, ' ', ' ')
	for *m.selectedRunItem() != (runItem{kind: itemReview, round: 1}) {
		m, _ = press(m, tea.KeyDown)
	}

	ended := time.Now()
	r.s.State, r.s.EndedAt = chunkd.SessionDone, &ended
	r.s.Stages[0].State = chunkd.StageFailed
	m = poll(m, r)
	run := m.selectedRun()
	assert.Assert(t, m.roundOpen(run.s, 0) && m.roundOpen(run.s, 1))
	assert.Equal(t, *m.selectedRunItem(), runItem{kind: itemReview, round: 1})
}

// A run that failed early and then passed opens the round that passed, not
// the earlier failure.
func TestPassedRunOpensItsLastRound(t *testing.T) {
	r := failedRun()
	r.s.Stages[0].State, r.s.Stages[0].Note = chunkd.StageDone, ""
	r.s.Factory.Result = chunkd.ResultPassed
	r.s.Rounds = r.s.Rounds[:2]
	r.s.Rounds[0].Checks[0].Status, r.s.Rounds[1].Checks[0].Status = "failed", "passed"
	m := sessModel(r)
	assert.Assert(t, !m.roundOpen(r.s, 0) && m.roundOpen(r.s, 1))
}

// While a round's checks and reviews run, validate says how many are done.
func TestOpenRoundCountsWhatIsStillRunning(t *testing.T) {
	r := factoryRun("abc", chunkd.SessionRunning, time.Now().Add(-time.Minute))
	round := liveRound(1, "passed")
	round.State = chunkd.RoundChecking
	round.Checks[1].Status = ""
	round.Reviews[0].State = chunkd.PromptRunning
	r.s.Rounds = []chunkd.Round{round}
	m := sessModel(r)
	m.selRun = "abc"
	out := stripANSI(m.render())
	assert.Assert(t, strings.Contains(out, "validate · 1 of 3 done"), out)
}

// A setup warning on a live run shows with infra folded; a plain step does not.
func TestLiveRunShowsItsLatestSetupWarning(t *testing.T) {
	for _, tc := range []struct {
		level chunkd.FeedLevel
		shown bool
	}{
		{chunkd.FeedStep, false},
		{chunkd.FeedWarn, true},
		{chunkd.FeedError, true},
	} {
		r := factoryRun("abc", chunkd.SessionRunning, time.Now().Add(-time.Minute))
		r.s.Factory.Progress = chunkd.Feed{Lines: []chunkd.FeedLine{{Level: tc.level, Text: "sidecar slow to start"}}, Total: 1}
		m := sessModel(r)
		m.selRun = "abc"
		out := stripANSI(m.render())
		assert.Assert(t, strings.Contains(out, "▸ infra"), out)
		assert.Equal(t, strings.Contains(out, "sidecar slow to start"), tc.shown, "%s:\n%s", tc.level, out)
	}
}

// i is offered only when the run has something under infra.
func TestFooterOffersInfraOnlyWhenThereIsSome(t *testing.T) {
	r := factoryRun("abc", chunkd.SessionRunning, time.Now())
	m := sessModel(r).reselectRun()
	assert.Assert(t, strings.Contains(m.renderFooter(m.styles()), "infra"))

	r.s.Factory.Worktree = ""
	m = sessModel(r).reselectRun()
	assert.Assert(t, !strings.Contains(m.renderFooter(m.styles()), "infra"))
}

func TestCheckRowShowsFailureTail(t *testing.T) {
	st := newWatchStyles(false)
	row := checkRow(st, chunkd.RoundCheck{
		Name:   "test",
		Status: chunkd.CheckFailed,
		Output: "ok  pkg/a\n\n--- FAIL: TestX\n    x_test.go:9: want 3, got 4\nFAIL\n",
	})
	assert.Equal(t, len(row.extra), failTailLines)
	assert.Assert(t, strings.Contains(row.extra[len(row.extra)-1], "FAIL"))

	passed := checkRow(st, chunkd.RoundCheck{Name: "lint", Status: chunkd.CheckPassed, Output: "noise"})
	assert.Equal(t, len(passed.extra), 0)

	// Raw output's colour and progress redraws are resolved, not drawn: a bare
	// \r would overwrite the left pane.
	assert.DeepEqual(t, tailLines("fetching  10%\rfetching 100%\n\x1b[31mFAIL\x1b[0m pkg\r\n", 3),
		[]string{"fetching 100%", "FAIL pkg"})
}

func TestItemFailureReport(t *testing.T) {
	s := chunkd.Session{Rounds: []chunkd.Round{{
		Number:    2,
		Implement: &chunkd.RoundImplement{State: chunkd.ImplementFailed, Error: "claude exited 1"},
		Checks: []chunkd.RoundCheck{
			{Name: "lint", Status: chunkd.CheckPassed, Output: "noise"},
			{Name: "test", Status: chunkd.CheckFailed, Output: "--- FAIL: TestX\nFAIL\n"},
			{Name: "build", Status: chunkd.CheckErrored, Error: "sidecar unreachable"},
		},
		Reviews: []chunkd.ReviewPrompt{
			{Name: "bugs", State: chunkd.PromptDone, Status: chunkd.CheckPassed},
			{Name: "style", State: chunkd.PromptDone, Status: chunkd.CheckFailed, Worth: 2},
			{Name: "tests", State: chunkd.PromptFailed, Error: "claude timed out"},
		},
	}}}
	items := roundItems(s, 0)
	assert.Equal(t, len(items), 7)

	for i, want := range []string{
		"Round 2 implementer failed: claude exited 1",
		"",
		"Round 2 check: test failed\n\n```\n--- FAIL: TestX\nFAIL\n```",
		"Round 2 check: build failed: sidecar unreachable",
		"",
		"Round 2 review: style failed: 2 issues worth changing",
		"Round 2 review: tests failed: claude timed out",
	} {
		assert.Equal(t, itemFailureReport(s, items[i]), want, "item %d", i)
	}

	s.Rounds[0].Implement.State = chunkd.ImplementApplied
	assert.Equal(t, itemFailureReport(s, items[0]), "")
}

// c copies the selected row's failure, and on a row that passed, the latest
// round's failures: an earlier round's were what the next one set out to fix.
func TestCopyFailureCopiesTheSelectedOrLatestRoundsFailures(t *testing.T) {
	clipboard := func(cmd tea.Cmd) string {
		t.Helper()
		assert.Assert(t, cmd != nil, "nothing was copied")
		return fmt.Sprint(cmd())
	}

	r := factoryRun("abc", chunkd.SessionRunning, time.Now())
	r.s.Rounds = []chunkd.Round{
		{Number: 1, Checks: []chunkd.RoundCheck{{Name: "test", Status: chunkd.CheckFailed, Output: "--- FAIL: TestOld"}}},
		{Number: 2, Checks: []chunkd.RoundCheck{
			{Name: "test", Status: chunkd.CheckFailed, Output: "--- FAIL: TestNew"},
			{Name: "lint", Status: chunkd.CheckPassed},
		}},
	}
	m := sessModel(r)
	m.selRun = "abc"
	m, _ = press(m, tea.KeyRight)
	assert.Assert(t, strings.Contains(m.renderFooter(m.styles()), "copy failure"))

	m, cmd := press(m, 'c')
	got := clipboard(cmd)
	assert.Assert(t, strings.HasPrefix(got, "chunk factory run (repo · chunk/factory/abc · 0123456)\n"), got)
	assert.Assert(t, strings.Contains(got, "TestOld") && !strings.Contains(got, "TestNew"), got)
	assert.Equal(t, m.runNote, "copied failure to clipboard")

	m, cmd = press(m, tea.KeyDown, tea.KeyDown, 'c')
	got = clipboard(cmd)
	assert.Assert(t, strings.Contains(got, "Round 2 check: test failed") && !strings.Contains(got, "TestOld"), got)

	// Once the latest round passes, nothing is left to copy.
	m.sessions[0].s.Rounds[1].Checks[0].Status = chunkd.CheckPassed
	m.sessions[0].s.State = chunkd.SessionDone
	m, cmd = press(m, 'c')
	assert.Assert(t, cmd == nil)
	assert.Equal(t, m.runNote, "nothing failed in this run")

	// A run that failed before any round copies its error.
	m.sessions[0].s.Rounds = nil
	m.sessions[0].s.State, m.sessions[0].s.Error = chunkd.SessionFailed, "no sidecar image"
	m.runItem = 0
	_, cmd = press(m, 'c')
	got = clipboard(cmd)
	assert.Assert(t, strings.HasPrefix(got, "chunk factory run failed ("), got)
	assert.Assert(t, strings.HasSuffix(got, "Error: no sidecar image"), got)
}

// The latest round's failures are copied even when that round is folded and
// its rows are not on screen.
func TestCopyFailureReachesAFoldedLatestRound(t *testing.T) {
	r := failedRun()
	r.s.Rounds[2].Checks[0].Status = chunkd.CheckFailed
	r.s.Rounds[2].Checks[0].Output = "--- FAIL: TestLatest"
	m := sessModel(r)
	m.selRun = "abc"
	m.runOpen = map[string]map[int]bool{"abc": {2: false}}
	m, _ = press(m, tea.KeyRight) // round 1's header; round 1 passed

	_, cmd := press(m, 'c')
	assert.Assert(t, cmd != nil, "nothing was copied")
	got := fmt.Sprint(cmd())
	assert.Assert(t, strings.Contains(got, "Round 3 check: lint failed") && strings.Contains(got, "TestLatest"), got)
}

// A failed check's tail sits under its row, so selecting it scrolls far enough
// to show the tail as well as the row.
func TestSelectedFailedCheckKeepsItsTailInView(t *testing.T) {
	r := factoryRun("abc", chunkd.SessionRunning, time.Now())
	var checks []chunkd.RoundCheck
	for i := range 12 {
		checks = append(checks, chunkd.RoundCheck{Name: fmt.Sprintf("check-%d", i), Status: chunkd.CheckPassed})
	}
	checks = append(checks, chunkd.RoundCheck{Name: "test", Status: chunkd.CheckFailed, Output: "one\ntwo\nlast tail line"})
	r.s.Rounds = []chunkd.Round{{Number: 1, Checks: checks}}
	m := sessModel(r)
	m.selRun = "abc"
	m.height = 16
	m, _ = press(m, tea.KeyRight)
	m.runItem = len(checks) // past the round's header, the last check

	out := stripANSI(m.render())
	assert.Assert(t, strings.Contains(out, "last tail line"), out)
}

func typeText(m Model, text string) Model {
	for _, r := range text {
		next, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(Model)
	}
	return m
}

// searchModel is a dashboard with two runs, the newest selected, on the left pane.
func searchModel() Model {
	a := factoryRun("aaa", chunkd.SessionDone, time.Now().Add(-time.Hour))
	a.s.Factory.Prompt = "Add retry to the webhook sender"
	b := factoryRun("bbb", chunkd.SessionDone, time.Now().Add(-2*time.Hour))
	b.s.Factory.Prompt = "Fix flaky cache test"
	m := sessModel(a, b)
	m.selRun, m.focusedPane = "aaa", paneLeft
	return m
}

// / opens a search over the runs: they narrow as it is typed, the selection
// moves to one that is still listed, Enter keeps the filter and Esc drops it.
func TestSlashSearchesTheRunList(t *testing.T) {
	m := searchModel()
	assert.Assert(t, strings.Contains(stripANSI(m.render()), "search runs"), "the search is discoverable before it is used:\n%s", m.render())
	m, _ = press(m, '/')
	assert.Assert(t, m.searching)

	m = typeText(m, "cache")
	assert.Equal(t, len(m.sessions), 1)
	assert.Equal(t, m.selRun, "bbb", "the selection follows to a run that is still listed")
	out := stripANSI(m.render())
	assert.Assert(t, strings.Contains(out, "⌕ cache▏"), "the query is shown:\n%s", out)
	assert.Assert(t, !strings.Contains(out, "webhook"), "the other run is hidden:\n%s", out)

	// While searching, keys are text: q does not quit.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	assert.Assert(t, cmd == nil)

	m, _ = press(m, tea.KeyEnter)
	assert.Assert(t, !m.searching)
	assert.Equal(t, m.query, "cache", "Enter keeps the filter")
	assert.Equal(t, len(m.sessions), 1)

	// Esc outside a search drops a kept filter before it would quit.
	m, cmd = press(m, tea.KeyEscape)
	assert.Equal(t, m.query, "")
	assert.Equal(t, len(m.sessions), 2)
	assert.Assert(t, cmd == nil)
}

func TestSearchEscClearsAndBackspaceWidens(t *testing.T) {
	m, _ := press(searchModel(), '/')
	m = typeText(m, "cachx")
	assert.Equal(t, len(m.sessions), 0)
	assert.Assert(t, strings.Contains(stripANSI(m.render()), "no runs match"))

	m, _ = press(m, tea.KeyBackspace)
	assert.Equal(t, m.query, "cach")
	assert.Equal(t, len(m.sessions), 1)

	m, _ = press(m, tea.KeyEscape)
	assert.Assert(t, !m.searching)
	assert.Equal(t, m.query, "")
	assert.Equal(t, len(m.sessions), 2)
}

// A poll while a filter is on keeps filtering instead of bringing every run back.
func TestPollKeepsTheRunFilter(t *testing.T) {
	m := searchModel()
	all := m.allRuns
	m = m.setQuery("cache")
	m = poll(m, all...)
	assert.Equal(t, len(m.allRuns), 2)
	assert.Equal(t, len(m.sessions), 1)
	assert.Equal(t, m.selRun, "bbb")
	assert.Equal(t, len(filterRuns(all, "CACHE flaky", nil)), 1, "every word must match, ignoring case")
}

// A filter narrows the list, not what the header and sidecar pane know: a
// hidden run still counts as running, and its sidecars are not loose ones.
func TestFilterDoesNotHideRunsFromTheHeader(t *testing.T) {
	live := liveSession("live", chunkd.SessionRunning, time.Now())
	m := sessModel(live)
	m = m.setQuery("nothing matches this")
	assert.Equal(t, len(m.sessions), 0)
	out := stripANSI(m.render())
	assert.Assert(t, strings.Contains(out, "1 running"), "a hidden live run is still in flight:\n%s", out)
	assert.Assert(t, !strings.Contains(strings.Join(m.renderLeftPane(m.styles(), 30), "\n"), "sidecars"), out)
}

// A sidecar nobody has touched for days is not listed, even though the API
// still knows it.
func TestFilterSidecarsDropsOnesOlderThanFiveDays(t *testing.T) {
	now := time.Now()
	kept := filterSidecars([]sidecarInfo{
		{id: "fresh", verified: true, lastActivity: now.Add(-2 * time.Hour)},
		{id: "old", verified: true, lastActivity: now.Add(-6 * 24 * time.Hour)},
	})
	assert.Equal(t, len(kept), 1)
	assert.Equal(t, kept[0].id, "fresh")
}

// The run list leads a run with its state as a block, then says when or how far.
func TestRunListLeadsWithAStateChip(t *testing.T) {
	r := factoryRun("aaa", chunkd.SessionDone, time.Now().Add(-time.Hour))
	ended := time.Now().Add(-10 * time.Minute)
	r.s.EndedAt = &ended
	m := sessModel(r)
	m.selRun = "aaa"
	out := stripANSI(strings.Join(m.renderRunList(m.styles()), "\n"))
	assert.Assert(t, strings.Contains(out, " done  10m ago"), out)
}

// An open round's checks and reviews hang off one tree, the last on └─.
func TestOpenRoundDrawsATree(t *testing.T) {
	r := factoryRun("aaa", chunkd.SessionRunning, time.Now())
	r.s.Rounds = []chunkd.Round{{
		Number: 1, State: chunkd.RoundChecking,
		Checks: []chunkd.RoundCheck{{Name: "lint", Status: chunkd.CheckPassed}, {Name: "test", Status: chunkd.CheckPassed}},
	}}
	m := sessModel(r)
	m.selRun = "aaa"
	out := stripANSI(m.render())
	assert.Assert(t, strings.Contains(out, "├─ ✓  lint"), out)
	assert.Assert(t, strings.Contains(out, "└─ ✓  test"), out)
}
