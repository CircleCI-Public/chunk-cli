package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// silentSocket is a socket stdin whose other end stays open and sends
// nothing, as an agent's background Bash command gets; send writes to it.
func silentSocket(t *testing.T) (stdin *os.File, send *os.File) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	assert.NilError(t, err)
	stdin, send = os.NewFile(uintptr(fds[0]), "stdin"), os.NewFile(uintptr(fds[1]), "send")
	t.Cleanup(func() { _ = stdin.Close(); _ = send.Close() })
	return stdin, send
}

// silentPipe is silentSocket over os.Pipe, as a shell pipe gives.
func silentPipe(t *testing.T) (stdin *os.File, send *os.File) {
	t.Helper()
	stdin, send, err := os.Pipe()
	assert.NilError(t, err)
	t.Cleanup(func() { _ = stdin.Close(); _ = send.Close() })
	return stdin, send
}

// shortWaits shortens the hook payload waits for one test. A test that calls
// it must not run in parallel.
func shortWaits(t *testing.T) {
	t.Helper()
	oldWait, oldRead := hookPayloadWait, hookPayloadRead
	hookPayloadWait, hookPayloadRead = 50*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { hookPayloadWait, hookPayloadRead = oldWait, oldRead })
}

// countingReader counts the Read calls made on r and the bytes they return.
type countingReader struct {
	r     io.Reader
	reads int
	bytes int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	n, err := c.r.Read(p)
	c.bytes += n
	return n, err
}

const commitPayload = `{"session_id":"s","hook_event_name":"PreToolUse","tool_input":{"command":"git commit -m x"}}`

func writePayload(w *os.File) { _, _ = w.WriteString(commitPayload) }

func TestReadHookPayload(t *testing.T) {
	commitHook := &hookContext{sessionID: "s", event: "PreToolUse", toolCommand: "git commit -m x"}
	for _, tc := range []struct {
		name     string
		kind     string           // "socket" or "pipe"
		write    func(w *os.File) // run in a goroutine; nil writes nothing
		closeW   bool
		wantHook *hookContext
		minWait  time.Duration // it must not return sooner
		maxWait  time.Duration // it must return by then
		wantErr  string        // a substring of what errOut must hold; "" for nothing
	}{
		{
			name:    "silent socket",
			kind:    "socket",
			minWait: 50 * time.Millisecond,
			maxWait: 500 * time.Millisecond,
		},
		{
			name:    "silent pipe",
			kind:    "pipe",
			minWait: 50 * time.Millisecond,
			maxWait: 500 * time.Millisecond,
		},
		{
			name:     "payload on a socket, closed",
			kind:     "socket",
			write:    writePayload,
			closeW:   true,
			wantHook: commitHook,
			maxWait:  500 * time.Millisecond,
		},
		{
			name:     "payload on a pipe, closed",
			kind:     "pipe",
			write:    writePayload,
			closeW:   true,
			wantHook: commitHook,
			maxWait:  500 * time.Millisecond,
		},
		{
			name:    "empty pipe, closed",
			kind:    "pipe",
			closeW:  true,
			maxWait: 40 * time.Millisecond, // under hookPayloadWait: it did not wait
		},
		{
			name: "first byte too late",
			kind: "socket",
			write: func(w *os.File) {
				time.Sleep(100 * time.Millisecond)
				writePayload(w)
			},
			minWait: 50 * time.Millisecond,
			maxWait: 500 * time.Millisecond,
		},
		{
			name:    "payload never finishes",
			kind:    "socket",
			write:   func(w *os.File) { _, _ = w.WriteString(`{"session_id":`) },
			minWait: 200 * time.Millisecond,
			maxWait: 1 * time.Second,
			wantErr: "no hook payload within",
		},
		{
			name:    "payload too large",
			kind:    "pipe",
			write:   func(w *os.File) { _, _ = w.Write(bytes.Repeat([]byte("x"), maxHookPayload+1)) },
			closeW:  true,
			maxWait: 1 * time.Second,
			wantErr: "more than",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortWaits(t)
			var stdin, send *os.File
			switch tc.kind {
			case "socket":
				stdin, send = silentSocket(t)
			case "pipe":
				stdin, send = silentPipe(t)
			default:
				t.Fatalf("unknown kind %q", tc.kind)
			}
			go func() {
				if tc.write != nil {
					tc.write(send)
				}
				if tc.closeW {
					_ = send.Close()
				}
			}()

			var errOut bytes.Buffer
			start := time.Now()
			got, err := readHookPayload(t.Context(), stdin, &errOut)
			elapsed := time.Since(start)

			assert.NilError(t, err)
			assert.Check(t, cmp.Equal(got == nil, tc.wantHook == nil), "got %+v", got)
			if got != nil && tc.wantHook != nil {
				assert.Check(t, cmp.Equal(got.sessionID, tc.wantHook.sessionID))
				assert.Check(t, cmp.Equal(got.event, tc.wantHook.event))
				assert.Check(t, cmp.Equal(got.toolCommand, tc.wantHook.toolCommand))
			}
			assert.Check(t, elapsed >= tc.minWait, "returned after %s, before %s", elapsed, tc.minWait)
			assert.Check(t, elapsed <= tc.maxWait, "returned after %s, past %s", elapsed, tc.maxWait)
			if tc.wantErr != "" {
				assert.Check(t, cmp.Contains(errOut.String(), tc.wantErr))
			} else {
				assert.Check(t, cmp.Equal(errOut.String(), ""))
			}
		})
	}
}

