package watchd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
)

// startTestDaemon runs a real daemon over a real Unix socket in a temp dir and
// returns once it is answering. Mocking the transport here would test nothing:
// the socket, the routes and the JSON shapes are the whole point.
func startTestDaemon(t *testing.T) {
	t.Helper()
	startTestDaemonWithAuth(t, nil)
}

// startTestDaemonWithAuth is startTestDaemon with an explicit credential
// failure. Both run the daemon with a nil client: resolution is the caller's
// job now, so no test touches the developer's real keychain.
func startTestDaemonWithAuth(t *testing.T, authErr error) {
	t.Helper()
	// Not t.TempDir(): it embeds the test name, and a unix socket path is capped
	// at 104 bytes on darwin, so a descriptive name silently breaks listen.
	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_WATCHD_DIR", dir)
	// Keep the first poll hermetic: pointed at a real data dir it makes a git
	// call per known project and can outlast the wait below.
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- RunDaemon(ctx, nil, authErr) }()
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reachable, _ := ping(sockPath); reachable {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon did not become reachable within 5s")
}

// postCommand registers a command over the socket and returns the status code.
func postCommand(t *testing.T, body []byte) int {
	t.Helper()
	sockPath, err := SocketPath()
	assert.NilError(t, err)
	resp, err := unixClient(sockPath).Post("http://watchd/command", "application/json", bytes.NewReader(body))
	assert.NilError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestCommandRouteRejectsMissingID(t *testing.T) {
	startTestDaemon(t)

	body, err := json.Marshal(CommandReg{SidecarID: "sc-1", ProjectRoot: "/repo"})
	assert.NilError(t, err)
	code := postCommand(t, body)
	assert.Check(t, cmp.Equal(code, http.StatusBadRequest))
}

func TestCommandRouteRejectsGet(t *testing.T) {
	startTestDaemon(t)

	sockPath, err := SocketPath()
	assert.NilError(t, err)
	resp, err := unixClient(sockPath).Get("http://watchd/command")
	assert.NilError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusMethodNotAllowed))
}

func TestCommandRouteRejectsMalformedBody(t *testing.T) {
	startTestDaemon(t)
	code := postCommand(t, []byte("{not json"))
	assert.Check(t, cmp.Equal(code, http.StatusBadRequest))
}

// A command for a project the daemon has never polled must still be accepted:
// the process registering it knows about the project, and refusing would lose
// the output of the very first run in a fresh checkout.
func TestCommandRouteAcceptsUnknownProject(t *testing.T) {
	startTestDaemon(t)

	body, err := json.Marshal(CommandReg{
		CommandID:   "cmd-unknown-project",
		SidecarID:   "sc-1",
		ProjectRoot: "/definitely/not/a/polled/project",
	})
	assert.NilError(t, err)
	code := postCommand(t, body)
	assert.Check(t, cmp.Equal(code, http.StatusAccepted))
}

func TestCommandRouteAcceptsDuplicateRegistration(t *testing.T) {
	startTestDaemon(t)

	body, err := json.Marshal(CommandReg{
		CommandID:   "cmd-dup",
		SidecarID:   "sc-1",
		ProjectRoot: "/repo",
	})
	assert.NilError(t, err)

	// The second registration is a no-op rather than an error: a retrying caller
	// must not get a failure for a command the daemon already has.
	assert.Check(t, cmp.Equal(postCommand(t, body), http.StatusAccepted))
	assert.Check(t, cmp.Equal(postCommand(t, body), http.StatusAccepted))
}

func TestOutputRouteRequiresCommandID(t *testing.T) {
	startTestDaemon(t)

	sockPath, err := SocketPath()
	assert.NilError(t, err)
	resp, err := unixClient(sockPath).Get("http://watchd/output")
	assert.NilError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusBadRequest))
}

func TestOutputRouteRejectsNegativeOffset(t *testing.T) {
	startTestDaemon(t)

	sockPath, err := SocketPath()
	assert.NilError(t, err)
	resp, err := unixClient(sockPath).Get("http://watchd/output?command_id=x&offset=-1")
	assert.NilError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusBadRequest))
}

// An unknown command is an ordinary answer, not an error: the dashboard asking
// about a command the daemon has forgotten is expected after a restart.
func TestFetchOutputUnknownCommandIsNotFound(t *testing.T) {
	startTestDaemon(t)

	chunk, err := FetchOutput("no-such-command", 0)
	assert.NilError(t, err)
	assert.Check(t, !chunk.Found)
}

// The registration path must survive the daemon being absent without erroring,
// because it sits in front of a command the developer is waiting on.
func TestRegisterCommandIsBestEffortWithoutDaemon(t *testing.T) {
	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_WATCHD_DIR", dir)

	done := make(chan struct{})
	go func() {
		// No daemon is listening on this socket path at all.
		RegisterCommand(CommandReg{CommandID: "cmd-1", SidecarID: "sc-1", ProjectRoot: "/repo"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(registerTimeout + 3*time.Second):
		t.Fatal("RegisterCommand blocked with no daemon running")
	}
}

func TestSnapshotReportsNoAuthErrorWhenResolutionSucceeded(t *testing.T) {
	startTestDaemon(t)

	// Nothing failed, so the daemon must not claim an auth problem: a message
	// here would send someone re-running `chunk auth login` for no reason.
	snap, err := FetchSnapshot(nil)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(snap.AuthError, ""))
}

func TestSnapshotReportsAuthErrorWhenCredentialsAreMissing(t *testing.T) {
	startTestDaemonWithAuth(t, authprompt.ErrNeedsAuth)

	snap, err := FetchSnapshot(nil)
	assert.NilError(t, err)
	assert.Check(t, cmp.Contains(snap.AuthError, "chunk auth login"))
}

// The daemon is useful without credentials: it cannot stream output, but it can
// still say the command ran. Losing the registration too would leave the
// dashboard blank with nothing to explain it.
func TestCommandIsRecordedWithoutCredentials(t *testing.T) {
	startTestDaemonWithAuth(t, authprompt.ErrNeedsAuth)

	body, err := json.Marshal(CommandReg{
		CommandID:   "cmd-no-creds",
		SidecarID:   "sc-1",
		ProjectRoot: "/repo",
	})
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(postCommand(t, body), http.StatusAccepted))

	sockPath, err := SocketPath()
	assert.NilError(t, err)
	resp, err := unixClient(sockPath).Get("http://watchd/output?command_id=cmd-no-creds")
	assert.NilError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var chunk OutputChunk
	assert.NilError(t, json.NewDecoder(resp.Body).Decode(&chunk))
	assert.Check(t, chunk.Found)
	assert.Check(t, !chunk.Running)
	assert.Check(t, cmp.Contains(chunk.Error, "credentials"))
}
