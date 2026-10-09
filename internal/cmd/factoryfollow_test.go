package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/chunkd/server"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// fakeSessionConfig is a daemon setup with a Claude credential and nothing
// else. Tests set RunFactory, so no sandbox is booted and no API is called.
func fakeSessionConfig() server.ReviewConfig {
	return server.ReviewConfig{
		Credential: review.Credential{EnvVar: config.EnvAnthropicAPIKey, Value: "sk-test"},
	}
}

// startSessionDaemon runs a real daemon on a Unix socket with a fake backend.
func startSessionDaemon(t *testing.T, cfg server.ReviewConfig) {
	t.Helper()
	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_DAEMON_DIR", dir)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- server.RunDaemon(ctx, nil, "", nil, nil, server.WithReview(cfg)) }()
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
