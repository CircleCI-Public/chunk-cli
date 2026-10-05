package factory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// newTestLog opens a log in a temp directory with a fixed clock.
func newTestLog(t *testing.T, verbose bool) (*runLog, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "run.log")
	lg, err := openLog(path, "20261002-150405", verbose)
	assert.NilError(t, err)
	lg.now = func() time.Time { return time.Date(2026, 10, 2, 15, 4, 5, 0, time.UTC) }
	return lg, path
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	assert.NilError(t, err)
	return string(b)
}

// TestLogRecordsWhatTheDisplayLeavesOut guards the log's purpose: a run read
// back from it alone, by a person or an agent judging the prompts, has the
// full context, not the display's summary of it.
func TestLogRecordsWhatTheDisplayLeavesOut(t *testing.T) {
	lg, path := newTestLog(t, false)
	lg.attempts = 3
	var shown []string
	opts := lg.wrap(RunOptions{Status: func(_ iostream.Level, msg string) { shown = append(shown, msg) }})

	opts.Status(iostream.LevelStep, "Preparing sidecars...")
	opts.OnEvent(Event{Kind: EventImplementing, Round: 2, Prompt: "Fix these:\n- lint"})
	opts.OnActivity(Activity{Tool: "Edit", Detail: "main.go"})
	opts.OnActivity(Activity{Detail: "Fixing the lint\nfailure."})
	opts.OnEvent(Event{Kind: EventImplemented, Round: 2, Turn: Turn{Summary: "Fixed it.", Duration: 3 * time.Second, CostUSD: 0.5}})
	opts.OnCheck(Check{Name: "lint", Kind: KindValidate, Status: StatusFailed, SidecarID: "impl", ExitCode: 1, Output: "main.go:3: unused"})
	opts.OnCheck(Check{Name: "test", Kind: KindValidate, Status: StatusPassed, SidecarID: "impl", Output: "ok pkg"})
	opts.OnEvent(Event{Kind: EventChecked, Round: 2, Checks: []Check{
		{Name: "bugs", Kind: KindReview, Status: StatusFailed, SidecarID: "rev-1", Prose: "One bug.", Findings: []review.Finding{
			{File: "a.go", Line: 3, Severity: "high", Body: "nil deref\nwhen empty"},
			{File: "a.go", Line: 9, Severity: "low", Body: "rename x"},
		}},
		{Name: "style", Kind: KindReview, Status: StatusErrored, SidecarID: "rev-2", Error: "timed out"},
		{Name: "lint", Kind: KindValidate, Status: StatusFailed},
	}})
	lg.close(Report{}, errors.New("round 3: implement: boom"))

	// The display still gets its status lines, and only those.
	assert.DeepEqual(t, shown, []string{"Preparing sidecars..."})
	assert.Equal(t, readLog(t, path), strings.Join([]string{
		"2026-10-02T15:04:05Z step  Preparing sidecars...",
		"2026-10-02T15:04:05Z info  round 2: prompt sent to the implementer:",
		"    Fix these:",
		"    - lint",
		"2026-10-02T15:04:05Z info  implementer: Edit main.go",
		"2026-10-02T15:04:05Z info  implementer:",
		"    Fixing the lint",
		"    failure.",
		"2026-10-02T15:04:05Z info  round 2: implementer finished in 3s ($0.50)",
		"2026-10-02T15:04:05Z info  round 2: implementer's summary:",
		"    Fixed it.",
		"2026-10-02T15:04:05Z error validate lint on impl failed in 0s, exit 1",
		"2026-10-02T15:04:05Z info    output:",
		"    main.go:3: unused",
		// A passing command's output is left out without verbose.
		"2026-10-02T15:04:05Z done  validate test on impl passed in 0s",
		"2026-10-02T15:04:05Z error round 2: review bugs on rev-1 failed in 0s with 2 finding(s)",
		"2026-10-02T15:04:05Z info    [high] a.go:3 (sent to the implementer):",
		"    nil deref",
		"    when empty",
		// A finding below the bar is kept too, marked as not sent.
		"2026-10-02T15:04:05Z info    [low] a.go:9:",
		"    rename x",
		"2026-10-02T15:04:05Z info    prose:",
		"    One bug.",
		"2026-10-02T15:04:05Z warn  round 2: review style on rev-2 errored in 0s: timed out",
		"2026-10-02T15:04:05Z info  round 2: 0 of 3 checks passed; failed: review bugs, validate lint; could not run: review style",
		"2026-10-02T15:04:05Z end   stopped: round 3: implement: boom",
		"",
	}, "\n"))

	info, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))
}

// TestLogDoesNotSayTheLastRoundsFindingsWereSent guards the log's account of
// what the implementer was told: no round follows the last, so its findings,
// worth changing or not, were not sent.
func TestLogDoesNotSayTheLastRoundsFindingsWereSent(t *testing.T) {
	lg, path := newTestLog(t, false)
	lg.start("20261002-150405", RunOptions{Prompt: "p", Attempts: 2})
	checks := []Check{{Name: "bugs", Kind: KindReview, Status: StatusFailed, SidecarID: "rev-1", Findings: []review.Finding{
		{File: "a.go", Line: 3, Severity: "high", Body: "nil deref"},
	}}}
	lg.event(Event{Kind: EventChecked, Round: 1, Checks: checks})
	lg.event(Event{Kind: EventChecked, Round: 2, Checks: checks})
	lg.close(Report{}, nil)

	got := strings.Split(readLog(t, path), "[high] a.go:3")
	assert.Equal(t, len(got), 3, readLog(t, path))
	assert.Assert(t, strings.HasPrefix(got[1], " (sent to the implementer):"), got[1])
	assert.Assert(t, strings.HasPrefix(got[2], ":"), got[2])
}

