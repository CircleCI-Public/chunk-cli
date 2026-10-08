package factory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// LogDefault is the RunOptions.Log that puts the run's log at DefaultLogPath.
const LogDefault = "default"

// DefaultLogPath is where a run's log goes when no path is given. The run ID
// is already the run's start time, so it alone names the file.
func DefaultLogPath(runID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".chunk", "factory", "run-"+runID+".log"), nil
}

// runLog is a plain-text record of a factory run that outlives it, written for
// a person or an agent to read back and judge the run, and the prompts, by. It
// holds the run's full context, whatever any display filters out: every
// status line, each prompt the implementer is sent and what it did and said,
// and every check in full. With verbose it also holds the review prompts, the
// output of commands that passed, and each round's check that every reviewer
// has the implementer's change. A nil log records nothing.
type runLog struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	verbose bool
	now     func() time.Time
	// attempts is the most rounds the run checks, set by start: the last
	// round's findings are not fed back, since no round follows it.
	attempts int
}

// openLog creates the log at path, LogDefault for DefaultLogPath, or returns
// nil when path is "".
func openLog(path, runID string, verbose bool) (*runLog, error) {
	if path == "" {
		return nil, nil
	}
	if path == LogDefault {
		var err error
		if path, err = DefaultLogPath(runID); err != nil {
			return nil, err
		}
	}
	// The log holds the code under review and what reviewers said of it, so it
	// is kept private.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	return &runLog{f: f, path: path, verbose: verbose, now: time.Now}, nil
}

// wrap returns opts with its status and hooks also writing to the log, and
// with the reviewer check on when verbose.
func (l *runLog) wrap(opts RunOptions) RunOptions {
	if l == nil {
		return opts
	}
	next := opts.Status
	opts.Status = func(level iostream.Level, msg string) {
		l.line(levelName(level), msg)
		next(level, msg)
	}
	opts.OnEvent = chain(l.event, opts.OnEvent)
	opts.OnActivity = chain(l.activity, opts.OnActivity)
	opts.OnReviewProgress = chain(l.reviewProgress, opts.OnReviewProgress)
	opts.OnCheck = chain(l.check, opts.OnCheck)
	return opts
}

// chain calls first, then next when it is set.
func chain[T any](first, next func(T)) func(T) {
	return func(v T) {
		first(v)
		if next != nil {
			next(v)
		}
	}
}

// start records what the run is about to do. request is what the reviewers
// check the work against, which for a continued run is not the implementer's
// prompt.
func (l *runLog) start(runID string, opts RunOptions, request string) {
	if l == nil {
		return
	}
	l.attempts = opts.Attempts
	l.line("info", "run "+runID)
	l.block("prompt", opts.Prompt)
	l.line("info", fmt.Sprintf("attempts %d, %d reviewer sidecar(s)", opts.Attempts, opts.Reviewers))
	names := make([]string, len(opts.Prompts))
	for i, p := range opts.Prompts {
		names[i] = p.Name
	}
	l.line("info", fmt.Sprintf("review prompts: %s", orNone(strings.Join(names, ", "))))
	cmds := make([]string, len(opts.Commands))
	for i, c := range opts.Commands {
		cmds[i] = c.Name + ": " + c.Run
	}
	l.line("info", fmt.Sprintf("validation commands: %s", orNone(strings.Join(cmds, "; "))))
	if !l.verbose {
		return
	}
	// The prompts as they are sent: the implementer's system prompt comes with
	// every turn, and each review gets the original request and the location of
	// the change under review.
	l.block("implementer system prompt", implementerSystemPrompt(opts.ImplementerInstructions))
	for _, p := range scopePrompts(request, opts.Prompts) {
		l.block("review prompt "+p.Name, p.Body)
	}
}

func (l *runLog) event(e Event) {
	switch e.Kind {
	case EventImplementing:
		l.block(fmt.Sprintf("round %d: prompt sent to the implementer", e.Round), e.Prompt)
	case EventImplemented:
		l.line("info", fmt.Sprintf("round %d: implementer finished in %s ($%.2f)", e.Round, e.Turn.Duration.Round(time.Second), e.Turn.CostUSD))
		l.block(fmt.Sprintf("round %d: implementer's summary", e.Round), e.Turn.Summary)
	case EventCollected:
		if e.Change.Empty() {
			l.line("info", fmt.Sprintf("round %d: no changes", e.Round))
			return
		}
		l.line("info", fmt.Sprintf("round %d: change %s: %s", e.Round, shortHash(e.Change.Fingerprint), e.Change.Stat))
	case EventChecking:
		l.line("step", fmt.Sprintf("round %d: reviewing and validating", e.Round))
	case EventChecked:
		for _, c := range e.Checks {
			if c.Kind == KindReview {
				l.review(e.Round, c)
			}
		}
		l.line("info", fmt.Sprintf("round %d: %s", e.Round, checkTally(e.Checks)))
	}
}

// activity records what the implementer did, or said, as it works.
func (l *runLog) activity(a Activity) {
	if a.Tool == "" {
		l.block("implementer", a.Detail)
		return
	}
	l.line("info", fmt.Sprintf("implementer: %s %s", a.Tool, oneLine(a.Detail)))
}

