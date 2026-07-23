package sidecar_test

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func intPtr(n int) *int { return &n }

// capture collects the output/error streams and any warning status messages
// produced during an ExecAsync run.
type capture struct {
	out, errOut bytes.Buffer
	warns       []string
}

func (c *capture) streams() iostream.Streams {
	return iostream.Streams{Out: &c.out, Err: &c.errOut}
}

func (c *capture) statusFn() iostream.StatusFunc {
	return func(level iostream.Level, msg string) {
		if level == iostream.LevelWarn {
			c.warns = append(c.warns, msg)
		}
	}
}

func TestExecAsyncHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.OutputStreamFunc = func(_ int, w io.Writer) {
		fakes.WriteOutputLines(w, []fakes.OutputLine{
			{Index: 0, Stream: "stdout", Line: "hello"},
			{Index: 1, Stream: "stderr", Line: "a warning"},
			{Index: 2, Stream: "stdout", Line: "world"},
			{CommandID: "cmd-123", ExitCode: intPtr(0)},
		})
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cp := &capture{}
	code, err := sidecar.ExecAsync(context.Background(), newClient(t, srv.URL),
		"sb-1", "echo hello", nil, cp.statusFn(), cp.streams())

	assert.NilError(t, err)
	assert.Equal(t, code, 0)
	// The server sends bare lines; the client re-adds a single newline each.
	assert.Equal(t, cp.out.String(), "hello\nworld\n")
	assert.Equal(t, cp.errOut.String(), "a warning\n")
	assert.Equal(t, len(cp.warns), 0)
}

func TestExecAsyncNonZeroExit(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.OutputStreamFunc = func(_ int, w io.Writer) {
		fakes.WriteOutputLines(w, []fakes.OutputLine{
			{Index: 0, Stream: "stderr", Line: "boom"},
			{CommandID: "cmd-123", ExitCode: intPtr(2)},
		})
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cp := &capture{}
	code, err := sidecar.ExecAsync(context.Background(), newClient(t, srv.URL),
		"sb-1", "false", nil, cp.statusFn(), cp.streams())

	assert.NilError(t, err)
	assert.Equal(t, code, 2)
	assert.Equal(t, cp.errOut.String(), "boom\n")
}

// A clean EOF with no terminal event on an already-ended command should finish
// via a status poll rather than spending reconnect attempts.
func TestExecAsyncCleanEOFEndedCommand(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.OutputStreamFunc = func(_ int, w io.Writer) {
		fakes.WriteOutputLines(w, []fakes.OutputLine{
			{Index: 0, Stream: "stdout", Line: "done"},
		})
	}
	cci.CommandResponse = &fakes.CommandResponse{
		ID:       "cmd-123",
		Phase:    "ended",
		ExitCode: intPtr(0),
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cp := &capture{}
	code, err := sidecar.ExecAsync(context.Background(), newClient(t, srv.URL),
		"sb-1", "echo done", nil, cp.statusFn(), cp.streams())

	assert.NilError(t, err)
	assert.Equal(t, code, 0)
	assert.Equal(t, cp.out.String(), "done\n")
	// No reconnect attempts, so no interruption warnings.
	assert.Equal(t, len(cp.warns), 0)
}

// When the stream closes cleanly while the command is still running, the loop
// reconnects and resumes from the recorded offset.
func TestExecAsyncReconnectResumesFromOffset(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	// First connection (offset 0) delivers two lines then closes cleanly with
	// no terminal event; the reconnect (offset 2) delivers the terminal event.
	cci.OutputStreamFunc = func(offset int, w io.Writer) {
		if offset == 0 {
			fakes.WriteOutputLines(w, []fakes.OutputLine{
				{Index: 0, Stream: "stdout", Line: "line one"},
				{Index: 1, Stream: "stdout", Line: "line two"},
			})
			return
		}
		fakes.WriteOutputLines(w, []fakes.OutputLine{
			{Index: 2, Stream: "stdout", Line: "line three"},
			{CommandID: "cmd-123", ExitCode: intPtr(0)},
		})
	}
	// Command is still running when the first stream closes, forcing a reconnect.
	cci.CommandResponse = &fakes.CommandResponse{ID: "cmd-123", Phase: "running"}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cp := &capture{}
	code, err := sidecar.ExecAsync(context.Background(), newClient(t, srv.URL),
		"sb-1", "echo", nil, cp.statusFn(), cp.streams())

	assert.NilError(t, err)
	assert.Equal(t, code, 0)
	// Each line appears exactly once — the offset prevents replaying line one/two.
	assert.Equal(t, cp.out.String(), "line one\nline two\nline three\n")
}
