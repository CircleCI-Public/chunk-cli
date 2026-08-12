package circleci

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// execWith runs a command against a fake configured with the given canned
// result, returning the streamed bytes alongside the response.
func execWith(t *testing.T, resp *fakes.ExecResponse, tweak func(*fakes.FakeCircleCI)) (*ExecResponse, []byte, []byte, error) {
	t.Helper()
	out, stdout, stderr, _, err := execCounting(t, resp, tweak)
	return out, stdout, stderr, err
}

// execCounting also reports how many times the output stream was requested, so a
// resume test cannot pass without the reconnect actually happening.
func execCounting(
	t *testing.T, resp *fakes.ExecResponse, tweak func(*fakes.FakeCircleCI),
) (*ExecResponse, []byte, []byte, int, error) {
	t.Helper()

	// Keep reconnect backoff out of the test's wall clock; the delay logic itself
	// is not what these tests are about.
	previous := streamRetryBase
	streamRetryBase = time.Millisecond
	t.Cleanup(func() { streamRetryBase = previous })

	fake := fakes.NewFakeCircleCI()
	fake.ExecResponse = resp
	if tweak != nil {
		tweak(fake)
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	client := newTestClient(t, srv.URL)

	var stdout, stderr bytes.Buffer
	onOutput := func(stream string, data []byte) {
		if stream == StreamStderr {
			stderr.Write(data)
		} else {
			stdout.Write(data)
		}
	}

	out, err := client.Exec(context.Background(), "sb-1", "sh", nil, nil, onOutput)

	attempts := 0
	for _, req := range fake.Recorder.AllRequests() {
		if strings.HasSuffix(req.URL.Path, "/output") {
			attempts++
		}
	}
	return out, stdout.Bytes(), stderr.Bytes(), attempts, err
}

// Bytes must reach the caller exactly as the remote command wrote them. Every
// payload here was corrupted by the previous line-oriented format: carriage
// returns were destroyed by line splitting, and invalid UTF-8 was silently
// replaced with U+FFFD by JSON encoding.
func TestExecPreservesBytesExactly(t *testing.T) {
	payloads := []struct {
		name string
		data string
	}{
		{"carriage return redraw", "a\rb\rc"},
		{"no trailing newline", "Continue? [y/N] "},
		{"ansi escapes", "\x1b[32mok\x1b[0m\n"},
		{"invalid utf8", "\xed\xa0\x80"},
		{"nul bytes", "a\x00b"},
		{"crlf", "one\r\ntwo\r\n"},
	}

	for _, p := range payloads {
		t.Run(p.name, func(t *testing.T) {
			_, stdout, _, err := execWith(t, &fakes.ExecResponse{
				CommandID: "cmd-1", Stdout: p.data,
			}, nil)
			assert.NilError(t, err)
			assert.Check(t, cmp.DeepEqual(stdout, []byte(p.data)))
		})
	}
}

func TestExecSeparatesStreams(t *testing.T) {
	_, stdout, stderr, err := execWith(t, &fakes.ExecResponse{
		CommandID: "cmd-1", Stdout: "out\n", Stderr: "err\n",
	}, nil)
	assert.NilError(t, err)
	assert.Equal(t, string(stdout), "out\n")
	assert.Equal(t, string(stderr), "err\n")
	assert.Check(t, !bytes.Contains(stdout, []byte("err")), "stderr leaked into stdout")
}

// Output larger than bufio.Scanner's old 64KiB token limit must arrive whole.
func TestExecLargeOutput(t *testing.T) {
	big := strings.Repeat("x", 300<<10)
	_, stdout, _, err := execWith(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: big}, nil)
	assert.NilError(t, err)
	assert.Equal(t, len(stdout), len(big))
}

func TestExecExitCodeAndSignal(t *testing.T) {
	resp, _, _, err := execWith(t, &fakes.ExecResponse{
		CommandID: "cmd-1", Stdout: "bye\n", ExitCode: 137, Signal: "killed",
	}, nil)
	assert.NilError(t, err)
	assert.Equal(t, resp.ExitCode, 137)
	assert.Equal(t, resp.Signal, "killed")
}