func (l *runLog) reviewProgress(e review.ProgressEvent) {
	switch e.State {
	case review.StateQueued:
	case review.StateRunning:
		l.line("info", fmt.Sprintf("review %s started on %s", e.Prompt, e.SidecarID))
	case review.StateDone:
		l.line("info", fmt.Sprintf("review %s finished in %s", e.Prompt, e.Duration.Round(time.Second)))
	case review.StateFailed:
		l.line("warn", fmt.Sprintf("review %s could not run: %s", e.Prompt, e.Error))
	}
}

// review records everything a review said: each finding in full, whatever its
// severity, and its prose.
func (l *runLog) review(round int, c Check) {
	head := fmt.Sprintf("round %d: review %s on %s %s in %s", round, c.Name, c.SidecarID, c.Status, c.Duration.Round(time.Second))
	if c.Status == StatusErrored {
		l.line("warn", head+": "+c.Error)
		return
	}
	l.line(statusLevel(c.Status), fmt.Sprintf("%s with %d finding(s)", head, len(c.Findings)))
	for _, f := range c.Findings {
		// Only a finding worth changing is fed back, and only when a round
		// follows this one.
		sent := ""
		if f.WorthChanging() && round < l.attempts {
			sent = " (sent to the implementer)"
		}
		l.block(fmt.Sprintf("  [%s] %s%s", f.Severity, f.Location(), sent), f.Body)
	}
	l.block("  prose", c.Prose)
}

// check records a validation command as it finishes, with its output when it
// failed, and with verbose when it passed too.
func (l *runLog) check(c Check) {
	head := fmt.Sprintf("validate %s on %s %s in %s", c.Name, c.SidecarID, c.Status, c.Duration.Round(time.Second))
	switch c.Status {
	case StatusErrored:
		l.line("warn", head+": "+c.Error)
	case StatusFailed:
		l.line("error", fmt.Sprintf("%s, exit %d", head, c.ExitCode))
		l.block("  output", c.Output)
	case StatusPassed:
		l.line("done", head)
		if l.verbose {
			l.block("  output", c.Output)
		}
	}
}

// reviewerTree records that a reviewer has the implementer's change, or warns
// through status that it does not: its review would be of other code.
func (l *runLog) reviewerTree(status iostream.StatusFunc, t ReviewerTree) {
	switch {
	case t.Err != nil:
		status(iostream.LevelWarn, fmt.Sprintf("could not check reviewer %s has the implementer's change: %v", t.SidecarID, t.Err))
	case !t.Matches():
		status(iostream.LevelWarn, fmt.Sprintf("reviewer %s does not have the implementer's change: it has %s, the implementer %s",
			t.SidecarID, shortHash(t.Fingerprint), shortHash(t.Want)))
	default:
		l.line("info", fmt.Sprintf("round %d: reviewer %s has the implementer's change (%s)", t.Round, t.SidecarID, shortHash(t.Fingerprint)))
	}
}

// close records how the run ended and closes the log.
func (l *runLog) close(rep Report, err error) {
	if l == nil {
		return
	}
	if rep.Started {
		l.line("end", fmt.Sprintf("result %s after %d round(s), committed %t to %s", rep.Outcome.Result, rep.Outcome.Rounds, rep.Committed, rep.Worktree.Branch))
	}
	if err != nil {
		l.line("end", "stopped: "+err.Error())
	} else {
		l.line("end", "run finished")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.f.Close()
}

// block records a multi-line body under a heading, indented so it reads as
// one entry. An empty body is left out.
func (l *runLog) block(heading, body string) {
	body = strings.TrimSpace(body)
	if l == nil || body == "" {
		return
	}
	l.line("info", heading+":\n    "+strings.ReplaceAll(body, "\n", "\n    "))
}

func (l *runLog) line(level, msg string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// A log that cannot be written must not stop the run it records.
	_, _ = fmt.Fprintf(l.f, "%s %-5s %s\n", l.now().UTC().Format(time.RFC3339), level, msg)
}

// checkTally sums up a round's checks in one line, naming those that did not
// pass.
func checkTally(checks []Check) string {
	var failed, errored []string
	for _, c := range checks {
		switch c.Status {
		case StatusPassed:
		case StatusFailed:
			failed = append(failed, string(c.Kind)+" "+c.Name)
		case StatusErrored:
			errored = append(errored, string(c.Kind)+" "+c.Name)
		}
	}
	line := fmt.Sprintf("%d of %d checks passed", len(checks)-len(failed)-len(errored), len(checks))
	if len(failed) > 0 {
		line += "; failed: " + strings.Join(failed, ", ")
	}
	if len(errored) > 0 {
		line += "; could not run: " + strings.Join(errored, ", ")
	}
	return line
}

func levelName(level iostream.Level) string {
	switch level {
	case iostream.LevelStep:
		return "step"
	case iostream.LevelInfo:
		return "info"
	case iostream.LevelWarn:
		return "warn"
	case iostream.LevelDone:
		return "done"
	case iostream.LevelError:
		return "error"
	}
	return "info"
}

func statusLevel(s Status) string {
	if s == StatusPassed {
		return "done"
	}
	return "error"
}

// shortHash abbreviates a fingerprint for display, the way git abbreviates
// object IDs.
func shortHash(h string) string {
	if h == "" {
		return "nothing"
	}
	return h[:min(len(h), 12)]
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
