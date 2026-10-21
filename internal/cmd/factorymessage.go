package cmd

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// runStartMessage is the plain-language line a factory build run prints
// before any of its plumbing, so someone who just handed chunk a task knows
// what it is about to do without reading its flags: that chunk implements
// (or, picking up existing work, reviews) the change, reviews it and runs
// the project's checks until they pass, what the run ends with, and that
// they do not have to stay and watch it. continuing is true for
// `build --continue`, whose work already exists, so it is reviewed rather
// than implemented. remote is true for `build --remote`, which runs on a
// sidecar of its own rather than the local daemon and so cannot open a pull
// request yet, whatever pr says. attempts is --max-attempts, named with the
// flag so the cap on rounds is not a surprise. Keep this easy to extend:
// FACT-586 adds round progress on top of it, not a message of its own.
func runStartMessage(pr prFlags, remote, continuing bool, attempts int) string {
	first := "Chunk will implement this change, then review it"
	if continuing {
		first = "Chunk will review the existing work"
	}
	loop := " and run your checks until they pass, for " + roundsClause(attempts) + "."

	switch {
	case remote:
		// --remote keeps the wording it had: the chunk watch hints are for
		// runs on the local daemon.
		return first + loop + " It keeps running on its own sidecar; " +
			"`chunk factory pull` brings the work back. It does not open a pull request yet. " +
			"Follow along with `chunk watch`, or leave it running."
	case pr.none:
		return first + loop + " It does not open a pull request; " +
			"the work stays on the local branch.\n" + watchStartHint
	// pr.mode() only turns Required on with --open-pr; --draft alone still
	// resolves to Auto, so it is refused before the run starts only when
	// combined with --open-pr. Without that, the project may still end with
	// no pull request at all (no GitHub token, or an origin not on GitHub),
	// the same case the default branch hedges below.
	case pr.draft && pr.open:
		return first + loop + " When they do, it opens the pull request as a draft.\n" + watchStartHint
	case pr.draft:
		return first + loop + " When they do, it opens a pull request as a draft if it can.\n" + watchStartHint
	case pr.open:
		return first + loop + " When they do, it opens the pull request.\n" + watchStartHint
	default:
		// PullRequestAuto: the daemon opens one when the project can, but
		// still runs and says why it opened none when it cannot, so the
		// message must not promise one outright.
		return first + loop + " When they do, it opens a pull request if it can.\n" + watchStartHint
	}
}

// watchStartHint is the line under a run's start message that says where to
// follow it: chunk watch, and what it shows that the plain output does not.
// FACT-585's message says what chunk will do; this says where to follow it.
const watchStartHint = "Best way to follow this run and every other one: `chunk watch` " +
	"(findings, check output, the pull request). Or leave it running."

// roundsClause is how many review rounds a run gets and the flag that changes
// it: "up to 3 review rounds (change with --max-attempts)".
func roundsClause(attempts int) string {
	if attempts == 1 {
		return "1 review round (change with --max-attempts)"
	}
	return fmt.Sprintf("up to %d review rounds (change with --max-attempts)", attempts)
}

// factoryStartMessage is chunk factory start's own message. Unlike a build,
// start reviews nothing and pushes nothing, so it must not promise either; it
// only sets up a worktree and branch, from the diff when one was given. How
// the work eventually ends is decided by the flags a later
// `build --continue` is given, not by anything start knows, so it names no
// particular ending beyond the default.
func factoryStartMessage(diff bool) string {
	msg := "Chunk will set up a worktree and branch for this change."
	if diff {
		msg = "Chunk will set up a worktree and branch for this change, starting with the diff."
	}
	return msg + " Nothing is reviewed or pushed yet. When the work is ready, " +
		"`chunk factory build --continue` reviews it, runs your checks until they pass, and, by default, opens the pull request.\n" +
		"Best way to follow it from there, and every other run: `chunk watch` (findings, check output, the pull request)."
}

// printRunStartMessage prints msg to streams' stderr once, before anything
// else a run prints, unless the output is not for a person to read (see
// showHints), which must keep exactly the output it had before this message
// existed.
func printRunStartMessage(streams iostream.Streams, jsonOut bool, msg string) {
	if !showHints(streams, jsonOut) {
		return
	}
	streams.ErrPrintf("%s\n", msg)
}

// showHints reports whether output meant only for a person reading a terminal,
// such as the start message and the pointers to chunk watch, should be
// printed. It is not with --json, whose stdout a program consumes, in CI, or
// when stdout or stderr is not a terminal (a log file, a pipe): each of those
// keeps exactly the output it had before these hints existed.
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
// The file comes back too because a caller that draws in place needs it to ask
// the terminal's width.
func terminalFile(w io.Writer) (*os.File, bool) {
	f, ok := w.(*os.File)
	return f, ok && term.IsTerminal(int(f.Fd()))
}
