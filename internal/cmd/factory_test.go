package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
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

func TestFactoryPrompt(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		stdin   string
		want    string
		wantErr string
	}{
		{name: "argument", args: []string{"add a flag"}, stdin: "ignored", want: "add a flag"},
		{name: "no argument reads stdin", stdin: "# Task\n\nadd a flag\n", want: "# Task\n\nadd a flag\n"},
		{name: "dash reads stdin", args: []string{"-"}, stdin: "# Task\n\nadd a flag\n", want: "# Task\n\nadd a flag\n"},
		{name: "empty implicit stdin", stdin: " \n", wantErr: "empty"},
		{name: "empty explicit stdin", args: []string{"-"}, stdin: " \n", wantErr: "empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := factoryPrompt(strings.NewReader(tc.stdin), tc.args, false)
			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, got, tc.want)
		})
	}
}

// A prompt argument with a file redirected to stdin would silently drop the
// file's prompt, as in --log run.log < prompt.md, so it is refused. Empty files
// and non-files are what scripts inherit as stdin, so they are not.
func TestFactoryPromptRefusesAnArgumentAndARedirectedFile(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.md")
	assert.NilError(t, os.WriteFile(promptFile, []byte("# Task\n\nadd a flag\n"), 0o644))
	emptyFile := filepath.Join(dir, "empty")
	assert.NilError(t, os.WriteFile(emptyFile, nil, 0o644))
	open := func(path string) *os.File {
		f, err := os.Open(path)
		assert.NilError(t, err)
		t.Cleanup(func() { _ = f.Close() })
		return f
	}

	_, err := factoryPrompt(open(promptFile), []string{"/tmp/run.log"}, true)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), `Got the prompt "/tmp/run.log" as an argument and another prompt on stdin.`)
	assert.Equal(t, ue.Suggestion(), "--log takes no file. To log to /tmp/run.log, write --log-file /tmp/run.log.")
	assert.Equal(t, ue.UserExitCode(), ExitBadArgs)

	_, err = factoryPrompt(open(promptFile), []string{"add a flag"}, false)
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.Suggestion(), "Pass the prompt as the argument or on stdin, not both.")

	got, err := factoryPrompt(open(promptFile), []string{"-"}, false)
	assert.NilError(t, err)
	assert.Equal(t, got, "# Task\n\nadd a flag\n")

	got, err = factoryPrompt(open(emptyFile), []string{"add a flag"}, true)
	assert.NilError(t, err)
	assert.Equal(t, got, "add a flag")
}

func TestFactoryArgs(t *testing.T) {
	cmd := newFactoryCmd()
	assert.NilError(t, cmd.Args(cmd, nil))
	assert.NilError(t, cmd.Args(cmd, []string{"add a flag"}))
	assert.NilError(t, cmd.Args(cmd, []string{"-"}))
	assert.ErrorContains(t, cmd.Args(cmd, []string{""}), "one argument or on stdin")
	assert.ErrorContains(t, cmd.Args(cmd, []string{"one", "two"}), "one argument or on stdin")
}

func TestKeepWorkHint(t *testing.T) {
	// A branch that starts at the developer's HEAD merges.
	clean := &chunkd.FactoryRun{Branch: "chunk/factory/run-1", Baseline: "abc", Head: "abc"}
	assert.Equal(t, keepWorkHint(clean), "Merge it with: git merge chunk/factory/run-1")

	// One that starts from their uncommitted work would collide with that work
	// in their checkout, so only the run's own changes are applied.
	dirty := &chunkd.FactoryRun{Branch: "chunk/factory/run-1", Baseline: "def", Head: "abc"}
	assert.Equal(t, keepWorkHint(dirty), "Apply it with: git diff --binary def chunk/factory/run-1 | git apply")
}

func TestFactoryLogPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Date(2026, 10, 2, 15, 4, 5, 0, time.UTC)
	byTime := filepath.Join(home, ".chunk", "factory", "run-20261002-150405.log")
	for _, tc := range []struct {
		name string
		path string
		on   bool
		want string
	}{
		{name: "no log", want: ""},
		// The default is named here, so this command can show the log.
		{name: "default", on: true, want: byTime},
		{name: "file without the switch", path: "/tmp/run.log", want: "/tmp/run.log"},
		{name: "file with the switch", path: "/tmp/run.log", on: true, want: "/tmp/run.log"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := factoryLogPath(tc.path, tc.on, now)
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

// TestFactoryLogFileTakesTheNextWord guards --log-file FILE, which an optional
// value would have read as a log at the default path and a prompt of FILE.
func TestFactoryLogFileTakesTheNextWord(t *testing.T) {
	for _, args := range [][]string{
		{"--log-file", "run.log", "add a flag"},
		{"--log-file=run.log", "add a flag"},
	} {
		cmd := newFactoryCmd()
		assert.NilError(t, cmd.ParseFlags(args), "args %q", args)
		assert.Equal(t, cmd.Flags().Lookup("log-file").Value.String(), "run.log", "args %q", args)
		assert.DeepEqual(t, cmd.Flags().Args(), []string{"add a flag"})
	}
}

// TestTailLogCopiesTheWholeLogByTheTimeItStops guards --verbose's last lines:
// what the run wrote just before it ended is shown, not lost with the tail.
func TestTailLogCopiesTheWholeLogByTheTimeItStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	var out bytes.Buffer
	stop := tailLog(path, 0, &out)
	// The log does not exist when the tail starts; the run creates it.
	assert.NilError(t, os.WriteFile(path, []byte("start\nend\n"), 0o600))
	stop()
	assert.Equal(t, out.String(), "start\nend\n")
}

// TestTailLogSkipsWhatTheLogHeldBeforeTheRun guards --log-file <existing file>: the
// run appends to it, and the earlier runs' lines are not shown again.
func TestTailLogSkipsWhatTheLogHeldBeforeTheRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	assert.NilError(t, os.WriteFile(path, []byte("earlier run\n"), 0o600))
	from := logSize(path)
	assert.Equal(t, from, int64(len("earlier run\n")))
	assert.Equal(t, logSize(filepath.Join(t.TempDir(), "none.log")), int64(0))

	var out bytes.Buffer
	stop := tailLog(path, from, &out)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	assert.NilError(t, err)
	_, err = f.WriteString("this run\n")
	assert.NilError(t, err)
	assert.NilError(t, f.Close())
	stop()
	assert.Equal(t, out.String(), "this run\n")
}

// TestLogTailCopiesWholeLines guards --verbose's terminal output: each line of
// the run's log once, and never half of one the run is still writing.
func TestLogTailCopiesWholeLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	var out bytes.Buffer
	tail := &logTail{w: &out, path: path}

	tail.follow() // not created yet
	assert.Equal(t, out.String(), "")

	assert.NilError(t, os.WriteFile(path, []byte("one\ntw"), 0o600))
	tail.follow()
	assert.Equal(t, out.String(), "one\n")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	assert.NilError(t, err)
	_, err = f.WriteString("o\nthree\n")
	assert.NilError(t, err)
	assert.NilError(t, f.Close())
	tail.follow()
	tail.follow()
	assert.Equal(t, out.String(), "one\ntwo\nthree\n")
}

// TestLogTailKeepsSidecarOutputOffTheTerminal guards --verbose: the log holds
// command output and agents' text from the sidecars, and its escape sequences
// are not passed on to the terminal.
func TestLogTailKeepsSidecarOutputOffTheTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	assert.NilError(t, os.WriteFile(path, []byte("ok\tpkg \x1b]0;title\x07\x1b[31mFAIL\x1b[0m\r\n\u00e9\n"), 0o600))
	var out bytes.Buffer
	(&logTail{w: &out, path: path}).follow()
	// The escape bytes and the carriage return are gone; text, tabs, newlines
	// and non-ASCII letters stay.
	assert.Equal(t, out.String(), "ok\tpkg ]0;title[31mFAIL[0m\n\u00e9\n")
}

func TestPrintFactoryLeftoversPointsOnlyAtAKeptWorktree(t *testing.T) {
	collect := func(f *chunkd.FactoryRun) []string {
		var said []string
		status := func(_ iostream.Level, msg string) { said = append(said, msg) }
		printFactoryLeftovers(f, status, iostream.Streams{Out: io.Discard, Err: io.Discard})
		return said
	}

	kept := collect(&chunkd.FactoryRun{Worktree: "/wt/run-1"})
	assert.DeepEqual(t, kept, []string{"The work was not committed. It is in the worktree /wt/run-1"})

	// A run that failed before the implementer started had its empty worktree
	// removed; pointing at it would send the reader to a path that is gone.
	assert.Check(t, cmp.Len(collect(&chunkd.FactoryRun{Worktree: "/wt/run-1", WorktreeRemoved: true}), 0))
}
