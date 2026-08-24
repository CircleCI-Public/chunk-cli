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
		if ping(sockPath) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.Assert(t, ping(sockPath), "daemon did not become reachable within 5s")

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
