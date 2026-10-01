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
		Task: "add a --verbose flag", WorkBranch: "chunk/factory/" + id, WorkDir: "/tmp/wt-" + id,
		Stages: []watchd.Stage{
			{ID: watchd.StageImplement, State: watchd.StageDone},
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
	running := watchd.Session{ID: "running", State: watchd.SessionRunning, StartedAt: now.Add(-2 * time.Hour)}

	got := collectSessions([]watchd.ProjectSnapshot{
		{Root: "/a", RepoName: "a", Sessions: []watchd.Session{old, done}},
		{Root: "/b", Sessions: []watchd.Session{running}},
	})

	assert.Equal(t, got[0].s.ID, "running", "a live session comes first")
	assert.Equal(t, got[1].s.ID, "done-new")
	assert.Equal(t, got[2].s.ID, "done-old")
	assert.Equal(t, got[0].label, "b", "a project with no repo name falls back to its directory")
}

// The view must show the whole flow, what the turns changed, and where the
// work is.
func TestSessionViewShowsTheTimelineTurnsAndWorkAndFitsTheScreen(t *testing.T) {
	s := liveSession("sess-1", watchd.SessionRunning, time.Now().Add(-time.Minute))
	s.s.Implement = &watchd.RoundFix{State: watchd.FixApplied, Insertions: 10,
		Files: []watchd.FileChange{{Path: "main.go", Insertions: 10}}}
	s.s.Rounds = []watchd.Round{{
		Number: 1, State: watchd.RoundDone, Findings: 3, Worth: 1,
		Reviews: []watchd.ReviewPrompt{{Name: "bugs", State: watchd.PromptDone}, {Name: "test", Kind: watchd.CheckValidate, State: watchd.PromptDone}},
		Fix: &watchd.RoundFix{State: watchd.FixApplied, Insertions: 4, Deletions: 1,
			Files: []watchd.FileChange{{Path: "app.go", Insertions: 4, Deletions: 1}}},
	}, {
		Number: 2, State: watchd.RoundReviewing,
		Reviews: []watchd.ReviewPrompt{{Name: "bugs", State: watchd.PromptRunning, SidecarID: "sc-1"}},
		Fix:     &watchd.RoundFix{State: watchd.FixRunning, Activity: "Edit app.go"},
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
		assert.Equal(t, strings.Count(out, "not built yet"), 4, "rebase, CI, approval and PR are shown as not built:\n%s", out)
		for _, want := range []string{"add a --verbose flag", "main.go", "app.go", "Round 1", "Round 2", "bugs", "$ test", "Edit app.go", "chunk/factory/sess-1"} {
			assert.Assert(t, strings.Contains(out, want), "missing %q:\n%s", want, out)
		}
	}
}

func TestSessionViewKeysQuitDetachesAndCancelNeedsConfirmation(t *testing.T) {
	m := sessModel(
		liveSession("run-1", watchd.SessionRunning, time.Now()),
		liveSession("run-2", watchd.SessionRunning, time.Now().Add(-time.Minute)),
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

	id, err := watchd.StartSession(watchd.SessionRequest{ProjectRoot: root, Task: "add a flag"})
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
	m, _ = press(m, 'r')
	assert.Assert(t, m.selectedSession() != nil)

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