// A connection that ends without a terminal event means "interrupted": the
// client must reconnect from its cursor and reassemble the transcript rather
// than reporting a truncated stream as a result.
func TestExecResumesAfterDroppedConnection(t *testing.T) {
	resp, stdout, _, attempts, err := execCounting(t, &fakes.ExecResponse{
		CommandID: "cmd-1", Stdout: "abcdefghij", ExitCode: 0,
	}, func(f *fakes.FakeCircleCI) {
		f.DropStreamsBeforeExit = 1
	})
	assert.NilError(t, err)
	assert.Equal(t, string(stdout), "abcdefghij",
		"resume must not duplicate or drop bytes")
	assert.Equal(t, resp.ExitCode, 0)
	assert.Equal(t, attempts, 2, "the dropped stream must have been reconnected exactly once")
}

// Repeated drops must still converge, exercising the backoff loop rather than
// giving up on the first retry.
func TestExecResumesAcrossSeveralDrops(t *testing.T) {
	resp, stdout, _, attempts, err := execCounting(t, &fakes.ExecResponse{
		CommandID: "cmd-1", Stdout: "hello world", ExitCode: 3,
	}, func(f *fakes.FakeCircleCI) {
		f.DropStreamsBeforeExit = 2
	})
	assert.NilError(t, err)
	assert.Equal(t, string(stdout), "hello world")
	assert.Equal(t, resp.ExitCode, 3)
	assert.Equal(t, attempts, 3)
}

// The server caps every connection on a timer, so a long command is served as a
// series of short streams. Reconnecting many times without new output must
// therefore be normal, not a reason to give up — a command can be silent for
// minutes while it compiles, and abandoning it then would be the whole bug back
// again in a new form.
func TestExecKeepsResumingWhileServerStaysAlive(t *testing.T) {
	drops := maxStreamStalls * 4

	resp, stdout, _, attempts, err := execCounting(t, &fakes.ExecResponse{
		CommandID: "cmd-1", Stdout: "done\n", ExitCode: 0,
	}, func(f *fakes.FakeCircleCI) {
		f.DropStreamsBeforeExit = drops
	})
	assert.NilError(t, err, "a stream that keeps reconnecting must not be abandoned")
	assert.Equal(t, string(stdout), "done\n", "output must not be duplicated across reconnects")
	assert.Equal(t, resp.ExitCode, 0)
	assert.Equal(t, attempts, drops+1)
}

// A command that produces no output while it runs forces every reconnect to
// return an empty stream (no SSE frames at all). That is the normal "connection
// interrupted" signal from the API and must not be counted as a failure.
func TestExecKeepsResumingOnEmptyStreams(t *testing.T) {
	empties := maxStreamStalls * 4

	resp, stdout, _, attempts, err := execCounting(t, &fakes.ExecResponse{
		CommandID: "cmd-1", Stdout: "done\n", ExitCode: 0,
	}, func(f *fakes.FakeCircleCI) {
		f.EmptyStreamsBeforeExit = empties
	})
	assert.NilError(t, err, "empty streams must not be treated as failures")
	assert.Equal(t, string(stdout), "done\n")
	assert.Equal(t, resp.ExitCode, 0)
	assert.Equal(t, attempts, empties+1)
}

// A server that keeps talking but never terminates the stream is still bounded,
// so a bug there cannot spin forever.
func TestExecGivesUpWhenStreamNeverTerminates(t *testing.T) {
	previous := maxStreamAttempts
	maxStreamAttempts = 5
	t.Cleanup(func() { maxStreamAttempts = previous })

	_, _, _, attempts, err := execCounting(t, &fakes.ExecResponse{
		CommandID: "cmd-1", Stdout: "x", ExitCode: 0,
	}, func(f *fakes.FakeCircleCI) {
		f.DropStreamsBeforeExit = 10_000
	})
	assert.Check(t, err != nil, "an endlessly interrupted stream must eventually fail")
	assert.Check(t, strings.Contains(err.Error(), "without the command finishing"), "got %v", err)
	assert.Equal(t, attempts, 5)
}

