package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

func liveSession(id string, state watchd.SessionState, started time.Time) sessionInfo {
	return sessionInfo{label: "repo", s: watchd.Session{
		ID: id, State: state, StartedAt: started, Branch: "feature", HeadSHA: "0123456789abcdef",
		Stages: []watchd.Stage{
			{ID: watchd.StageReviewLoop, State: watchd.StageRunning},
			{ID: watchd.StageRebase, State: watchd.StageNotBuilt},
			{ID: watchd.StageCI, State: watchd.StageNotBuilt},
			{ID: watchd.StageApproval, State: watchd.StageNotBuilt},
			{ID: watchd.StagePR, State: watchd.StageNotBuilt},
		},
	}}
}

func sessModel(sessions ...sessionInfo) Model {
	m := New(nil, true).WithConnection(watchd.Connection{})
	m.width, m.height = 110, 40
	m.sessions = sessions
	return m
}

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func press(m Model, codes ...rune) (Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, c := range codes {
		var next tea.Model
		next, cmd = m.Update(key(c))
		m = next.(Model)
	}
	return m, cmd
}

func TestCollectSessionsPutsLiveOnesFirstThenNewest(t *testing.T) {
	now := time.Now()
	ended := now
	done := watchd.Session{ID: "done-new", State: watchd.SessionDone, StartedAt: now, EndedAt: &ended}
	old := watchd.Session{ID: "done-old", State: watchd.SessionDone, StartedAt: now.Add(-time.Hour), EndedAt: &ended}
	paused := watchd.Session{ID: "paused", State: watchd.SessionPaused, StartedAt: now.Add(-2 * time.Hour)}

	got := collectSessions([]watchd.ProjectSnapshot{
		{Root: "/a", RepoName: "a", Sessions: []watchd.Session{old, done}},
		{Root: "/b", Sessions: []watchd.Session{paused}},
	})

	assert.Equal(t, got[0].s.ID, "paused", "a session waiting on the user comes first")
	assert.Equal(t, got[1].s.ID, "done-new")
	assert.Equal(t, got[2].s.ID, "done-old")
	assert.Equal(t, got[0].label, "b", "a project with no repo name falls back to its directory")
}

// poll delivers one dataMsg with these sessions and sidecars, the way the
// dashboard receives them.
func poll(m Model, sessions []sessionInfo, sidecars ...sidecarInfo) Model {
	next, _ := m.Update(dataMsg{sessions: sessions, sidecars: sidecars})
	return next.(Model)
}

func endedSession(id string, ago time.Duration) sessionInfo {
	s := liveSession(id, watchd.SessionDone, time.Now().Add(-ago-time.Minute))
	ended := time.Now().Add(-ago)
	s.s.EndedAt = &ended
	return s
}

// The session pane must show the whole flow, what the fixes changed, and why it
// paused, and the dashboard must still fit the terminal around it.
func TestSessionPaneShowsTheTimelineFixesAndPauseAndFitsTheScreen(t *testing.T) {
	s := liveSession("sess-1", watchd.SessionPaused, time.Now().Add(-time.Minute))
	s.s.PauseReason = "files changed while the session was running: mine.txt"
	s.s.Restore = &watchd.RestorePoint{Ref: "refs/chunk/restore/sess-1", Paths: []string{"app.go"}}
	s.s.Rounds = []watchd.Round{{
		Number: 1, State: watchd.RoundDone, Findings: 3, Worth: 1,
		Reviews: []watchd.ReviewPrompt{{Name: "bugs", State: watchd.PromptDone}, {Name: "style", State: watchd.PromptDone}},
		Fix: &watchd.RoundFix{State: watchd.FixApplied, Insertions: 4, Deletions: 1,
			Files: []watchd.FileChange{{Path: "app.go", Insertions: 4, Deletions: 1}}},
	}, {
		Number: 2, State: watchd.RoundReviewing,
		Reviews: []watchd.ReviewPrompt{{Name: "bugs", State: watchd.PromptRunning, SidecarID: "sc-1"}},
	}}
	for _, width := range []int{80, 110} {
		for _, height := range []int{14, 24, 50} {
			m := sessModel()
			m.width, m.height = width, height
			m = poll(m, []sessionInfo{s}, sidecarInfo{id: "sc-1", repoName: "repo", branch: "feature"})
			assert.Equal(t, m.sessionSel, "sess-1", "a live session is selected on the first poll")

			out := m.render()
			assert.Assert(t, strings.Count(out, "\n") <= height, "%dx%d: %d lines", width, height, strings.Count(out, "\n"))
			for _, line := range strings.Split(out, "\n") {
				assert.Assert(t, lipgloss.Width(line) <= width, "%dx%d: line too wide: %q", width, height, line)
			}
			if height < 50 || width < 110 {
				continue
			}
			assert.Equal(t, strings.Count(out, "not built yet"), 4, "rebase, CI, approval and PR are shown as not built:\n%s", out)
			for _, want := range []string{"review sessions", "sidecars", "mine.txt", "app.go", "Round 1", "Round 2", "bugs", "chunk session restore sess-1"} {
				assert.Assert(t, strings.Contains(out, want), "missing %q:\n%s", want, out)
			}
		}
	}
}

