package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// fakeClaudeOnPath puts a claude on PATH that runs script in the worktree, so
// a draft is made without Claude Code or an API key.
func fakeClaudeOnPath(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"+script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A prompt sent from the dashboard is drafted by Claude in a factory run's
// worktree, and the draft is its diff from the developer's files.
func TestWatchDraftHasClaudeDraftThePrompt(t *testing.T) {
	project := factoryProject(t)
	args := filepath.Join(t.TempDir(), "args")
	fakeClaudeOnPath(t, `echo "$@" > `+args+`
printf 'package main\n' > flag.go
echo "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Write\",\"input\":{\"file_path\":\"$PWD/flag.go\"}}]}}"
echo '{"type":"result","result":"Added flag.go"}'
`)
	drafts := newWatchDrafts(t.Context())
	defer drafts.stop()

	var activity []string
	d, err := drafts.draft(t.Context(), project, "add a flag", func(s string) { activity = append(activity, s) })
	assert.NilError(t, err)

	rec, err := factory.LoadRecord(project, d.RunID)
	assert.NilError(t, err)
	assert.Assert(t, rec.NeedsWork)
	assert.Equal(t, rec.Prompt, "add a flag")
	assert.Equal(t, d.Summary, "Added flag.go")
	assert.Assert(t, strings.Contains(d.Stat, "flag.go | 1 +"), d.Stat)
	assert.Assert(t, strings.Contains(d.Diff, "+package main"), d.Diff)
	// Claude names the file by its full path in the worktree; it is shown as
	// it is in the repo.
	assert.DeepEqual(t, activity, []string{"Write flag.go"})
	assert.Assert(t, strings.Contains(readArgs(t, args), "--model sonnet"), "the draft is made with Sonnet")
}

// What build would refuse is refused before Claude is asked, so no one waits
// on a draft the factory cannot take.
func TestWatchDraftChecksWhatBuildChecksFirst(t *testing.T) {
	project := factoryProject(t)
	assert.NilError(t, os.Remove(filepath.Join(project, ".chunk", "config.json")))
	asked := filepath.Join(t.TempDir(), "asked")
	fakeClaudeOnPath(t, "touch "+asked+"\n")
	drafts := newWatchDrafts(t.Context())
	defer drafts.stop()

	_, err := drafts.draft(t.Context(), project, "add a flag", func(string) {})
	assert.Error(t, err, "No validate commands configured. Run 'chunk init' first.")
	_, statErr := os.Stat(asked)
	assert.Assert(t, os.IsNotExist(statErr), "Claude was not asked")
}

// Closing the dashboard stops a draft still being made, and waits for it, so
// Claude does not carry on as a process of its own.
func TestClosingTheDashboardStopsADraft(t *testing.T) {
	project := factoryProject(t)
	started := filepath.Join(t.TempDir(), "started")
	fakeClaudeOnPath(t, "touch "+started+"\nsleep 30\n")
	drafts := newWatchDrafts(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := drafts.draft(drafts.ctx, project, "add a flag", func(string) {})
		done <- err
	}()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		assert.Assert(t, time.Now().Before(deadline), "claude never started")
	}

	start := time.Now()
	drafts.stop()
	assert.Assert(t, time.Since(start) < 10*time.Second, "stop waited %s", time.Since(start))
	assert.Assert(t, errors.Is(<-done, context.Canceled))
}

// Sending a draft continues its run on the daemon with guidance to finish it,
// so the implementer starts at once rather than after a round of checks.
func TestSendDraftContinuesItsRun(t *testing.T) {
	project := factoryProject(t)
	writeFactoryRecord(t, project, "run-0")
	cfg := fakeFactoryConfig(t, factory.ResultPassed)
	got := make(chan factory.RunOptions, 1)
	run := cfg.RunFactory
	cfg.RunFactory = func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		got <- opts
		return run(ctx, opts)
	}
	startSessionDaemon(t, cfg)

	id, err := sendDraft(project, "run-0")
	assert.NilError(t, err)
	assert.Assert(t, id != "")

	opts := <-got
	assert.Assert(t, opts.Continue != nil, "the run was not told to continue")
	assert.Equal(t, opts.Continue.From.RunID, "run-0")
	assert.Equal(t, opts.Continue.Guidance, draftGuidance, "the implementer starts on the draft at once")
	assert.Equal(t, opts.Model, "sonnet", "a draft is finished with the faster model")
}

// Editors' built-in terminals get a margin at the bottom of the dashboard;
// other terminals use every row.
func TestEditorRowMargin(t *testing.T) {
	for _, tc := range []struct {
		termProgram, emulator string
		want                  int
	}{
		{termProgram: "vscode", want: 2},
		{emulator: "JetBrains-JediTerm", want: 2},
		{termProgram: "iTerm.app", want: 0},
		{termProgram: "Apple_Terminal", want: 0},
		{want: 0},
	} {
		t.Setenv("TERM_PROGRAM", tc.termProgram)
		t.Setenv("TERMINAL_EMULATOR", tc.emulator)
		assert.Equal(t, editorRowMargin(), tc.want, "TERM_PROGRAM=%q TERMINAL_EMULATOR=%q", tc.termProgram, tc.emulator)
	}
}

func readArgs(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	assert.NilError(t, err)
	return string(b)
}
