package ui

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	spinInterval     = 80 * time.Millisecond
	elapsedThreshold = 10 * time.Second
)

// Spinner shows a braille dot animation on stderr during long operations.
// No-op when stderr is not a TTY.
type Spinner struct {
	mu         sync.Mutex
	w          io.Writer
	message    string
	frameIndex int
	startTime  time.Time
	ticker     *time.Ticker
	done       chan struct{}
	isTTY      bool
}

// NewSpinner creates a spinner that writes to stderr.
func NewSpinner() *Spinner {
	return &Spinner{
		w:     os.Stderr,
		isTTY: term.IsTerminal(int(os.Stderr.Fd())),
	}
}

// Enabled reports whether the spinner draws anything: it does only on a
// terminal. A caller with something to say that the spinner would have shown
// says it some other way when it does not.
func (s *Spinner) Enabled() bool { return s.isTTY }

func (s *Spinner) Start(message string) {
	if !s.isTTY {
		return
	}
	s.mu.Lock()
	s.message = message
	s.frameIndex = 0
	s.startTime = time.Now()
	s.done = make(chan struct{})
	s.ticker = time.NewTicker(spinInterval)
	s.mu.Unlock()

	s.render()
	go func() {
		for {
			select {
			case <-s.ticker.C:
				s.render()
			case <-s.done:
				return
			}
		}
	}()
}

func (s *Spinner) Update(message string) {
	if !s.isTTY {
		return
	}
	s.mu.Lock()
	s.message = message
	s.mu.Unlock()
}

func (s *Spinner) Stop() {
	if !s.isTTY {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ticker != nil {
		s.ticker.Stop()
	}
	if s.done != nil {
		select {
		case <-s.done:
		default:
			close(s.done)
		}
	}
	_, _ = fmt.Fprint(s.w, "\r\x1b[2K")
}

func (s *Spinner) StopWithMessage(message string) {
	s.Stop()
	_, _ = fmt.Fprintln(s.w, message)
}

// Log prints a message on its own line while keeping the spinner running. It
// is safe to call from other goroutines, and after Stop, when it only prints.
func (s *Spinner) Log(message string) {
	if !s.isTTY {
		_, _ = fmt.Fprintln(s.w, message)
		return
	}
	s.mu.Lock()
	_, _ = fmt.Fprint(s.w, "\r\x1b[2K")
	_, _ = fmt.Fprintln(s.w, message)
	s.mu.Unlock()
	s.render()
}

// render draws the spinner's line, unless it has stopped. It writes under the
// lock, so a redraw never lands in the middle of a line Log is printing.
func (s *Spinner) render() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		return
	}
	select {
	case <-s.done:
		return
	default:
	}
	frame := frames[s.frameIndex%len(frames)]
	s.frameIndex++

	line := fmt.Sprintf("%s %s", frame, s.message)
	if elapsed := time.Since(s.startTime); elapsed >= elapsedThreshold {
		line += ErrDim(fmt.Sprintf(" (%ds)", int(elapsed.Seconds())))
	}
	_, _ = fmt.Fprintf(s.w, "\r\x1b[2K%s", line)
}
