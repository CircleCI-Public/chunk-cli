package watch

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// paneModel builds the dashboard the output pane is opened over: one factory
// run, selected, with the right pane focused.
func paneModel(t *testing.T) Model {
	t.Helper()
	m := sessModel(factoryRun("abc", chunkd.SessionRunning, time.Now()))
	m.width, m.height = 120, 40
	m.focusedPane = paneRight
	return m.reselectRun()
}

// Closing must not schedule a dashboard tick: the dashboard's own poll chain ran
// throughout, so starting another leaves two chains polling and doubles the rate
// on every open/close cycle.
func TestClosingOutputPaneDoesNotStartASecondPollChain(t *testing.T) {
	m := paneModel(t)
	m.output = &outputPane{commandID: "cmd-1"}

	next, cmd := m.updateOutputKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Check(t, cmp.Nil(nm.output), "escape closes the pane")
	assert.Check(t, cmp.Nil(cmd), "closing must not schedule another poll")
}

// A tick belonging to a previous pane opening must die rather than poll
// alongside the current one.
func TestStaleOutputTickIsDropped(t *testing.T) {
	m := paneModel(t)
	// Running, so that the reschedule turns on the sequence check alone — a
	// finished pane stops for its own reason and would not test this.
	m.output = &outputPane{commandID: "cmd-2", running: true}
	m.outputSeq = 2

	_, cmd := m.Update(outputTickMsg{seq: 1})
	assert.Check(t, cmp.Nil(cmd), "a tick from a closed pane must not continue")

	_, live := m.Update(outputTickMsg{seq: 2})
	assert.Check(t, live != nil, "the current pane's tick must continue")
}

func TestOutputTickStopsWhenPaneClosed(t *testing.T) {
	m := paneModel(t)
	_, cmd := m.Update(outputTickMsg{seq: 0})
	assert.Check(t, cmp.Nil(cmd))
}

func TestWithOutputChunkIgnoresChunkForAnotherCommand(t *testing.T) {
	m := paneModel(t)
	m.output = &outputPane{commandID: "cmd-current"}

	next, _ := m.Update(outputMsg{
		commandID: "cmd-stale",
		chunk:     chunkd.OutputChunk{Found: true, Data: []byte("wrong output"), NextOffset: 12},
	})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Check(t, cmp.Len(nm.output.lines, 0), "output for a different command must be discarded")
	assert.Check(t, cmp.Equal(nm.output.offset, int64(0)))
}

func TestWithOutputChunkReportsMissingCommand(t *testing.T) {
	m := paneModel(t)
	m.output = &outputPane{commandID: "cmd-1"}

	next, _ := m.Update(outputMsg{commandID: "cmd-1", chunk: chunkd.OutputChunk{Found: false}})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Check(t, nm.output.err != nil, "a forgotten command must be reported, not shown as empty")
}

func TestWithOutputChunkAdvancesOffsetAndState(t *testing.T) {
	m := paneModel(t)
	m.output = &outputPane{commandID: "cmd-1", pinned: true}
	exit := 2

	next, _ := m.Update(outputMsg{commandID: "cmd-1", chunk: chunkd.OutputChunk{
		Found: true, Data: []byte("line\n"), NextOffset: 5, Running: false, ExitCode: &exit, Truncated: true,
	}})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Check(t, cmp.Equal(nm.output.offset, int64(5)))
	assert.Check(t, cmp.Len(nm.output.lines, 1))
	assert.Check(t, nm.output.truncated)
	assert.Assert(t, nm.output.exitCode != nil)
	assert.Check(t, cmp.Equal(*nm.output.exitCode, 2))
}

// truncated must latch: a later chunk that happens not to be truncated does not
// mean the earlier gap healed.
func TestWithOutputChunkKeepsTruncatedLatched(t *testing.T) {
	m := paneModel(t)
	m.output = &outputPane{commandID: "cmd-1", truncated: true}

	next, _ := m.Update(outputMsg{commandID: "cmd-1", chunk: chunkd.OutputChunk{
		Found: true, Data: []byte("more\n"), NextOffset: 5, Running: true,
	}})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Check(t, nm.output.truncated)
}

