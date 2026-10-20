package cmd

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

var ansiCodes = regexp.MustCompile("\x1b\\[[0-9;]*m")

var turnStart = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// turnDetail is a session whose first round's implementer turn is in state,
// with progress p.
func turnDetail(state chunkd.ImplementState, p chunkd.ImplementProgress) chunkd.SessionDetail {
	return chunkd.SessionDetail{Session: chunkd.Session{Rounds: []chunkd.Round{{
		Number:    1,
		StartedAt: turnStart,
		Implement: &chunkd.RoundImplement{State: state, Progress: p},
	}}}}
}

// Without a terminal, a running turn is said every implementerHeartbeat and
// not in between, and a new turn starts the count again.
func TestImplementerLineBeatsWithoutATerminal(t *testing.T) {
	var lines []string
	var out bytes.Buffer
	l := newImplementerLine(&out, func(_ iostream.Level, msg string) { lines = append(lines, msg) })
	now := turnStart
	l.now = func() time.Time { return now }
	running := turnDetail(chunkd.ImplementRunning, chunkd.ImplementProgress{Doing: "Run the tests", Actions: 3, At: turnStart})

	l.update(running)
	now = now.Add(implementerHeartbeat - time.Second)
	l.update(running)
	assert.Equal(t, len(lines), 0)

	now = now.Add(time.Second)
	l.update(running)
	assert.DeepEqual(t, lines, []string{"implementer still working: 2m0s · Run the tests · 3 actions"})

	l.update(turnDetail(chunkd.ImplementApplied, chunkd.ImplementProgress{}))
	now = now.Add(implementerHeartbeat)
	l.update(running)
	assert.Equal(t, len(lines), 1)
	assert.Equal(t, out.String(), "", "nothing is drawn without a terminal")
}

// On a terminal the line is redrawn in place, and cleared once the turn ends.
func TestImplementerLineRedrawsInPlaceOnATerminal(t *testing.T) {
	var out bytes.Buffer
	l := &implementerLine{w: &out, tty: true, width: func() int { return 200 }, now: func() time.Time { return turnStart.Add(90 * time.Second) }}

	l.update(turnDetail(chunkd.ImplementRunning, chunkd.ImplementProgress{Doing: "Run the tests", Said: "Adding the flag.", Actions: 1, At: turnStart}))
	assert.Assert(t, l.drawn)
	got := out.String()
	assert.Assert(t, strings.HasPrefix(got, "\r\x1b[2K⠋ implementing 1m30s · Run the tests · 1 action  "), got)
	assert.Assert(t, strings.Contains(got, "Adding the flag."), got)
	assert.Assert(t, !strings.Contains(got, "\n"), "the line never adds to the scrollback")

	out.Reset()
	l.update(turnDetail(chunkd.ImplementApplied, chunkd.ImplementProgress{}))
	assert.Equal(t, out.String(), "\r\x1b[2K")
	assert.Assert(t, !l.drawn)

	out.Reset()
	l.clear()
	assert.Equal(t, out.String(), "", "a line not drawn is not cleared")
}

// setupLine redraws its spinner line in place, the way implementerLine does,
// and clear() erases it, leaving nothing in the scrollback.
func TestSetupLineRedrawsInPlace(t *testing.T) {
	var out bytes.Buffer
	l := &setupLine{w: &out, width: func() int { return 80 }}

	l.update()
	assert.Assert(t, l.drawn)
	first := out.String()
	assert.Equal(t, first, "\r\x1b[2K⠋ Setting up sandboxes...")
	assert.Assert(t, !strings.Contains(first, "\n"), "the line never adds to the scrollback")

	out.Reset()
	l.update()
	assert.Equal(t, out.String(), "\r\x1b[2K⠙ Setting up sandboxes...", "the spinner frame advances on redraw")

	out.Reset()
	l.clear()
	assert.Equal(t, out.String(), "\r\x1b[2K")
	assert.Assert(t, !l.drawn)

	out.Reset()
	l.clear()
	assert.Equal(t, out.String(), "", "a line not drawn is not cleared")
}

