package chunkd

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestBuildID_distinguishesBuildsTheVersionCannot(t *testing.T) {
	mod := time.Unix(1_700_000_000, 0)

	// Every local build reports the same development version, so the version
	// alone would call two different binaries the same daemon.
	same := buildID("dev", "/usr/local/bin/chunk", 100, mod)
	assert.Equal(t, same, buildID("dev", "/usr/local/bin/chunk", 100, mod))

	// A rebuild in place: same path, new size and timestamp.
	assert.Assert(t, buildID("dev", "/usr/local/bin/chunk", 101, mod) != same)
	assert.Assert(t, buildID("dev", "/usr/local/bin/chunk", 100, mod.Add(time.Second)) != same)

	// A dev build alongside an installed one.
	assert.Assert(t, buildID("dev", "/repo/dist/chunk", 100, mod) != same)

	// A tagged release differs from a dev build of the same binary.
	assert.Assert(t, buildID("v1.2.3", "/usr/local/bin/chunk", 100, mod) != same)
}

func TestBuildID_fallsBackToTheVersionWithoutAnExecutable(t *testing.T) {
	assert.Equal(t, buildID("v1.2.3", "", 0, time.Time{}), "v1.2.3")
}

func TestBuildID_survivesTheBinaryBeingRebuiltUnderIt(t *testing.T) {
	// The daemon runs for days across rebuilds of the binary that launched it.
	// Re-reading the executable per call would have it answer /ping with the
	// identity of whatever replaced it, match a client built from that same
	// file, and never be recognised as stale.
	exe, err := os.Executable()
	assert.NilError(t, err)
	fi, err := os.Stat(exe)
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.Chtimes(exe, time.Now(), fi.ModTime()) })

	before := BuildID()
	assert.NilError(t, os.Chtimes(exe, time.Now(), fi.ModTime().Add(time.Hour)))
	assert.Equal(t, before, BuildID())

	// And the frozen stat really is the live file's, so the assertion above is
	// about caching rather than about a stat that never worked.
	assert.Assert(t, statExecutable().mod != executableAtStartup.mod)
}

// A daemon that predates the identity answers /ping with an empty body, so the
// client must read that as a mismatch and replace it rather than trust it.
func TestPing_emptyBuildIsAMismatch(t *testing.T) {
	assert.Assert(t, BuildID() != "", "BuildID must never be empty, or a stale daemon would look current")
}

// Best-effort: with nothing running there is nothing to stop, and a login must
// not fail because of it.
func TestStopForCredentialChangeIsANoopWithNoDaemon(t *testing.T) {
	t.Setenv("CHUNK_DAEMON_DIR", t.TempDir())
	StopForCredentialChange()
}

// The registration path must survive the daemon being absent without erroring,
// because it sits in front of a command the developer is waiting on.
func TestRegisterCommandIsBestEffortWithoutDaemon(t *testing.T) {
	dir := socketDir(t)
	t.Setenv("CHUNK_DAEMON_DIR", dir)

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

func TestEnsureRunning_SkipsInRemoteMode(t *testing.T) {
	t.Setenv("CHUNK_DAEMON_DIR", t.TempDir())
	// Point CHUNK_DAEMON_REMOTE_ADDR at a non-existent host so that any attempt
	// to touch a local daemon would clearly succeed (no local state exists).
	t.Setenv("CHUNK_DAEMON_REMOTE_ADDR", "127.0.0.1:9")
	// If the remote guard is missing, EnsureRunning will call launchDaemon which
	// will fail on the missing executable path — the test would not return nil.
	err := EnsureRunning()
	assert.NilError(t, err)
}

func TestEnsureLaunched_SkipsInRemoteMode(t *testing.T) {
	t.Setenv("CHUNK_DAEMON_DIR", t.TempDir())
	t.Setenv("CHUNK_DAEMON_REMOTE_ADDR", "127.0.0.1:9")
	err := EnsureLaunched()
	assert.NilError(t, err)
}

// hangingDaemon listens on the daemon socket and never answers, which is what
// a daemon busy elsewhere looks like from the client side.
func hangingDaemon(t *testing.T) {
	t.Helper()
	// Not t.TempDir(): a unix socket path is capped at 104 bytes on darwin and
	// the test name pushes a temp dir past it.
	dir := socketDir(t)
	t.Setenv("CHUNK_DAEMON_DIR", dir)

	ln, err := net.Listen("unix", filepath.Join(dir, "daemon.sock"))
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
	t.Setenv("CHUNK_DAEMON_DIR", socketDir(t))

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
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "daemon.sock"), nil, 0o600))
	assert.NilError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	t.Setenv("CHUNK_DAEMON_DIR", dir)

	_, err := FetchConflicts(t.TempDir())
	assert.Check(t, errors.Is(err, ErrDaemonPermission), "want ErrDaemonPermission, got: %v", err)
	assert.Check(t, !errors.Is(err, ErrDaemonUnreachable),
		"an unreadable socket must not be reported as no daemon at all")
	assert.Check(t, cmp.Contains(err.Error(), filepath.Join(dir, "daemon.sock")),
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
			got := classifyDialError(tc.err, "/tmp/daemon.sock")
			assert.Check(t, errors.Is(got, tc.want), "want %v, got: %v", tc.want, got)
		})
	}
}

