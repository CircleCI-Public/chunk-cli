package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// DefaultImplementTimeout bounds one implementer turn. A turn writes code and
// may run the tests several times, so it gets longer than a review.
const DefaultImplementTimeout = 30 * time.Minute

// baseImplementerSystemPrompt keeps the implementer's work where the loop
// collects it. Reviewers are shown the uncommitted changes against the baseline
// commit, so a commit, reset or branch switch would hide the work from them.
const baseImplementerSystemPrompt = `You are working in a disposable copy of the repository on a remote machine.
Make the requested change by editing files. Leave your changes uncommitted: do not run git commit, git stash, git reset, git checkout, git switch or git rebase. Your changes are collected from the working tree and reviewed.
When you finish, reply with a short summary of what you changed.`

// implementerSystemPrompt returns the base system prompt, followed by the
// run's own instructions when there are any.
func implementerSystemPrompt(instructions string) string {
	if strings.TrimSpace(instructions) == "" {
		return baseImplementerSystemPrompt
	}
	return baseImplementerSystemPrompt + "\n\nRun-specific instructions:\n" + strings.TrimSpace(instructions)
}

// disallowedGit are the git commands that would move the implementer's work
// out of the working tree the loop collects it from.
var disallowedGit = []string{
	"Bash(git commit:*)", "Bash(git stash:*)", "Bash(git reset:*)",
	"Bash(git checkout:*)", "Bash(git switch:*)", "Bash(git rebase:*)",
}

// Turn is the outcome of one implementer turn.
type Turn struct {
	Summary  string
	Duration time.Duration
	CostUSD  float64
}

// Implementer runs Claude Code on a sidecar to write code. Every turn resumes
// the same session, so feedback arrives with the context of the work so far.
type Implementer struct {
	Exec         sidecar.Execer
	Entry        *sidecar.PoolEntry
	Credential   claudecode.Credential
	BaseURL      string
	Model        string
	Timeout      time.Duration
	Instructions string
	OnActivity   func(claudecode.Activity)

	sessionID string
	started   bool
}

// Run sends prompt to the implementer and waits for its turn to end.
func (im *Implementer) Run(ctx context.Context, prompt string) (Turn, error) {
	if im.sessionID == "" {
		im.sessionID = uuid.NewString()
	}
	timeout := im.Timeout
	if timeout <= 0 {
		timeout = DefaultImplementTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	s := claudecode.NewStream(im.OnActivity)
	var stderr bytes.Buffer
	env := claudecode.Env(im.Credential, im.BaseURL)
	// The sidecar is a sandbox, which is what lets claude skip permission
	// prompts there even when the image runs it as root.
	env["IS_SANDBOX"] = "1"
	code, err := im.Exec(ctx, im.Entry, im.script(prompt), env, func(stream string, data []byte) {
		if stream == circleci.StreamStderr {
			if stderr.Len() < outputTail {
				stderr.Write(data)
			}
			return
		}
		s.Write(data)
	}, nil)
	s.Flush()
	res, sawResult := s.Result()
	// The session exists once claude has started it, even if this turn failed,
	// so the next turn must resume it rather than try to create it again.
	if s.SessionID() != "" {
		im.started = true
	}
	turn := Turn{Summary: res.Result, Duration: time.Since(start), CostUSD: res.CostUSD}

	// claude puts why a turn failed in its result, so that explains a failed
	// exit before stderr does.
	explain := res.Result
	if strings.TrimSpace(explain) == "" {
		explain = stderr.String()
	}
	// explain stands in for stderr, so stderr goes in with the result for the
	// credential check: a 401 there must not hide behind a result that says
	// something else.
	switch err := claudecode.RunError(ctx, timeout, code, err, res.Result+"\n"+stderr.String(), explain); {
	case err != nil:
		return turn, fmt.Errorf("implementer: %w", err)
	case res.IsError:
		return turn, fmt.Errorf("implementer: %s", review.Tail(strings.TrimSpace(explain), 2000))
	case !sawResult:
		return turn, errors.New("implementer ended without a result")
	}
	return turn, nil
}

// script builds the shell script for one turn.
func (im *Implementer) script(prompt string) string {
	args := []string{
		"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions",
		"--disallowedTools", strings.Join(disallowedGit, ","),
		"--append-system-prompt", implementerSystemPrompt(im.Instructions),
	}
	if im.started {
		args = append(args, "--resume", im.sessionID)
	} else {
		args = append(args, "--session-id", im.sessionID)
	}
	if im.Model != "" {
		args = append(args, "--model", im.Model)
	}
	return claudecode.Script(im.Entry.RepoPath, prompt, args)
}