// The daemon explaining why a stream broke is worth nothing if the pane drops it.
func TestWithOutputChunkSurfacesStreamError(t *testing.T) {
	m := paneModel(t)
	m.output = &outputPane{commandID: "cmd-1"}

	next, _ := m.Update(outputMsg{commandID: "cmd-1", chunk: chunkd.OutputChunk{
		Found: true, Data: []byte("partial\n"), NextOffset: 8,
		Error: "400 Bad Request — Invalid command ID",
	}})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Assert(t, nm.output.err != nil)
	assert.Check(t, cmp.Contains(nm.output.err.Error(), "Invalid command ID"))
	// The output that did arrive is still kept.
	assert.Check(t, cmp.Len(nm.output.lines, 1))

	out := nm.renderOutputPane(newWatchStyles(false))
	assert.Check(t, cmp.Contains(out, "Invalid command ID"))
}

func TestWithOutputChunkClearsStreamErrorWhenItResolves(t *testing.T) {
	m := paneModel(t)
	m.output = &outputPane{commandID: "cmd-1"}

	next, _ := m.Update(outputMsg{commandID: "cmd-1", chunk: chunkd.OutputChunk{
		Found: true, Running: true, Error: "transient",
	}})
	nm := next.(Model)
	assert.Assert(t, nm.output.err != nil)

	next, _ = nm.Update(outputMsg{commandID: "cmd-1", chunk: chunkd.OutputChunk{
		Found: true, Running: true,
	}})
	nm2 := next.(Model)
	assert.Check(t, cmp.Nil(nm2.output.err), "a recovered stream must stop reporting an error")
}

func TestRenderOutputPaneShowsStatusAndHints(t *testing.T) {
	m := paneModel(t)
	exit := 1
	m.output = &outputPane{
		commandID: "cmd-1",
		name:      "go test ./...",
		lines:     []string{"FAIL\tpkg/foo"},
		pinned:    true,
		exitCode:  &exit,
		truncated: true,
	}

	out := m.renderOutputPane(newWatchStyles(false))
	assert.Check(t, cmp.Contains(out, "go test ./..."))
	assert.Check(t, cmp.Contains(out, "exit 1"))
	assert.Check(t, cmp.Contains(out, "FAIL"))
	assert.Check(t, cmp.Contains(out, "earlier output dropped"))
	assert.Check(t, cmp.Contains(out, "esc"))
}

func TestRenderUsesOutputPaneWhenOpen(t *testing.T) {
	m := paneModel(t)
	m.output = &outputPane{commandID: "cmd-1", name: "lint", lines: []string{"only-in-output-pane"}}

	out := m.render()
	assert.Check(t, cmp.Contains(out, "only-in-output-pane"))
	// The two-pane dashboard must not also be drawn.
	assert.Check(t, !strings.Contains(out, "runs"), "the dashboard should be replaced, not appended")
}

// TestOutputPaneHoldsTheDashboardHeight pins the layout budget. The pane replaces
// the dashboard in the same fixed-height frame, so drawing one line more scrolls
// the terminal on every render — and an error line added on top of a full
// scrollback is exactly how that happens.
func TestOutputPaneHoldsTheDashboardHeight(t *testing.T) {
	dashboard := paneModel(t)
	want := strings.Count(dashboard.render(), "\n")

	// Enough output to fill the pane, so nothing is absorbed by blank padding.
	filled := func() *outputPane {
		p := &outputPane{commandID: "cmd-1", name: "go test ./...", pinned: true}
		p.feed([]byte(strings.Repeat("a line of output\n", 200)))
		return p
	}

	quiet := paneModel(t)
	quiet.output = filled()
	assert.Check(t, cmp.Equal(strings.Count(quiet.render(), "\n"), want))

	failed := paneModel(t)
	failed.output = filled()
	failed.output.err = errors.New("stream ended early")
	assert.Check(t, cmp.Equal(strings.Count(failed.render(), "\n"), want),
		"an error line must come out of the scrollback budget, not add to it")
}

func TestOutputPaneRendersAtDegenerateHeights(t *testing.T) {
	for _, height := range []int{0, 1, 2, 5, 6} {
		m := paneModel(t)
		m.height = height
		m.output = &outputPane{commandID: "cmd-1", name: "go test", pinned: true}
		m.output.feed([]byte("one\ntwo\n"))
		m.output.err = errors.New("boom")
		// Nothing to assert beyond "does not panic and draws something": a pane one
		// line tall is a terminal being resized, not a state worth a layout.
		assert.Check(t, m.render() != "", "height %d", height)
	}
}