func TestTurnLineFitsTheTerminal(t *testing.T) {
	r := turnDetail(chunkd.ImplementRunning, chunkd.ImplementProgress{
		Doing: "Run the tests", Said: strings.Repeat("word ", 40), At: turnStart,
	}).Rounds[0]
	now := turnStart.Add(10 * time.Second)

	// What the implementer said fills the room left, and no more.
	line := ansiCodes.ReplaceAllString(turnLine("⠋", r, now, 60), "")
	assert.Assert(t, strings.HasPrefix(line, "⠋ implementing 10.0s · Run the tests  word word"), line)
	assert.Assert(t, strings.HasSuffix(line, "…"), line)
	assert.Equal(t, len([]rune(line)), 59, "the last column is left empty")

	// Too narrow for it, the remark is left out; narrower still, the
	// "implementing" label gives way, then the doing text is shortened too,
	// but the elapsed time never is, and the last column is still empty.
	assert.Equal(t, turnLine("⠋", r, now, 45), "⠋ implementing 10.0s · Run the tests")
	line20 := turnLine("⠋", r, now, 20)
	assert.Equal(t, line20, "⠋ 10.0s · Run the …")
	assert.Equal(t, len([]rune(line20)), 19, "the last column is left empty")
}

// The elapsed time and the action count must stay visible no matter how long
// the doing text is or how narrow the terminal; the doing text is shortened
// instead, and the line still fits.
func TestTurnLineKeepsTheElapsedTimeAndActionCountVisible(t *testing.T) {
	r := turnDetail(chunkd.ImplementRunning, chunkd.ImplementProgress{
		Doing: strings.Repeat("word ", 40), Actions: 1234, At: turnStart,
	}).Rounds[0]
	now := turnStart.Add(10 * time.Second)

	line := ansiCodes.ReplaceAllString(turnLine("⠋", r, now, 80), "")
	assert.Assert(t, strings.Contains(line, "10.0s"), line)
	assert.Assert(t, strings.Contains(line, "1234 actions"), line)
	assert.Assert(t, len([]rune(line)) <= 79, line)
}

// Even with no doing text to shorten, a long action count must not push the
// line past the terminal's width: the "implementing" label gives way first.
func TestTurnLineShrinksTheLabelBeforeOverflowingOnActionsAlone(t *testing.T) {
	r := turnDetail(chunkd.ImplementRunning, chunkd.ImplementProgress{
		Actions: 1234, At: turnStart,
	}).Rounds[0]
	now := turnStart.Add(10 * time.Second)

	line := turnLine("⠋", r, now, 30)
	assert.Assert(t, strings.Contains(line, "10.0s"), line)
	assert.Assert(t, strings.Contains(line, "1234 actions"), line)
	assert.Assert(t, len([]rune(line)) <= 29, line)
}

// A terminal too narrow for even the elapsed time and the action count still
// gets a line that leaves its last column empty.
func TestTurnLineNeverOverflowsANarrowTerminal(t *testing.T) {
	r := turnDetail(chunkd.ImplementRunning, chunkd.ImplementProgress{
		Actions: 1234, Doing: "editing go.mod", At: turnStart,
	}).Rounds[0]
	now := turnStart.Add(10 * time.Second)

	for _, width := range []int{1, 10, 20, 22} {
		line := turnLine("⠋", r, now, width)
		assert.Assert(t, len([]rune(line)) <= max(width-1, 1), "width %d: %q", width, line)
	}
}

// The quiet marker is the next thing dropped, before the line is ever
// allowed past the terminal's width: the elapsed time and the action count
// still come first.
func TestTurnLineDropsTheQuietMarkerBeforeOverflowing(t *testing.T) {
	r := turnDetail(chunkd.ImplementRunning, chunkd.ImplementProgress{
		Actions: 1234, At: turnStart,
	}).Rounds[0]
	now := turnStart.Add(6 * time.Minute)

	// Sanity check: at this gap the facts do include the quiet marker.
	assert.DeepEqual(t, ui.TurnFacts(r, now), []string{"6m0s", "1234 actions", "quiet 6m"})

	line := turnLine("⠋", r, now, 30)
	assert.Assert(t, strings.Contains(line, "6m0s"), line)
	assert.Assert(t, strings.Contains(line, "1234 actions"), line)
	assert.Assert(t, !strings.Contains(line, "quiet"), line)
	assert.Assert(t, len([]rune(line)) <= 29, line)
}
