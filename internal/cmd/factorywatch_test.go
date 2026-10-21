package cmd

import (
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// onATerminal makes the command's outputs count as a terminal, outside CI, as
// they are for a person running chunk by hand. Every test's outputs are
// buffers otherwise, which never are.
func onATerminal(t *testing.T) {
	t.Helper()
	t.Setenv("CI", "")
	prev := onTerminal
	onTerminal = func(iostream.Streams) bool { return true }
	t.Cleanup(func() { onTerminal = prev })
}

// The pointers to chunk watch are for a person at a terminal: --json, CI and
// an output that is not a terminal each leave them out.
func TestShowHints(t *testing.T) {
	streams, _, _ := testStreams()
	assert.Assert(t, !showHints(streams, false), "a buffer is not a terminal")

	onATerminal(t)
	assert.Assert(t, showHints(streams, false))
	assert.Assert(t, !showHints(streams, true), "--json is for a program")
	t.Setenv("CI", "true")
	assert.Assert(t, !showHints(streams, false), "CI is not a person")
}

func TestStartMessagesRecommendWatch(t *testing.T) {
	const what = "(findings, check output, the pull request)"
	got := runStartMessage(prFlags{}, false, false, 3)
	assert.Assert(t, strings.HasSuffix(got, "\n"+watchStartHint), got)
	assert.Assert(t, strings.Contains(watchStartHint, "chunk watch` "+what), watchStartHint)
	got = factoryStartMessage(false)
	assert.Assert(t, strings.Contains(got, "\nBest way to follow it from there, and every other run: `chunk watch` "+what), got)
	// --remote keeps the wording it had.
	got = runStartMessage(prFlags{}, true, false, 3)
	assert.Assert(t, !strings.Contains(got, watchStartHint), got)
}

func TestSessionReporterPointsFailuresAtWatch(t *testing.T) {
	failed := chunkd.SessionDetail{Session: chunkd.Session{
		Rounds: []chunkd.Round{{
			Number: 1,
			State:  chunkd.RoundDone,
			Checks: []chunkd.RoundCheck{
				{Name: "test", Status: chunkd.CheckFailed, DurationMS: 3000},
				{Name: "lint", Status: chunkd.CheckPassed, DurationMS: 1000},
			},
		}},
	},
		Details: []chunkd.RoundDetail{{Number: 1, Results: []chunkd.ReviewResult{
			{Prompt: "bugs", Status: chunkd.CheckFailed, Findings: []chunkd.Finding{{File: "a.go", Line: 1, Severity: "high", Body: "x"}}},
			{Prompt: "style", Status: chunkd.CheckPassed},
		}}},
	}
	for _, tc := range []struct {
		name  string
		hints bool
		want  []string
	}{
		{name: "terminal", hints: true, want: []string{
			"round 1: done",
			"  test failed in 3s (details in chunk watch)",
			"  lint passed in 1s",
			"review bugs: 1 finding(s)",
			"  [high] a.go:1 x",
			"review style: no findings worth changing",
		}},
		{name: "not a terminal", want: []string{
			"round 1: done",
			"  test failed in 3s",
			"  lint passed in 1s",
			"review bugs: 1 finding(s)",
			"  [high] a.go:1 x",
			"review style: no findings worth changing",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lines []string
			rep := newSessionReporter(func(_ iostream.Level, msg string) { lines = append(lines, msg) })
			rep.hints = tc.hints
			rep.report(failed)
			assert.DeepEqual(t, lines, tc.want)
		})
	}
}

func TestFactoryRunOnATerminalRecommendsWatch(t *testing.T) {
	factoryProject(t)
	startSessionDaemon(t, fakeFactoryConfig(t, factory.ResultPassed))
	onATerminal(t)

	_, stderr, err := runFactoryBuildCmd(t, "add a --verbose flag")
	assert.NilError(t, err, stderr)
	assertOrdered(t, stderr,
		"Chunk will implement this change",
		watchStartHint,
		"started on the chunk daemon",
		"All checks passed after 1 round(s).",
		"Run run-1: see its reviews and checks in chunk watch.",
	)
}

func TestFactoryExhaustedOnATerminalPointsAtWatchThenContinue(t *testing.T) {
	factoryProject(t)
	startSessionDaemon(t, fakeFactoryConfig(t, factory.ResultExhausted))
	onATerminal(t)

	_, stderr, err := runFactoryBuildCmd(t, "--max-attempts", "1", "add a --verbose flag")
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assertOrdered(t, stderr,
		"review bugs: 1 finding(s) (details in chunk watch)",
		"Run run-1: see the findings in chunk watch.",
	)
	// --continue <run id> follows, as the error's suggestion.
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk factory build --continue run-1"), ue.Suggestion())
}

// With --continue naming the run by another ID, the hint leaves its own out.
func TestWatchHintDoesNotNameTwoIDs(t *testing.T) {
	same := chunkd.SessionDetail{Session: chunkd.Session{ID: "run-1", Factory: &chunkd.FactoryRun{RunID: "run-1", Branch: "chunk/run-1"}}}
	assert.Equal(t, watchHint(same, "see the findings"), "Run run-1: see the findings in chunk watch.")

	continued := chunkd.SessionDetail{Session: chunkd.Session{ID: "run-2", Factory: &chunkd.FactoryRun{RunID: "run-2", Branch: "chunk/run-1"}}}
	assert.Equal(t, watchHint(continued, "see the findings"), "see the findings in chunk watch.")
}

// None of the pointers reach --json or an output that is not a terminal.
func TestFactoryRunLeavesWatchOutOfJSONAndNonTerminals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		args     []string
	}{
		{name: "not a terminal"},
		{name: "--json on a terminal", terminal: true, args: []string{"--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factoryProject(t)
			startSessionDaemon(t, fakeFactoryConfig(t, factory.ResultExhausted))
			if tc.terminal {
				onATerminal(t)
			}
			_, stderr, err := runFactoryBuildCmd(t, append(tc.args, "--max-attempts", "1", "add a --verbose flag")...)
			assert.Assert(t, err != nil)
			assert.Assert(t, !strings.Contains(stderr, "chunk watch"), stderr)
		})
	}
}

func TestPrintInitSummaryRecommendsWatchOnATerminal(t *testing.T) {
	ui.SetColorEnabled(false)
	defer ui.SetColorEnabled(true)
	const line = "Keep chunk watch open in another terminal to stay on top of your runs."

	streams, _, errOut := testStreams()
	printInitSummary(nil, streams)
	assert.Assert(t, !strings.Contains(errOut.String(), "chunk watch"), errOut.String())

	onATerminal(t)
	streams, _, errOut = testStreams()
	printInitSummary(nil, streams)
	assert.Assert(t, strings.HasSuffix(errOut.String(), line+"\n"), errOut.String())
}
