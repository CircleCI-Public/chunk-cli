package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/chunkd/server"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
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
	done := chunkd.Session{ID: "done-new", State: chunkd.SessionDone, StartedAt: now, EndedAt: &ended}
	old := chunkd.Session{ID: "done-old", State: chunkd.SessionDone, StartedAt: now.Add(-time.Hour), EndedAt: &ended}
	running := chunkd.Session{ID: "running", State: chunkd.SessionRunning, StartedAt: now.Add(-2 * time.Hour)}

	got := collectSessions([]chunkd.ProjectSnapshot{
		{Root: "/a", RepoName: "a", Sessions: []chunkd.Session{old, done}},
		{Root: "/b", Sessions: []chunkd.Session{running}},
	})

	assert.Equal(t, got[0].s.ID, "running", "a live session comes first")
	assert.Equal(t, got[1].s.ID, "done-new")
	assert.Equal(t, got[2].s.ID, "done-old")
	assert.Equal(t, got[0].label, "b", "a project with no repo name falls back to its directory")
}

// The view must show the whole flow, with the factory loop's rounds inside it.
func TestSessionViewShowsTheTimelineAndRoundsAndFitsTheScreen(t *testing.T) {
	s := liveSession("sess-1", chunkd.SessionRunning, time.Now().Add(-time.Minute))
	s.s.Rounds = []chunkd.Round{{
		Number: 1, State: chunkd.RoundDone, Findings: 3, Worth: 1, Note: "1 of 3 checks passed",
		Reviews: []chunkd.ReviewPrompt{{Name: "bugs", State: chunkd.PromptDone}, {Name: "style", State: chunkd.PromptDone}},
	}, {
		Number: 2, State: chunkd.RoundChecking,
		Reviews: []chunkd.ReviewPrompt{{Name: "bugs", State: chunkd.PromptRunning, SidecarID: "sc-1"}},
	}}
	for _, height := range []int{14, 24, 50} {
		m := sessModel(s)
		m.height = height
		m, _ = press(m, 'r')

		out := m.render()
		assert.Assert(t, strings.Count(out, "\n") <= height, "height %d: %d lines", height, strings.Count(out, "\n"))
		if height < 50 {
			continue
		}
		for _, want := range []string{"Factory loop", "Round 1", "1 of 3 checks passed", "Round 2", "reviewing and validating", "bugs", "style"} {
			assert.Assert(t, strings.Contains(out, want), "missing %q:\n%s", want, out)
		}
	}
}

func TestSessionViewKeysQuitDetachesCancelNeedsConfirmation(t *testing.T) {
	m := sessModel(
		liveSession("run-1", chunkd.SessionRunning, time.Now()),
		liveSession("run-2", chunkd.SessionRunning, time.Now().Add(-time.Minute)),
	)
	m, _ = press(m, 'r')
	assert.Assert(t, m.sessionView != nil)

	// Cancel: the first x only asks, another key or another session withdraws it,
	// and only a second x on the same session sends the request.
	m, cmd := press(m, 'x')
	assert.Assert(t, cmd == nil)
	assert.Equal(t, m.sessionView.confirm, "run-1")
	m, cmd = press(m, tea.KeyDown, 'x')
	assert.Assert(t, cmd == nil)
	assert.Equal(t, m.sessionView.confirm, "run-2")
	_, cmd = press(m, 'x')
	assert.Assert(t, cmd != nil, "the second x on the same session sends the cancel")

	// q leaves the dashboard, and Esc only goes back.
	_, cmd = press(m, 'q')
	_, isQuit := cmd().(tea.QuitMsg)
	assert.Assert(t, isQuit)
	m, cmd = press(m, tea.KeyEscape)
	assert.Assert(t, m.sessionView == nil && cmd == nil)
}

// Against a real daemon: the dashboard sees a session started elsewhere, quitting
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
		Credential: review.Credential{EnvVar: config.EnvAnthropicAPIKey, Value: "sk-test"},
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
	m, _ = press(m, 'r')
	assert.Assert(t, m.selectedSession() != nil)

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
