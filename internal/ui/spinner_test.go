package ui

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

// syncBuffer is a bytes.Buffer safe for the spinner's goroutine to write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// Lines logged after the spinner stops print plainly, with no spinner drawn
// back under them.
func TestSpinnerLogAfterStopOnlyPrints(t *testing.T) {
	var out syncBuffer
	s := &Spinner{w: &out, isTTY: true}
	s.Start("working")
	s.Log("a line")
	s.Stop()
	out.Reset()

	s.Log("after")
	time.Sleep(3 * spinInterval)

	assert.Equal(t, out.String(), "\r\x1b[2Kafter\n")
}

// Lines logged from other goroutines while it spins each land whole.
func TestSpinnerLogKeepsLinesWhole(t *testing.T) {
	var out syncBuffer
	s := &Spinner{w: &out, isTTY: true}
	s.Start("working")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { s.Log("line") })
	}
	wg.Wait()
	s.Stop()

	assert.Assert(t, !bytes.Contains([]byte(out.String()), []byte("\r\x1b[2K\r")), "a redraw landed inside a logged line: %q", out.String())
}
