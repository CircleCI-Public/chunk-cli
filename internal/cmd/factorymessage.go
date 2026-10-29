package cmd

import (
	"io"
	"os"

	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// watchStartHint is the line printed once a plan is building that says where
// to follow it.
const watchStartHint = "Follow it and your other plans in chunk watch."

// showHints reports whether output meant only for a person reading a terminal,
// such as the pointers to chunk watch, should be printed. It is not with
// --json, whose stdout a program consumes, in CI, or when stdout or stderr is
// not a terminal (a log file, a pipe): for those the output is the same with
// or without hints.
func showHints(streams iostream.Streams, jsonOut bool) bool {
	return !jsonOut && !nonInteractive() && onTerminal(streams)
}

// onTerminal reports whether both of streams' outputs are terminals. Tests
// replace it to see what a person at a terminal would.
var onTerminal = func(streams iostream.Streams) bool {
	_, outTTY := terminalFile(streams.Out)
	_, errTTY := terminalFile(streams.Err)
	return outTTY && errTTY
}

// terminalFile is the file w writes to and whether that file is a terminal.
func terminalFile(w io.Writer) (*os.File, bool) {
	f, ok := w.(*os.File)
	return f, ok && term.IsTerminal(int(f.Fd()))
}
