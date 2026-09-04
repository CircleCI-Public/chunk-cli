package watch

import (
	"fmt"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestResolveCR(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no carriage return is untouched",
			in:   "plain line",
			want: "plain line",
		},
		{
			name: "trailing CR is stripped",
			in:   "crlf line\r",
			want: "crlf line",
		},
		{
			// The realistic worst case: a test runner's progress bar. Each frame
			// overwrites the last, so only what remains visible should survive.
			name: "progress bar keeps the last frame",
			in:   "  0%\r 50%\r100%",
			want: "100%",
		},
		{
			// A short rewrite does not erase what it does not reach. Truncating
			// to just the rewrite would silently drop output.
			name: "short rewrite overwrites only as far as it reaches",
			in:   "abcdef\rXY",
			want: "XYcdef",
		},
		{
			name: "leading CR is harmless",
			in:   "\rvalue",
			want: "value",
		},
		{
			name: "empty stays empty",
			in:   "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCR(tt.in)
			assert.Check(t, cmp.Equal(got, tt.want))
		})
	}
}

// Captured verbatim from a real sidecar run of
//
//	printf "\033[31mRED TEXT\033[0m\n"; for i in 1 2 3 4 5; do printf "\r%d%% complete" $((i*20)); done
//
// Real output is the only fixture that catches the combination this feature has
// to survive: colour, a carriage-return progress bar, and a plain line together.
const realSidecarOutput = "\x1b[31mRED TEXT\x1b[0m\n\r20% complete\r40% complete\r60% complete\r80% complete\r100% complete\nnow failing\n"

func TestOutputPaneHandlesRealSidecarOutput(t *testing.T) {
	var p outputPane
	p.feed([]byte(realSidecarOutput))

	assert.Assert(t, cmp.Len(p.lines, 3))
	// Colour is passed through untouched.
	assert.Check(t, cmp.Equal(p.lines[0], "\x1b[31mRED TEXT\x1b[0m"))
	// Five progress frames collapse to the last one, rather than stacking five
	// lines or corrupting the split-pane layout.
	assert.Check(t, cmp.Equal(p.lines[1], "100% complete"))
	assert.Check(t, cmp.Equal(p.lines[2], "now failing"))
	assert.Check(t, cmp.Equal(p.pending, ""))
}

func TestOutputPaneFeedSplitsLinesAndHoldsPartial(t *testing.T) {
	var p outputPane
	p.feed([]byte("first\nsecond\npart"))

	assert.Assert(t, cmp.Len(p.lines, 2))
	assert.Check(t, cmp.Equal(p.lines[0], "first"))
	assert.Check(t, cmp.Equal(p.lines[1], "second"))
	// The trailing fragment is held back until its newline arrives, so a line is
	// never rendered half-written.
	assert.Check(t, cmp.Equal(p.pending, "part"))

	p.feed([]byte("ial\n"))
	assert.Assert(t, cmp.Len(p.lines, 3))
	assert.Check(t, cmp.Equal(p.lines[2], "partial"))
	assert.Check(t, cmp.Equal(p.pending, ""))
}

func TestOutputPaneFeedAcrossChunkBoundary(t *testing.T) {
	var p outputPane
	// A CR sequence split across two reads must still resolve correctly — the
	// daemon chunks on byte counts, not line boundaries.
	p.feed([]byte("  0%\r 50"))
	p.feed([]byte("%\r100%\n"))
	assert.Assert(t, cmp.Len(p.lines, 1))
	assert.Check(t, cmp.Equal(p.lines[0], "100%"))
}

func TestOutputPaneFeedEmptyIsNoop(t *testing.T) {
	var p outputPane
	p.feed(nil)
	p.feed([]byte{})
	assert.Check(t, cmp.Len(p.lines, 0))
	assert.Check(t, cmp.Equal(p.pending, ""))
}

func TestOutputPanePreservesANSI(t *testing.T) {
	var p outputPane
	// Colour must survive into the pane: the exec path passes raw bytes through
	// deliberately so remote output renders as intended.
	p.feed([]byte("\x1b[31mFAIL\x1b[0m\n"))
	assert.Assert(t, cmp.Len(p.lines, 1))
	assert.Check(t, cmp.Contains(p.lines[0], "\x1b[31m"))
}

func TestClipMeasuresDisplayWidthNotBytes(t *testing.T) {
	// An escape sequence occupies no columns, so a coloured word that fits must
	// not be clipped on its byte length.
	colored := "\x1b[31mFAIL\x1b[0m"
	assert.Check(t, cmp.Equal(clip(colored, 10), colored))

	// Over-wide input is cut rather than left to wrap and break the layout.
	long := strings.Repeat("x", 40)
	assert.Check(t, cmp.Equal(len(clip(long, 10)), 10))

	assert.Check(t, cmp.Equal(clip("anything", 0), ""))
}

