package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// assertOrdered fails unless every substring in order appears in got, each
// one later than the last: the request requires the message's parts (what
// chunk does, that it reviews and checks, what it ends with, that you can
// walk away) to read in that order, not merely all be present somewhere.
func assertOrdered(t *testing.T, got string, order ...string) {
	t.Helper()
	pos := -1
	for _, s := range order {
		i := strings.Index(got[pos+1:], s)
		assert.Assert(t, i >= 0, "missing %q (in order) in:\n%s", s, got)
		pos += 1 + i
	}
}

// The wording must say what chunk is about to do and, accurately, what the
// run ends with: never more than the flags actually promise.
func TestRunStartMessage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pr         prFlags
		remote     bool
		continuing bool
		want       []string
		notWant    []string
	}{
		{
			name: "default",
			want: []string{"Chunk will implement this change", "review it", "run your checks",
				"opens a pull request if it can", "chunk watch"},
		},
		{
			name:    "--open-pr",
			pr:      prFlags{open: true},
			want:    []string{"Chunk will implement this change", "opens the pull request"},
			notWant: []string{"if it can", "draft"},
		},
		{
			// --draft alone still resolves to the auto PR mode (prFlags.mode
			// only goes to Required with --open-pr), so the project may still
			// end with no pull request at all; the wording must hedge the
			// same way the plain default does.
			name:    "--draft",
			pr:      prFlags{draft: true},
			want:    []string{"Chunk will implement this change", "opens a pull request as a draft if it can"},
			notWant: []string{"opens the pull request as a draft."},
		},
		{
			// --draft with --open-pr insists on the pull request, so it is
			// refused up front if the project cannot open one: the message
			// can promise it unconditionally.
			name:    "--draft --open-pr",
			pr:      prFlags{draft: true, open: true},
			want:    []string{"Chunk will implement this change", "opens the pull request as a draft"},
			notWant: []string{"if it can"},
		},
		{
			name:    "--no-open-pr",
			pr:      prFlags{none: true},
			want:    []string{"Chunk will implement this change", "does not open a pull request", "local branch"},
			notWant: []string{"opens the pull request", "opens a pull request"},
		},
		{
			name:   "--remote",
			remote: true,
			want: []string{"Chunk will implement this change", "its own sidecar", "chunk factory pull",
				"does not open a pull request yet"},
			notWant: []string{"opens the pull request", "opens a pull request"},
		},
		{
			name:       "--continue",
			continuing: true,
			want:       []string{"Chunk will review the existing work", "opens a pull request if it can"},
			notWant:    []string{"implement this change"},
		},
		{
			name:       "--continue --open-pr",
			continuing: true,
			pr:         prFlags{open: true},
			want:       []string{"Chunk will review the existing work", "opens the pull request"},
			notWant:    []string{"implement this change", "if it can"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runStartMessage(tc.pr, tc.remote, tc.continuing, 3)
			for _, want := range tc.want {
				assert.Assert(t, strings.Contains(got, want), "missing %q in:\n%s", want, got)
			}
			for _, notWant := range tc.notWant {
				assert.Assert(t, !strings.Contains(got, notWant), "unexpected %q in:\n%s", notWant, got)
			}
			// tc.want is written in the order the request requires: what
			// chunk does, then that it reviews and runs checks, then what it
			// ends with, then that you can walk away.
			assertOrdered(t, got, tc.want...)
		})
	}
}

// The run-start message says how many rounds the run gets and the flag that
// changes it, before what the run ends with.
func TestRunStartMessageNamesTheRoundCap(t *testing.T) {
	for _, tc := range []struct {
		attempts int
		want     string
	}{
		{attempts: 1, want: "for 1 review round (change with --max-attempts)."},
		{attempts: 3, want: "for up to 3 review rounds (change with --max-attempts)."},
		{attempts: 10, want: "for up to 10 review rounds (change with --max-attempts)."},
	} {
		got := runStartMessage(prFlags{}, false, false, tc.attempts)
		assertOrdered(t, got, "run your checks until they pass", tc.want, "opens a pull request if it can")
		got = runStartMessage(prFlags{}, true, false, tc.attempts)
		assertOrdered(t, got, "run your checks until they pass", tc.want, "its own sidecar")
	}
}

// chunk factory start reviews nothing and pushes nothing, so its own message
// must not claim either, whether or not it starts from a diff.
func TestFactoryStartMessage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		diff    bool
		want    []string
		notWant []string
	}{
		{
			name:    "no diff",
			want:    []string{"worktree and branch", "Nothing is reviewed or pushed yet", "build --continue"},
			notWant: []string{"starting with the diff"},
		},
		{
			name: "--diff",
			diff: true,
			want: []string{"worktree and branch", "starting with the diff", "Nothing is reviewed or pushed yet", "build --continue"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := factoryStartMessage(tc.diff)
			for _, want := range tc.want {
				assert.Assert(t, strings.Contains(got, want), "missing %q in:\n%s", want, got)
			}
			for _, notWant := range tc.notWant {
				assert.Assert(t, !strings.Contains(got, notWant), "unexpected %q in:\n%s", notWant, got)
			}
			assertOrdered(t, got, tc.want...)
		})
	}
}

// --json and a non-terminal stderr (a log file, a pipe, CI) must both leave
// the message out: --json because a program reads stdout and this is new
// output that was not there before, and a non-terminal stderr because a
// bytes.Buffer, as every test's stderr is, is never a *os.File and so never
// passes the same terminal check newImplementerLine uses.
func TestPrintRunStartMessageSkipsJSONAndNonTTY(t *testing.T) {
	for _, tc := range []struct {
		name    string
		jsonOut bool
	}{
		{name: "non-tty stderr, not json"},
		{name: "non-tty stderr, json", jsonOut: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			streams := iostream.Streams{Out: &out, Err: &errOut}
			printRunStartMessage(streams, tc.jsonOut, "Chunk will implement this change.")
			assert.Equal(t, errOut.String(), "")
		})
	}
}

// A bytes.Buffer fails terminalFile's *os.File assertion outright, so it alone
// cannot tell "not a *os.File" apart from "a *os.File that is not a terminal"
// (a redirected log file, a pipe, CI's captured stderr) — the exact case the
// request says must keep today's output byte for byte. A pipe is a real
// *os.File, so it exercises term.IsTerminal itself; a mutant that dropped that
// check and returned true for any *os.File would print here.
func TestTerminalFileRejectsANonTTYFile(t *testing.T) {
	r, w, err := os.Pipe()
	assert.NilError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })

	gotFile, isTTY := terminalFile(w)
	assert.Assert(t, !isTTY, "a pipe must not read as a terminal")
	assert.Equal(t, gotFile, w, "the file comes back either way, for a caller that needs its width")

	streams := iostream.Streams{Out: io.Discard, Err: w}
	printRunStartMessage(streams, false, "Chunk will implement this change.")
	assert.NilError(t, w.Close())

	got, err := io.ReadAll(r)
	assert.NilError(t, err)
	assert.Equal(t, string(got), "")
}