func TestSessionKeysQuitDetachesCancelNeedsConfirmationResumeOnlyWhenPaused(t *testing.T) {
	m := poll(sessModel(), []sessionInfo{
		liveSession("run-1", watchd.SessionRunning, time.Now()),
		liveSession("run-2", watchd.SessionRunning, time.Now().Add(-time.Minute)),
	})
	assert.Equal(t, m.sessionSel, "run-1")

	// Cancel: the first x only asks, another key or another session withdraws it,
	// and only a second x on the same session sends the request.
	m, cmd := press(m, 'x')
	assert.Assert(t, cmd == nil)
	assert.Equal(t, m.sessPane.confirm, "run-1")
	assert.Assert(t, strings.Contains(m.render(), "press x again"))
	m, cmd = press(m, tea.KeyDown, 'x')
	assert.Assert(t, cmd == nil)
	assert.Equal(t, m.sessionSel, "run-2")
	assert.Equal(t, m.sessPane.confirm, "run-2")
	_, cmd = press(m, 'x')
	assert.Assert(t, cmd != nil, "the second x on the same session sends the cancel")

	// Resume does nothing for a session that is not paused.
	_, cmd = press(m, 'c')
	assert.Assert(t, cmd == nil)

	// q and Esc leave the dashboard, which only detaches.
	for _, k := range []rune{'q', tea.KeyEscape} {
		_, cmd = press(m, k)
		_, isQuit := cmd().(tea.QuitMsg)
		assert.Assert(t, isQuit)
	}

	paused := poll(sessModel(), []sessionInfo{liveSession("p", watchd.SessionPaused, time.Now())})
	_, cmd = press(paused, 'c')
	assert.Assert(t, cmd != nil, "a paused session can be continued")
}

// The left pane is one list, sessions then sidecars, and the right pane follows
// whichever is selected.
func TestSelectionMovesBetweenSessionsAndSidecars(t *testing.T) {
	sc := sidecarInfo{id: "sc-a", repoName: "repo", branch: "main", lastActivity: time.Now()}
	sessions := []sessionInfo{liveSession("live", watchd.SessionRunning, time.Now()), endedSession("old", time.Hour)}
	m := poll(sessModel(), sessions, sc)
	assert.Equal(t, m.sessionSel, "live")
	assert.Assert(t, strings.Contains(m.render(), "Review loop"), "the right pane shows the session")

	m, _ = press(m, tea.KeyDown)
	assert.Equal(t, m.sessionSel, "old")
	m, _ = press(m, tea.KeyDown)
	assert.Equal(t, m.sessionSel, "", "moving past the last session selects the first sidecar")
	assert.Equal(t, m.selectedID, "sc-a")
	out := m.render()
	assert.Assert(t, strings.Contains(out, "activity") && !strings.Contains(out, "Review loop"), out)

	m, _ = press(m, tea.KeyDown)
	assert.Equal(t, m.selectedID, "sc-a", "the last row stays selected")
	m, _ = press(m, tea.KeyUp)
	assert.Equal(t, m.sessionSel, "old")

	// A poll that re-sorts the sessions keeps the selection on the same one.
	m = poll(m, []sessionInfo{liveSession("new", watchd.SessionRunning, time.Now()), sessions[0], sessions[1]}, sc)
	assert.Equal(t, m.sessionSel, "old")

	// A session the daemon no longer has hands the selection back to a sidecar.
	m = poll(m, sessions[:1], sc)
	assert.Equal(t, m.sessionSel, "")
	assert.Equal(t, m.selectedID, "sc-a")
}

func TestFirstPollSelectsASidecarWhenNoSessionIsLive(t *testing.T) {
	sc := sidecarInfo{id: "sc-a", repoName: "repo", branch: "main", lastActivity: time.Now()}
	m := poll(sessModel(), []sessionInfo{endedSession("old", time.Hour)}, sc)
	assert.Equal(t, m.sessionSel, "")
	assert.Equal(t, m.selectedID, "sc-a")
	assert.Assert(t, strings.Contains(m.render(), "review sessions"), "finished sessions are still listed")

	// Only the first poll picks: a session starting later does not steal the
	// selection from what the reader is looking at.
	m = poll(m, []sessionInfo{liveSession("live", watchd.SessionRunning, time.Now())}, sc)
	assert.Equal(t, m.sessionSel, "")
}