// Anything unrecognised keeps its own text. Reporting it as an absent daemon
// would hand the reader advice that is wrong and that nothing on screen would
// let them doubt.
func TestClassifyDialErrorDoesNotCallAnUnknownFailureAMissingDaemon(t *testing.T) {
	got := classifyDialError(errors.New("boom"), "/tmp/daemon.sock")

	assert.Check(t, !errors.Is(got, ErrDaemonUnreachable))
	assert.Check(t, !errors.Is(got, ErrDaemonPermission))
	assert.Check(t, !errors.Is(got, ErrDaemonTimeout))
	assert.Check(t, cmp.Contains(got.Error(), "boom"))
	assert.Check(t, cmp.Contains(got.Error(), "/tmp/daemon.sock"))
}

func TestSocketPathTooLongIsReportedUpFront(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", maxSocketPath()))
	t.Setenv("CHUNK_DAEMON_DIR", long)

	_, err := SocketPath()
	assert.ErrorContains(t, err, "too long")
	assert.ErrorContains(t, err, "CHUNK_DAEMON_DIR")
	assert.ErrorContains(t, err, long)

	// The launch path fails the same way, before any daemon is spawned.
	err = EnsureRunning()
	assert.ErrorContains(t, err, "CHUNK_DAEMON_DIR")

	t.Setenv("CHUNK_DAEMON_DIR", socketDir(t))
	_, err = SocketPath()
	assert.NilError(t, err)
}

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

// fakeDaemon stands in for a running daemon: a pid file naming this process and
// a socket that answers /ping with build. It returns a count of launches the
// client attempts while it is up.
func fakeDaemon(t *testing.T, build string) *int {
	t.Helper()
	dir := socketDir(t)
	t.Setenv("CHUNK_DAEMON_DIR", dir)
	serveFakeDaemon(t, dir, build)
	return stubLaunch(t)
}

// serveFakeDaemon writes a pid file naming this process into dir and answers
// /ping with build on dir/daemon.sock.
func serveFakeDaemon(t *testing.T, dir, build string) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(dir, 0o700))
	assert.NilError(t, WritePID(filepath.Join(dir, "daemon.pid"), os.Getpid()))
	ln, err := net.Listen("unix", filepath.Join(dir, "daemon.sock"))
	assert.NilError(t, err)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, build)
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// legacyHome points the daemon at a fresh XDG state dir under a HOME whose
// ~/.chunk/daemon is where chunk releases before the XDG move ran it.
func legacyHome(t *testing.T) (stateDir, legacyDir string) {
	t.Helper()
	home := socketDir(t)
	t.Setenv("CHUNK_DAEMON_DIR", "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "s"))
	return filepath.Join(home, "s", "chunk", "daemon"), filepath.Join(home, ".chunk", "daemon")
}

func TestFindDaemon_reachesADaemonOnTheLegacySocket(t *testing.T) {
	_, legacy := legacyHome(t)
	serveFakeDaemon(t, legacy, BuildID())
	launches := stubLaunch(t)

	pidPath, sockPath, err := findDaemon()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(pidPath, filepath.Join(legacy, "daemon.pid")))
	assert.Check(t, cmp.Equal(sockPath, filepath.Join(legacy, "daemon.sock")))

	assert.NilError(t, EnsureRunning())
	assert.NilError(t, EnsureLaunched())
	assert.Check(t, cmp.Equal(*launches, 0))
	assert.Check(t, IsDaemonCompatible())
}

