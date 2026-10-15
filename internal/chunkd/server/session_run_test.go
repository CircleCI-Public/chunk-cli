package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

const testSecret = "sk-ant-super-secret-value"

// fakeBackend stands in for the exec API: it answers each submitted script
// with canned output. Nothing here touches CircleCI or Anthropic.
type fakeBackend struct {
	mu      sync.Mutex
	nextCmd int
}

func (f *fakeBackend) config() ReviewConfig {
	return ReviewConfig{
		Credential: claudecode.Credential{EnvVar: config.EnvAnthropicAPIKey, Value: testSecret},
		Submit: func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.nextCmd++
			return fmt.Sprintf("cmd-%d", f.nextCmd), nil
		},
		Stream: func(_ context.Context, _ *sidecar.PoolEntry, _ string, onOutput circleci.OutputFn) (int, error) {
			onOutput(circleci.StreamStdout, []byte("ok"))
			return 0, nil
		},
	}
}

// newSessionDaemon builds a daemon that tracks one real git repo holding two
// review prompts. It returns the daemon and the project's canonical root.
func newSessionDaemon(t *testing.T, backend *fakeBackend) (*daemon, string) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv("CHUNK_DAEMON_DIR", socketDir(t))

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

// waitForSessionEnd waits for a session to finish and settle.
func waitForSessionEnd(t *testing.T, d *daemon, id string) chunkd.SessionDetail {
	t.Helper()
	entry := d.sessions.get(id)
	assert.Assert(t, entry != nil, "no session %s", id)
	waitUntil(t, "session to end", func() bool { return entry.snapshot().State.Finished() })
	<-entry.done
	return entry.detail()
}

func TestSessionSnapshotHasStateButNeitherTextNorCredential(t *testing.T) {
	d, root := newFactoryDaemon(t, scriptedRun(factory.ResultPassed))
	sess, err := d.startFactory(chunkd.FactoryRequest{ProjectRoot: root, Prompt: "add a flag"})
	assert.NilError(t, err)
	waitForSessionEnd(t, d, sess.ID)
	d.poll()

	snap := d.snapshot(nil)
	assert.Equal(t, len(snap.Projects[0].Sessions), 1)
	raw, err := json.Marshal(snap)
	assert.NilError(t, err)
	// "nil deref" is the body of the finding scriptedRun's review reports.
	assert.Assert(t, !strings.Contains(string(raw), "nil deref"), "review text leaked into the snapshot")
	assert.Assert(t, !strings.Contains(string(raw), testSecret), "credential leaked into the snapshot")

	detailRaw, err := json.Marshal(d.sessions.get(sess.ID).detail())
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(detailRaw), "nil deref"))
	assert.Assert(t, !strings.Contains(string(detailRaw), testSecret), "credential leaked into the session detail")
}

func TestFactoryRefusesRequestsItCannotStart(t *testing.T) {
	d, root := newFactoryDaemon(t, scriptedRun(factory.ResultPassed))

	cases := map[string]struct {
		req    chunkd.FactoryRequest
		status int
	}{
		"unknown project":  {chunkd.FactoryRequest{ProjectRoot: t.TempDir(), Prompt: "x"}, http.StatusNotFound},
		"no prompt":        {chunkd.FactoryRequest{ProjectRoot: root, Prompt: "  "}, http.StatusBadRequest},
		"absolute reviews": {chunkd.FactoryRequest{ProjectRoot: root, Prompt: "x", ReviewsDir: "/etc"}, http.StatusBadRequest},
		"escaping reviews": {chunkd.FactoryRequest{ProjectRoot: root, Prompt: "x", ReviewsDir: "../elsewhere"}, http.StatusBadRequest},
		"relative log":     {chunkd.FactoryRequest{ProjectRoot: root, Prompt: "x", Log: "run.log"}, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := d.startFactory(tc.req)
			var ae *apiError
			assert.Assert(t, errors.As(err, &ae), "got %v", err)
			assert.Equal(t, ae.status, tc.status, ae.msg)
		})
	}

	// Without a credential no session starts, and the reason is the daemon's own.
	d.rcfg.Credential = claudecode.Credential{}
	d.rcfg.AuthError = "no Claude credential — run: chunk auth set anthropic-oauth"
	_, err := d.startFactory(chunkd.FactoryRequest{ProjectRoot: root, Prompt: "x"})
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae))
	assert.Equal(t, ae.status, http.StatusServiceUnavailable)
	assert.Equal(t, d.snapshot(nil).ReviewAuthError, d.rcfg.AuthError)
}

