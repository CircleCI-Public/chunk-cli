package watch

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// modelWithInvocation builds a model holding one sidecar, one invocation, and
// optionally a buffered command matching it.
func modelWithInvocation(t *testing.T, cmds []watchd.CommandState) Model {
	t.Helper()
	start := time.Now().Add(-time.Minute)
	events := []eventlog.Event{
		{Ts: start, SidecarID: "sc-1", Op: eventlog.OpValidate, Level: "info", Msg: "$ go test ./..."},
		{Ts: start.Add(5 * time.Second), SidecarID: "sc-1", Op: eventlog.OpValidate, Level: "error", Msg: "1/2 passed  5.0s"},
	}
	return Model{
		width:  120,
		height: 40,
		sidecars: []sidecarInfo{{
			id:         "sc-1",
			sidecarIDs: []string{"sc-1"},
			projectIdx: 0,
		}},
		events:      [][]eventlog.Event{events},
		commands:    [][]watchd.CommandState{cmds},
		focusedPane: paneRight,
	}
}

func TestCommandForInvocationMatchesOnSidecarAndTime(t *testing.T) {
	submitted := time.Now().Add(-time.Minute).Add(time.Second)
	m := modelWithInvocation(t, []watchd.CommandState{{
		CommandID:   "cmd-1",
		SidecarID:   "sc-1",
		SubmittedAt: submitted,
	}})

	groups := m.currentInvocGroups()
	assert.Assert(t, cmp.Len(groups, 1))
	got := m.commandForInvocation(groups[0])
	assert.Assert(t, got != nil)
	assert.Check(t, cmp.Equal(got.CommandID, "cmd-1"))
}

func TestCommandForInvocationIgnoresOtherSidecar(t *testing.T) {
	submitted := time.Now().Add(-time.Minute).Add(time.Second)
	m := modelWithInvocation(t, []watchd.CommandState{{
		CommandID:   "cmd-other",
		SidecarID:   "sc-2",
		SubmittedAt: submitted,
	}})

	groups := m.currentInvocGroups()
	got := m.commandForInvocation(groups[0])
	assert.Check(t, cmp.Nil(got), "a command from another sidecar must not be offered")
}

func TestCommandForInvocationIgnoresCommandOutsideSpan(t *testing.T) {
	m := modelWithInvocation(t, []watchd.CommandState{{
		CommandID:   "cmd-old",
		SidecarID:   "sc-1",
		SubmittedAt: time.Now().Add(-2 * time.Hour),
	}})

	groups := m.currentInvocGroups()
	got := m.commandForInvocation(groups[0])
	assert.Check(t, cmp.Nil(got), "a command from a different run must not be joined to this one")
}

func TestCommandForInvocationPrefersLatestMatch(t *testing.T) {
	base := time.Now().Add(-time.Minute)
	m := modelWithInvocation(t, []watchd.CommandState{
		{CommandID: "cmd-first", SidecarID: "sc-1", SubmittedAt: base.Add(time.Second)},
		{CommandID: "cmd-second", SidecarID: "sc-1", SubmittedAt: base.Add(3 * time.Second)},
	})

	groups := m.currentInvocGroups()
	got := m.commandForInvocation(groups[0])
	assert.Assert(t, got != nil)
	assert.Check(t, cmp.Equal(got.CommandID, "cmd-second"), "a re-run inside one group resolves to the newer command")
}

func TestOpenSelectedOutputRequiresACommand(t *testing.T) {
	m := modelWithInvocation(t, nil)
	opened, cmd := m.openSelectedOutput()
	assert.Check(t, cmp.Nil(opened), "with no buffered command there is nothing to open")
	assert.Check(t, cmp.Nil(cmd))
}

