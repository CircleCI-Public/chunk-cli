package sandboximage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/gitexec"
)

// MaxRepairAttempts is how many times Claude may edit the Dockerfile.
const MaxRepairAttempts = 3

// RepairAction is what the repair loop does next.
type RepairAction int

// The steps of the repair loop.
const (
	RepairReport  RepairAction = iota // print the failure and stop: no repair is possible or wanted
	RepairAsk                         // ask for consent
	RepairAttempt                     // run another attempt
	RepairGiveUp                      // attempts are used up
)

// RepairState is what DecideRepair decides on.
type RepairState struct {
	// Interactive is whether a person can answer. Repair never runs otherwise.
	Interactive bool
	// HaveClaude is whether the host has a claude CLI.
	HaveClaude bool
	// Consent is nil until the person was asked, then their answer.
	Consent *bool
	// Attempts is how many attempts have run.
	Attempts int
}

// DecideRepair picks the next step of the repair loop.
func DecideRepair(s RepairState) RepairAction {
	switch {
	case !s.Interactive || !s.HaveClaude:
		return RepairReport
	case s.Consent == nil:
		return RepairAsk
	case !*s.Consent:
		return RepairReport
	case s.Attempts >= MaxRepairAttempts:
		return RepairGiveUp
	}
	return RepairAttempt
}

// Fixer edits the Dockerfile in dir given a prompt describing the failure.
type Fixer interface {
	Fix(ctx context.Context, dir, prompt string) error
}

// ErrOtherFilesChanged reports that a repair attempt touched more than the Dockerfile.
var ErrOtherFilesChanged = errors.New("repair changed files other than the Dockerfile")

// RepairOptions steer RunRepair.
type RepairOptions struct {
	Dir        string
	Dockerfile string
	Fixer      Fixer
	// Failure describes the failure to fix.
	Failure string
	// Retry rebuilds and re-verifies, returning the new failure or "" on success.
	Retry func(ctx context.Context) (string, error)
	// OnAttempt is called before each attempt, with its 1-based number.
	OnAttempt func(n int)
}

// RepairResult is how a repair ended.
type RepairResult struct {
	Fixed    bool
	Attempts int
	// Failure is the last failure, when not fixed.
	Failure string
}

// RunRepair lets the Fixer edit the Dockerfile and rebuilds after each attempt,
// up to MaxRepairAttempts. If an attempt changes any other file, it stops with
// ErrOtherFilesChanged.
func RunRepair(ctx context.Context, opts RepairOptions) (RepairResult, error) {
	res := RepairResult{Failure: opts.Failure}
	git := gitexec.Runner{Dir: opts.Dir}
	for res.Attempts < MaxRepairAttempts {
		res.Attempts++
		if opts.OnAttempt != nil {
			opts.OnAttempt(res.Attempts)
		}
		before, err := porcelain(ctx, git)
		if err != nil {
			return res, err
		}
		if err := opts.Fixer.Fix(ctx, opts.Dir, RepairPrompt(opts.Dockerfile, res.Failure)); err != nil {
			return res, fmt.Errorf("repair attempt %d: %w", res.Attempts, err)
		}
		after, err := porcelain(ctx, git)
		if err != nil {
			return res, err
		}
		if !OnlyChanged(before, after, opts.Dockerfile) {
			return res, ErrOtherFilesChanged
		}
		failure, err := opts.Retry(ctx)
		if err != nil {
			return res, err
		}
		if failure == "" {
			res.Fixed, res.Failure = true, ""
			return res, nil
		}
		res.Failure = failure
	}
	return res, nil
}

func porcelain(ctx context.Context, git gitexec.Runner) (string, error) {
	out, err := git.Output(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	return string(out), nil
}

// OnlyChanged reports whether two `git status --porcelain` outputs differ at
// most in the lines for dockerfile.
func OnlyChanged(before, after, dockerfile string) bool {
	return withoutFile(before, dockerfile) == withoutFile(after, dockerfile)
}

func withoutFile(status, file string) string {
	var kept []string
	for line := range strings.SplitSeq(status, "\n") {
		if line != "" && strings.TrimSpace(line[min(3, len(line)):]) != file {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// RepairPrompt is what Claude is told: the failure and the rules of the edit.
func RepairPrompt(dockerfile, failure string) string {
	return fmt.Sprintf(`Building or verifying the factory sandbox image from %[1]s failed:

%[2]s

Fix it. Edit only %[1]s: keep its section headers and the "smelt agent kit" section, and change nothing else in the repo. Reply with one sentence saying what you changed.`, dockerfile, failure)
}

// ClaudeFixer runs the host's claude CLI headlessly with only Read and Edit.
type ClaudeFixer struct {
	// Token, when set, is passed as CLAUDE_CODE_OAUTH_TOKEN. It is never printed.
	Token string
}

// HaveClaude reports whether a claude CLI is on the PATH.
func HaveClaude() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// Fix implements Fixer.
func (f ClaudeFixer) Fix(ctx context.Context, dir, prompt string) error {
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--allowedTools", "Read,Edit")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if f.Token != "" {
		cmd.Env = append(cmd.Env, config.EnvClaudeOAuthToken+"="+f.Token)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("claude: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
