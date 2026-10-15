package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// firstPassSystemPrompt keeps a first pass where the run that refines it
// looks for the work: uncommitted in the worktree.
const firstPassSystemPrompt = `You are making a first pass at a change in a copy of the developer's repository. Another agent will finish it, run the checks and get it ready to merge.
Make the requested change by editing files. Leave your changes uncommitted: do not run git commit, git stash, git reset, git checkout, git switch or git rebase.
When you finish, reply with a short summary of what you changed.`

// claudeBinary is the Claude Code command a first pass runs.
const claudeBinary = "claude"

// ErrNoClaude is returned when Claude Code is not installed on the
// developer's machine, so there is nothing to make a first pass with.
var ErrNoClaude = errors.New("claude is not installed")

// CheckClaude returns ErrNoClaude when there is no claude on PATH to make a
// first pass with, so a developer is told before typing a prompt.
func CheckClaude() error {
	if _, err := exec.LookPath(claudeBinary); err != nil {
		return ErrNoClaude
	}
	return nil
}

// FirstPass has Claude Code make a first pass at a started run's prompt in its
// worktree, on the developer's machine with their own claude login, for a
// developer who fires the prompt from chunk rather than an agent of their
// own. Edits in the worktree are accepted; anything else is up to the
// developer's own Claude Code permissions, since no one is there to approve
// it. The work is left uncommitted, where 'chunk factory build --continue'
// picks it up.
func FirstPass(ctx context.Context, rec Record, onActivity func(claudecode.Activity)) (Turn, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultImplementTimeout)
	defer cancel()

	start := time.Now()
	s := claudecode.NewStream(onActivity)
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, claudeBinary,
		"-p", "--output-format", "stream-json", "--verbose",
		"--permission-mode", "acceptEdits",
		"--disallowedTools", strings.Join(disallowedGit, ","),
		"--append-system-prompt", firstPassSystemPrompt,
	)
	cmd.Dir = rec.Worktree
	cmd.Stdin = strings.NewReader(rec.Prompt)
	cmd.Stdout = streamWriter{s}
	cmd.Stderr = &limitWriter{buf: &stderr}
	runErr := cmd.Run()
	s.Flush()
	res, sawResult := s.Result()
	turn := Turn{Summary: res.Result, Duration: time.Since(start), CostUSD: res.CostUSD}

	switch {
	case errors.Is(runErr, exec.ErrNotFound):
		return turn, ErrNoClaude
	case errors.Is(ctx.Err(), context.Canceled):
		// Stopped by the developer: whatever claude did so far stays.
		return turn, ctx.Err()
	}
	code := 0
	if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
		code, runErr = exitErr.ExitCode(), nil
	}
	explain := res.Result
	if strings.TrimSpace(explain) == "" {
		explain = stderr.String()
	}
	switch err := claudecode.RunError(ctx, DefaultImplementTimeout, code, runErr, res.Result+"\n"+stderr.String(), explain); {
	case err != nil:
		return turn, fmt.Errorf("first pass: %w", err)
	case res.IsError:
		return turn, fmt.Errorf("first pass: %s", review.Tail(strings.TrimSpace(explain), 2000))
	case !sawResult:
		return turn, errors.New("first pass ended without a result")
	}
	return turn, nil
}

// WorkStat is git's per-file summary of a started run's work so far, committed
// or not, against its baseline, or "" when there is none. It stages the work
// to count new files, as continuing the run would when it commits them.
func WorkStat(ctx context.Context, rec Record) (string, error) {
	git := gitexec.Runner{Dir: rec.Worktree}
	if out, err := git.CombinedOutput(ctx, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage the work: %w: %s", err, strings.TrimSpace(string(out)))
	}
	out, err := git.Output(ctx, "diff", "--cached", "--stat", rec.Baseline)
	if err != nil {
		return "", fmt.Errorf("summarize the work: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// streamWriter feeds claude's stdout to a Stream as it arrives.
type streamWriter struct{ s *claudecode.Stream }

func (w streamWriter) Write(p []byte) (int, error) {
	w.s.Write(p)
	return len(p), nil
}

// limitWriter keeps the start of stderr, up to outputTail, and drops the rest.
type limitWriter struct{ buf *bytes.Buffer }

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.buf.Len() < outputTail {
		w.buf.Write(p)
	}
	return len(p), nil
}
