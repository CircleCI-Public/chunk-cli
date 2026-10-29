package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// hookPayloadWait is how long a run with a non-terminal stdin waits for a
// hook payload to start; hookPayloadRead how long the whole payload may take.
// Variables so tests can shorten them.
var (
	hookPayloadWait = 1 * time.Second
	hookPayloadRead = 5 * time.Second
)

// maxHookPayload is the most of stdin a hook payload may be.
const maxHookPayload = 1 << 20

// readHookPayload is the hook payload on in, or nil when there is none: in is
// a terminal, nothing arrives within hookPayloadWait, or the payload does not
// finish within hookPayloadRead or is over maxHookPayload, which it says on
// errOut. A reader that is not a file is decoded directly by detectHook, with
// no timers. It returns ctx's error if ctx ends first.
//
// After a timeout or a cancel, the goroutine reading in is left blocked on it
// until the process exits. That is safe because nothing else in validate reads
// a non-terminal stdin.
func readHookPayload(ctx context.Context, in io.Reader, errOut io.Writer) (*hookContext, error) {
	f, ok := in.(*os.File)
	if !ok {
		return detectHook(in), nil
	}
	if term.IsTerminal(int(f.Fd())) {
		return nil, nil
	}
	started := make(chan struct{}) // closed at the first byte, or at EOF or an error before one
	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		r := io.LimitReader(f, maxHookPayload+1)
		p := make([]byte, 32<<10)
		once := sync.OnceFunc(func() { close(started) })
		for {
			n, err := r.Read(p)
			if n > 0 {
				buf.Write(p[:n])
				once()
			}
			if err != nil {
				once()
				done <- buf.Bytes()
				return
			}
		}
	}()
	wait := time.NewTimer(hookPayloadWait)
	defer wait.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-wait.C:
		return nil, nil // silent: not a hook
	case <-started:
	}
	read := time.NewTimer(hookPayloadRead)
	defer read.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-read.C:
		_, _ = fmt.Fprintf(errOut, "chunk: ignoring stdin: no hook payload within %s\n", hookPayloadRead)
		return nil, nil
	case b := <-done:
		if len(b) > maxHookPayload {
			_, _ = fmt.Fprintf(errOut, "chunk: ignoring stdin: more than %d bytes\n", maxHookPayload)
			return nil, nil
		}
		return detectHook(bytes.NewReader(b)), nil
	}
}

// hookInput is a run's stdin with its hook payload read at most once.
type hookInput struct {
	in     io.Reader
	errOut io.Writer
	once   sync.Once
	hook   *hookContext
	err    error
}

func newHookInput(in io.Reader, errOut io.Writer) *hookInput {
	return &hookInput{in: in, errOut: errOut}
}

// Hook is the run's hook payload, read from stdin on the first call.
func (h *hookInput) Hook(ctx context.Context) (*hookContext, error) {
	h.once.Do(func() { h.hook, h.err = readHookPayload(ctx, h.in, h.errOut) })
	return h.hook, h.err
}

// Read reads the rest of stdin, after whatever Hook consumed.
func (h *hookInput) Read(p []byte) (int, error) { return h.in.Read(p) }

// runHook is the run's hook payload: stdin is wrapped in a hookInput on the
// first call, and the same one answers the rest of the run.
func runHook(cmd *cobra.Command) (*hookContext, error) {
	in, ok := cmd.InOrStdin().(*hookInput)
	if !ok {
		in = newHookInput(cmd.InOrStdin(), cmd.ErrOrStderr())
		cmd.SetIn(in)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	hook, err := in.Hook(ctx)
	if err != nil {
		return nil, newUserError("Interrupted while waiting for a hook payload.").wrap(err)
	}
	return hook, nil
}
