package factory

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// DefaultImplementTimeout bounds one implementer turn. A turn writes code and
// may run the tests several times, so it gets longer than a review.
const DefaultImplementTimeout = 30 * time.Minute

// defaultQuietWarning is how long an implementer turn may report nothing
// before the run says so, and how often it says so again. A turn that has
// gone quiet is waiting on something: a command on the sidecar that has not
// returned, the Anthropic API, or a stream that no longer reaches claude.
const defaultQuietWarning = 5 * time.Minute

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

// The claude flags every local and sidecar run of Claude Code passes.
const (
	flagAllowedTools       = "--allowedTools"
	flagSettingSources     = "--setting-sources"
	flagDisallowedTools    = "--disallowedTools"
	flagAppendSystemPrompt = "--append-system-prompt"
)

// streamArgs starts a claude command line, after prefix, that answers in
// stream-json for a Stream to read.
func streamArgs(prefix ...string) []string {
	return append(prefix, "-p", "--output-format", "stream-json", "--verbose")
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
	// Status, when set, hears which command on the sidecar a turn is, and
	// that a turn has gone quiet.
	Status func(iostream.Level, string)
	// QuietWarning is defaultQuietWarning when zero.
	QuietWarning time.Duration

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
	quiet := im.quietWarning()
	watch := newQuietWatch(quiet, func(d time.Duration, last string) {
		im.status(iostream.LevelWarn, quietMessage(d, last))
	})
	defer watch.stop()
	s := claudecode.NewStream(func(a claudecode.Activity) {
		watch.saw(a)
		if im.OnActivity != nil {
			im.OnActivity(a)
		}
	})
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
		watch.heard()
		s.Write(data)
	}, func(commandID string) {
		// The command's ID is what finds it on the sidecar, and in the exec
		// API, when a turn has to be looked into.
		im.status(iostream.LevelInfo, fmt.Sprintf("implementer: command %s on sidecar %s", commandID, im.Entry.ID))
	})
	s.Flush()
	res, sawResult := s.Result()
	// The session exists once claude has started it, even if this turn failed,
	// so the next turn must resume it rather than try to create it again.
	if s.SessionID() != "" {
		im.started = true
	}
	turn := Turn{Summary: res.Result, Duration: time.Since(start), CostUSD: res.CostUSD}

	return turn, turnError(ctx, "implementer", timeout, code, err, res, sawResult, stderr.String())
}

// turnError is why a claude turn failed, or nil when it did not, from how the
// process ended (code, err), the result claude reported and what it wrote to
// stderr. who names the agent that ran it.
func turnError(ctx context.Context, who string, timeout time.Duration, code int, err error, res claudecode.StreamResult, sawResult bool, stderr string) error {
	// claude puts why a turn failed in its result, so that explains a failed
	// exit before stderr does.
	explain := res.Result
	if strings.TrimSpace(explain) == "" {
		explain = stderr
	}
	// explain stands in for stderr, so stderr goes in with the result for the
	// credential check: a 401 there must not hide behind a result that says
	// something else.
	switch err := claudecode.RunError(ctx, timeout, code, err, res.Result+"\n"+stderr, explain); {
	case err != nil:
		return fmt.Errorf("%s: %w", who, err)
	case res.IsError:
		return fmt.Errorf("%s: %s", who, review.Tail(strings.TrimSpace(explain), 2000))
	case !sawResult:
		return fmt.Errorf("%s ended without a result", who)
	}
	return nil
}

func (im *Implementer) status(level iostream.Level, msg string) {
	if im.Status != nil {
		im.Status(level, msg)
	}
}

func (im *Implementer) quietWarning() time.Duration {
	if im.QuietWarning > 0 {
		return im.QuietWarning
	}
	return defaultQuietWarning
}

// quietMessage says a turn has reported nothing for d, and what it last did.
func quietMessage(d time.Duration, last string) string {
	msg := fmt.Sprintf("the implementer has reported nothing for %s", d.Round(time.Second))
	if last == "" {
		return msg + " since it started"
	}
	return msg + "; its last step was " + last
}

// quietWatch calls warn while a turn reports nothing: once it has been quiet
// for after, and again each time after passes with still nothing.
type quietWatch struct {
	mu      sync.Mutex
	after   time.Duration
	since   time.Time
	last    string
	stopped bool
	timer   *time.Timer
	warn    func(quiet time.Duration, last string)
}

func newQuietWatch(after time.Duration, warn func(time.Duration, string)) *quietWatch {
	w := &quietWatch{after: after, since: time.Now(), warn: warn}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer = time.AfterFunc(after, w.fire)
	return w
}

// heard records that the turn reported something.
func (w *quietWatch) heard() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.since = time.Now()
	w.timer.Reset(w.after)
}

// saw records a, the turn's latest step, for the warning to name.
func (w *quietWatch) saw(a claudecode.Activity) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.last = stepLine(a)
}

func (w *quietWatch) fire() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	quiet, last := time.Since(w.since), w.last
	// The turn may have reported something while this waited for the lock;
	// heard has already set the timer again for that.
	if quiet < w.after {
		w.mu.Unlock()
		return
	}
	w.timer.Reset(w.after)
	w.mu.Unlock()
	w.warn(quiet, last)
}

func (w *quietWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	w.timer.Stop()
}

// maxStepLine caps how much of a step a quiet warning repeats.
const maxStepLine = 120

// stepLine is a on one line, short enough for a warning: the tool and its
// target, or what claude wrote.
func stepLine(a claudecode.Activity) string {
	line := oneLine(a.Detail)
	if a.Tool != "" {
		line = a.Tool + " " + line
	} else {
		line = "writing " + line
	}
	if r := []rune(line); len(r) > maxStepLine {
		line = string(r[:maxStepLine]) + "…"
	}
	return line
}

// script builds the shell script for one turn.
func (im *Implementer) script(prompt string) string {
	args := append(streamArgs("claude"),
		"--dangerously-skip-permissions",
		flagDisallowedTools, strings.Join(disallowedGit, ","),
		flagAppendSystemPrompt, implementerSystemPrompt(im.Instructions),
	)
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
