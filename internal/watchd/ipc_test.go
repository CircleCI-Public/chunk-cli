package watchd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// startTestDaemon runs a real daemon over a real Unix socket in a temp dir and
// returns once it is answering. Mocking the transport here would test nothing:
// the socket, the routes and the JSON shapes are the whole point.
func startTestDaemon(t *testing.T) {
	t.Helper()
	startTestDaemonWithAuth(t, "")
}

// startTestDaemonWithAuth is startTestDaemon with an explicit credential
// failure. Both run the daemon with a nil client: resolution is the caller's
// job now, so no test touches the developer's real keychain.
// What the cmd layer renders for a missing credential. Duplicated as a literal
// rather than imported: watchd must not depend on the auth flow, and pinning
// the daemon's own behaviour does not need the real message, only a message.
const testAuthMessage = "not authenticated to CircleCI — command output unavailable (run: chunk auth login)"

// socketDir is a temp dir whose name is short enough to hold a unix socket
// path. Not t.TempDir(): it embeds the test name, and a unix socket path is
// capped at 104 bytes on darwin, so a descriptive name silently breaks listen —
// and makes a dial fail with EINVAL before it can fail for the reason a test is
// actually about.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func startTestDaemonWithAuth(t *testing.T, authMessage string) {
	t.Helper()
	dir := socketDir(t)
	t.Setenv("CHUNK_WATCHD_DIR", dir)
	// Keep the first poll hermetic: pointed at a real data dir it makes a git
	// call per known project and can outlast the wait below.
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- RunDaemon(ctx, nil, authMessage, nil) }()
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
	dir := socketDir(t)
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
	startTestDaemonWithAuth(t, testAuthMessage)

	snap, err := FetchSnapshot(nil)
	assert.NilError(t, err)
	assert.Check(t, cmp.Contains(snap.AuthError, "chunk auth login"))
}

// The daemon is useful without credentials: it cannot stream output, but it can
// still say the command ran. Losing the registration too would leave the
// dashboard blank with nothing to explain it.
func TestCommandIsRecordedWithoutCredentials(t *testing.T) {
	startTestDaemonWithAuth(t, testAuthMessage)

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

func TestConflictsEndpointRequiresRoot(t *testing.T) {
	startTestDaemon(t)
	sockPath, err := SocketPath()
	assert.NilError(t, err)

	resp, err := unixClient(sockPath).Get("http://watchd/conflicts")
	assert.NilError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusBadRequest))
}

func TestConflictsEndpointReportsUnknownRootAsUnknown(t *testing.T) {
	// The distinction the endpoint exists to carry: a root the daemon does not
	// track is "no answer", not "no conflicts". Collapsing them would have a
	// hook tell an agent a branch is clean when nothing ever looked.
	startTestDaemon(t)

	report, err := FetchConflicts(t.TempDir())
	assert.NilError(t, err)
	assert.Check(t, !report.Known)
	assert.Check(t, cmp.Nil(report.Conflict))
	// And nothing is advised on the back of it.
	notice := ConflictNotice(report)
	assert.Check(t, cmp.Equal(notice, ""))
}

func TestConflictsEndpointServesStoredState(t *testing.T) {
	startTestDaemon(t)

	root := t.TempDir()
	d := &daemon{projects: map[string]*projectState{
		root: {
			root:      root,
			canonRoot: canonicalRoot(root),
			conflict: &ConflictState{
				Branch: "feature", Target: "origin/main",
				Conflicted: true, Paths: []string{"a.go"}, TotalPaths: 1,
			},
		},
	}}

	report := d.conflictReport(root)
	assert.Check(t, report.Known)
	assert.Assert(t, report.Conflict != nil)
	assert.Check(t, report.Conflict.Conflicted)
	assert.Check(t, cmp.DeepEqual(report.Conflict.Paths, []string{"a.go"}))
}

func TestConflictReportMatchesASymlinkedRoot(t *testing.T) {
	// Two callers can name the same project by different paths — git's
	// --show-toplevel resolves symlinks, a shell's $PWD does not. Matching only
	// the literal string would report the project as unknown.
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	err := os.Symlink(target, link)
	assert.NilError(t, err)

	d := &daemon{projects: map[string]*projectState{
		target: {root: target, canonRoot: canonicalRoot(target), conflict: &ConflictState{Branch: "feature"}},
	}}

	report := d.conflictReport(link)
	assert.Check(t, report.Known, "a symlinked root must resolve to the same project")
	assert.Assert(t, report.Conflict != nil)
	assert.Check(t, cmp.Equal(report.Conflict.Branch, "feature"))
}

// hangingDaemon listens on the daemon socket and never answers, which is what
// a daemon busy elsewhere looks like from the client side.
func hangingDaemon(t *testing.T) {
	t.Helper()
	// Not t.TempDir(): a unix socket path is capped at 104 bytes on darwin and
	// the test name pushes a temp dir past it.
	dir := socketDir(t)
	t.Setenv("CHUNK_WATCHD_DIR", dir)

	ln, err := net.Listen("unix", filepath.Join(dir, "watchd.sock"))
	assert.NilError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			// Hold the connection open without replying. Closing it would be a
			// refusal, which is the case this test exists to be distinct from.
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
}

