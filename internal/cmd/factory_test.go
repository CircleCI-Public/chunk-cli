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
	"sync"
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

// reviewResult is claude's JSON result for a review that says prose and has
// no findings.
func reviewResult(prose string) string {
	structured := map[string]any{"review": prose, "findings": []any{}}
	text, _ := json.Marshal(structured)
	raw, _ := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": string(text), "structured_output": structured,
	})
	return string(raw)
}

// addHello is the patch the fake implementer's turn leaves behind.
const addHello = "diff --git a/hello.txt b/hello.txt\nnew file mode 100644\n--- /dev/null\n+++ b/hello.txt\n@@ -0,0 +1 @@\n+hello\n"

// fakeSessionConfig is a daemon setup that boots no sandboxes and calls no API.
// The implementer's first turn adds hello.txt and every review finds nothing,
// so a session passes after one round.
func fakeSessionConfig() watchd.ReviewConfig {
	var next atomic.Int32
	var mu sync.Mutex
	scripts := map[string]string{}
	diffs := 0
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
		DeletePool: func(context.Context, watchd.ReviewPoolSpec) error { return nil },
		Submit: func(_ context.Context, _ *sidecar.PoolEntry, script string, _ map[string]string) (string, error) {
			id := fmt.Sprintf("cmd-%d", next.Add(1))
			mu.Lock()
			scripts[id] = script
			mu.Unlock()
			return id, nil
		},
		Stream: func(_ context.Context, _ *sidecar.PoolEntry, id string, on circleci.OutputFn) (int, error) {
			mu.Lock()
			script := scripts[id]
			out := reviewResult("fine")
			switch {
			case strings.Contains(script, "git diff --binary"):
				out = ""
				if diffs++; diffs == 1 {
					out = addHello
				}
			case strings.Contains(script, "git write-tree"):
				out = strings.Repeat("a", 40) + "\n"
			case strings.Contains(script, "--dangerously-skip-permissions"):
				out = `{"type":"result","is_error":false,"result":"Added hello.txt."}` + "\n"
			}
			mu.Unlock()
			on(circleci.StreamStdout, []byte(out))
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

func runFactoryCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newFactoryCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	cmd.SetContext(t.Context())
	err := cmd.Execute()
	return out.String(), err
}

func TestFactoryDetachThenAttachShowsTheWorkOnItsBranch(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	startSessionDaemon(t, fakeSessionConfig())
	project := sessionProject(t)

	out, err := runFactoryCmd(t, "add hello.txt", "--detach", "--json", "--project", project)
	assert.NilError(t, err)
	var started map[string]string
	assert.NilError(t, json.Unmarshal([]byte(out), &started))
	assert.Assert(t, started["id"] != "")

	out, err = runFactoryCmd(t, "attach", started["id"], "--json")
	assert.NilError(t, err, "a session whose checks pass exits cleanly")
	var detail watchd.SessionDetail
	assert.NilError(t, json.Unmarshal([]byte(out), &detail))
	assert.Equal(t, detail.State, watchd.SessionDone)
	assert.Equal(t, detail.Outcome, watchd.OutcomePassed)
	assert.Equal(t, detail.Task, "add hello.txt")
	assert.Equal(t, len(detail.Rounds[0].Reviews), 2)
	assert.Assert(t, detail.WorkCommit != "")

	show := exec.Command("git", "show", detail.WorkBranch+":hello.txt")
	show.Dir = project
	content, err := show.Output()
	assert.NilError(t, err)
	assert.Equal(t, string(content), "hello\n")

	out, err = runFactoryCmd(t, "list")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, started["id"]), out)
	assert.Assert(t, strings.Contains(out, detail.WorkBranch), out)
}

func TestFactoryCommandsExplainTheirFailures(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	startSessionDaemon(t, fakeSessionConfig())

	_, err := runFactoryCmd(t)
	assert.ErrorContains(t, err, "Pass the task as one argument")

	_, err = runFactoryCmd(t, "cancel", "no-such-session")
	assert.ErrorContains(t, err, "no such session")

	_, err = runFactoryCmd(t, "x", "--detach", "--project", t.TempDir())
	assert.ErrorContains(t, err, "not inside a git repository")

	_, err = runFactoryCmd(t, "x", "--rounds", "11")
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Equal(t, ue.exitCode, ExitBadArgs)

	// A session works on this machine's files, so a remote daemon is refused.
	t.Setenv("CHUNK_WATCHD_REMOTE_ADDR", "127.0.0.1:1")
	_, err = runFactoryCmd(t, "list")
	assert.Assert(t, errors.As(err, &ue))
	assert.Equal(t, ue.exitCode, ExitBadArgs)
}

func TestFactoryWithoutAnOriginRemoteFailsBeforeAnySidecarWork(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	// No daemon is running: the check must come before the daemon is needed.
	project := sessionProject(t)
	c := exec.Command("git", "remote", "remove", "origin")
	c.Dir = project
	out, err := c.CombinedOutput()
	assert.NilError(t, err, string(out))

	_, err = runFactoryCmd(t, "x", "--detach", "--project", project)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), "origin"))
	assert.Assert(t, strings.Contains(ue.Suggestion(), "git remote add origin"))
	assert.Equal(t, ue.exitCode, ExitBadArgs)
}

func TestSessionOutcomeErrorPassesOnlyAPassedSession(t *testing.T) {
	t.Parallel()
	assert.NilError(t, sessionOutcomeError(watchd.Session{State: watchd.SessionDone, Outcome: watchd.OutcomePassed}))
	for _, s := range []watchd.Session{
		{State: watchd.SessionDone, Outcome: watchd.OutcomeExhausted},
		{State: watchd.SessionDone, Outcome: watchd.OutcomeStuck},
		{State: watchd.SessionDone, Outcome: watchd.OutcomeNoChange},
		{State: watchd.SessionFailed, Error: "boom"},
		{State: watchd.SessionCancelled},
	} {
		assert.Assert(t, sessionOutcomeError(s) != nil, "%+v", s)
	}
}