func TestOpenSelectedOutputSetsUpPane(t *testing.T) {
	submitted := time.Now().Add(-time.Minute).Add(time.Second)
	exit := 1
	m := modelWithInvocation(t, []watchd.CommandState{{
		CommandID:   "cmd-1",
		SidecarID:   "sc-1",
		Name:        "go test ./...",
		SubmittedAt: submitted,
		ExitCode:    &exit,
	}})

	opened, cmd := m.openSelectedOutput()
	assert.Assert(t, opened != nil)
	assert.Assert(t, opened.output != nil)
	assert.Check(t, cmp.Equal(opened.output.commandID, "cmd-1"))
	assert.Check(t, cmp.Equal(opened.output.name, "go test ./..."))
	assert.Check(t, opened.output.pinned, "a freshly opened pane follows the end")
	assert.Check(t, cmd != nil)
	assert.Check(t, opened.outputSeq > m.outputSeq, "opening must advance the tick generation")
}

func TestOpenSelectedOutputFallsBackToInvocationLabel(t *testing.T) {
	submitted := time.Now().Add(-time.Minute).Add(time.Second)
	m := modelWithInvocation(t, []watchd.CommandState{{
		CommandID:   "cmd-1",
		SidecarID:   "sc-1",
		SubmittedAt: submitted,
	}})

	opened, _ := m.openSelectedOutput()
	assert.Assert(t, opened != nil)
	// Taken from the "$ ..." status event when the registration carried no name.
	assert.Check(t, cmp.Equal(opened.output.name, "go test ./..."))
}

// Closing must not schedule a dashboard tick: the dashboard's own poll chain ran
// throughout, so starting another leaves two chains polling and doubles the rate
// on every open/close cycle.
func TestClosingOutputPaneDoesNotStartASecondPollChain(t *testing.T) {
	m := modelWithInvocation(t, nil)
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
	m := modelWithInvocation(t, nil)
	m.output = &outputPane{commandID: "cmd-2"}
	m.outputSeq = 2

	_, cmd := m.Update(outputTickMsg{seq: 1})
	assert.Check(t, cmp.Nil(cmd), "a tick from a closed pane must not continue")

	_, live := m.Update(outputTickMsg{seq: 2})
	assert.Check(t, live != nil, "the current pane's tick must continue")
}

func TestOutputTickStopsWhenPaneClosed(t *testing.T) {
	m := modelWithInvocation(t, nil)
	_, cmd := m.Update(outputTickMsg{seq: 0})
	assert.Check(t, cmp.Nil(cmd))
}

// With the pane covering the dashboard, a click would move a selection the
// reader cannot see.
func TestMouseClickIgnoredWhileOutputPaneOpen(t *testing.T) {
	m := modelWithInvocation(t, nil)
	m.rightSelectedIdx = 3
	m.output = &outputPane{commandID: "cmd-1"}

	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 90, Y: 6})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Check(t, cmp.Equal(nm.rightSelectedIdx, 3), "selection must not move behind the pane")
}

func TestWithOutputChunkIgnoresChunkForAnotherCommand(t *testing.T) {
	m := modelWithInvocation(t, nil)
	m.output = &outputPane{commandID: "cmd-current"}

	next, _ := m.Update(outputMsg{
		commandID: "cmd-stale",
		chunk:     watchd.OutputChunk{Found: true, Data: []byte("wrong output"), NextOffset: 12},
	})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Check(t, cmp.Len(nm.output.lines, 0), "output for a different command must be discarded")
	assert.Check(t, cmp.Equal(nm.output.offset, int64(0)))
}

func TestWithOutputChunkReportsMissingCommand(t *testing.T) {
	m := modelWithInvocation(t, nil)
	m.output = &outputPane{commandID: "cmd-1"}

	next, _ := m.Update(outputMsg{commandID: "cmd-1", chunk: watchd.OutputChunk{Found: false}})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Check(t, nm.output.err != nil, "a forgotten command must be reported, not shown as empty")
}