func TestFetchConflictsSeparatesASlowDaemonFromAMissingOne(t *testing.T) {
	// A daemon that accepts and then stalls must not be reported as absent. The
	// advice differs: one says start it, the other says it is already up.
	hangingDaemon(t)

	_, err := FetchConflicts(t.TempDir())
	assert.Check(t, errors.Is(err, ErrDaemonTimeout), "want ErrDaemonTimeout, got: %v", err)
	assert.Check(t, !errors.Is(err, ErrDaemonUnreachable),
		"a stalled daemon must not be reported as no daemon at all")
}

func TestFetchConflictsReportsAMissingDaemonAsUnreachable(t *testing.T) {
	// The counterpart, so the split above cannot be satisfied by calling
	// everything a timeout.
	t.Setenv("CHUNK_WATCHD_DIR", socketDir(t))

	_, err := FetchConflicts(t.TempDir())
	assert.Check(t, errors.Is(err, ErrDaemonUnreachable), "want ErrDaemonUnreachable, got: %v", err)
	assert.Check(t, !errors.Is(err, ErrDaemonTimeout))
}

// A socket this user cannot open is not a socket that is missing. The advice
// attached to ErrDaemonUnreachable is to go start a daemon, which is the one
// thing that cannot help: a second daemon would leave this socket just as
// unreadable.
func TestFetchConflictsSeparatesAnUnreadableSocketFromAMissingOne(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the denial cannot be staged")
	}
	dir := socketDir(t)
	// Chmod the directory rather than the socket: BSD-derived kernels have not
	// always enforced the mode on the socket file itself, but path resolution
	// through an unsearchable directory denies everywhere.
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "watchd.sock"), nil, 0o600))
	assert.NilError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	t.Setenv("CHUNK_WATCHD_DIR", dir)

	_, err := FetchConflicts(t.TempDir())
	assert.Check(t, errors.Is(err, ErrDaemonPermission), "want ErrDaemonPermission, got: %v", err)
	assert.Check(t, !errors.Is(err, ErrDaemonUnreachable),
		"an unreadable socket must not be reported as no daemon at all")
	assert.Check(t, cmp.Contains(err.Error(), filepath.Join(dir, "watchd.sock")),
		"the message must name the socket, since fixing this means looking at it")
}

// The sentinels carry advice, so what maps onto them is worth pinning even for
// the causes a test cannot stage for real.
func TestClassifyDialErrorMapsCausesOntoAdvice(t *testing.T) {
	// The shape net/http actually returns: the syscall wrapped three deep.
	dialErr := func(e error) error {
		return &neturl.Error{Op: "Get", Err: &net.OpError{
			Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", e),
		}}
	}
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"nothing listening on a stale socket", dialErr(syscall.ECONNREFUSED), ErrDaemonUnreachable},
		{"no socket at all", dialErr(syscall.ENOENT), ErrDaemonUnreachable},
		{"socket owned by another user", dialErr(syscall.EACCES), ErrDaemonPermission},
		{"not permitted", dialErr(syscall.EPERM), ErrDaemonPermission},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDialError(tc.err, "/tmp/watchd.sock")
			assert.Check(t, errors.Is(got, tc.want), "want %v, got: %v", tc.want, got)
		})
	}
}

// Anything unrecognised keeps its own text. Reporting it as an absent daemon
// would hand the reader advice that is wrong and that nothing on screen would
// let them doubt.
func TestClassifyDialErrorDoesNotCallAnUnknownFailureAMissingDaemon(t *testing.T) {
	got := classifyDialError(errors.New("boom"), "/tmp/watchd.sock")

	assert.Check(t, !errors.Is(got, ErrDaemonUnreachable))
	assert.Check(t, !errors.Is(got, ErrDaemonPermission))
	assert.Check(t, !errors.Is(got, ErrDaemonTimeout))
	assert.Check(t, cmp.Contains(got.Error(), "boom"))
	assert.Check(t, cmp.Contains(got.Error(), "/tmp/watchd.sock"))
}

func TestConflictReportIsKnownWithNoAnswerBeforeTheFirstCheck(t *testing.T) {
	// The state the daemon is in for firstConflictDelay after it starts: the
	// project is tracked, ps.conflict is still nil. Known must be true anyway —
	// the daemon does know the project — while Conflict stays nil, because no
	// check has run. Collapsing either way loses a distinction the callers rely
	// on: Known false sends someone to register a project already registered,
	// and a non-nil Conflict would be an answer nobody computed.
	root := t.TempDir()
	d := &daemon{projects: map[string]*projectState{
		root: {root: root, canonRoot: canonicalRoot(root)},
	}}

	report := d.conflictReport(root)
	assert.Check(t, report.Known, "a tracked project is known before its first check")
	assert.Check(t, cmp.Nil(report.Conflict), "no check has run, so there is no answer to report")

	// And nothing is said or claimed on the back of it.
	assert.Check(t, cmp.Equal(ConflictNotice(report), ""))
	assert.Check(t, cmp.Contains(ConflictStatus(report), "No conflict check has completed"))
}