// Attempts that deliver nothing at all are the ones worth counting: a far end
// that cannot be reached will never be reached by trying harder.
func TestExecGivesUpWhenAttemptsReturnNothing(t *testing.T) {
	_, _, _, attempts, err := execCounting(t, &fakes.ExecResponse{
		CommandID: "cmd-1", ExitCode: 0,
	}, func(f *fakes.FakeCircleCI) {
		f.CommandOutputStatusCode = 503
		f.CommandOutputMessage = "upstream unavailable"
	})
	assert.Check(t, err != nil)
	assert.Check(t, strings.Contains(err.Error(), "returned nothing"), "got %v", err)
	// The request count exceeds the loop's attempt count because the HTTP client
	// retries a 5xx itself; what matters is that the whole thing is bounded
	// rather than spinning.
	assert.Check(t, attempts > 0 && attempts < 50, "stalls must be bounded, got %d requests", attempts)
}

// An API speaking a format this build does not know must say so, rather than
// looking like a stalled stream and sending someone hunting a network fault.
func TestExecReportsUnrecognisedFormat(t *testing.T) {
	// A server emitting only event types this client does not handle — which is
	// exactly what an older API looks like.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/exec") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":"cmd-1","attributes":{"phase":"received"}}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: output\ndata: {\"stream\":\"stdout\",\"line\":\"hi\"}\n\n" +
			"event: result\ndata: {\"command_id\":\"cmd-1\",\"exit_code\":0}\n\n"))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv.URL).Exec(context.Background(), "sb-1", "sh", nil, nil, nil)
	assert.Check(t, errors.Is(err, ErrOutputFormatUnsupported), "got %v", err)
}

// With no onOutput the client accumulates, which is what callers needing the
// whole transcript rely on.
func TestExecAccumulatesWhenNoCallback(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	fake.ExecResponse = &fakes.ExecResponse{
		CommandID: "cmd-1", Stdout: "a\rb", Stderr: "oops\n", ExitCode: 0,
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	resp, err := newTestClient(t, srv.URL).Exec(context.Background(), "sb-1", "sh", nil, nil, nil)
	assert.NilError(t, err)
	assert.Equal(t, resp.Stdout, "a\rb")
	assert.Equal(t, resp.Stderr, "oops\n")
}

// A definitive rejection must fail immediately rather than being retried, since
// reconnecting would only repeat it.
func TestExecDoesNotRetryClientErrors(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	fake.ExecResponse = &fakes.ExecResponse{CommandID: "cmd-1"}
	fake.CommandOutputStatusCode = 410
	fake.CommandOutputMessage = "sidecar is out of date"
	srv := httptest.NewServer(fake)
	defer srv.Close()

	_, err := newTestClient(t, srv.URL).Exec(context.Background(), "sb-1", "sh", nil, nil, nil)
	assert.Check(t, err != nil)

	// One submit plus exactly one output attempt — no retry storm.
	outputAttempts := 0
	for _, req := range fake.Recorder.AllRequests() {
		if strings.HasSuffix(req.URL.Path, "/output") {
			outputAttempts++
		}
	}
	assert.Equal(t, outputAttempts, 1, "a 410 must not be retried")
}

// The V3 error envelope nests the human-readable text as an object. Decoding
// "error" as a string fails on it outright, which is why V3 errors used to reach
// users as a raw JSON envelope complete with trace id.
func TestExtractServerMessage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "v3 nested envelope",
			body: `{"error":{"id":"ff5c93354749c51f","title":"sidecar is out of date; delete and recreate with: chunk sidecar create"}}`,
			want: "sidecar is out of date; delete and recreate with: chunk sidecar create",
		},
		{name: "bare error string", body: `{"error":"gone away"}`, want: "gone away"},
		{name: "message field", body: `{"message":"nope"}`, want: "nope"},
		{name: "empty body", body: ``, want: ""},
		{name: "unparseable falls back to raw", body: `not json`, want: "not json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, extractServerMessage([]byte(tt.body)), tt.want)
		})
	}
}