func TestSessionSectionIsHiddenWithoutSessionsAndCappedWithMany(t *testing.T) {
	sc := sidecarInfo{id: "sc-a", repoName: "repo", branch: "main", lastActivity: time.Now()}
	m := poll(sessModel(), nil, sc)
	assert.Assert(t, !strings.Contains(m.render(), "review sessions"))

	var many []sessionInfo
	for i := range 6 {
		many = append(many, endedSession(fmt.Sprintf("s-%d", i), time.Duration(i)*time.Hour))
	}
	m = poll(sessModel(), many, sc)
	out := m.render()
	assert.Assert(t, strings.Contains(out, "+2 older"), out)

	// The older ones cannot be selected: moving down from the last listed session
	// goes to the sidecars.
	m, _ = press(m, tea.KeyUp)
	assert.Equal(t, m.sessionSel, "s-3")
	m, _ = press(m, tea.KeyDown)
	assert.Equal(t, m.sessionSel, "")

	// Live sessions are always listed, even past the cap.
	var live []sessionInfo
	for i := range 5 {
		live = append(live, liveSession(fmt.Sprintf("l-%d", i), watchd.SessionRunning, time.Now()))
	}
	assert.Equal(t, visibleSessions(append(live, many...)), 5)
}

func TestSessionFooterOffersOnlyTheActionsTheStateAllows(t *testing.T) {
	hints := func(m Model) string {
		var parts []string
		for _, k := range m.footerKeys() {
			parts = append(parts, k.key+" "+k.action)
		}
		return strings.Join(parts, ", ")
	}
	m := poll(sessModel(), []sessionInfo{liveSession("p", watchd.SessionPaused, time.Now()), endedSession("done", time.Minute)})
	assert.Equal(t, m.sessionSel, "p")
	assert.Assert(t, strings.Contains(hints(m), "c resume, x cancel"), hints(m))

	m, _ = press(m, tea.KeyDown)
	assert.Equal(t, m.sessionSel, "done")
	assert.Assert(t, !strings.Contains(hints(m), "resume") && !strings.Contains(hints(m), "cancel"), hints(m))

	running := poll(sessModel(), []sessionInfo{liveSession("r", watchd.SessionRunning, time.Now())})
	assert.Assert(t, !strings.Contains(hints(running), "resume") && strings.Contains(hints(running), "x cancel"), hints(running))

	// Why a session cannot be started rides the footer, but only alongside
	// sessions: the command that starts them is still hidden, and the warning
	// would nag everyone else.
	const authWarn = "no Anthropic API key"
	footer := func(sessions []sessionInfo) string {
		next, _ := sessModel().Update(dataMsg{sessions: sessions, reviewAuthErr: authWarn})
		withErr := next.(Model)
		return withErr.renderFooter(withErr.styles())
	}
	shown := footer([]sessionInfo{liveSession("r", watchd.SessionRunning, time.Now())})
	assert.Assert(t, strings.Contains(shown, authWarn), shown)
	hidden := footer(nil)
	assert.Assert(t, !strings.Contains(hidden, authWarn), hidden)
}

// A dashboard opened for a session selects that one, even when another is live
// and even if the first poll does not list it yet, and only once.
func TestWithSessionSelectsItWhenListedThenLetsGo(t *testing.T) {
	other := liveSession("other", watchd.SessionRunning, time.Now())
	mine := liveSession("mine", watchd.SessionRunning, time.Now().Add(-time.Second))
	m := sessModel().WithSession("mine")

	m = poll(m, []sessionInfo{other})
	assert.Equal(t, m.sessionSel, "", "the other live session is not picked while waiting for this one")

	m = poll(m, []sessionInfo{other, mine})
	assert.Equal(t, m.sessionSel, "mine")

	m, _ = press(m, tea.KeyUp)
	assert.Equal(t, m.sessionSel, "other")
	m = poll(m, []sessionInfo{other, mine})
	assert.Equal(t, m.sessionSel, "other", "after the first selection the reader decides")
}

