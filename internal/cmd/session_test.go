package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// fakeSessionConfig is a daemon setup that boots no sandboxes and calls no API:
// every command answers with output.
// reviewResult is claude's JSON result for a review that says prose and has
// no findings. A reviewer that prints anything else has failed.
func reviewResult(prose string) string {
	structured := map[string]any{"review": prose, "findings": []any{}}
	text, _ := json.Marshal(structured)
	raw, _ := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": string(text), "structured_output": structured,
	})
	return string(raw)
}

func fakeSessionConfig(output string) watchd.ReviewConfig {
	var next atomic.Int32
	return watchd.ReviewConfig{
		Credential: review.Credential{EnvVar: config.EnvAnthropicAPIKey, Value: "sk-test"},
		NewPool: func(_ context.Context, spec watchd.ReviewPoolSpec) (*watchd.ReviewPool, error) {
			free := make(chan *sidecar.PoolEntry, spec.Size)
			for i := range spec.Size {
				free <- &sidecar.PoolEntry{ID: fmt.Sprintf("sc-%d", i+1), RepoPath: "/work"}
			}
			return &watchd.ReviewPool{
				Acquire:   func(context.Context) (*sidecar.PoolEntry, error) { return <-free, nil },
				Release:   func(e *sidecar.PoolEntry) { free <- e },
				WaitReady: func(context.Context) error { return nil },
				Close:     func(context.Context) {},
			}, nil
		},
		Submit: func(context.Context, *sidecar.PoolEntry, string, map[string]string) (string, error) {
			return fmt.Sprintf("cmd-%d", next.Add(1)), nil
		},
		Stream: func(_ context.Context, _ *sidecar.PoolEntry, _ string, on circleci.OutputFn) (int, error) {
			on(circleci.StreamStdout, []byte(reviewResult(output)))
			return 0, nil
		},
	}
}

// startSessionDaemon runs a real daemon on a Unix socket with a fake backend.
func startSessionDaemon(t *testing.T, cfg watchd.ReviewConfig) {
	t.Helper()
	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_WATCHD_DIR", dir)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- watchd.RunDaemon(ctx, nil, "", nil, nil, watchd.WithReview(cfg)) }()
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
		if watchd.IsDaemonRunning() {
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

func runSessionCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newSessionCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	cmd.SetContext(t.Context())
	err := cmd.Execute()
	return out.String(), err
}

func TestSessionStartDetachThenAttachShowsTheFinishedSession(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	startSessionDaemon(t, fakeSessionConfig("fine"))
	project := sessionProject(t)

	out, err := runSessionCmd(t, "start", "--detach", "--json", "--project", project)
	assert.NilError(t, err)
	var started map[string]string
	assert.NilError(t, json.Unmarshal([]byte(out), &started))
	assert.Assert(t, started["id"] != "")

	out, err = runSessionCmd(t, "attach", started["id"], "--json")
	assert.NilError(t, err)
	var detail watchd.SessionDetail
	assert.NilError(t, json.Unmarshal([]byte(out), &detail))
	assert.Equal(t, detail.State, watchd.SessionDone)
	assert.Equal(t, len(detail.Rounds[0].Reviews), 2)

	out, err = runSessionCmd(t, "list")
	assert.NilError(t, err)
	assert.Assert(t, bytes.Contains([]byte(out), []byte(started["id"])))
}

func TestSessionCommandsExplainTheirFailures(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	startSessionDaemon(t, fakeSessionConfig("x"))

	_, err := runSessionCmd(t, "cancel", "no-such-session")
	assert.ErrorContains(t, err, "no such session")

	_, err = runSessionCmd(t, "start", "--detach", "--project", t.TempDir())
	assert.ErrorContains(t, err, "not inside a git repository")

	// A session works on this machine's files, so a remote daemon is refused.
	t.Setenv("CHUNK_WATCHD_REMOTE_ADDR", "127.0.0.1:1")
	_, err = runSessionCmd(t, "list")
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Equal(t, ue.exitCode, ExitBadArgs)
}

func TestSessionStartWithoutAnOriginRemoteFailsBeforeAnySandboxWork(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	// No daemon is running: the check must come before the daemon is needed.
	project := sessionProject(t)
	c := exec.Command("git", "remote", "remove", "origin")
	c.Dir = project
	out, err := c.CombinedOutput()
	assert.NilError(t, err, string(out))

	_, err = runSessionCmd(t, "start", "--detach", "--project", project)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), "origin"))
	assert.Assert(t, strings.Contains(ue.Suggestion(), "git remote add origin"))
	assert.Equal(t, ue.exitCode, ExitBadArgs)
}

func TestSessionRestoreAndResumeExplainWhyTheyCannotRun(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	startSessionDaemon(t, fakeSessionConfig("Looks good."))
	project := sessionProject(t)
	out, err := runSessionCmd(t, "start", "--detach", "--json", "--project", project)
	assert.NilError(t, err)
	var started map[string]string
	assert.NilError(t, json.Unmarshal([]byte(out), &started))
	_, err = runSessionCmd(t, "attach", started["id"], "--json")
	assert.NilError(t, err)

	// Nothing was found, so nothing was changed and there is nothing to restore.
	_, err = runSessionCmd(t, "restore", started["id"])
	assert.ErrorContains(t, err, "did not change any files")
	// And a session that is not paused cannot be resumed.
	_, err = runSessionCmd(t, "resume", started["id"])
	assert.ErrorContains(t, err, "not paused")
}