func TestWithOutputChunkAdvancesOffsetAndState(t *testing.T) {
	m := modelWithInvocation(t, nil)
	m.output = &outputPane{commandID: "cmd-1", pinned: true}
	exit := 2

	next, _ := m.Update(outputMsg{commandID: "cmd-1", chunk: watchd.OutputChunk{
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
	m := modelWithInvocation(t, nil)
	m.output = &outputPane{commandID: "cmd-1", truncated: true}

	next, _ := m.Update(outputMsg{commandID: "cmd-1", chunk: watchd.OutputChunk{
		Found: true, Data: []byte("more\n"), NextOffset: 5, Running: true,
	}})
	nm, ok := next.(Model)
	assert.Assert(t, ok)
	assert.Check(t, nm.output.truncated)
}

// The daemon explaining why a stream broke is worth nothing if the pane drops it.
func TestWithOutputChunkSurfacesStreamError(t *testing.T) {
	m := modelWithInvocation(t, nil)
	m.output = &outputPane{commandID: "cmd-1"}

	next, _ := m.Update(outputMsg{commandID: "cmd-1", chunk: watchd.OutputChunk{
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
	m := modelWithInvocation(t, nil)
	m.output = &outputPane{commandID: "cmd-1"}

	next, _ := m.Update(outputMsg{commandID: "cmd-1", chunk: watchd.OutputChunk{
		Found: true, Running: true, Error: "transient",
	}})
	nm := next.(Model)
	assert.Assert(t, nm.output.err != nil)

	next, _ = nm.Update(outputMsg{commandID: "cmd-1", chunk: watchd.OutputChunk{
		Found: true, Running: true,
	}})
	nm2 := next.(Model)
	assert.Check(t, cmp.Nil(nm2.output.err), "a recovered stream must stop reporting an error")
}

func TestRenderOutputPaneShowsStatusAndHints(t *testing.T) {
	m := modelWithInvocation(t, nil)
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
	m := modelWithInvocation(t, nil)
	m.output = &outputPane{commandID: "cmd-1", name: "lint", lines: []string{"only-in-output-pane"}}

	out := m.render()
	assert.Check(t, cmp.Contains(out, "only-in-output-pane"))
	// The two-pane dashboard must not also be drawn.
	assert.Check(t, !strings.Contains(out, "sidecars"), "the dashboard should be replaced, not appended")
}

// TestOutputPaneHoldsTheDashboardHeight pins the layout budget. The pane replaces
// the dashboard in the same fixed-height frame, so drawing one line more scrolls
// the terminal on every render — and an error line added on top of a full
// scrollback is exactly how that happens.
func TestOutputPaneHoldsTheDashboardHeight(t *testing.T) {
	dashboard := modelWithInvocation(t, nil)
	want := strings.Count(dashboard.render(), "\n")

	// Enough output to fill the pane, so nothing is absorbed by blank padding.
	filled := func() *outputPane {
		p := &outputPane{commandID: "cmd-1", name: "go test ./...", pinned: true}
		p.feed([]byte(strings.Repeat("a line of output\n", 200)))
		return p
	}

	quiet := modelWithInvocation(t, nil)
	quiet.output = filled()
	assert.Check(t, cmp.Equal(strings.Count(quiet.render(), "\n"), want))

	failed := modelWithInvocation(t, nil)
	failed.output = filled()
	failed.output.err = errors.New("stream ended early")
	assert.Check(t, cmp.Equal(strings.Count(failed.render(), "\n"), want),
		"an error line must come out of the scrollback budget, not add to it")
}

func TestOutputPaneRendersAtDegenerateHeights(t *testing.T) {
	for _, height := range []int{0, 1, 2, 5, 6} {
		m := modelWithInvocation(t, nil)
		m.height = height
		m.output = &outputPane{commandID: "cmd-1", name: "go test", pinned: true}
		m.output.feed([]byte("one\ntwo\n"))
		m.output.err = errors.New("boom")
		// Nothing to assert beyond "does not panic and draws something": a pane one
		// line tall is a terminal being resized, not a state worth a layout.
		assert.Check(t, m.render() != "", "height %d", height)
	}
}
