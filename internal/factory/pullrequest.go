package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
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

// PromptSource says where a pull request's review prompts can be read: which
// prompts ran, and how to link to them at the commit the pull request shows.
type PromptSource struct {
	Prompts []review.Prompt
	// Owner, Repo and Commit make a link to a prompt's file. Without all three
	// the checks are listed without links.
	Owner, Repo, Commit string
	// Dir is the prompts' directory relative to the project root, "" for the
	// default.
	Dir string
}

// link is the address of a prompt's file at the pull request's head commit, or
// "" when it cannot be worked out.
func (s PromptSource) link(p review.Prompt) string {
	if s.Owner == "" || s.Repo == "" || s.Commit == "" || p.File == "" {
		return ""
	}
	dir := s.Dir
	if dir == "" {
		dir = review.DefaultDir
	}
	return fmt.Sprintf("https://github.com/%s/%s/blob/%s/%s", s.Owner, s.Repo, s.Commit,
		path.Join(filepath.ToSlash(dir), p.File))
}

// PullRequestBody is a run's pull request description: what was asked and how
// the review went. The diff and its stat are on the pull request already.
func PullRequestBody(prompt string, rep Report, src PromptSource) string {
	var b strings.Builder
	b.WriteString(cutRunes(strings.TrimSpace(prompt), maxPRPromptRunes) + "\n")
	fmt.Fprintf(&b, "\n## Review\n\nAll checks passed after %d round(s):\n\n", rep.Outcome.Rounds)
	for _, c := range rep.Outcome.Checks {
		if c.Status != StatusPassed {
			continue
		}
		if c.Kind == KindValidate {
			fmt.Fprintf(&b, "- %s\n", c.Name)
			continue
		}
		b.WriteString("- " + reviewLine(c.Name, src) + "\n")
	}
	b.WriteString("\n---\nAssembled with care by **chunk factory 🏭**\n")
	return b.String()
}

// reviewLine describes a review check: its name, a line on what it looks for,
// and a link to its prompt, each when there is one.
func reviewLine(name string, src PromptSource) string {
	var p review.Prompt
	for _, candidate := range src.Prompts {
		if candidate.Name == name {
			p = candidate
			break
		}
	}
	line := name
	if summary := promptSummary(p.Body); summary != "" {
		line += ": " + summary
	}
	if link := src.link(p); link != "" {
		line += " ([prompt](" + link + "))"
	}
	return line
}

// maxSummaryRunes keeps a check's summary to one line of a list.
const maxSummaryRunes = 140

// promptSummary is the first paragraph of a prompt, on one line and cut to its
// first sentence if that is shorter than a line. Headings come before it and
// are skipped; "" means the prompt has no paragraph to take it from.
func promptSummary(body string) string {
	var para []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" && len(para) > 0:
			return summarize(para)
		case line == "", strings.HasPrefix(line, "#") && len(para) == 0:
			continue
		}
		para = append(para, line)
	}
	return summarize(para)
}

func summarize(lines []string) string {
	text := strings.Join(lines, " ")
	if i := strings.Index(text, ". "); i >= 0 {
		text = text[:i+1]
	}
	return cutWords(text, maxSummaryRunes)
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