func TestFindDaemon_prefersTheStateDir(t *testing.T) {
	state, legacy := legacyHome(t)
	serveFakeDaemon(t, legacy, "old")
	serveFakeDaemon(t, state, "new")

	_, sockPath, err := findDaemon()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(sockPath, filepath.Join(state, "daemon.sock")))
}

func TestFindDaemon_defaultsToTheStateDir(t *testing.T) {
	state, _ := legacyHome(t)

	pidPath, sockPath, err := findDaemon()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(pidPath, filepath.Join(state, "daemon.pid")))
	assert.Check(t, cmp.Equal(sockPath, filepath.Join(state, "daemon.sock")))
}

// stubLaunch replaces starting a daemon with counting the attempts.
func stubLaunch(t *testing.T) *int {
	t.Helper()
	launches := 0
	prev := launch
	launch = func() error { launches++; return nil }
	t.Cleanup(func() { launch = prev })
	return &launches
}

// EnsureLaunched must leave a reachable daemon alone whatever build it reports,
// so a failed poll in one dashboard cannot restart the daemon another is using.
func TestEnsureLaunched_leavesAReachableDaemonAlone(t *testing.T) {
	launches := fakeDaemon(t, "some-other-build")
	assert.NilError(t, EnsureLaunched())
	assert.Equal(t, *launches, 0, "a reachable daemon was relaunched")
}

func TestFetchRelaunching_successNeverTouchesTheDaemon(t *testing.T) {
	fetches, relaunches := 0, 0
	_, err := fetchRelaunching(
		func() (Snapshot, error) { fetches++; return Snapshot{}, nil },
		func() error { relaunches++; return nil })
	assert.NilError(t, err)
	assert.Equal(t, fetches, 1)
	assert.Equal(t, relaunches, 0)
}

func TestFetchRelaunching_relaunchesAndRetriesOnce(t *testing.T) {
	fetches, relaunches := 0, 0
	_, err := fetchRelaunching(
		func() (Snapshot, error) {
			fetches++
			if fetches == 1 {
				return Snapshot{}, errors.New("connect to chunk daemon: no such file")
			}
			return Snapshot{}, nil
		},
		func() error { relaunches++; return nil })
	assert.NilError(t, err)
	assert.Equal(t, fetches, 2)
	assert.Equal(t, relaunches, 1)
}

func TestFetchRelaunching_reportsTheFetchErrorWhenRelaunchFails(t *testing.T) {
	_, err := fetchRelaunching(
		func() (Snapshot, error) { return Snapshot{}, errors.New("connect to chunk daemon: no such file") },
		func() error { return errors.New("could not spawn") })
	// The fetch failure is what the reader is looking at, not the relaunch one.
	assert.Error(t, err, "connect to chunk daemon: no such file")
}

func TestFetchRelaunching_reportsTheRetryErrorWhenBothFetchesFail(t *testing.T) {
	errs := []error{errors.New("first"), errors.New("second")}
	fetches := 0
	_, err := fetchRelaunching(
		func() (Snapshot, error) { e := errs[fetches]; fetches++; return Snapshot{}, e },
		func() error { return nil })
	assert.Error(t, err, "second")
	assert.Equal(t, fetches, 2)
}

func TestFetchSnapshotRelaunching_neverRelaunchesARemoteDaemon(t *testing.T) {
	t.Setenv("CHUNK_DAEMON_REMOTE_ADDR", "127.0.0.1:1")
	prev := launch
	launches := 0
	launch = func() error { launches++; return nil }
	t.Cleanup(func() { launch = prev })

	_, err := FetchSnapshotRelaunching(nil)
	assert.Assert(t, err != nil)
	assert.Equal(t, launches, 0)
}

