package cmd

import (
	"bytes"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// runValidateResults invokes args against a root command with no daemon
// reachable: the watchd dir is a temp dir, so there is no socket to connect to
// and the no-daemon path is what runs.
func runValidateResults(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	isolateConfig(t)
	t.Setenv("CHUNK_WATCHD_DIR", t.TempDir())

	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// The subcommand reads daemon state and validates nothing, so it must work in a
// directory with no chunk config at all. If it were routed through the normal
// validate path instead, the missing config would fail it before it ever asked
// the daemon anything.
func TestValidateResultsNeedsNoProjectConfig(t *testing.T) {
	stdout, _, err := runValidateResults(t, "validate", "results", "--project", t.TempDir())

	assert.NilError(t, err)
	assert.Equal(t, stdout, "", "nothing finished in the background, so nothing belongs on stdout")
}

// No daemon means no background runs, which is the common case rather than a
// failure. This runs on every prompt, so an error here would put noise in front
// of the agent constantly — and a non-zero exit from a UserPromptSubmit hook is
// shown to the user.
func TestValidateResultsIsQuietWithoutADaemon(t *testing.T) {
	stdout, stderr, err := runValidateResults(t, "validate", "results", "--project", t.TempDir())

	assert.NilError(t, err)
	assert.Equal(t, stdout, "")
	assert.Equal(t, stderr, "", "a missing daemon is not worth a word")
}

// --collect is the spelling the results subcommand replaced, and it is written
// into .claude/settings.json by every version of chunk init before this one.
// Those hooks keep invoking it until the settings are regenerated, so it has to
// keep reaching the same place.
func TestValidateCollectFlagStillReachesResults(t *testing.T) {
	stdout, stderr, err := runValidateResults(t, "validate", "--collect", "--project", t.TempDir())

	assert.NilError(t, err)
	assert.Equal(t, stdout, "")
	assert.Equal(t, stderr, "")
}

// Hidden, because it is deprecated: a developer reading --help should be pointed
// at the subcommand, while the flag keeps working for hooks already on disk.
func TestValidateCollectFlagIsHidden(t *testing.T) {
	root := newTestRootCmd()
	validateCmd, _, err := root.Find([]string{"validate"})
	assert.NilError(t, err)

	flag := validateCmd.Flags().Lookup("collect")
	assert.Assert(t, flag != nil, "--collect was removed outright, breaking hooks already installed")
	assert.Assert(t, flag.Hidden, "--collect is deprecated and should not be advertised")
}

// A discarded run has to reach the agent as a discard: no verdict, and no
// silence either. Silence is what it used to be, and it was indistinguishable
// from no run having happened — so a project whose validate commands write
// anything git is watching got nothing, every turn, with nothing to explain it.
func TestResultsReportsADiscardedRunWithoutAVerdict(t *testing.T) {
	var out bytes.Buffer
	streams := iostream.Streams{Out: &out, Err: &out}

	printResults(streams, []watchd.TaskState{{ID: "abcdef123456", Stale: true}})

	got := out.String()
	assert.Assert(t, strings.Contains(got, "discarded"),
		"a discarded run was not reported at all, got: %q", got)
	assert.Assert(t, strings.Contains(got, "abcdef12"),
		"the discard did not identify which run, got: %q", got)
	// The words that would make an agent act. A discard is not a verdict.
	assert.Assert(t, !strings.Contains(got, "passed"),
		"a discarded run read as a pass, got: %q", got)
	assert.Assert(t, !strings.Contains(got, "FAILED"),
		"a discarded run read as a failure, got: %q", got)
}

// The output a stale task carried is dropped by the daemon before it travels,
// but the printer must not resurrect it either: a failure about code that has
// since changed sends an agent after a problem that may already be gone.
func TestResultsDoesNotPrintOutputOfADiscardedRun(t *testing.T) {
	var out bytes.Buffer
	streams := iostream.Streams{Out: &out, Err: &out}

	printResults(streams, []watchd.TaskState{
		{ID: "abcdef123456", Stale: true, ExitCode: 1, Output: "some_test.go:42: FAIL"},
	})

	got := out.String()
	assert.Assert(t, !strings.Contains(got, "some_test.go"),
		"a discarded run's output was shown, got: %q", got)
	assert.Assert(t, !strings.Contains(got, "exit status"),
		"a discarded run reported an exit status, got: %q", got)
}

// A genuine verdict still reads as one, either way.
func TestResultsStillReportsRealVerdicts(t *testing.T) {
	var out bytes.Buffer
	streams := iostream.Streams{Out: &out, Err: &out}

	printResults(streams, []watchd.TaskState{
		{ID: "aaaaaaaa1111", ExitCode: 0},
		{ID: "bbbbbbbb2222", ExitCode: 1, Output: "0/1 passed"},
	})

	got := out.String()
	assert.Assert(t, strings.Contains(got, "passed in the background"), "got: %q", got)
	assert.Assert(t, strings.Contains(got, "FAILED in the background"), "got: %q", got)
	assert.Assert(t, strings.Contains(got, "0/1 passed"), "a real failure lost its output, got: %q", got)
}
