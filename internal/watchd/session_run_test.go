package watchd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

const testSecret = "sk-ant-super-secret-value"

// fakeBackend stands in for the sandbox pool and the exec API: it hands out fake
// pool members and answers each submitted script with canned output. Nothing
// here touches CircleCI or Anthropic.
type fakeBackend struct {
	mu      sync.Mutex
	nextCmd int
	scripts map[string]string
	// respond decides the output and exit code for a script.
	respond func(script string) (string, int)
	// block makes every command wait until its context is cancelled.
	block bool
	pools []ReviewPoolSpec
}

func (f *fakeBackend) config() ReviewConfig {
	return ReviewConfig{
		Credential: review.Credential{EnvVar: config.EnvAnthropicAPIKey, Value: testSecret},
		NewPool: func(_ context.Context, spec ReviewPoolSpec) (*ReviewPool, error) {
			f.mu.Lock()
			f.pools = append(f.pools, spec)
			f.mu.Unlock()
			free := make(chan *sidecar.PoolEntry, spec.Size)
			for i := range spec.Size {
				free <- &sidecar.PoolEntry{ID: fmt.Sprintf("sc-%d", i+1), RepoPath: "/work/repo"}
			}
			return &ReviewPool{
				Acquire: func(ctx context.Context) (*sidecar.PoolEntry, error) {
					select {
					case e := <-free:
						return e, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
				Release:   func(e *sidecar.PoolEntry) { free <- e },
				WaitReady: func(context.Context) error { return nil },
				Close:     func(context.Context) {},
			}, nil
		},
		Submit: func(_ context.Context, _ *sidecar.PoolEntry, script string, _ map[string]string) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.nextCmd++
			id := fmt.Sprintf("cmd-%d", f.nextCmd)
			if f.scripts == nil {
				f.scripts = map[string]string{}
			}
			f.scripts[id] = script
			return id, nil
		},
		Stream: func(ctx context.Context, _ *sidecar.PoolEntry, id string, onOutput circleci.OutputFn) (int, error) {
			if f.block {
				<-ctx.Done()
				return 0, ctx.Err()
			}
			f.mu.Lock()
			script := f.scripts[id]
			f.mu.Unlock()
			out, code := "ok", 0
			if f.respond != nil {
				out, code = f.respond(script)
			}
			onOutput(circleci.StreamStdout, []byte(out))
			return code, nil
		},
	}
}

// newSessionDaemon builds a daemon that tracks one real git repo holding two
// review prompts. It returns the daemon and the project's canonical root.
func newSessionDaemon(t *testing.T, backend *fakeBackend) (*daemon, string) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv("CHUNK_WATCHD_DIR", socketDir(t))

	dir := initRepo(t)
	prompts := filepath.Join(dir, ".chunk", "reviews")
	assert.NilError(t, os.MkdirAll(prompts, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(prompts, "bugs.md"), []byte("find bugs"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(prompts, "style.md"), []byte("check style"), 0o644))

	dataDir, err := config.ProjectDataDir(dir)
	assert.NilError(t, err)
	assert.NilError(t, sidecar.RegisterProjectRoot(dataDir, dir))
	root, err := filepath.EvalSymlinks(dir)
	assert.NilError(t, err)

	d := newTestDaemon()
	d.rcfg = backend.config()
	d.poll()
	t.Cleanup(d.sessions.stopAll)
	return d, root
}

// waitForSession waits until the session has ended or paused.
func waitForSession(t *testing.T, d *daemon, id string) SessionDetail {
	t.Helper()
	entry := d.sessions.get(id)
	assert.Assert(t, entry != nil, "no session %s", id)
	waitUntil(t, "session to settle", func() bool {
		st := entry.snapshot().State
		return st != SessionRunning
	})
	if entry.snapshot().State != SessionPaused {
		<-entry.done
	}
	return entry.detail()
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSessionRecordsTheRoundAndEveryStageOfTheFlow(t *testing.T) {
	d, root := newSessionDaemon(t, &fakeBackend{})

	sess, err := d.startSession(SessionRequest{ProjectRoot: root})
	assert.NilError(t, err)
	assert.Equal(t, sess.Branch, "main")
	assert.Equal(t, len(sess.HeadSHA), 40)
	detail := waitForSession(t, d, sess.ID)

	assert.Equal(t, detail.State, SessionDone)
	// The whole flow is on the record from the start; only the loop is built.
	var stages []string
	for _, s := range detail.Stages {
		stages = append(stages, string(s.ID)+"="+string(s.State))
	}
	assert.DeepEqual(t, stages, []string{
		"review_loop=done", "rebase=not_built", "ci=not_built", "approval=not_built", "pr=not_built",
	})

	assert.Equal(t, len(detail.Rounds), 1)
	round := detail.Rounds[0]
	assert.Equal(t, round.Number, 1)
	assert.Equal(t, round.State, RoundDone)
	assert.Equal(t, len(round.Reviews), 2)
	for _, p := range round.Reviews {
		assert.Equal(t, p.State, PromptDone, p.Name)
		assert.Assert(t, p.SidecarID != "" && p.CommandID != "", "review %s has no sandbox or command", p.Name)
	}
	assert.Equal(t, len(detail.Details[0].Results), 2)

	// Each review's log is reachable through the output store.
	cmds := d.out.commandsFor(root)
	assert.Equal(t, len(cmds), 2)
	for _, c := range cmds {
		assert.Assert(t, strings.HasPrefix(c.Name, "round 1 review: "), c.Name)
	}
}

func TestSessionSnapshotHasStateButNeitherTextNorCredential(t *testing.T) {
	d, root := newSessionDaemon(t, &fakeBackend{respond: func(string) (string, int) { return "LONG-REVIEW-PROSE", 0 }})
	sess, err := d.startSession(SessionRequest{ProjectRoot: root})
	assert.NilError(t, err)
	waitForSession(t, d, sess.ID)
	d.poll()

	snap := d.snapshot(nil)
	assert.Equal(t, len(snap.Projects[0].Sessions), 1)
	raw, err := json.Marshal(snap)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(raw), "LONG-REVIEW-PROSE"), "review text leaked into the snapshot")
	assert.Assert(t, !strings.Contains(string(raw), testSecret), "credential leaked into the snapshot")

	detailRaw, err := json.Marshal(d.sessions.get(sess.ID).detail())
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(detailRaw), "LONG-REVIEW-PROSE"))
	assert.Assert(t, !strings.Contains(string(detailRaw), testSecret), "credential leaked into the session detail")
}