func TestOutputPaneVisibleLinesFollowsTailWhenPinned(t *testing.T) {
	p := outputPane{pinned: true}
	for i := 0; i < 10; i++ {
		p.lines = append(p.lines, string(rune('a'+i)))
	}

	lines, atEnd := p.visibleLines(3)
	assert.Assert(t, cmp.Len(lines, 3))
	assert.Check(t, cmp.Equal(lines[2], "j"), "a pinned pane shows the end of the output")
	assert.Check(t, atEnd)
}

func TestOutputPaneVisibleLinesIncludesPending(t *testing.T) {
	p := outputPane{pinned: true, lines: []string{"done"}, pending: "in progress"}
	lines, _ := p.visibleLines(5)
	assert.Assert(t, cmp.Len(lines, 2))
	// A partial line is still worth showing while tailing — it is the newest
	// thing the command has written.
	assert.Check(t, cmp.Equal(lines[1], "in progress"))
}

func TestOutputPaneScrollUnpinsAndRepins(t *testing.T) {
	p := outputPane{pinned: true}
	for i := 0; i < 10; i++ {
		p.lines = append(p.lines, string(rune('a'+i)))
	}

	// Scrolling up detaches from the bottom, so arriving output does not yank
	// the view away from what is being read.
	p.scrollBy(-2, 3)
	assert.Check(t, !p.pinned)
	lines, atEnd := p.visibleLines(3)
	assert.Check(t, cmp.Equal(lines[0], "f"))
	assert.Check(t, !atEnd)

	// Returning to the bottom re-pins without a separate key.
	p.scrollBy(2, 3)
	assert.Check(t, p.pinned)
	_, atEnd = p.visibleLines(3)
	assert.Check(t, atEnd)
}

func TestOutputPaneScrollStaysPinnedWhenContentFits(t *testing.T) {
	p := outputPane{pinned: true, lines: []string{"one", "two"}}
	p.scrollBy(-5, 10)
	assert.Check(t, p.pinned, "there is nothing to scroll when everything fits")
}

func TestOutputPaneScrollClampsAtTop(t *testing.T) {
	p := outputPane{pinned: true}
	for i := 0; i < 10; i++ {
		p.lines = append(p.lines, string(rune('a'+i)))
	}
	p.scrollBy(-100, 3)
	assert.Check(t, cmp.Equal(p.scroll, 0))
	lines, _ := p.visibleLines(3)
	assert.Check(t, cmp.Equal(lines[0], "a"))
}

// TestOutputPaneCapsScrollback covers the case the daemon's own byte cap does not:
// a tail is served from an advancing offset, so a long verbose command feeds the
// pane far more than the daemon ever holds at once.
func TestOutputPaneCapsScrollback(t *testing.T) {
	p := outputPane{pinned: true}
	for i := range maxPaneLines + 500 {
		p.feed(fmt.Appendf(nil, "line-%d\n", i))
	}

	assert.Check(t, cmp.Len(p.lines, maxPaneLines))
	assert.Check(t, p.truncated, "dropping scrollback must be reported, not silent")
	// The tail is what survives; the oldest lines are the ones that went.
	assert.Check(t, cmp.Equal(p.lines[len(p.lines)-1], fmt.Sprintf("line-%d", maxPaneLines+499)))
	assert.Check(t, cmp.Equal(p.lines[0], "line-500"))
}

// TestOutputPaneTrimKeepsUnpinnedViewOnItsContent checks that trimming shifts a
// scrolled-back reader by the number of lines dropped, so the pane keeps showing
// the same text instead of appearing to jump forward on its own.
func TestOutputPaneTrimKeepsUnpinnedViewOnItsContent(t *testing.T) {
	p := outputPane{}
	for i := range maxPaneLines {
		p.lines = append(p.lines, fmt.Sprintf("line-%d", i))
	}
	p.scroll = 1000
	p.pinned = false
	// Copied, not aliased: visibleLines returns a window into p.lines, and the
	// trim below rewrites that array in place.
	window, _ := p.visibleLines(3)
	before := append([]string(nil), window...)

	p.feed([]byte("line-a\nline-b\n"))

	assert.Check(t, cmp.Equal(p.scroll, 998))
	after, _ := p.visibleLines(3)
	assert.DeepEqual(t, before, after)
}

func TestOutputPaneVisibleLinesHandlesDegenerateSizes(t *testing.T) {
	p := outputPane{lines: []string{"a", "b"}}
	lines, atEnd := p.visibleLines(0)
	assert.Check(t, cmp.Len(lines, 0))
	assert.Check(t, atEnd)

	empty := outputPane{}
	lines, atEnd = empty.visibleLines(5)
	assert.Check(t, cmp.Len(lines, 0))
	assert.Check(t, atEnd)
}
