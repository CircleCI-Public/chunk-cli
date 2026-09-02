package cmd

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

func TestRemoteCommandLabel(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   string
	}{
		{
			// The shape newExecFn actually builds.
			name:   "strips the workspace cd",
			script: "cd '/home/user/repo' && go test ./...",
			want:   "go test ./...",
		},
		{
			name:   "keeps later && intact",
			script: "cd '/home/user/repo' && go build ./... && go test ./...",
			want:   "go build ./... && go test ./...",
		},
		{
			// WorkspaceExists probes with a bare test, no cd.
			name:   "script without a cd is used whole",
			script: "test -d '/home/user/repo'",
			want:   "test -d '/home/user/repo'",
		},
		{
			name:   "multi-line script is titled by its first line",
			script: "cd '/repo' && set -e\ngo vet ./...\ngo test ./...",
			want:   "set -e",
		},
		{
			name:   "empty script stays empty",
			script: "",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := remoteCommandLabel(tt.script)
			assert.Check(t, cmp.Equal(got, tt.want))
		})
	}
}

func TestRemoteCommandLabelIsBounded(t *testing.T) {
	// A label is a pane header, so an enormous generated script must not become
	// one. The output is shown in full regardless.
	long := strings.Repeat("x", 500)
	got := remoteCommandLabel("cd '/repo' && " + long)
	assert.Check(t, len(got) <= 120, "label should be capped, got %d chars", len(got))
}

func TestReportExitCode(t *testing.T) {
	// Reading succeeded in every one of these cases, so none of them is an error:
	// making `logs` fail because the command it reports on failed would leave the
	// exit status meaning nothing.
	var errOut strings.Builder
	io := iostream.Streams{Out: &errOut, Err: &errOut}

	err := reportExitCode(nil, io)
	assert.Check(t, cmp.Nil(err), "an unterminated command is not a failure")

	zero := 0
	err = reportExitCode(&zero, io)
	assert.Check(t, cmp.Nil(err))
	assert.Check(t, cmp.Equal(errOut.String(), ""), "a passing command says nothing")

	two := 2
	err = reportExitCode(&two, io)
	assert.Check(t, cmp.Nil(err), "logs must not fail because the logged command did")
	// The status still has to be visible, just on stderr so stdout stays pipeable.
	assert.Check(t, cmp.Contains(errOut.String(), "exit status 2"))
}

func TestSidecarLogsRequiresExactlyOneArg(t *testing.T) {
	cmd := newSidecarLogsCmd()
	err := cmd.Args(cmd, nil)
	assert.Check(t, err != nil, "no command ID should be rejected")

	err = cmd.Args(cmd, []string{"a", "b"})
	assert.Check(t, err != nil, "two command IDs should be rejected")

	err = cmd.Args(cmd, []string{"cmd-1"})
	assert.NilError(t, err)
}

func TestSidecarLogsIsRegistered(t *testing.T) {
	var found bool
	for _, sub := range newSidecarCmd().Commands() {
		if sub.Name() == "logs" {
			found = true
			break
		}
	}
	assert.Check(t, found, "sidecar logs should be registered as a subcommand")
}

// fakeWatchd serves /output over a unix socket at the daemon's expected path,
// returning the supplied chunks in order.
func fakeWatchd(t *testing.T, chunks []watchd.OutputChunk) {
	t.Helper()
	// Not t.TempDir(): a unix socket path is capped near 104 bytes and macOS
	// temp dirs are long enough to blow that on their own.
	dir, err := os.MkdirTemp("/tmp", "watchd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_WATCHD_DIR", dir)

	ln, err := net.Listen("unix", filepath.Join(dir, "watchd.sock"))
	assert.NilError(t, err)

	var mu sync.Mutex
	var i int
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			chunk := chunks[len(chunks)-1]
			if i < len(chunks) {
				chunk = chunks[i]
				i++
			}
			_ = json.NewEncoder(w).Encode(chunk)
		})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// A stream that dies mid-follow leaves no exit code, so reporting only the code
// would end the follow silently at exit 0 as though the command had finished.
func TestFollowReportsAStreamThatEndedEarly(t *testing.T) {
	fakeWatchd(t, []watchd.OutputChunk{
		{Found: true, Running: true, Data: []byte("first\n"), NextOffset: 6},
		{Found: true, Running: false, NextOffset: 6, Error: "connection reset"},
	})

	var out, errOut strings.Builder
	cmd := newSidecarLogsCmd()
	cmd.SetContext(context.Background())

	err := followFromDaemon(cmd, "cmd-1", 0, iostream.Streams{Out: &out, Err: &errOut})
	assert.NilError(t, err)
	assert.Check(t, cmp.Contains(out.String(), "first"))
	assert.Check(t, cmp.Contains(errOut.String(), "output stream ended early: connection reset"))
}
