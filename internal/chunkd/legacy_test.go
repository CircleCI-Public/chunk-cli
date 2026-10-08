//go:build !windows

package chunkd

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// unsetenv clears name for the test and restores it afterwards. t.Setenv first
// so the testing package records the value to put back.
func unsetenv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	assert.NilError(t, os.Unsetenv(name))
}

func TestOldVariableNamesAreReadWhenTheNewOnesAreNotSet(t *testing.T) {
	unsetenv(t, EnvRemoteAddr)
	t.Setenv("CHUNK_WATCHD_REMOTE_ADDR", "sandbox-host:7777")

	assert.Equal(t, TCPRemoteAddr(), "sandbox-host:7777")
	assert.DeepEqual(t, DeprecatedEnv(), []string{
		"CHUNK_WATCHD_REMOTE_ADDR is deprecated and will stop working in a future release; set CHUNK_DAEMON_REMOTE_ADDR instead",
	})
}

func TestTheNewVariableNameWinsOverTheOld(t *testing.T) {
	t.Setenv(EnvTCPToken, "new-token")
	t.Setenv("CHUNK_WATCHD_TCP_TOKEN", "old-token")

	assert.Equal(t, TCPToken(), "new-token")
	assert.Check(t, cmp.Len(DeprecatedEnv(), 0), "nothing is read from the old name, so there is nothing to warn about")
}

// Setting the new name to empty is how to turn a remote daemon off in a shell
// that still exports the old one.
func TestTheNewVariableNameSetEmptyTurnsTheOldOneOff(t *testing.T) {
	t.Setenv(EnvRemoteAddr, "")
	t.Setenv("CHUNK_WATCHD_REMOTE_ADDR", "sandbox-host:7777")

	assert.Equal(t, TCPRemoteAddr(), "")
	assert.Check(t, cmp.Len(DeprecatedEnv(), 0))
}

func TestTheOldDirectoryVariableStillPlacesTheDaemon(t *testing.T) {
	dir := socketDir(t)
	unsetenv(t, EnvDir)
	t.Setenv("CHUNK_WATCHD_DIR", dir)

	sockPath, err := SocketPath()
	assert.NilError(t, err)
	assert.Equal(t, sockPath, filepath.Join(dir, "daemon.sock"))
}

const legacyDaemonDirEnv = "CHUNKD_TEST_LEGACY_DAEMON_DIR"

// TestHelperLegacyDaemon is a daemon from before the rename: it writes
// watchd.pid, answers /ping on watchd.sock, and exits on SIGTERM.
func TestHelperLegacyDaemon(t *testing.T) {
	dir := os.Getenv(legacyDaemonDirEnv)
	if dir == "" {
		t.Skip("child process helper for TestEnsureRunningStopsADaemonFromBeforeTheRename")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("unix", filepath.Join(dir, "watchd.sock"))
	assert.NilError(t, err)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "old-build")
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	assert.NilError(t, WritePID(filepath.Join(dir, "watchd.pid"), os.Getpid()))

	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("never asked to stop")
	}
	_ = srv.Close()
}

// After the rename the new daemon serves from daemon.sock, so an old one left
// running on watchd.sock is never found by the build check and would go on
// polling every project beside it. Starting the new one stops the old.
func TestEnsureRunningStopsADaemonFromBeforeTheRename(t *testing.T) {
	dir := socketDir(t)
	t.Setenv(EnvDir, dir)
	launches := stubLaunch(t)

	exe, err := os.Executable()
	assert.NilError(t, err)
	child := exec.Command(exe, "-test.run=^TestHelperLegacyDaemon$")
	child.Env = append(os.Environ(), legacyDaemonDirEnv+"="+dir)
	assert.NilError(t, child.Start())
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	t.Cleanup(func() { _ = child.Process.Kill() })

	legacySock := filepath.Join(dir, "watchd.sock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if reachable, _ := ping(legacySock); reachable {
			break
		}
		assert.Assert(t, time.Now().Before(deadline), "legacy daemon never answered")
		time.Sleep(20 * time.Millisecond)
	}

	assert.NilError(t, EnsureRunning())

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon from before the rename is still running")
	}
	assert.Equal(t, *launches, 1, "a new daemon is started in its place")
}

// A pid file left behind names whatever process now has that pid. Without a
// daemon answering on the old socket beside it there is nothing to stop.
func TestEnsureRunningLeavesAPidWithNoOldDaemonAlone(t *testing.T) {
	dir := socketDir(t)
	t.Setenv(EnvDir, dir)
	launches := stubLaunch(t)
	sleeper := exec.Command("sleep", "30")
	assert.NilError(t, sleeper.Start())
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	assert.NilError(t, WritePID(filepath.Join(dir, "watchd.pid"), sleeper.Process.Pid))

	assert.NilError(t, EnsureRunning())

	assert.NilError(t, sleeper.Process.Signal(syscall.Signal(0)), "an unrelated process was signalled")
	assert.Equal(t, *launches, 1)
}
