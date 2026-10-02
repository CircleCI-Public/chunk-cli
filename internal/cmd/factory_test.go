package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// TestFactoryChecksOnlyToleratesMissingDefaultReviews guards against a
// mistyped --reviews silently running no reviews at all.
func TestFactoryChecksOnlyToleratesMissingDefaultReviews(t *testing.T) {
	workDir := t.TempDir()
	cfg := &config.ProjectConfig{Commands: []config.Command{{Name: "test", Run: "go test ./..."}}}

	prompts, commands, err := factoryChecks(workDir, "", cfg, false)
	assert.NilError(t, err, "a missing default directory is fine when there are commands")
	assert.Equal(t, len(prompts), 0)
	assert.Equal(t, len(commands), 1)

	_, _, err = factoryChecks(workDir, filepath.Join(workDir, "reviws"), cfg, false)
	assert.ErrorContains(t, err, "reviws")

	empty := filepath.Join(workDir, "empty")
	assert.NilError(t, os.Mkdir(empty, 0o755))
	_, _, err = factoryChecks(workDir, empty, cfg, false)
	assert.ErrorIs(t, err, review.ErrNoPrompts)
}

func TestKeepWorkHint(t *testing.T) {
	// A branch that starts at the developer's HEAD merges.
	clean := factory.Worktree{Branch: "chunk/factory/run-1", Baseline: "abc", Head: "abc"}
	assert.Equal(t, keepWorkHint(clean), "Merge it with: git merge chunk/factory/run-1")

	// One that starts from their uncommitted work would collide with that work
	// in their checkout, so only the run's own changes are applied.
	dirty := factory.Worktree{Branch: "chunk/factory/run-1", Baseline: "def", Head: "abc"}
	assert.Equal(t, keepWorkHint(dirty), "Apply it with: git diff --binary def chunk/factory/run-1 | git apply")
}

// A stuck run's record ends on a round that was never checked; its outcome is
// the last round that was.
func TestFactoryOutcomeIsTheLastCheckedRound(t *testing.T) {
	bug := review.Finding{File: "main.go", Line: 3, Severity: "high", Body: "nil deref"}
	detail := watchd.SessionDetail{
		Session: watchd.Session{
			Factory: &watchd.FactoryRun{Result: string(factory.ResultStuck), Rounds: 1},
			Rounds:  []watchd.Round{{Number: 1}, {Number: 2}},
		},
		Details: []watchd.RoundDetail{
			{Number: 1, Results: []watchd.ReviewResult{{Prompt: "bugs", Status: "failed", Findings: []review.Finding{bug}}}},
			{Number: 2},
		},
	}
	o := factoryOutcome(detail)
	assert.Equal(t, o.Rounds, 1)
	assert.Equal(t, len(o.Checks), 1)
	assert.Equal(t, o.Checks[0].Name, "bugs")
	assert.Equal(t, len(o.Checks[0].Findings), 1)
}

func TestFactoryLogPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Date(2026, 10, 2, 15, 4, 5, 0, time.UTC)
	byTime := filepath.Join(home, ".chunk", "factory", "run-20261002-150405.log")
	for _, tc := range []struct {
		name    string
		path    string
		verbose bool
		want    string
	}{
		{name: "no log", want: ""},
		// The default is named here, so this command can show the log.
		{name: "default", path: factory.LogDefault, want: byTime},
		{name: "verbose implies the default", verbose: true, want: byTime},
		{name: "absolute kept", path: "/tmp/run.log", want: "/tmp/run.log"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := factoryLogPath(tc.path, tc.verbose, now)
			assert.NilError(t, err)
			assert.Equal(t, got, tc.want)
		})
	}

	// The daemon does not share this process's working directory, so a
	// relative path is resolved here.
	got, err := factoryLogPath("run.log", false, now)
	assert.NilError(t, err)
	wd, err := os.Getwd()
	assert.NilError(t, err)
	assert.Equal(t, got, filepath.Join(wd, "run.log"))
}

// TestTailLogCopiesTheWholeLogByTheTimeItStops guards --verbose's last lines:
// what the run wrote just before it ended is shown, not lost with the tail.
func TestTailLogCopiesTheWholeLogByTheTimeItStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	var out bytes.Buffer
	stop := tailLog(path, &out)
	// The log does not exist when the tail starts; the run creates it.
	assert.NilError(t, os.WriteFile(path, []byte("start\nend\n"), 0o600))
	stop()
	assert.Equal(t, out.String(), "start\nend\n")
}

// TestLogTailCopiesWholeLines guards --verbose's terminal output: each line of
// the run's log once, and never half of one the run is still writing.
func TestLogTailCopiesWholeLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	var out bytes.Buffer
	tail := &logTail{w: &out}

	tail.follow(path) // not created yet
	tail.follow("")
	assert.Equal(t, out.String(), "")

	assert.NilError(t, os.WriteFile(path, []byte("one\ntw"), 0o600))
	tail.follow(path)
	assert.Equal(t, out.String(), "one\n")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	assert.NilError(t, err)
	_, err = f.WriteString("o\nthree\n")
	assert.NilError(t, err)
	assert.NilError(t, f.Close())
	tail.follow(path)
	tail.follow(path)
	assert.Equal(t, out.String(), "one\ntwo\nthree\n")
}
