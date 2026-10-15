package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

const (
	// maxPRTitleRunes keeps a pull request's title to a line a listing shows whole.
	maxPRTitleRunes = 72
	// maxPRPromptRunes keeps a long prompt from filling the description.
	maxPRPromptRunes = 8000
)

// Push publishes the worktree's branch to remote and makes it the branch's
// upstream. It never asks for a credential, as the daemon has no terminal to
// ask on: one git cannot find fails the push with git's own explanation. Hooks
// are skipped, as for Commit: the work has already been checked.
func (w Worktree) Push(ctx context.Context, remote string) error {
	git := gitexec.Runner{Dir: w.Path, Env: append(os.Environ(), "GIT_TERMINAL_PROMPT=0")}
	if out, err := git.CombinedOutput(ctx, "push", "--no-verify", "--set-upstream", remote, w.Branch); err != nil {
		return fmt.Errorf("push %s to %s: %w: %s", w.Branch, remote, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoteHasBranch reports whether remote, as the repository at dir knows it,
// has a branch called name. It asks the remote, not the last fetch, since a
// pull request needs the branch to be there now.
func RemoteHasBranch(ctx context.Context, dir, remote, name string) (bool, error) {
	git := gitexec.Runner{Dir: dir, Env: append(os.Environ(), "GIT_TERMINAL_PROMPT=0")}
	out, err := git.CombinedOutput(ctx, "ls-remote", "--exit-code", "--heads", remote, name)
	if err == nil {
		return true, nil
	}
	// --exit-code makes "no such branch" exit 2, apart from a remote that could
	// not be reached, which exits 128.
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		return false, nil
	}
	return false, fmt.Errorf("look for %s on %s: %w: %s", name, remote, err, strings.TrimSpace(string(out)))
}

// PullRequestTitle is the title a run's pull request gets: the prompt's first
// line, cut between words to what a listing shows. A prompt with no words in
// it falls back to naming the run.
func PullRequestTitle(prompt, runID string) string {
	for _, line := range strings.Split(prompt, "\n") {
		// A prompt written as a markdown document leads with a heading mark.
		line = strings.Join(strings.Fields(strings.TrimLeft(strings.TrimSpace(line), "# ")), " ")
		if line == "" {
			continue
		}
		return cutWords(line, maxPRTitleRunes)
	}
	return "chunk factory run " + runID
}

// PullRequestBody is a run's pull request description: what was asked, what
// changed, how the review went, and where it came from. A change a person is
// asked to merge should say how it was checked.
func PullRequestBody(prompt, runID string, rep Report) string {
	var b strings.Builder
	b.WriteString("## Request\n\n")
	for _, line := range strings.Split(cutRunes(strings.TrimSpace(prompt), maxPRPromptRunes), "\n") {
		b.WriteString("> " + line + "\n")
	}
	if rep.Stat != "" {
		fmt.Fprintf(&b, "\n## Changes\n\n%s\n", rep.Stat)
		// The run's branch starts from the developer's uncommitted work, committed
		// first, so the pull request's own diff is larger than the run's change.
		if rep.Worktree.Baseline != "" && rep.Worktree.Baseline != rep.Worktree.Head {
			b.WriteString("\nThat counts only what the run changed. The first commit on this branch is the " +
				"uncommitted work the run started from, which is part of this pull request too.\n")
		}
	}
	fmt.Fprintf(&b, "\n## Review\n\nAll checks passed after %d round(s):\n\n", rep.Outcome.Rounds)
	for _, c := range rep.Outcome.Checks {
		if c.Status != StatusPassed {
			continue
		}
		kind := "review"
		if c.Kind == KindValidate {
			kind = "validation"
		}
		fmt.Fprintf(&b, "- %s (%s)\n", c.Name, kind)
	}
	fmt.Fprintf(&b, "\n---\nOpened by `chunk factory build`, run %s.\n", runID)
	return b.String()
}

// cutRunes cuts s to at most n characters, marking a cut with an ellipsis. It
// cuts between characters: a byte cut can split one and print garbage.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// cutWords cuts s to at most n characters, between words where it can, marking
// a cut with an ellipsis. A single word longer than n is cut inside it.
func cutWords(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := n - 1
	// Back up to the last space that leaves a title worth reading, not one so
	// early that most of the line is lost.
	for i := cut; i > n/2; i-- {
		if r[i] == ' ' {
			return strings.TrimRight(string(r[:i]), " ,;:-") + "…"
		}
	}
	return string(r[:cut]) + "…"
}
