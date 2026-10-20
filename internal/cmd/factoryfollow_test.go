package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/chunkd/server"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// fakeSessionConfig is a daemon setup with a Claude credential and nothing
// else. Tests set RunFactory, so no sandbox is booted and no API is called.
func fakeSessionConfig() server.ReviewConfig {
	return server.ReviewConfig{
		Credential: claudecode.Credential{EnvVar: config.EnvAnthropicAPIKey, Value: "sk-test"},
	}
}

// startSessionDaemon runs a real daemon on a Unix socket with a fake backend.
func startSessionDaemon(t *testing.T, cfg server.ReviewConfig) {
	t.Helper()
	startSessionDaemonWithGitHub(t, cfg, nil)
}

// startSessionDaemonWithGitHub is startSessionDaemon for a daemon that has a
// GitHub client, as one started with a token does.
func startSessionDaemonWithGitHub(t *testing.T, cfg server.ReviewConfig, gh *github.Client) {
	t.Helper()
	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_DAEMON_DIR", dir)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- server.RunDaemon(ctx, nil, "", nil, gh, server.WithReview(cfg)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not shut down")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if chunkd.IsDaemonRunning() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon did not become reachable")
}

// sessionProject makes a real git repo with two review prompts.
func sessionProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		assert.NilError(t, err, string(out))
	}
	run("init", "-b", "main")
	run("remote", "add", "origin", "https://github.com/acme/widgets.git")
	prompts := filepath.Join(dir, ".chunk", "reviews")
	assert.NilError(t, os.MkdirAll(prompts, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(prompts, "bugs.md"), []byte("find bugs"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(prompts, "style.md"), []byte("check style"), 0o644))
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

// followSession's gating for collapsing setup noise must hold for every
// combination of flags: a person on a terminal gets it, but --verbose,
// --json and a non-terminal (chunk watch and --log see a non-terminal too)
// must always see every line, even when run on a real terminal.
func TestCollapseSetupNoiseGating(t *testing.T) {
	for _, tc := range []struct {
		tty, jsonOut, verbose bool
		want                  bool
	}{
		{tty: true, want: true},
		{tty: false, want: false},
		{tty: true, jsonOut: true, want: false},
		{tty: true, verbose: true, want: false},
		{tty: true, jsonOut: true, verbose: true, want: false},
		{tty: false, jsonOut: true, verbose: true, want: false},
	} {
		got := collapseSetupNoise(tc.tty, tc.jsonOut, tc.verbose)
		assert.Equal(t, got, tc.want, "tty=%v jsonOut=%v verbose=%v", tc.tty, tc.jsonOut, tc.verbose)
	}
}

// The project needs an origin remote for its sidecars to clone, so a project
// without one is refused before anything is started or registered.
func TestFactoryWithoutAnOriginRemoteFailsBeforeAnySandboxWork(t *testing.T) {
	// No daemon is running: the check must come before the daemon is needed.
	project := factoryProject(t)
	c := exec.Command("git", "remote", "remove", "origin")
	c.Dir = project
	out, err := c.CombinedOutput()
	assert.NilError(t, err, string(out))

	_, _, err = runFactoryBuildCmd(t, "add a --verbose flag")
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), "origin"))
	assert.Assert(t, strings.Contains(ue.Suggestion(), "git remote add origin"))
	assert.Equal(t, ue.exitCode, ExitBadArgs)
}

// A factory run works from this machine's files, so a remote daemon is
// refused.
func TestFactoryRefusesARemoteDaemon(t *testing.T) {
	factoryProject(t)
	t.Setenv("CHUNK_DAEMON_REMOTE_ADDR", "127.0.0.1:1")
	_, _, err := runFactoryBuildCmd(t, "add a --verbose flag")
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.exitCode, ExitBadArgs)
}

// reportedLines follows a session through snapshots and returns what was
// said, one string per line.
func reportedLines(snapshots ...chunkd.SessionDetail) []string {
	var lines []string
	rep := newSessionReporter(func(_ iostream.Level, msg string) { lines = append(lines, msg) })
	for _, d := range snapshots {
		rep.report(d)
	}
	return lines
}

func TestSessionReporterSaysHowEachRoundWent(t *testing.T) {
	bug := chunkd.Finding{File: "main.go", Line: 3, Severity: "high", Body: "nil deref"}
	output := strings.Repeat("ok\n", 30) + "--- FAIL: TestFlag"
	checking := chunkd.SessionDetail{Session: chunkd.Session{
		Factory: &chunkd.FactoryRun{Attempts: 3},
		Rounds: []chunkd.Round{{
			Number:  1,
			State:   chunkd.RoundChecking,
			Reviews: []chunkd.ReviewPrompt{{Name: "bugs", State: chunkd.PromptDone, DurationMS: 12000}},
			Checks:  []chunkd.RoundCheck{{Name: "test", Status: "failed", DurationMS: 3000, Output: output}},
		}},
	}}
	done := checking
	done.Rounds = []chunkd.Round{checking.Rounds[0]}
	done.Rounds[0].State, done.Rounds[0].Note = chunkd.RoundDone, "0 of 2 checks passed"
	done.Details = []chunkd.RoundDetail{{Number: 1, Results: []chunkd.ReviewResult{{Prompt: "bugs", Status: "failed", Findings: []chunkd.Finding{bug}}}}}

	// The round is seen done twice; its findings are said once.
	lines := reportedLines(checking, done, done)

	want := []string{"round 1/3: checking", "  test failed in 3s"}
	for range failedOutputLines - 1 {
		want = append(want, "    ok")
	}
	want = append(want,
		"    --- FAIL: TestFlag",
		"bugs reviewed in 12s",
		"round 1/3: done — 0 of 2 checks passed",
		"review bugs: 1 finding(s)",
		"  [high] main.go:3 nil deref",
	)
	assert.DeepEqual(t, lines, want)
}

