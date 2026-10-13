package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/claude"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/review"
	"github.com/smeltdev/smelt/internal/sidecar"
)

// DefaultImplementTimeout bounds one implementer turn. A turn writes code and
// may run the tests several times, so it gets longer than a review.
const DefaultImplementTimeout = 30 * time.Minute

// baseImplementerSystemPrompt keeps the implementer's work where the loop
// collects it. Reviewers are shown the uncommitted changes against the baseline
// commit, so a commit, reset or branch switch would hide the work from them.
const baseImplementerSystemPrompt = `You are working in a disposable copy of the repository on a remote machine.
Make the requested change by editing files. Leave your changes uncommitted: do not run git commit, git stash, git reset, git checkout, git switch or git rebase. Your changes are collected from the working tree and reviewed.
Implement everything the request asks for in this run, including every phase, test and acceptance item. Do not stop after part of it, and do not defer parts to a later change.
A review finding that says part of the request is missing or incomplete is required work, not a matter of scope.
If something truly cannot be done here (a credential, a manual step, a decision the request leaves open), finish everything else, then end your summary with a line "BLOCKED: <what and why>".
When you finish, reply with a short summary of what you changed.`

// blockedMarker starts the line an implementer ends its summary with when
// something in the request cannot be done in the run.
const blockedMarker = "BLOCKED:"

// BlockedReason is what the implementer's summary says it is blocked on, or
// "" when it has no BLOCKED: line.
func BlockedReason(summary string) string {
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "*_>- "))
		if rest, ok := strings.CutPrefix(line, blockedMarker); ok {
			return strings.TrimSpace(strings.Trim(rest, "*_ "))
		}
	}
	return ""
}

// implementerSystemPrompt returns the base system prompt, followed by the
// run's own instructions when there are any.
func implementerSystemPrompt(instructions string, gates []config.Command) string {
	prompt := baseImplementerSystemPrompt
	if len(gates) > 0 {
		prompt += "\n\n" + gatesInstructions(gates)
	}
	if strings.TrimSpace(instructions) == "" {
		return prompt
	}
	return prompt + "\n\nRun-specific instructions:\n" + strings.TrimSpace(instructions)
}

// gatesInstructions tells the implementer to prove its work with the project's
// gates. smelt checks the claim against what the implementer ran.
func gatesInstructions(gates []config.Command) string {
	var b strings.Builder
	b.WriteString("Before you finish, run the project's gates yourself, after your last edit, and paste the last lines of each one's output under a " + gatesHeading + " heading in your summary. ")
	b.WriteString("Do not say a gate passes unless you ran it after your last edit; smelt checks. Fix what fails and run them again. The gates:\n")
	for _, g := range gates {
		fmt.Fprintf(&b, "- %s: %s\n", g.Name, g.Run)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Activity is one thing the implementer did, for display.
type Activity = harness.Activity

// Turn is the outcome of one implementer turn.
type Turn struct {
	Summary  string
	Duration time.Duration
	CostUSD  float64
	// Harness and Model are what ran the turn; Model is "" when the CLI's own
	// default was used.
	Harness      harness.Name
	Model        string
	InputTokens  int
	CachedTokens int
	OutputTokens int
	// RateLimit is the last rate-limit signal the agent emitted, if any.
	RateLimit *harness.RateLimit
	// Activity is what the implementer did in the turn, in order.
	Activity []Activity
	// Nudged reports that the summary claimed passing gates that were not run
	// after the last edit, and the implementer was asked to run them.
	Nudged bool
	// Unproven are the gates the summary still claims to pass although they
	// were not run after the last edit, after the nudge.
	Unproven []string
}

// Implementer runs a coding agent on a sidecar to write code. Every turn
// resumes the same session, so feedback arrives with the context of the work so
// far.
type Implementer struct {
	// Harness is the agent; Claude Code when nil.
	Harness      harness.Harness
	Exec         review.Execer
	Entry        *sidecar.PoolEntry
	Credential   review.Credential
	BaseURL      string
	Model        string
	Timeout      time.Duration
	Instructions string
	// Gates are the commands the implementer is asked to run before it
	// finishes; see gateCommands.
	Gates      []config.Command
	OnActivity func(Activity)

	sessionID string
	started   bool
}

// agent is the harness that runs the turns.
func (im *Implementer) agent() harness.Harness {
	if im.Harness == nil {
		return claude.Harness{}
	}
	return im.Harness
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

	h := im.agent()
	start := time.Now()
	var acts []Activity
	parser := h.NewImplementParser(func(a Activity) {
		acts = append(acts, a)
		if im.OnActivity != nil {
			im.OnActivity(a)
		}
	})
	var stderr bytes.Buffer
	script := h.ImplementScript(harness.ImplementInput{
		RepoPath: im.Entry.RepoPath, Prompt: prompt, SystemPrompt: implementerSystemPrompt(im.Instructions, im.Gates),
		Model: im.Model, SessionID: im.sessionID, Resume: im.started,
	})
	code, err := im.Exec(ctx, im.Entry, script, h.Env(im.Credential, im.BaseURL), func(stream string, data []byte) {
		if stream == iostream.StreamStderr {
			if stderr.Len() < outputTail {
				stderr.Write(data)
			}
			return
		}
		parser.Write(data)
	}, nil)
	parser.Flush()
	out := parser.Outcome()
	// The session exists once the agent has started it, even if this turn
	// failed, so the next turn must resume it rather than try to create it
	// again.
	if out.Turn.SessionID != "" {
		im.started = true
		im.sessionID = out.Turn.SessionID
	}
	turn := Turn{
		Summary: out.Turn.Summary, Duration: time.Since(start), CostUSD: out.Turn.CostUSD,
		Harness: h.Name(), Model: im.Model, InputTokens: out.Turn.InputTokens,
		CachedTokens: out.Turn.CachedTokens, OutputTokens: out.Turn.OutputTokens, RateLimit: out.Turn.RateLimit,
		Activity: acts,
	}

	// The agent puts why a turn failed in its result, so that explains a failed
	// exit before stderr does.
	explain := out.Message
	if strings.TrimSpace(explain) == "" {
		explain = stderr.String()
	}
	// explain stands in for stderr, so stderr goes in with the result for the
	// credential check: a 401 there must not hide behind a result that says
	// something else.
	switch err := h.RunError(ctx, timeout, code, err, out.Message+"\n"+stderr.String(), explain); {
	case err != nil:
		return turn, fmt.Errorf("implementer: %w", err)
	case out.Failed:
		return turn, fmt.Errorf("implementer: %s", review.Tail(strings.TrimSpace(explain), 2000))
	case !out.Done:
		return turn, errors.New("implementer ended without a result")
	}
	return turn, nil
}