func TestLogVerboseAddsPromptsAndPassingOutput(t *testing.T) {
	opts := RunOptions{
		Prompt:   "add a flag",
		Attempts: 3, Reviewers: 1,
		Prompts:  []review.Prompt{{Name: "adversarial", Body: "Find real problems.\n\nProve them."}},
		Commands: []config.Command{{Name: "test", Run: "task test"}},
	}
	for _, verbose := range []bool{false, true} {
		lg, path := newTestLog(t, verbose)
		lg.start("20261002-150405", opts)
		lg.check(Check{Name: "test", Kind: KindValidate, Status: StatusPassed, SidecarID: "impl", Output: "ok pkg"})
		lg.close(Report{Started: true, Committed: true, Outcome: Outcome{Result: ResultPassed, Rounds: 1}, Worktree: Worktree{Branch: "chunk/factory/1"}}, nil)
		got := readLog(t, path)

		// Every log says what the run was set to do.
		for _, want := range []string{
			"info  run 20261002-150405\n",
			"info  prompt:\n    add a flag\n",
			"info  attempts 3, 1 reviewer sidecar(s)\n",
			"info  review prompts: adversarial\n",
			"info  validation commands: test: task test\n",
			"end   result passed after 1 round(s), committed true to chunk/factory/1\n",
			"end   run finished\n",
		} {
			assert.Assert(t, strings.Contains(got, want), "verbose=%t: missing %q in:\n%s", verbose, want, got)
		}
		// The prompts are logged as they are sent: a review's with the original
		// request and scope, and the implementer's system prompt.
		prompt := strings.Contains(got, "info  review prompt adversarial:\n    Review the implementation against the original requested change") &&
			strings.Contains(got, "\n    ## Requested change\n    \n    add a flag\n") &&
			strings.Contains(got, "\n    ## Change under review\n    \n    The change under review is the uncommitted work") &&
			strings.Contains(got, "\n    ## Review instructions\n") &&
			strings.Contains(got, "\n    Find real problems.\n    \n    Prove them.\n")
		system := strings.Contains(got, "info  implementer system prompt:\n    You are working in a disposable copy")
		output := strings.Contains(got, "info    output:\n    ok pkg\n")
		assert.Equal(t, prompt, verbose, got)
		assert.Equal(t, system, verbose, got)
		assert.Equal(t, output, verbose, got)
	}
}

func TestLogReviewerTree(t *testing.T) {
	lg, path := newTestLog(t, true)
	var shown []string
	status := lg.wrap(RunOptions{Status: func(_ iostream.Level, msg string) { shown = append(shown, msg) }}).Status

	want := "0123456789abcdef0123"
	lg.reviewerTree(status, ReviewerTree{Round: 2, SidecarID: "rev-1", Fingerprint: want, Want: want})
	lg.reviewerTree(status, ReviewerTree{Round: 2, SidecarID: "rev-2", Fingerprint: "fedcba9876543210fedc", Want: want})
	lg.reviewerTree(status, ReviewerTree{Round: 2, SidecarID: "rev-3", Want: want, Err: errors.New("exited 128")})
	lg.close(Report{}, nil)

	// Only what went wrong reaches the display.
	assert.DeepEqual(t, shown, []string{
		"reviewer rev-2 does not have the implementer's change: it has fedcba987654, the implementer 0123456789ab",
		"could not check reviewer rev-3 has the implementer's change: exited 128",
	})
	assert.Equal(t, readLog(t, path), strings.Join([]string{
		"2026-10-02T15:04:05Z info  round 2: reviewer rev-1 has the implementer's change (0123456789ab)",
		"2026-10-02T15:04:05Z warn  reviewer rev-2 does not have the implementer's change: it has fedcba987654, the implementer 0123456789ab",
		"2026-10-02T15:04:05Z warn  could not check reviewer rev-3 has the implementer's change: exited 128",
		"2026-10-02T15:04:05Z end   run finished",
		"",
	}, "\n"))
}

func TestLogDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	lg, err := openLog(LogDefault, "20261002-150405", false)
	assert.NilError(t, err)
	lg.close(Report{}, nil)
	assert.Equal(t, lg.path, filepath.Join(home, ".chunk", "factory", "run-20261002-150405.log"))
}

// TestLogOffRecordsNothing guards the nil log: with no --log, the run's hooks
// are its caller's, untouched.
func TestLogOffRecordsNothing(t *testing.T) {
	lg, err := openLog("", "20261002-150405", false)
	assert.NilError(t, err)
	assert.Assert(t, lg == nil)

	called := false
	opts := lg.wrap(RunOptions{Status: func(iostream.Level, string) { called = true }})
	assert.Assert(t, opts.OnEvent == nil && opts.OnCheck == nil)
	opts.Status(iostream.LevelInfo, "x")
	lg.start("id", RunOptions{Prompt: "p"})
	lg.close(Report{}, nil)
	assert.Assert(t, called)
}