func TestSessionCancelStopsReviewsAndMarksThemCancelled(t *testing.T) {
	d, root := newSessionDaemon(t, &fakeBackend{block: true})
	sess, err := d.startSession(SessionRequest{ProjectRoot: root})
	assert.NilError(t, err)
	waitUntil(t, "a review to be running", func() bool {
		rounds := d.sessions.get(sess.ID).snapshot().Rounds
		return len(rounds) > 0 && len(rounds[0].Reviews) > 0 && rounds[0].Reviews[0].State == PromptRunning
	})

	// A second session for the same project is refused while one is active.
	_, err = d.startSession(SessionRequest{ProjectRoot: root})
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae), "got %v", err)
	assert.Equal(t, ae.status, http.StatusConflict)

	found, active := d.sessions.cancelSession(sess.ID)
	assert.Assert(t, found && active)
	detail := waitForSession(t, d, sess.ID)

	assert.Equal(t, detail.State, SessionCancelled)
	assert.Equal(t, detail.Stages[0].State, StageFailed)
	assert.Assert(t, detail.EndedAt != nil)
	for _, p := range detail.Rounds[0].Reviews {
		assert.Equal(t, p.State, PromptFailed)
		assert.Equal(t, p.Error, "cancelled")
	}
	// Cancelling again, or an unknown session, is harmless.
	_, active = d.sessions.cancelSession(sess.ID)
	assert.Assert(t, !active)
	found, _ = d.sessions.cancelSession("nope")
	assert.Assert(t, !found)
}