// A finished command's buffer never changes again, so the fast tail must stop
// rather than keep asking. The pane can sit open on a finished command for as
// long as someone is reading it, and at 200ms that is a socket round trip five
// times a second for output that cannot move.
func TestOutputTickStopsOnceTheCommandFinishes(t *testing.T) {
	m := paneModel(t)
	m.output = &outputPane{commandID: "cmd-1", running: true}
	m.outputSeq = 1

	_, live := m.Update(outputTickMsg{seq: 1})
	assert.Assert(t, live != nil, "a running command must keep tailing")

	// The chunk that reports the exit is complete by construction: the daemon
	// appends everything before marking the command done.
	code := 0
	next, _ := m.Update(outputMsg{
		commandID: "cmd-1",
		chunk:     chunkd.OutputChunk{Found: true, Running: false, ExitCode: &code},
	})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Check(t, !nm.output.running)

	_, done := nm.Update(outputTickMsg{seq: 1})
	assert.Check(t, cmp.Nil(done), "a finished command must not reschedule the tail")
}

// An evicted buffer is not coming back, so the tail has nothing to wait for.
// Without this the chain polls forever for a command the daemon has forgotten.
func TestOutputTickStopsWhenTheBufferWasEvicted(t *testing.T) {
	m := paneModel(t)
	m.output = &outputPane{commandID: "cmd-1", running: true}
	m.outputSeq = 1

	next, _ := m.Update(outputMsg{
		commandID: "cmd-1",
		chunk:     chunkd.OutputChunk{Found: false},
	})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Check(t, nm.output.err != nil, "the pane must say why it is empty")
	assert.Check(t, !nm.output.running)

	_, done := nm.Update(outputTickMsg{seq: 1})
	assert.Check(t, cmp.Nil(done), "an evicted buffer must not keep being polled")
}

// The render path must not scale with the scrollback. It runs on every 200ms
// tick and every keypress, and the pane holds up to maxPaneLines, so building
// the whole thing to show forty rows was the difference between a fixed cost
// and a per-frame copy of the entire buffer.
func TestVisibleLinesDoesNotCopyTheWholeScrollback(t *testing.T) {
	p := &outputPane{pending: "still typing", pinned: true}
	for i := range maxPaneLines {
		p.lines = append(p.lines, fmt.Sprintf("line %d", i))
	}

	const height = 40

	// Bytes, not allocation count: the old implementation was also a couple of
	// allocations, they were just the size of the whole scrollback. Counting
	// events would have called this fixed when it was not.
	res := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			p.visibleLines(height)
		}
	})
	perRender := res.AllocedBytesPerOp()

	// A window of `height` string headers, with room to spare. The whole
	// scrollback would be maxPaneLines headers, two orders of magnitude more.
	assert.Check(t, perRender < 4096,
		"visibleLines allocated %d bytes per render for a %d-row window of %d lines",
		perRender, height, len(p.lines))

	got, atEnd := p.visibleLines(height)
	assert.Check(t, cmp.Len(got, height))
	assert.Check(t, atEnd)
	assert.Check(t, cmp.Equal(got[height-1], "still typing"), "the pending line stays last")
}

// The pending line must not be written into the scrollback's spare capacity:
// feed and trim both leave room behind len(p.lines), so an in-place append
// would be overwritten by the next feed under a slice the renderer still holds.
func TestVisibleLinesDoesNotWriteIntoTheScrollback(t *testing.T) {
	p := &outputPane{pinned: true}
	p.lines = make([]string, 2, 8) // spare capacity, as feed leaves behind
	p.lines[0], p.lines[1] = "one", "two"
	p.pending = "three"

	got, _ := p.visibleLines(10)
	assert.Check(t, cmp.Equal(strings.Join(got, "|"), "one|two|three"))

	// Reading the window must not have extended the scrollback itself.
	assert.Check(t, cmp.Len(p.lines, 2))
	assert.Check(t, cmp.Equal(p.lines[:cap(p.lines)][2], ""),
		"the pending line leaked into the backing array")
}
