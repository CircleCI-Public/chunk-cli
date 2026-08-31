package watchd

import (
	"context"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

// TestDaemonRoundTrip starts the daemon in-process, waits for it to accept
// connections, issues a FetchSnapshot, then cancels the context and verifies
// clean shutdown. Uses CHUNK_WATCHD_DIR to avoid touching ~/.chunk/watchd.
func TestDaemonRoundTrip(t *testing.T) {
	t.Setenv("CHUNK_WATCHD_DIR", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- RunDaemon(ctx) }()

	sockPath, err := SocketPath()
	assert.NilError(t, err)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reachable, _ := ping(sockPath); reachable {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	reachable, build := ping(sockPath)
	assert.Assert(t, reachable, "daemon did not become reachable within 5s")
	// The daemon runs in this process, so it reports this build.
	assert.Equal(t, build, BuildID())

	_, err = FetchSnapshot(nil)
	assert.NilError(t, err)

	cancel()

	select {
	case err := <-errCh:
		assert.NilError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not shut down within 5s after context cancel")
	}
}

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

// A daemon that predates the identity answers /ping with an empty body, so the
// client must read that as a mismatch and replace it rather than trust it.
func TestPing_emptyBuildIsAMismatch(t *testing.T) {
	assert.Assert(t, BuildID() != "", "BuildID must never be empty, or a stale daemon would look current")
}
