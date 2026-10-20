package cmd

import (
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
// request yet, whatever pr says. Keep this easy to extend: FACT-586 adds
// round progress on top of it, not a message of its own.
func runStartMessage(pr prFlags, remote, continuing bool) string {
	first := "Chunk will implement this change, then review it"
	if continuing {
		first = "Chunk will review the existing work"
	}
	const follow = "Follow along with `chunk watch`, or leave it running."

	switch {
	case remote:
		return first + " and run your checks until they pass. It keeps running on its own sidecar; " +
			"`chunk factory pull` brings the work back. It does not open a pull request yet. " + follow
	case pr.none:
		return first + " and run your checks until they pass. It does not open a pull request; " +
			"the work stays on the local branch. " + follow
	// pr.mode() only turns Required on with --open-pr; --draft alone still
	// resolves to Auto, so it is refused before the run starts only when
	// combined with --open-pr. Without that, the project may still end with
	// no pull request at all (no GitHub token, or an origin not on GitHub),
	// the same case the default branch hedges below.
	case pr.draft && pr.open:
		return first + " and run your checks until they pass. When they do, it opens the pull request as a draft. " + follow
	case pr.draft:
		return first + " and run your checks until they pass. When they do, it opens a pull request as a draft if it can. " + follow
	case pr.open:
		return first + " and run your checks until they pass. When they do, it opens the pull request. " + follow
	default:
		// PullRequestAuto: the daemon opens one when the project can, but
		// still runs and says why it opened none when it cannot, so the
		// message must not promise one outright.
		return first + " and run your checks until they pass. When they do, it opens a pull request if it can. " + follow
	}
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
		"`chunk factory build --continue` reviews it, runs your checks until they pass, and, by default, opens the pull request."
}

// printRunStartMessage prints msg to streams' stderr once, before anything
// else a run prints, unless the output is not for a person to read: --json,
// whose stdout a program consumes, or stderr that is not a terminal (a log
// file, a pipe, CI), which must keep exactly the output it had before this
// message existed.
func printRunStartMessage(streams iostream.Streams, jsonOut bool, msg string) {
	if _, ok := terminalFile(streams.Err); jsonOut || !ok {
		return
	}
	streams.ErrPrintf("%s\n", msg)
}

// terminalFile is the file w writes to and whether that file is a terminal.
// The file comes back too because a caller that draws in place needs it to ask
// the terminal's width.
func terminalFile(w io.Writer) (*os.File, bool) {
	f, ok := w.(*os.File)
	return f, ok && term.IsTerminal(int(f.Fd()))
}