// In the right pane the cursor runs through every round's reviews in turn.
func TestSessionCursorWalksReviewsAcrossRounds(t *testing.T) {
	s := liveSession("s", watchd.SessionRunning, time.Now())
	s.s.Rounds = []watchd.Round{
		{Number: 1, State: watchd.RoundDone, Reviews: []watchd.ReviewPrompt{{Name: "a", State: watchd.PromptDone}, {Name: "b", State: watchd.PromptDone}}},
		{Number: 2, State: watchd.RoundReviewing, Reviews: []watchd.ReviewPrompt{{Name: "c", State: watchd.PromptRunning}}},
	}
	m := poll(sessModel(), []sessionInfo{s})
	round, rv := cursorAt(s.s, m.sessPane.cursor)
	assert.Equal(t, round, 1, "the cursor starts on the latest round")
	assert.Equal(t, rv, 0)

	m, _ = press(m, tea.KeyRight, tea.KeyUp)
	round, rv = cursorAt(s.s, m.sessPane.cursor)
	assert.Equal(t, round, 0)
	assert.Equal(t, rv, 1)
	assert.Equal(t, m.sessionSel, "s", "up in the right pane moves the cursor, not the selection")

	m, _ = press(m, tea.KeyEnter)
	assert.Equal(t, m.sessPane.note, "no output yet for b")

	// No reviews yet: the cursor sits on the latest round with none selected.
	round, rv = cursorAt(watchd.Session{Rounds: []watchd.Round{{Number: 1}}}, 3)
	assert.Equal(t, round, 0)
	assert.Equal(t, rv, -1)
}

// startSessionDaemon runs a real daemon whose reviews never finish on their own,
// over one registered project with a review prompt, and returns the project's
// root. withOrigin gives the project the origin remote sessions clone from.
func startSessionDaemon(t *testing.T, withOrigin bool) string {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	sockDir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	t.Setenv("CHUNK_WATCHD_DIR", sockDir)

	project := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = project
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@t")
		out, runErr := c.CombinedOutput()
		assert.NilError(t, runErr, string(out))
	}
	run("init", "-b", "main")
	if withOrigin {
		run("remote", "add", "origin", "https://github.com/acme/widgets.git")
	}
	prompts := filepath.Join(project, ".chunk", "reviews")
	assert.NilError(t, os.MkdirAll(prompts, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(prompts, "bugs.md"), []byte("find bugs"), 0o644))
	run("add", ".")
	run("commit", "-m", "init")
	dataDir, err := config.ProjectDataDir(project)
	assert.NilError(t, err)
	assert.NilError(t, sidecar.RegisterProjectRoot(dataDir, project))
	root := config.CanonicalProjectRoot(project)

	cfg := watchd.ReviewConfig{
		Credential: review.Credential{EnvVar: config.EnvAnthropicAPIKey, Value: "sk-test"},
		NewPool: func(_ context.Context, spec watchd.ReviewPoolSpec) (*watchd.ReviewPool, error) {
			free := make(chan *sidecar.PoolEntry, spec.Size)
			for i := range spec.Size {
				free <- &sidecar.PoolEntry{ID: fmt.Sprintf("sc-%d", i+1), RepoPath: "/work"}
			}
			return &watchd.ReviewPool{
				Acquire:   func(context.Context) (*sidecar.PoolEntry, error) { return <-free, nil },
				Release:   func(e *sidecar.PoolEntry) { free <- e },
				WaitReady: func(context.Context) error { return nil },
				Close:     func(context.Context) {},
			}, nil
		},
		Submit: func(context.Context, *sidecar.PoolEntry, string, map[string]string) (string, error) {
			return "cmd-1", nil
		},
		// A review that never finishes on its own.
		Stream: func(ctx context.Context, _ *sidecar.PoolEntry, _ string, _ circleci.OutputFn) (int, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- watchd.RunDaemon(ctx, nil, "", nil, nil, watchd.WithReview(cfg)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not shut down")
		}
	})
	waitForCond(t, "daemon", watchd.IsDaemonRunning)
	return root
}