// setupProgress is the noise a factory run says while its sandboxes are made
// and synced, before any round: the step line announcing it, then a sidecar
// pool's own lines.
func setupProgress(reviewers int) chunkd.Feed {
	lines := []chunkd.FeedLine{
		{Level: chunkd.FeedStep, Text: fmt.Sprintf("Preparing sandboxes (implementer + %d reviewers)...", reviewers)},
		{Level: chunkd.FeedInfo, Text: "created sidecar 1 (sc-1)"},
		{Level: chunkd.FeedInfo, Text: "created sidecar 2 (sc-2)"},
		{Level: chunkd.FeedInfo, Text: "Syncing workspace /repo..."},
		{Level: chunkd.FeedDone, Text: "Synced"},
		{Level: chunkd.FeedInfo, Text: "created seed snapshot sc-3"},
	}
	return chunkd.Feed{Lines: lines, Total: len(lines)}
}

// A terminal following a run without --verbose or --json holds its setup
// noise back rather than saying it, down to the step line that announces it
// starting, and says it is ready once the first round appears.
func TestSessionReporterCollapsesSetupOnATerminal(t *testing.T) {
	setup := chunkd.SessionDetail{Session: chunkd.Session{
		State: chunkd.SessionRunning, Factory: &chunkd.FactoryRun{Progress: setupProgress(2), Attempts: 3},
	}}
	ready := setup
	ready.Rounds = []chunkd.Round{{Number: 1, State: chunkd.RoundStarted}}

	var lines []string
	rep := newSessionReporter(func(_ iostream.Level, msg string) { lines = append(lines, msg) })
	start := time.Now().Add(-42 * time.Second)
	rep.collapseSetup(start)

	rep.report(setup)
	assert.DeepEqual(t, lines, []string{"Preparing sandboxes (implementer + 2 reviewers)..."})
	assert.Assert(t, rep.settingUp())

	rep.report(ready)
	for _, notWant := range []string{"created sidecar", "Syncing", "Synced", "created seed snapshot"} {
		for _, l := range lines {
			assert.Assert(t, !strings.Contains(l, notWant), "unexpected %q among %v", notWant, lines)
		}
	}
	assert.Assert(t, !rep.settingUp())
	found := false
	for _, l := range lines {
		if strings.Contains(l, "Sandboxes ready in") {
			found = true
		}
	}
	assert.Assert(t, found, "missing a \"Sandboxes ready in\" line among %v", lines)
	assert.Assert(t, strings.Contains(lines[len(lines)-1], "round 1/"), "the round line comes after setup is said: %v", lines)
}

// A session that ends before any round appears must still show what its
// setup said, since that is the only clue to why it failed.
func TestSessionReporterSaysHeldSetupLinesBeforeAFailure(t *testing.T) {
	failed := chunkd.SessionDetail{Session: chunkd.Session{
		State:   chunkd.SessionFailed,
		Factory: &chunkd.FactoryRun{Progress: setupProgress(1)},
	}}

	var lines []string
	rep := newSessionReporter(func(_ iostream.Level, msg string) { lines = append(lines, msg) })
	rep.collapseSetup(time.Now())

	rep.report(failed)
	assert.DeepEqual(t, lines, []string{
		"Preparing sandboxes (implementer + 1 reviewers)...",
		"created sidecar 1 (sc-1)",
		"created sidecar 2 (sc-2)",
		"Syncing workspace /repo...",
		"Synced",
		"created seed snapshot sc-3",
	})
	assert.Assert(t, !rep.settingUp())
}

// followSession's two "lost contact" branches (a refused session, and too
// many poll failures in a row) both go through reportLostContact so a
// failure never hides what a run's setup was holding back: this exercises
// that one shared function directly, the exact code both branches call.
func TestReportLostContactFlushesHeldSetupLines(t *testing.T) {
	var errOut bytes.Buffer
	streams := iostream.Streams{Out: io.Discard, Err: &errOut}

	var lines []string
	rep := newSessionReporter(func(_ iostream.Level, msg string) { lines = append(lines, msg) })
	rep.collapseSetup(time.Now())
	rep.sayProgress(iostream.LevelInfo, "created sidecar 1 (sc-1)")
	rep.sayProgress(iostream.LevelInfo, "Syncing workspace /repo...")
	assert.Equal(t, len(lines), 0, "progress is held while setup is open")

	f := &chunkd.FactoryRun{Worktree: "/data/run-1", Branch: "chunk/factory/run-1", Baseline: "base", Head: "head"}
	reportLostContact(streams, rep, f)

	assert.DeepEqual(t, lines, []string{"created sidecar 1 (sc-1)", "Syncing workspace /repo..."})
	assert.Assert(t, !rep.settingUp())
	assert.Assert(t, strings.Contains(errOut.String(), "/data/run-1"), errOut.String())
}

// Without a terminal, or with --verbose or --json, every setup line is said
// as it arrives, exactly as chunk watch and --log always see it: nothing is
// held back since collapseSetup is never called.
func TestSessionReporterSaysEverySetupLineWithoutCollapsing(t *testing.T) {
	setup := chunkd.SessionDetail{Session: chunkd.Session{Factory: &chunkd.FactoryRun{Progress: setupProgress(3)}}}
	lines := reportedLines(setup)
	assert.DeepEqual(t, lines, []string{
		"Preparing sandboxes (implementer + 3 reviewers)...",
		"created sidecar 1 (sc-1)",
		"created sidecar 2 (sc-2)",
		"Syncing workspace /repo...",
		"Synced",
		"created seed snapshot sc-3",
	})
}