func TestSessionRefusesRequestsItCannotStart(t *testing.T) {
	d, root := newSessionDaemon(t, &fakeBackend{})
	assert.NilError(t, os.MkdirAll(filepath.Join(root, "empty"), 0o755))

	cases := map[string]struct {
		req    SessionRequest
		status int
	}{
		"unknown project":   {SessionRequest{ProjectRoot: t.TempDir()}, http.StatusNotFound},
		"absolute prompts":  {SessionRequest{ProjectRoot: root, PromptsDir: "/etc"}, http.StatusBadRequest},
		"escaping prompts":  {SessionRequest{ProjectRoot: root, PromptsDir: "../elsewhere"}, http.StatusBadRequest},
		"no prompts in dir": {SessionRequest{ProjectRoot: root, PromptsDir: "empty"}, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := d.startSession(tc.req)
			var ae *apiError
			assert.Assert(t, errors.As(err, &ae), "got %v", err)
			assert.Equal(t, ae.status, tc.status, ae.msg)
		})
	}

	// Without a credential no session starts, and the reason is the daemon's own.
	d.rcfg.Credential = review.Credential{}
	d.rcfg.AuthError = "no Claude credential — run: chunk auth set anthropic-oauth"
	_, err := d.startSession(SessionRequest{ProjectRoot: root})
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae))
	assert.Equal(t, ae.status, http.StatusServiceUnavailable)
	assert.Equal(t, d.snapshot(nil).ReviewAuthError, d.rcfg.AuthError)
}

// The HTTP surface over the real Unix socket, with the client the CLI uses.
func TestSessionAPIRoundTripOverTheSocket(t *testing.T) {
	backend := &fakeBackend{}
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	dir := initRepo(t)
	prompts := filepath.Join(dir, ".chunk", "reviews")
	assert.NilError(t, os.MkdirAll(prompts, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(prompts, "only.md"), []byte("review"), 0o644))
	dataDir, err := config.ProjectDataDir(dir)
	assert.NilError(t, err)
	assert.NilError(t, sidecar.RegisterProjectRoot(dataDir, dir))
	root, err := filepath.EvalSymlinks(dir)
	assert.NilError(t, err)

	startTestDaemonWithReview(t, backend.config())

	id, err := StartSession(SessionRequest{ProjectRoot: root})
	assert.NilError(t, err)
	var detail SessionDetail
	waitUntil(t, "session to end", func() bool {
		detail, err = FetchSession(id)
		return err == nil && detail.State.Finished()
	})
	assert.Equal(t, detail.State, SessionDone)

	all, err := ListSessions("")
	assert.NilError(t, err)
	assert.Equal(t, len(all), 1)

	// A refusal is a readable answer, not "daemon unavailable".
	_, err = StartSession(SessionRequest{ProjectRoot: t.TempDir()})
	var refused *SessionRefused
	assert.Assert(t, errors.As(err, &refused), "got %v", err)
	assert.Equal(t, refused.Status, http.StatusNotFound)
	assert.Assert(t, !errors.Is(err, ErrDaemonUnavailable))
	assert.NilError(t, CancelSession(id))
}

// startTestDaemonWithReview runs a real daemon on a Unix socket with sessions
// configured, waiting for it to answer.
func startTestDaemonWithReview(t *testing.T, cfg ReviewConfig) {
	t.Helper()
	t.Setenv("CHUNK_WATCHD_DIR", socketDir(t))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- RunDaemon(ctx, nil, "", nil, nil, WithReview(cfg)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not shut down within 5s")
		}
	})
	sockPath, err := SocketPath()
	assert.NilError(t, err)
	waitUntil(t, "daemon to answer", func() bool {
		reachable, _ := ping(sockPath)
		return reachable
	})
}