// The HTTP surface over the real Unix socket, with the client the CLI uses.
func TestSessionAPIRoundTripOverTheSocket(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	dir := initRepo(t)
	chunkDir := filepath.Join(dir, ".chunk")
	assert.NilError(t, os.MkdirAll(filepath.Join(chunkDir, "reviews"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "reviews", "bugs.md"), []byte("review"), 0o644))
	cfg := `{"orgID":"org-1","commands":[{"name":"test","run":"go test ./..."}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(cfg), 0o644))
	dataDir, err := config.ProjectDataDir(dir)
	assert.NilError(t, err)
	assert.NilError(t, sidecar.RegisterProjectRoot(dataDir, dir))
	root, err := filepath.EvalSymlinks(dir)
	assert.NilError(t, err)

	rcfg := (&fakeBackend{}).config()
	rcfg.RunFactory = scriptedRun(factory.ResultPassed)
	startTestDaemonWithReview(t, rcfg)

	id, err := chunkd.StartFactory(chunkd.FactoryRequest{ProjectRoot: root, Prompt: "add a flag"})
	assert.NilError(t, err)
	var detail chunkd.SessionDetail
	waitUntil(t, "session to end", func() bool {
		detail, err = chunkd.FetchSession(id)
		return err == nil && detail.State.Finished()
	})
	assert.Equal(t, detail.State, chunkd.SessionDone)

	all, err := chunkd.ListSessions("")
	assert.NilError(t, err)
	assert.Equal(t, len(all), 1)

	// A refusal is a readable answer, not "daemon unavailable".
	_, err = chunkd.StartFactory(chunkd.FactoryRequest{ProjectRoot: t.TempDir(), Prompt: "add a flag"})
	var refused *chunkd.SessionRefused
	assert.Assert(t, errors.As(err, &refused), "got %v", err)
	assert.Equal(t, refused.Status, http.StatusNotFound)
	assert.Assert(t, !errors.Is(err, chunkd.ErrDaemonUnavailable))
	assert.NilError(t, chunkd.CancelSession(id))
}

// startTestDaemonWithReview runs a real daemon on a Unix socket with sessions
// configured, waiting for it to answer.
func startTestDaemonWithReview(t *testing.T, cfg ReviewConfig) {
	t.Helper()
	t.Setenv("CHUNK_DAEMON_DIR", socketDir(t))

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
	sockPath, err := chunkd.SocketPath()
	assert.NilError(t, err)
	waitUntil(t, "daemon to answer", func() bool {
		reachable, _ := ping(sockPath)
		return reachable
	})
}

// outputRequestCount counts requests to the command output endpoint, so a test
// can pin how many times a command's output was actually fetched.
func outputRequestCount(cci *fakes.FakeCircleCI) int {
	n := 0
	for _, req := range cci.Recorder.AllRequests() {
		if strings.HasSuffix(req.URL.Path, "/output") {
			n++
		}
	}
	return n
}

// TestExecerForRegisteredCommandStreamsOutputOnce guards against fetching a
// registered command's output twice: once for the output store's buffer and
// once for the caller, both from the same cursor.
func TestExecerForRegisteredCommandStreamsOutputOnce(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{CommandID: "cmd-1", Stdout: "hello\n", ExitCode: 0}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	d := newTestDaemon()
	pe := &sidecar.PoolEntry{ID: "sc-1", Client: newTestClient(t, srv.URL)}
	exec := d.execerFor("/repo", func(string, string) string { return "step" })

	var got strings.Builder
	code, err := exec(context.Background(), pe, "echo hello", nil, func(_ string, data []byte) {
		got.Write(data)
	}, nil)
	assert.NilError(t, err)
	assert.Equal(t, code, 0)
	assert.Equal(t, got.String(), "hello\n")

	chunk := d.out.read("cmd-1", 0)
	assert.Assert(t, chunk.Found)
	assert.Equal(t, string(chunk.Data), "hello\n")

	assert.Equal(t, outputRequestCount(cci), 1,
		"a registered command's output must be streamed once, not once per consumer")
}

// TestExecerForRegisteredCommandPropagatesStreamFailure pins the other half of
// sharing one stream between two consumers: the exec caller must see the same
// failure the output store's buffer records, not a clean exit.
func TestExecerForRegisteredCommandPropagatesStreamFailure(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.CommandOutputStatusCode = http.StatusGone
	cci.CommandOutputMessage = "sidecar is out of date"
	srv := httptest.NewServer(cci)
	defer srv.Close()

	d := newTestDaemon()
	pe := &sidecar.PoolEntry{ID: "sc-1", Client: newTestClient(t, srv.URL)}
	exec := d.execerFor("/repo", func(string, string) string { return "step" })

	_, err := exec(context.Background(), pe, "echo hello", nil, func(string, []byte) {}, nil)
	assert.Assert(t, err != nil, "a failed stream must not be reported to the caller as a clean exit")
	assert.Assert(t, strings.Contains(err.Error(), "out of date"), "got %q", err)

	chunk := d.out.read("cmd-123", 0)
	assert.Assert(t, chunk.Found)
	assert.Assert(t, strings.Contains(chunk.Error, "out of date"),
		"the output store's buffer must record the same failure, got %q", chunk.Error)
}

// TestExecerForUnregisteredCommandStillStreamsOutput pins the other half of the
// fix: a command with no log-pane name skips the output store, but the caller
// must still get its output from the one stream that does happen.
func TestExecerForUnregisteredCommandStillStreamsOutput(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{CommandID: "cmd-1", Stdout: "hello\n", ExitCode: 0}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	d := newTestDaemon()
	pe := &sidecar.PoolEntry{ID: "sc-1", Client: newTestClient(t, srv.URL)}
	exec := d.execerFor("/repo", func(string, string) string { return "" })

	var got strings.Builder
	code, err := exec(context.Background(), pe, "echo hello", nil, func(_ string, data []byte) {
		got.Write(data)
	}, nil)
	assert.NilError(t, err)
	assert.Equal(t, code, 0)
	assert.Equal(t, got.String(), "hello\n")

	assert.Equal(t, outputRequestCount(cci), 1)
}
