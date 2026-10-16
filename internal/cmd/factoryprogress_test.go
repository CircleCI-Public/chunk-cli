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

	// Too narrow for it, the remark is left out; narrower still, the facts are
	// cut.
	assert.Equal(t, turnLine("⠋", r, now, 45), "⠋ implementing 10.0s · Run the tests")
	assert.Equal(t, turnLine("⠋", r, now, 20), "⠋ implementing 10.…")
}
