package cmd

import (
	"os"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
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

// A pipe is a real *os.File that is not a terminal, so it exercises
// term.IsTerminal itself rather than the *os.File assertion.
func TestTerminalFileRejectsANonTTYFile(t *testing.T) {
	r, w, err := os.Pipe()
	assert.NilError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })

	gotFile, isTTY := terminalFile(w)
	assert.Assert(t, !isTTY, "a pipe must not read as a terminal")
	assert.Equal(t, gotFile, w)
}
