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
	s.mu.Unlock()
	_, _ = fmt.Fprint(s.w, "\r\x1b[2K")
}

func (s *Spinner) StopWithMessage(message string) {
	s.Stop()
	_, _ = fmt.Fprintln(s.w, message)
}

// Log prints a message on its own line while keeping the spinner running.
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

func (s *Spinner) render() {
	s.mu.Lock()
	frame := frames[s.frameIndex%len(frames)]
	s.frameIndex++
	msg := s.message
	start := s.startTime
	s.mu.Unlock()

	line := fmt.Sprintf("%s %s", frame, msg)
	if elapsed := time.Since(start); elapsed >= elapsedThreshold {
		line += Dim(fmt.Sprintf(" (%ds)", int(elapsed.Seconds())))
	}
	_, _ = fmt.Fprintf(s.w, "\r\x1b[2K%s", line)
}
