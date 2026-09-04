package watch

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// tailInterval is how often an open output pane asks the daemon for more bytes.
// Only the output request runs this fast; the snapshot request stays on
// pollInterval, so a tail does not multiply the cost of everything else.
const tailInterval = 200 * time.Millisecond

// maxPaneLines caps the scrollback one pane retains.
//
// The daemon's MaxCommandBytes caps what it *holds*, which is not the same bound:
// a tail is served incrementally from an advancing offset, so over a long run the
// pane receives every byte the command ever wrote, including bytes the daemon has
// since evicted. Without a cap here, watching a verbose test suite grows this
// slice for as long as the suite runs. The limit sits comfortably above the
// daemon's retained window, so opening a finished command still shows all of it.
const maxPaneLines = 5000

// outputMsg carries a chunk of command output back to the model.
type outputMsg struct {
	commandID string
	chunk     watchd.OutputChunk
	err       error
}

// outputTickMsg drives the fast poll while a pane is open. seq identifies which
// pane opening the tick belongs to, so a chain from a closed pane cannot keep
// polling alongside the current one.
type outputTickMsg struct{ seq int }

// outputPane is the scrollback view over one command's output.
type outputPane struct {
	commandID string
	// name labels the pane; taken from the invocation that opened it.
	name string
	// lines is the rendered scrollback, already carriage-return resolved.
	lines []string
	// pending holds a trailing partial line, waiting for its newline.
	pending string
	// offset is the next byte offset to request.
	offset int64
	// scroll is the first visible line; 0 means pinned to the bottom.
	scroll int
	// pinned tracks whether new output should keep the view at the bottom. Any
	// manual scroll unpins, so output arriving does not yank the view away from
	// what the developer is reading.
	pinned    bool
	running   bool
	exitCode  *int
	truncated bool
	err       error
}

// feed appends raw output bytes, resolving carriage returns into line rewrites.
//
// Raw bytes are what the remote command wrote, deliberately: the exec path passes
// them through so CR redraws and ANSI colour render as intended in a terminal.
// Inside a split-pane view they would corrupt the layout instead, so \r is
// interpreted here — a progress bar becomes the last frame it drew, not a
// hundred stacked copies.
func (p *outputPane) feed(data []byte) {
	if len(data) == 0 {
		return
	}
	text := p.pending + string(data)
	p.pending = ""

	parts := strings.Split(text, "\n")
	// The final part has no newline yet; hold it until one arrives.
	p.pending = parts[len(parts)-1]
	for _, raw := range parts[:len(parts)-1] {
		p.lines = append(p.lines, resolveCR(raw))
	}
	p.trim()
}

// trim drops the oldest scrollback past maxPaneLines, keeping the tail — the end
// of a run is what anyone opened the pane to read.
//
// An unpinned view is shifted by the same amount so it keeps showing the lines it
// was already showing, rather than appearing to jump forward because the content
// moved underneath it. Dropping lines is the same loss the daemon reports with
// Truncated, so it is flagged the same way.
func (p *outputPane) trim() {
	excess := len(p.lines) - maxPaneLines
	if excess <= 0 {
		return
	}
	// Copy the tail down rather than reslicing, so the backing array is reused
	// instead of growing without bound behind a moving window.
	p.lines = append(p.lines[:0], p.lines[excess:]...)
	p.truncated = true
	p.scroll = max(0, p.scroll-excess)
}

// resolveCR collapses carriage-return rewrites within a single line, keeping only
// what would still be visible after the last one.
//
// A bare \r moves the cursor to column zero, so text after it overwrites text
// before it — but only as far as it reaches. "abcdef\rXY" leaves "XYcdef" on a
// real terminal, not "XY", and truncating to "XY" would silently drop output.
func resolveCR(line string) string {
	line = strings.TrimSuffix(line, "\r")
	if !strings.Contains(line, "\r") {
		return line
	}
	var canvas []rune
	col := 0
	for _, r := range line {
		if r == '\r' {
			col = 0
			continue
		}
		if col < len(canvas) {
			canvas[col] = r
		} else {
			canvas = append(canvas, r)
		}
		col++
	}
	return string(canvas)
}

// visibleLines returns the slice of scrollback to draw, plus whether the view is
// showing the end of the output.
func (p *outputPane) visibleLines(height int) ([]string, bool) {
	all := p.lines
	if p.pending != "" {
		all = append(append([]string(nil), all...), resolveCR(p.pending))
	}
	if height <= 0 || len(all) == 0 {
		return nil, true
	}
	if len(all) <= height {
		return all, true
	}
	if p.pinned {
		return all[len(all)-height:], true
	}
	start := p.scroll
	if start > len(all)-height {
		start = len(all) - height
	}
	if start < 0 {
		start = 0
	}
	return all[start : start+height], start+height >= len(all)
}

// scrollBy moves the view, unpinning it from the bottom. Scrolling back to the
// end re-pins, so a developer who scrolls up to read and then returns to the
// bottom starts following again without a separate key.
func (p *outputPane) scrollBy(delta, height int) {
	total := len(p.lines)
	if p.pending != "" {
		total++
	}
	if total <= height {
		p.pinned = true
		return
	}
	if p.pinned {
		p.scroll = total - height
	}
	p.scroll += delta
	if p.scroll < 0 {
		p.scroll = 0
	}
	maxStart := total - height
	if p.scroll >= maxStart {
		p.scroll = maxStart
		p.pinned = true
		return
	}
	p.pinned = false
}

// fetchOutput asks the daemon for more of a command's output.
func fetchOutput(commandID string, offset int64) tea.Cmd {
	return func() tea.Msg {
		chunk, err := watchd.FetchOutput(commandID, offset)
		return outputMsg{commandID: commandID, chunk: chunk, err: err}
	}
}

// outputTick schedules the next fast poll for the pane opening identified by seq.
func outputTick(seq int) tea.Cmd {
	return tea.Tick(tailInterval, func(time.Time) tea.Msg { return outputTickMsg{seq: seq} })
}