// serveSessionDaemon is fakeDaemon for a daemon with sessions: /ping answers
// build and /snapshot lists the sessions in one project.
func serveSessionDaemon(t *testing.T, build string, sessions ...Session) (sockPath string, launches *int) {
	t.Helper()
	dir := socketDir(t)
	t.Setenv("CHUNK_DAEMON_DIR", dir)
	assert.NilError(t, WritePID(filepath.Join(dir, "daemon.pid"), os.Getpid()))
	sockPath = filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", sockPath)
	assert.NilError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, build) })
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Snapshot{Projects: []ProjectSnapshot{{Root: "/p"}, {Root: "/q", Sessions: sessions}}})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sockPath, stubLaunch(t)
}

// A newer binary must not replace a daemon that is in the middle of a plan
// build: the build dies with it.
func TestEnsureRunning_keepsAStaleDaemonWithARunGoing(t *testing.T) {
	_, launches := serveSessionDaemon(t, "some-other-build",
		Session{ID: "done", State: SessionDone},
		Session{ID: "live", State: SessionRunning})

	assert.NilError(t, EnsureRunning())
	assert.Equal(t, *launches, 0, "a daemon with a running session was replaced")
	assert.Check(t, IsDaemonRunning(), "the daemon was stopped")
}

func TestHasRunningSessions(t *testing.T) {
	t.Run("only finished sessions", func(t *testing.T) {
		sock, _ := serveSessionDaemon(t, "b",
			Session{ID: "a", State: SessionDone},
			Session{ID: "b", State: SessionCancelled})
		assert.Check(t, !hasRunningSessions(sock))
	})
	t.Run("a running session", func(t *testing.T) {
		sock, _ := serveSessionDaemon(t, "b", Session{ID: "a", State: SessionRunning})
		assert.Check(t, hasRunningSessions(sock))
	})
	t.Run("a daemon that lists no sessions", func(t *testing.T) {
		dir := socketDir(t)
		serveFakeDaemon(t, dir, "b")
		assert.Check(t, !hasRunningSessions(filepath.Join(dir, "daemon.sock")))
	})
	t.Run("nothing listening", func(t *testing.T) {
		assert.Check(t, !hasRunningSessions(filepath.Join(socketDir(t), "daemon.sock")))
	})
}

// fakeSessionDaemon serves h on the daemon socket for the test.
func fakeSessionDaemon(t *testing.T, h http.Handler) {
	t.Helper()
	dir := socketDir(t)
	t.Setenv("CHUNK_DAEMON_DIR", dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "daemon.sock"))
	assert.NilError(t, err)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func TestFetchPlanBuild(t *testing.T) {
	t.Run("a build", func(t *testing.T) {
		uris := make(chan string, 1)
		fakeSessionDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uris <- r.URL.RequestURI()
			_, _ = io.WriteString(w, `{"id":"s1","state":"running","first":2,"total":3,"lines":[{"level":"warn","text":"stuck"}]}`)
		}))
		got, found, err := FetchPlanBuild("a/b", 2)
		assert.NilError(t, err)
		assert.Check(t, found)
		assert.Check(t, cmp.DeepEqual(got, PlanBuildActivity{
			ID: "s1", State: SessionRunning, First: 2, Total: 3,
			Lines: []FeedLine{{Level: FeedWarn, Text: "stuck"}},
		}))
		uri := <-uris
		assert.Check(t, cmp.Equal(uri, "/factory/plans/a%2Fb/build?after=2"))
	})

	t.Run("no build of the plan", func(t *testing.T) {
		fakeSessionDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no build of plan x", http.StatusNotFound)
		}))
		_, found, err := FetchPlanBuild("x", 0)
		assert.NilError(t, err)
		assert.Check(t, !found)
	})

	t.Run("a daemon without the route", func(t *testing.T) {
		fakeSessionDaemon(t, http.NotFoundHandler())
		_, found, err := FetchPlanBuild("x", 0)
		assert.Check(t, cmp.ErrorIs(err, ErrDaemonUnavailable))
		assert.Check(t, !found)
	})

	t.Run("a daemon error", func(t *testing.T) {
		fakeSessionDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "broken", http.StatusInternalServerError)
		}))
		_, found, err := FetchPlanBuild("x", 0)
		var refused *SessionRefused
		assert.Assert(t, errors.As(err, &refused), "got %v", err)
		assert.Check(t, cmp.Equal(refused.Status, http.StatusInternalServerError))
		assert.Check(t, !found)
	})
}