// Against a real daemon: the dashboard sees a session started elsewhere, quitting
// the dashboard leaves it running, and only the confirmed cancel key stops it.
func TestQuittingTheDashboardDetachesAndOnlyConfirmedCancelStopsTheSession(t *testing.T) {
	root := startSessionDaemon(t, false)

	id, err := watchd.StartSession(watchd.SessionRequest{ProjectRoot: root})
	assert.NilError(t, err)
	state := func() watchd.SessionState {
		d, fetchErr := watchd.FetchSession(id)
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
	assert.Assert(t, m.selectedSession() != nil, "the running session is selected on the first poll")

	// Quitting detaches: the session is untouched.
	_, quitCmd := press(m, 'q')
	_, isQuit := quitCmd().(tea.QuitMsg)
	assert.Assert(t, isQuit)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, state(), watchd.SessionRunning)

	// The confirmed cancel key stops it.
	m, cmd := press(m, 'x', 'x')
	assert.Assert(t, cmd != nil)
	res, ok := cmd().(sessionActionMsg)
	assert.Assert(t, ok)
	assert.NilError(t, res.err)
	waitForCond(t, "session cancelled", func() bool { return state() == watchd.SessionCancelled })
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

// n starts a session for the selected row's project, and only on a second
// press: the first names the working tree the session's fixes would change.
func TestStartKeyNeedsConfirmationAndTargetsTheSelectedProject(t *testing.T) {
	m := sessModel()
	m.projects = []ProjectEntry{{ProjectRoot: "/work/a"}, {ProjectRoot: "/work/b"}}
	m = poll(m, nil, sidecarInfo{id: "sc-a", projectIdx: 0}, sidecarInfo{id: "sc-b", projectIdx: 1})
	m.projects = []ProjectEntry{{ProjectRoot: "/work/a"}, {ProjectRoot: "/work/b"}}

	m, _ = press(m, tea.KeyDown)
	m, cmd := press(m, 'n')
	assert.Assert(t, cmd == nil)
	assert.Equal(t, m.sessPane.startConfirm, "/work/b")
	assert.Assert(t, strings.Contains(m.render(), "press n again to start a review session in /work/b"), m.render())

	// Another key withdraws it.
	m, _ = press(m, tea.KeyUp)
	assert.Equal(t, m.sessPane.startConfirm, "")
	m, cmd = press(m, 'n', 'n')
	assert.Assert(t, cmd != nil, "the second n starts the session")
	assert.Equal(t, m.sessPane.note, "starting a session…")

	// Against a remote daemon there is nothing to start: the files are not there.
	remote := m.WithConnection(watchd.Connection{Remote: "host:1"})
	remote, cmd = press(remote, 'n', 'n')
	assert.Assert(t, cmd == nil)
	assert.Assert(t, strings.Contains(remote.sessPane.note, "local daemon"), remote.sessPane.note)

	// With several projects and nothing selected, there is no telling which.
	none := sessModel()
	none.projects = []ProjectEntry{{ProjectRoot: "/work/a"}, {ProjectRoot: "/work/b"}}
	none, cmd = press(none, 'n')
	assert.Assert(t, cmd == nil)
	assert.Assert(t, strings.Contains(none.sessPane.note, "select a sidecar or session"), none.sessPane.note)
}

func TestStartKeyRefusesAProjectWithoutAnOriginRemote(t *testing.T) {
	project := t.TempDir()
	out, err := exec.Command("git", "-C", project, "init", "-b", "main").CombinedOutput()
	assert.NilError(t, err, string(out))

	msg, ok := startSessionCmd(project)().(sessionStartedMsg)
	assert.Assert(t, ok)
	assert.ErrorContains(t, msg.err, "no git remote named origin")

	m := sessModel().withSessionStarted(msg)
	assert.Assert(t, strings.HasPrefix(m.sessPane.note, "could not start a session: "), m.sessPane.note)
	assert.Equal(t, m.focusSession, "")
}

// Against a real daemon: n n starts a session, and the dashboard selects it on
// the next poll.
func TestStartKeyStartsASessionAndSelectsIt(t *testing.T) {
	root := startSessionDaemon(t, true)

	m := sessModel()
	m.loadFn = loadFromDaemon
	m.projects = []ProjectEntry{{ProjectRoot: root}}
	m, cmd := press(m, 'n', 'n')
	assert.Assert(t, cmd != nil)
	started, ok := cmd().(sessionStartedMsg)
	assert.Assert(t, ok)
	assert.NilError(t, started.err)
	next, _ := m.Update(started)
	m = next.(Model)

	var msg tea.Msg
	waitForCond(t, "session in snapshot", func() bool {
		msg = m.loadData()
		dm, ok := msg.(dataMsg)
		return ok && len(dm.sessions) == 1
	})
	next, _ = m.Update(msg)
	m = next.(Model)
	assert.Equal(t, m.sessionSel, started.id)

	// A second start for the same project is refused by the daemon, and says so.
	m, cmd = press(m, 'n', 'n')
	refused, _ := cmd().(sessionStartedMsg)
	assert.Assert(t, refused.err != nil)
	m = m.withSessionStarted(refused)
	assert.Assert(t, strings.HasPrefix(m.sessPane.note, "could not start a session: "), m.sessPane.note)

	assert.NilError(t, watchd.CancelSession(started.id))
}
