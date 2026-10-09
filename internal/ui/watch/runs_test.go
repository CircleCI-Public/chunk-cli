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
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/harness/claudecode"
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
	m.sessions = sessions
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

// The factory run's pane shows the work a round did, each check and review,
// where the work is, and fits the screen however short it is.
func TestFactoryRunPaneShowsImplementerChecksAndReviews(t *testing.T) {
	r := factoryRun("abc", chunkd.SessionRunning, time.Now().Add(-time.Minute))
	r.s.Rounds = []chunkd.Round{{
		Number: 1, State: chunkd.RoundDone, Note: "1 of 3 checks passed",
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
			"Add a --verbose flag", "round 2/3", "/tmp/wt/abc", "Round 1", "Round 2",
			"implement", "$0.41", "3 files changed", "test", "lint", "review: bugs", "2 findings",
			"1 of 3 checks passed", "syncing the implementer",
		} {
			assert.Assert(t, strings.Contains(out, want), "missing %q:\n%s", want, out)
		}
		assert.Assert(t, !strings.Contains(out, "not built yet"), "stages nothing runs are not shown:\n%s", out)
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
	for _, want := range []string{"still failing", "checks still failed after 3 round(s)", "chunk/factory/abc", "committed · 2 files changed, 10 insertions(+)", "/tmp/run.log"} {
		assert.Assert(t, strings.Contains(out, want), "missing %q:\n%s", want, out)
	}
}

func TestNoRunsLeavesTheSidecarPaneAsItWas(t *testing.T) {
	m := sessModel()
	m.selRun = ""
	m.sidecars = []sidecarInfo{{id: "sc-1", name: "box", repoName: "repo", branch: "main", verified: true}}
	out := strings.Join(m.renderLeftPane(m.styles(), 30), "\n")
	assert.Assert(t, !strings.Contains(out, "runs"), out)
	assert.Assert(t, strings.HasPrefix(out, m.styles().emphasis("sidecars")), out)
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
	m, _ = press(m, tea.KeyRight)

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

	// A review opens its buffered Claude run.
	m, cmd = press(m, tea.KeyDown, tea.KeyEnter)
	assert.Assert(t, cmd != nil && m.output != nil)
	assert.Equal(t, m.output.commandID, "cmd-r")
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
	assert.Equal(t, row(chunkd.ReviewPrompt{Status: "passed", Findings: 2}), ui.IconOK+" 0ms  2 findings, none worth changing")
	assert.Equal(t, row(chunkd.ReviewPrompt{Status: "failed", Findings: 3, Worth: 1}), ui.IconFail+" 0ms  1 worth changing")
	assert.Equal(t, row(chunkd.ReviewPrompt{Status: "errored", Error: "timed out"}), ui.IconFail+" 0ms  timed out")
	// Until its status is known, any finding is flagged.
	assert.Equal(t, row(chunkd.ReviewPrompt{Findings: 2}), ui.IconWarn+" 0ms  2 findings")
}

func stripANSI(s string) string { return regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(s, "") }
