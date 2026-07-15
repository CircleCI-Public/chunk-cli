package telemetry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/segmentio/analytics-go/v3"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/poll"
)

// receiverBinPath is the path to a stub "chunk receive-telemetry" binary,
// built once by TestMain from testdata/receiverbin. Pointing
// delegateDestination.bin at it lets tests spawn a real subprocess and
// assert on what it sends to Segment, without any risk of re-execing the
// "go test" binary itself (the stub has no subcommands at all).
var receiverBinPath string

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "chunk-telemetry-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)

	receiverBinPath = filepath.Join(dir, "receiverbin")
	build := exec.Command("go", "build", "-o", receiverBinPath, "./testdata/receiverbin")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build receiverbin:", err)
		return 1
	}

	return m.Run()
}

func TestDelegateDestination_Close_DeliversToSegment(t *testing.T) {
	var mu sync.Mutex
	var batch []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		body, err := io.ReadAll(r.Body)
		assert.NilError(t, err)

		var payload struct {
			Batch []map[string]any `json:"batch"`
		}
		assert.NilError(t, json.Unmarshal(body, &payload))

		mu.Lock()
		batch = append(batch, payload.Batch...)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &delegateDestination{
		bin:      receiverBinPath,
		writeKey: "test-write-key",
		endpoint: srv.URL,
	}

	assert.NilError(t, d.Enqueue(analytics.Track{
		Event:  "test_event",
		UserId: "user-1",
	}))
	assert.NilError(t, d.Close())

	poll.WaitOn(t, func(t poll.LogT) poll.Result {
		mu.Lock()
		defer mu.Unlock()
		if len(batch) == 1 {
			return poll.Success()
		}
		return poll.Continue("received %d events, want 1", len(batch))
	}, poll.WithTimeout(5*time.Second))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, batch[0]["event"], "test_event")
	assert.Equal(t, batch[0]["userId"], "user-1")
}
