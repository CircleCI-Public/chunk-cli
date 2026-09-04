package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// captureRegistrations stands up a Unix socket answering /command the way the
// daemon does, and returns a channel of what was registered. A fake daemon
// rather than a stubbed client: the registration crossing a real socket is the
// part that has silently not happened before.
func captureRegistrations(t *testing.T) <-chan watchd.CommandReg {
	t.Helper()
	// Not t.TempDir(): it embeds the test name, and a unix socket path is capped
	// at 104 bytes on darwin, so a descriptive name silently breaks listen.
	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_WATCHD_DIR", dir)

	regs := make(chan watchd.CommandReg, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/command", func(w http.ResponseWriter, r *http.Request) {
		var reg watchd.CommandReg
		if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		regs <- reg
		w.WriteHeader(http.StatusAccepted)
	})

	ln, err := net.Listen("unix", filepath.Join(dir, "watchd.sock"))
	assert.NilError(t, err)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return regs
}

func newFakeSidecarClient(t *testing.T) *circleci.Client {
	t.Helper()
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return client
}

// TestSubmitAndStreamRegistersBeforeStreaming is the guarantee the whole feature
// rests on: a command is known to the daemon while it is still running, not once
// it has finished. A registration that landed after the stream would leave the
// daemon tailing from partway through a run it cannot rewind.
func TestSubmitAndStreamRegistersBeforeStreaming(t *testing.T) {
	regs := captureRegistrations(t)
	client := newFakeSidecarClient(t)

	_, err := submitAndStream(context.Background(), client,
		watchd.CommandReg{
			SidecarID:   "sb-1",
			ProjectRoot: "/tmp/proj",
			Op:          string(eventlog.OpExec),
			Name:        execCommandLabel("echo", []string{"hello"}),
		},
		"echo", []string{"hello"}, nil, func(string, []byte) {})
	assert.NilError(t, err)

	select {
	case reg := <-regs:
		assert.Check(t, reg.CommandID != "", "the daemon needs the ID the API assigned")
		assert.Check(t, cmp.Equal(reg.SidecarID, "sb-1"))
		assert.Check(t, cmp.Equal(reg.Op, string(eventlog.OpExec)))
		assert.Check(t, cmp.Equal(reg.Name, "echo hello"))
		assert.Check(t, !reg.SubmittedAt.IsZero(), "an unset submit time breaks the dashboard join")
	case <-time.After(5 * time.Second):
		t.Fatal("command was never registered with the daemon")
	}
}

// TestSidecarExecRegistersWithTheDaemon covers the path that used to be
// invisible: a command run by hand rather than by validate. Without this the
// dashboard can only ever show part of what ran on a sidecar.
func TestSidecarExecRegistersWithTheDaemon(t *testing.T) {
	regs := captureRegistrations(t)
	client := newFakeSidecarClient(t)

	_, err := submitAndStream(context.Background(), client,
		watchd.CommandReg{
			SidecarID:   "sb-2",
			ProjectRoot: t.TempDir(),
			Op:          string(eventlog.OpExec),
			Name:        execCommandLabel("sh", []string{"-c", "make test"}),
		},
		"sh", []string{"-c", "make test"}, nil, func(string, []byte) {})
	assert.NilError(t, err)

	select {
	case reg := <-regs:
		assert.Check(t, cmp.Equal(reg.Op, string(eventlog.OpExec)),
			"exec runs must be attributed to exec, not folded in with validate")
		assert.Check(t, cmp.Equal(reg.Name, "sh -c make test"))
	case <-time.After(5 * time.Second):
		t.Fatal("sidecar exec did not register")
	}
}

// A submission that fails must surface as the command's error rather than being
// masked by the registration that would follow it.
func TestSubmitAndStreamReportsSubmitFailure(t *testing.T) {
	regs := captureRegistrations(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	_, err = submitAndStream(context.Background(), client,
		watchd.CommandReg{SidecarID: "sb-3", Op: string(eventlog.OpExec)},
		"echo", nil, nil, func(string, []byte) {})
	assert.Check(t, err != nil, "a rejected submission must return an error")

	select {
	case reg := <-regs:
		t.Fatalf("nothing should be registered for a command that never started: %+v", reg)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestExecCommandLabel(t *testing.T) {
	assert.Check(t, cmp.Equal(execCommandLabel("echo", []string{"hello", "world"}), "echo hello world"))
	assert.Check(t, cmp.Equal(execCommandLabel("ls", nil), "ls"))
	// Long labels are bounded on rune boundaries, so the pane title stays valid
	// UTF-8 rather than rendering a replacement char.
	long := execCommandLabel("sh", []string{"-c", strings.Repeat("é", 300)})
	assert.Check(t, cmp.Equal(len([]rune(long)), 120))
}

// TestSubmitAndStreamWrappingPreservesErrorMatching guards the risk in adding
// context to these errors: three callers in sidecar.go dispatch on them with
// errors.Is/errors.As (notAuthorized, sidecarUnavailable, outdatedSidecarAPI).
// Wrapping with %w keeps that working; wrapping with %v would silently turn
// every one of those specific diagnostics into a generic failure.
func TestSubmitAndStreamWrappingPreservesErrorMatching(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	_, err = submitAndStream(context.Background(), client,
		watchd.CommandReg{SidecarID: "sb-1", Op: string(eventlog.OpExec)},
		"echo", nil, nil, func(string, []byte) {})

	assert.Assert(t, err != nil)
	assert.Check(t, strings.Contains(err.Error(), "submit"),
		"the phase must be named so a submit failure is distinguishable from a stream one: %v", err)
	assert.Check(t, errors.Is(err, circleci.ErrNotAuthorized),
		"wrapping must not break the errors.Is dispatch in sidecar.go: %v", err)
}
