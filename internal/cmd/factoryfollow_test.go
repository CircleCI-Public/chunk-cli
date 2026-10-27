package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
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

// sessionError is what followSession returns when the daemon is unreachable,
// refuses a session that is already active (409), or cannot find one (404).
// Every case but the last points at `chunk ui`, where the rest of the story
// is.
func TestSessionErrorPointsAtChunkUI(t *testing.T) {
	t.Run("daemon unreachable", func(t *testing.T) {
		var ue *userError
		assert.Assert(t, errors.As(sessionError(chunkd.ErrDaemonUnavailable), &ue))
		assert.Equal(t, ue.Suggestion(), "Start it with 'chunk ui'.")
	})

	t.Run("session already active", func(t *testing.T) {
		refused := &chunkd.SessionRefused{Status: http.StatusConflict, Message: "a run is already active"}
		var ue *userError
		assert.Assert(t, errors.As(sessionError(refused), &ue))
		assert.Equal(t, ue.Suggestion(), "Follow the active one in 'chunk ui'.")
	})

	t.Run("session not found", func(t *testing.T) {
		refused := &chunkd.SessionRefused{Status: http.StatusNotFound, Message: "no such session"}
		var ue *userError
		assert.Assert(t, errors.As(sessionError(refused), &ue))
		assert.Assert(t, !strings.Contains(ue.Suggestion(), "chunk ui"), ue.Suggestion())
	})
}

// The poll-failure path (maxSessionPollFailures in a row) builds its own
// message rather than going through sessionError, but points at the same
// place: stopping the daemon out from under a run in progress must still
// read "chunk ui", not the hidden alias.
func TestFactoryLostContactPointsAtChunkUI(t *testing.T) {
	factoryProject(t)
	started := make(chan struct{})
	cfg := fakeSessionConfig()
	cfg.RunFactory = func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		wt, err := factory.CreateWorktree(ctx, opts.Root, filepath.Join(t.TempDir(), "wt"), "run-1", opts.Prompt)
		if err != nil {
			return factory.Report{}, fmt.Errorf("create the run's worktree: %w", err)
		}
		opts.OnStart("run-1", wt)
		close(started)
		<-ctx.Done()
		return factory.Report{RunID: "run-1", Worktree: wt, Started: true}, ctx.Err()
	}

	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_DAEMON_DIR", dir)

	daemonCtx, stopDaemon := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- server.RunDaemon(daemonCtx, nil, "", nil, nil, server.WithReview(cfg)) }()
	t.Cleanup(func() {
		stopDaemon()
		select {
		case <-errc:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not shut down")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for !chunkd.IsDaemonRunning() {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not become reachable")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Stopping the daemon mid-run, rather than the build command's own
	// context, is what drives followSession into failed polls rather than
	// the clean "cancelled" path TestFactoryInterruptCancelsTheRunAndReportsTheWork
	// covers.
	go func() {
		<-started
		stopDaemon()
	}()

	_, _, buildErr := runFactoryBuildCmd(t, "add a --verbose flag")
	var ue *userError
	assert.Assert(t, errors.As(buildErr, &ue), "got %v", buildErr)
	assert.Equal(t, ue.UserMessage(), "Lost contact with the chunk daemon.")
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk ui"), ue.Suggestion())
}
