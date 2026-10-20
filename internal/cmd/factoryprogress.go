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
	l := &implementerLine{w: w, status: status, now: time.Now}
	l.width, l.tty = terminalWidth(w)
	return l
}

// terminalWidth is w's width, asked fresh each call since a terminal can be
// resized, and whether w is a terminal at all.
func terminalWidth(w io.Writer) (width func() int, tty bool) {
	f, ok := terminalFile(w)
	if !ok {
		return func() int { return defaultLineWidth }, false
	}
	return func() int {
		if cw, _, err := term.GetSize(int(f.Fd())); err == nil && cw > 0 {
			return cw
		}
		return defaultLineWidth
	}, true
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

// setupLine shows a factory run's sandbox setup while it is held back from a
// terminal's output, drawn in place the way implementerLine draws its line:
// one line, redrawn under the run's status lines and gone once the setup is
// over, so it adds nothing to the scrollback.
type setupLine struct {
	w     io.Writer
	width func() int
	frame int
	// drawn says the line is on the screen, so it must be cleared before
	// anything else is written.
	drawn bool
}

// newSetupLine draws on w, which must be a terminal: a caller following
// without one shows every setup line as it arrives instead.
func newSetupLine(w io.Writer) *setupLine {
	l := &setupLine{w: w}
	l.width, _ = terminalWidth(w)
	return l
}

// clear takes the line off the screen, if it is on it.
func (l *setupLine) clear() {
	if !l.drawn {
		return
	}
	_, _ = fmt.Fprint(l.w, "\r\x1b[2K")
	l.drawn = false
}

// update redraws the spinner line.
func (l *setupLine) update() {
	frame := spinnerFrames[l.frame%len(spinnerFrames)]
	l.frame++
	_, _ = fmt.Fprint(l.w, "\r\x1b[2K"+frame+" Setting up sandboxes...")
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
// The elapsed time and the action count, from ui.TurnFacts, never shrink or
// drop; what gives way, in order, is the "implementing" label, the quiet
// marker, the doing text, then the said text.
func turnLine(frame string, r chunkd.Round, now time.Time, width int) string {
	// The last column is left empty: a terminal wraps a line that fills it,
	// and the next redraw would then leave the first half behind.
	width--
	p := r.Implement.Progress
	facts := ui.TurnFacts(r, now)
	duration, rest := facts[0], facts[1:]
	doing := ""
	if p.Doing != "" {
		doing, rest = rest[0], rest[1:]
	}
	actions := ""
	if p.Actions > 0 {
		actions, rest = rest[0], rest[1:]
	}
	// Whatever is left is the quiet marker, said only when the implementer
	// has gone quiet long enough to be worth mentioning.
	quiet := ""
	if len(rest) > 0 {
		quiet = rest[0]
	}
	withSep := func(f string) string {
		if f == "" {
			return ""
		}
		return " · " + f
	}
	required := func(label string) int {
		return len([]rune(frame)) + len([]rune(label)) + len([]rune(duration)) + len([]rune(withSep(actions)))
	}
	// label is decoration, not a fact, so it is the first thing dropped when
	// even the elapsed time and the action count need its room.
	label := " implementing "
	if required(label) > width {
		label = " "
	}
	// The quiet marker is the next to go: it matters far less than the
	// elapsed time and the action count staying on screen.
	if quiet != "" && required(label)+len([]rune(withSep(quiet))) > width {
		quiet = ""
	}
	head := frame + label + duration
	tail := withSep(actions) + withSep(quiet)
	if doing != "" {
		const sep = " · "
		avail := width - len([]rune(head)) - len([]rune(tail)) - len([]rune(sep))
		switch d := []rune(doing); {
		case avail < 2:
			doing = ""
		case len(d) > avail:
			doing = string(d[:avail-1]) + "…"
		}
		if doing != "" {
			head += sep + doing
		}
	}
	head += tail
	// A terminal too narrow even for the elapsed time and the action count
	// gets them cut short rather than a line that wraps.
	if h := []rune(head); len(h) > width {
		return string(h[:max(width-1, 0)]) + "…"
	}
	said := p.Said
	room := width - len([]rune(head)) - 2
	if said == "" || room < 12 {
		return head
	}
	if s := []rune(said); len(s) > room {
		said = string(s[:room-1]) + "…"
	}
	return head + "  " + ui.ErrDim(said)
}
