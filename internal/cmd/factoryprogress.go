package cmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// implementerHeartbeat is how often a run followed without a terminal says the
// implementer is still working. A turn can run for half an hour, and silence
// that long reads as a hang.
const implementerHeartbeat = 2 * time.Minute

// defaultLineWidth is the progress line's width when the terminal's is unknown.
const defaultLineWidth = 80

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// implementerLine shows how an implementer turn is going while it runs. On a
// terminal it is one line, redrawn in place under the run's status lines and
// gone when the turn ends, so it adds nothing to the scrollback. Without one it
// is a status line every implementerHeartbeat.
type implementerLine struct {
	w   io.Writer
	tty bool
	// width is the terminal's width, asked each time the line is drawn, since
	// the terminal can be resized.
	width  func() int
	status iostream.StatusFunc
	now    func() time.Time

	frame int
	// drawn says the line is on the screen, so it must be cleared before
	// anything else is written.
	drawn bool
	// beat is when the last heartbeat was said, or when the turn was first seen
	// running; zero when no turn is.
	beat time.Time
}

// newImplementerLine draws on w when it is a terminal and through status
// otherwise.
func newImplementerLine(w io.Writer, status iostream.StatusFunc) *implementerLine {
	l := &implementerLine{w: w, status: status, now: time.Now, width: func() int { return defaultLineWidth }}
	if f, ok := terminalFile(w); ok {
		l.tty = true
		l.width = func() int {
			if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
				return w
			}
			return defaultLineWidth
		}
	}
	return l
}

// clear takes the line off the screen, if it is on it.
func (l *implementerLine) clear() {
	if !l.drawn {
		return
	}
	_, _ = fmt.Fprint(l.w, "\r\x1b[2K")
	l.drawn = false
}

// update shows the turn running in d, or clears the line when none is.
func (l *implementerLine) update(d chunkd.SessionDetail) {
	r, ok := runningTurn(d.Session)
	if !ok {
		l.clear()
		l.beat = time.Time{}
		return
	}
	now := l.now()
	if !l.tty {
		if l.beat.IsZero() {
			l.beat = now
			return
		}
		if now.Sub(l.beat) >= implementerHeartbeat {
			l.beat = now
			l.status(iostream.LevelInfo, "implementer still working: "+strings.Join(ui.TurnFacts(r, now), " · "))
		}
		return
	}
	frame := spinnerFrames[l.frame%len(spinnerFrames)]
	l.frame++
	_, _ = fmt.Fprint(l.w, "\r\x1b[2K"+turnLine(frame, r, now, l.width()))
	l.drawn = true
}

// runningTurn is the round whose implementer turn is running, if one is.
func runningTurn(s chunkd.Session) (chunkd.Round, bool) {
	for _, r := range s.Rounds {
		if r.Implement != nil && r.Implement.State == chunkd.ImplementRunning {
			return r, true
		}
	}
	return chunkd.Round{}, false
}

// turnLine is the progress line for a running turn, at most width columns:
// the facts, then what the implementer last said in whatever room is left.
func turnLine(frame string, r chunkd.Round, now time.Time, width int) string {
	// The last column is left empty: a terminal wraps a line that fills it,
	// and the next redraw would then leave the first half behind.
	width--
	head := frame + " implementing " + strings.Join(ui.TurnFacts(r, now), " · ")
	if n := len([]rune(head)); n > width {
		return string([]rune(head)[:max(width-1, 0)]) + "…"
	}
	said := r.Implement.Progress.Said
	room := width - len([]rune(head)) - 2
	if said == "" || room < 12 {
		return head
	}
	if s := []rune(said); len(s) > room {
		said = string(s[:room-1]) + "…"
	}
	return head + "  " + ui.ErrDim(said)
}