// A reader that is not a file is what the tests hand the command: it is
// decoded directly by detectHook, with no timers.
func TestReadHookPayloadReadsANonFileToEOF(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{
			name:    "commit gate",
			payload: commitPayload,
		},
		{
			name:    "codex stop",
			payload: `{"session_id":"abc","stop_hook_active":true,"hook_event_name":"Stop","turn_id":"turn-1"}`,
		},
		{
			name:    "not a hook",
			payload: `{}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readHookPayload(t.Context(), strings.NewReader(tc.payload), io.Discard)
			assert.NilError(t, err)
			want := detectHook(strings.NewReader(tc.payload))
			assert.Check(t, cmp.Equal(got == nil, want == nil), "got %+v, want %+v", got, want)
			if got != nil && want != nil {
				assert.Check(t, cmp.Equal(*got, *want))
			}
		})
	}
}

func TestReadHookPayloadStopsOnCancel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(w *os.File) // run at once; nil writes nothing
	}{
		{
			name: "cancel while waiting",
		},
		{
			name:  "cancel mid-payload",
			write: func(w *os.File) { _, _ = w.WriteString(`{"session_id":`) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortWaits(t)
			stdin, send := silentSocket(t)
			if tc.write != nil {
				tc.write(send)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			time.AfterFunc(20*time.Millisecond, cancel)

			start := time.Now()
			got, err := readHookPayload(ctx, stdin, io.Discard)
			elapsed := time.Since(start)

			// Had either timer fired first, it would have returned nil, nil:
			// the error shows the cancel ended the wait.
			assert.Check(t, cmp.ErrorIs(err, context.Canceled))
			assert.Check(t, got == nil)
			assert.Check(t, elapsed <= 150*time.Millisecond, "returned after %s", elapsed)
		})
	}
}

func TestRunHookWrapsStdinWithoutTheRoot(t *testing.T) {
	cmd := newValidateCmd()
	in := &countingReader{r: strings.NewReader(hookPayload)}
	cmd.SetIn(in)
	cmd.SetContext(t.Context())

	first, err := runHook(cmd)
	assert.NilError(t, err)
	assert.Assert(t, first != nil)
	assert.Check(t, cmp.Equal(first.sessionID, "test-session-001"))
	_, wrapped := cmd.InOrStdin().(*hookInput)
	assert.Check(t, wrapped, "stdin is %T", cmd.InOrStdin())
	reads := in.reads

	second, err := runHook(cmd)
	assert.NilError(t, err)
	assert.Check(t, first == second, "the second call decoded again")
	assert.Check(t, cmp.Equal(in.reads, reads), "stdin was read again")
	assert.Check(t, cmp.Equal(in.bytes, len(hookPayload)))
}
