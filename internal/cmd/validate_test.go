package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// hookPayload is the JSON Claude Code sends to Stop hooks via stdin.
const hookPayload = `{"session_id":"test-session-001","stop_hook_active":false}`

func runValidateHook(t *testing.T, workDir string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(hookPayload))
	root.SetArgs([]string{"validate", "--project", workDir})
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestWriteStopHookMessage(t *testing.T) {
	var out bytes.Buffer
	assert.NilError(t, writeStopHookMessage(&out, "validation completed: 2/2 passed"))

	var response hookResponse
	assert.NilError(t, json.Unmarshal(out.Bytes(), &response))
	assert.Equal(t, response.SystemMessage, "validation completed: 2/2 passed")

	// additionalContext on Stop continues the conversation, so a passing run
	// must never emit it — that is what looped the turn in #577.
	assert.Assert(t, !strings.Contains(out.String(), "additionalContext"),
		"Stop response must not inject model context; got: %s", out.String())
}

// TestValidateHookNoConfigIsSilent pins the unconfigured skip to empty stdout.
// Any response here is a signal to the agent that no check ran and none needs
// reporting, so silence is what lets the turn end.
func TestValidateHookNoConfigIsSilent(t *testing.T) {
	isolateConfig(t)
	stdout, _, err := runValidateHook(t, t.TempDir())
	assert.NilError(t, err)
	assert.Equal(t, stdout, "", "unconfigured hook must write nothing to stdout")
}

func TestValidateHookExitsOneWhenCircleCITokenMissingAndRemoteCommands(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	// Set up a project dir with a remote command. A non-git dir cannot be
	// fingerprinted, and an unusable fingerprint reads as not clean, so the hook
	// won't short-circuit on the clean-tree check.
	dir := t.TempDir()
	projCfg := &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "test", Run: "go test ./...", Remote: true},
		},
	}
	assert.NilError(t, config.SaveProjectConfig(dir, projCfg))

	_, stderr, err := runValidateHook(t, dir)

	assert.Assert(t, err != nil)
	var ec interface{ ExitCode() int }
	assert.Assert(t, errors.As(err, &ec), "expected ExitCode error, got %T: %v", err, err)
	assert.Equal(t, ec.ExitCode(), 1)
	assert.Assert(t, strings.Contains(stderr, "CircleCI auth is not configured"),
		"expected auth message in stderr, got: %q", stderr)
	assert.Assert(t, strings.Contains(stderr, "chunk auth login"),
		"expected auth hint in stderr, got: %q", stderr)
}

func TestValidateHookExitsOneWhenCircleCITokenMissingAndSidecarImage(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	dir := t.TempDir()
	projCfg := &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "test", Run: "npm test", Role: config.RoleGate},
		},
		Validation: &config.ValidationConfig{
			SidecarImage: "my-snapshot-abc123",
		},
	}
	assert.NilError(t, config.SaveProjectConfig(dir, projCfg))

	_, stderr, err := runValidateHook(t, dir)

	assert.Assert(t, err != nil)
	var ec interface{ ExitCode() int }
	assert.Assert(t, errors.As(err, &ec), "expected ExitCode error, got %T: %v", err, err)
	assert.Equal(t, ec.ExitCode(), 1)
	assert.Assert(t, strings.Contains(stderr, "CircleCI auth is not configured"),
		"expected auth message in stderr, got: %q", stderr)
}

// TestValidateHookRequiresAuthByDefault verifies that hook invocations always
// require CircleCI auth, even when no commands are explicitly marked remote.
func TestValidateHookRequiresAuthByDefault(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	dir := t.TempDir()
	projCfg := &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "lint", Run: "echo ok", Remote: false},
		},
	}
	assert.NilError(t, config.SaveProjectConfig(dir, projCfg))

	_, stderr, err := runValidateHook(t, dir)

	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(stderr, "CircleCI auth is not configured"),
		"auth check must fire because remote is the default, stderr: %q", stderr)
}

func TestRemoteExecEnv(t *testing.T) {
	t.Run("returns nil when token is empty", func(t *testing.T) {
		assert.Assert(t, remoteExecEnv("", nil) == nil)
	})

	t.Run("forwards token as CIRCLE_TOKEN", func(t *testing.T) {
		env := remoteExecEnv("abc123", nil)
		assert.Equal(t, env[config.EnvCircleToken], "abc123")
		_, hasAlias := env[config.EnvCircleCIToken]
		assert.Assert(t, !hasAlias)
	})

	t.Run("merges explicit env vars", func(t *testing.T) {
		env := remoteExecEnv("abc123", map[string]string{"FOO": "bar"})
		assert.Equal(t, env[config.EnvCircleToken], "abc123")
		assert.Equal(t, env["FOO"], "bar")
	})
}

func TestRunValidationPlanLocalOnlyDoesNotRequirePool(t *testing.T) {
	workDir := t.TempDir()
	plan := validate.Plan{
		LocalCommands: []config.Command{{Name: "test", Run: "printf ran > result"}},
	}

	result, err := runValidationPlan(
		context.Background(), nil, plan, config.ResolvedConfig{}, workDir, workDir, nil, nil,
		func(iostream.Level, string) {}, iostream.Streams{Out: io.Discard, Err: io.Discard},
	)

	assert.NilError(t, err)
	assert.Equal(t, result.Passed, 1)
	assert.Equal(t, result.Total, 1)
	data, err := os.ReadFile(filepath.Join(workDir, "result"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "ran")
}

func TestRunValidationPlanRemoteRequiresPool(t *testing.T) {
	plan := validate.Plan{
		RemoteCommands: []config.Command{{Name: "test", Run: "true"}},
		PoolSize:       1,
	}

	result, err := runValidationPlan(
		context.Background(), nil, plan, config.ResolvedConfig{}, t.TempDir(), t.TempDir(), nil, nil,
		func(iostream.Level, string) {}, iostream.Streams{Out: io.Discard, Err: io.Discard},
	)

	assert.Equal(t, result, validate.Result{})
	assert.ErrorContains(t, err, "requires a sidecar pool")
}

func TestValidationRepoPath(t *testing.T) {
	active := &sidecar.ActiveSidecar{Workspace: "/saved/workspace"}
	assert.Equal(t, validationRepoPath("/explicit/workspace", active), "/explicit/workspace")
	assert.Equal(t, validationRepoPath("", active), "/saved/workspace")
	assert.Equal(t, validationRepoPath("", nil), "")
}

func TestOpenAPIExecPassesEnvVars(t *testing.T) {
	isolateConfig(t)

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	// Point the daemon socket at an empty dir. Without this the command
	// registration would reach a watch daemon actually running on the developer's
	// machine, which is neither hermetic nor polite.
	t.Setenv("CHUNK_WATCHD_DIR", t.TempDir())

	envVars := map[string]string{"FOO": "bar", "BAZ": "qux"}
	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	target := sidecar.Target{Client: client, SidecarID: "sidecar-123", Workdir: "/workspace"}
	execFn, _, err := target.ExecRunner(context.Background(), ".", remoteExecEnv("", envVars), streams)
	assert.NilError(t, err)

	_, _, _, err = execFn(context.Background(), "echo hello")
	assert.NilError(t, err)

	// Find the exec request and verify env vars were included in the body.
	var execReq struct {
		Env map[string]string `json:"env"`
	}
	for _, req := range cci.Recorder.AllRequests() {
		if strings.Contains(req.URL.Path, "/exec") {
			assert.NilError(t, json.NewDecoder(bytes.NewReader(req.Body)).Decode(&execReq))
			break
		}
	}
	assert.Equal(t, execReq.Env["FOO"], "bar")
	assert.Equal(t, execReq.Env["BAZ"], "qux")
}

func TestValidateNoConfigShowsSkillHint(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()

	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"validate", "--project", dir})
	err := root.Execute()

	assert.Assert(t, err != nil, "expected error when no validate commands configured")
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "expected userError, got %T: %v", err, err)
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk init"),
		"expected suggestion to mention 'chunk init', got: %q", ue.Suggestion())
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk-sidecar"),
		"expected suggestion to mention chunk-sidecar skill, got: %q", ue.Suggestion())
}

// TestValidateDefaultsToRemote confirms that running without --local always
// attempts remote execution, even when no commands are marked Remote:true.
// Without a valid CircleCI token the attempt fails with an auth error — proving
// it never silently fell back to running the commands locally.
func TestValidateDefaultsToRemote(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "test", Run: "echo should-not-run-locally"},
		},
	}))

	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"validate", "--project", dir})
	err := root.Execute()

	assert.Assert(t, err != nil, "expected error: remote should be attempted and fail without a token")
	combined := outBuf.String() + errBuf.String()
	assert.Assert(t, !strings.Contains(combined, "should-not-run-locally"),
		"command must not have run locally, got: %q", combined)
}

// TestValidateLocalFlagRunsLocally confirms that --local executes commands in
// the local process without touching a sidecar.
func TestValidateLocalFlagRunsLocally(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "test", Run: "echo ran-locally"},
		},
	}))

	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"validate", "--local", "--project", dir})
	err := root.Execute()

	assert.NilError(t, err)
	combined := outBuf.String() + errBuf.String()
	assert.Assert(t, strings.Contains(combined, "ran-locally"),
		"--local must execute commands in the local process, got: %q", combined)
}

// TestValidateLocalFlagOverridesRemoteConfig confirms that --local wins over
// Remote:true in config — the flag is the explicit opt-out from remote-first.
func TestValidateLocalFlagOverridesRemoteConfig(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "test", Run: "echo ran-locally", Remote: true},
		},
	}))

	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"validate", "--local", "--project", dir})
	err := root.Execute()

	assert.NilError(t, err, "--local must bypass Remote:true config and run locally")
	combined := outBuf.String() + errBuf.String()
	assert.Assert(t, strings.Contains(combined, "ran-locally"),
		"--local must execute commands locally even when Remote:true, got: %q", combined)
}

// A run with no sidecar is the only record of itself: nothing is streamed to the
// watch daemon, which reads this same on-disk log. Registering the project is
// what tells the daemon the log exists at all, so a run that skips it leaves
// results nothing will ever show.
func TestValidateLocalRunRegistersProjectForTheDaemon(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{{Name: "test", Run: "echo ran-locally"}},
	}))

	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"validate", "--local", "--project", dir})
	assert.NilError(t, root.Execute())

	// Canonical, because that is what registration writes and the daemon keys on:
	// a darwin temp dir arrives here as /var/... and resolves to /private/var/...
	canonical, err := filepath.EvalSymlinks(dir)
	assert.NilError(t, err)
	roots, err := sidecar.AllProjectRoots()
	assert.NilError(t, err)
	assert.Assert(t, slices.Contains(roots, canonical),
		"a local run must register the project it logged to, got: %v", roots)

	// And the run it recorded there closes with a tally, so a daemon that starts
	// afterwards has a result to show rather than an open-ended run.
	dataDir, err := config.ProjectDataDir(dir)
	assert.NilError(t, err)
	log, err := eventlog.Open(dataDir)
	assert.NilError(t, err)
	events, err := log.Recent(10)
	assert.NilError(t, err)
	assert.Assert(t, len(events) > 0, "the run recorded no events")
	passed, total, ok := events[len(events)-1].Outcome()
	assert.Assert(t, ok, "last event does not close the run: %+v", events[len(events)-1])
	assert.Equal(t, passed, 1)
	assert.Equal(t, total, 1)
}

func TestPlanValidationRemoteFlagOverridesLocalConfig(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "format", Run: "task fmt", Local: true},
	}}

	plan := planValidationExecution(cfg, &validateOpts{remote: true}, "")

	assert.Equal(t, len(plan.LocalCommands), 0)
	assert.DeepEqual(t, plan.RemoteCommands, cfg.Commands)
	assert.Equal(t, plan.PoolSize, 1)
}

func TestValidateRejectsRemoteAndLocalFlagsTogether(t *testing.T) {
	root := newTestRootCmd()
	root.SetArgs([]string{"validate", "--remote", "--local", "--cmd", "true", "--dry-run"})

	err := root.Execute()

	assert.ErrorContains(t, err, "if any flags in the group")
}

func TestValidateExplicitLocalCommandNeedsNoSidecar(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{{Name: "format", Run: "echo ran-locally", Local: true}},
	}))

	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"validate", "--project", dir})
	err := root.Execute()

	assert.NilError(t, err)
	combined := outBuf.String() + errBuf.String()
	assert.Assert(t, strings.Contains(combined, "ran-locally"), "explicit local command did not run locally: %q", combined)
}

func TestValidateRejectsConflictingCommandPlacement(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	chunkDir := filepath.Join(dir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(`{"commands":[{"name":"test","run":"true","local":true,"remote":true}]}`), 0o644))

	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"validate", "--project", dir})
	err := root.Execute()

	assert.Assert(t, err != nil)
	assert.ErrorContains(t, err, `command "test" cannot be both local and remote`)
}

func TestValidateEnvFlagBadValue(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()

	// Write a minimal project config so validate doesn't fail with "no commands".
	cfgDir := filepath.Join(dir, ".chunk")
	assert.NilError(t, os.MkdirAll(cfgDir, 0o755))
	assert.NilError(t, os.WriteFile(
		filepath.Join(cfgDir, "config.json"),
		[]byte(`{"commands":[{"name":"test","run":"true"}]}`),
		0o644,
	))

	cmd := newValidateCmd()
	cmd.SetOut(os.Stderr)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"--project", dir, "--env", "BADVALUE"})

	err := cmd.Execute()
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "BADVALUE"), "got: %v", err)
}

// activeStopHookPayload is a re-signalled Stop hook: stop_hook_active is true,
// so initHook leaves the failure counter alone and the counter's fate is decided
// by how the run itself ends.
const activeStopHookPayload = `{"session_id":"test-session-001","stop_hook_active":true}`

// skipMsg is the one line that tells the agent no commands ran.
const skipMsg = "skipped (no changes since last successful run)"

// runActiveStopHook fires a re-signalled Stop hook against dir and returns what
// the agent would see, along with the exit error. Unlike runValidateHook it does
// not assert on the error, so failing runs can be exercised too.
// --local is passed so the tests focus on hook caching semantics rather than
// remote routing — caching is orthogonal to where commands run.
func runActiveStopHookOutput(t *testing.T, dir string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(activeStopHookPayload))
	root.SetArgs([]string{"validate", "--local", "--project", dir})
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func runActiveStopHook(t *testing.T, dir string) (stderr string, err error) {
	t.Helper()
	_, stderr, err = runActiveStopHookOutput(t, dir)
	return stderr, err
}

// countingCommand returns a command that appends a line to a marker file each
// time it runs and then exits with code, plus a func reporting how many times it
// has run. The marker lives outside the repo on purpose: written inside it, every
// run would change the working-tree digest and a re-run would prove nothing about
// the cache.
func countingCommand(t *testing.T, code int) (run string, runs func() int) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "runs")
	return fmt.Sprintf("echo x >> %s; exit %d", marker, code), func() int {
		t.Helper()
		data, err := os.ReadFile(marker)
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		assert.NilError(t, err)
		return len(strings.Fields(string(data)))
	}
}

// hookProject sets up a git repo with one configured command. Saving the config
// leaves .chunk/ untracked, so the tree is never clean and the hook reaches the
// cache instead of short-circuiting on the clean-tree check.
func hookProject(t *testing.T, run string) string {
	t.Helper()
	// The attempt counter lives under os.TempDir(); isolate it from other tests
	// sharing this session ID.
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	gitSetup(t, dir, "main")
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{{Name: "test", Run: run}},
	}))
	return dir
}

// TestValidateHookCacheHitResetsAttempts covers a Stop hook firing again on a
// tree that already validated. The commands are skipped, and because a cache hit
// is a success it must clear the failure counter — otherwise a stale count
// brings the "ask the user for guidance" bail-out forward by a turn.
func TestValidateHookCacheHitResetsAttempts(t *testing.T) {
	isolateConfig(t)
	const sessionID = "test-session-001"
	dir := hookProject(t, "exit 0")

	run := func() string {
		t.Helper()
		stderr, err := runActiveStopHook(t, dir)
		assert.NilError(t, err)
		return stderr
	}

	first := run()
	assert.Assert(t, !strings.Contains(first, skipMsg), "first run must execute, got: %q", first)

	// A failure at some other tree state leaves a count of 1 behind.
	assert.Equal(t, validate.TrackFailedAttempt(sessionID, nil), 1)

	stdout, second, err := runActiveStopHookOutput(t, dir)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(second, skipMsg), "second run must hit the cache, got: %q", second)
	var response hookResponse
	assert.NilError(t, json.Unmarshal([]byte(stdout), &response))
	assert.Equal(t, response.SystemMessage, "chunk validate: skipped, nothing changed since the last pass",
		"cache hit must return a successful hook response that says nothing ran")

	// The hit cleared the counter, so the next failure is attempt 1 again.
	assert.Equal(t, validate.TrackFailedAttempt(sessionID, nil), 1)
}

// TestValidateHookFailureIsNotCached is the guarantee the whole cache rests on.
// If a failing run were ever stored, every later hook invocation on the same tree
// would print "skipped" and return nil, so the agent would stop with the build
// broken — and nothing else in the suite would notice.
func TestValidateHookFailureIsNotCached(t *testing.T) {
	isolateConfig(t)
	run, runs := countingCommand(t, 1)
	dir := hookProject(t, run)

	_, err := runActiveStopHook(t, dir)
	assert.Assert(t, err != nil, "a failing command must fail the hook")
	assert.Equal(t, runs(), 1)

	second, err := runActiveStopHook(t, dir)
	assert.Assert(t, err != nil, "the second run must fail too, not report a hit")
	assert.Assert(t, !strings.Contains(second, skipMsg),
		"a failed run must not be cached, got: %q", second)
	assert.Equal(t, runs(), 2, "the commands must run again after a failure")
}

// TestValidateHookCacheMissAfterEdit is the other half of the contract: a hit is
// only correct while the tree is untouched, so an edit between runs has to reach
// the key and put the commands back on.
func TestValidateHookCacheMissAfterEdit(t *testing.T) {
	isolateConfig(t)
	run, runs := countingCommand(t, 0)
	dir := hookProject(t, run)

	_, err := runActiveStopHook(t, dir)
	assert.NilError(t, err)
	assert.Equal(t, runs(), 1)

	second, err := runActiveStopHook(t, dir)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(second, skipMsg), "unchanged tree must hit, got: %q", second)
	assert.Equal(t, runs(), 1, "a cache hit must not execute the commands")

	assert.NilError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))

	third, err := runActiveStopHook(t, dir)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(third, skipMsg),
		"an edited tree must miss the cache, got: %q", third)
	assert.Equal(t, runs(), 2, "the commands must run again after an edit")
}

// --- hookResultCache ---

// hookTree stands in for the fingerprint the hook computes once per run; gitutil
// owns the tests that prove a digest tracks the working tree.
var hookTree = gitutil.Worktree{Head: "abc123", Digest: "deadbeef"}

// hookResultCache is the boundary between "these runs are cacheable" and
// "these are not"; each guard below must return no cache so the run always
// executes.
func TestHookResultCacheDisabledCases(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{{Name: "test", Run: "go test ./..."}}}
	hook := &hookContext{sessionID: "s1"}

	tests := []struct {
		name      string
		hook      *hookContext
		inlineCmd string
		tree      gitutil.Worktree
	}{
		{name: "not a hook run", hook: nil, tree: hookTree},
		{name: "inline command", hook: hook, inlineCmd: "go test ./foo", tree: hookTree},
		{name: "unusable git state", hook: hook, tree: gitutil.Worktree{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache, key := hookResultCache(tt.hook, tt.inlineCmd, t.TempDir(), tt.tree, "", cfg, "")
			assert.Assert(t, cache == nil, "expected no cache")
			assert.Equal(t, key, "")
		})
	}
}

func TestHookResultCacheEnabledForNamedCommand(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{{Name: "test", Run: "go test ./..."}}}

	cache, key := hookResultCache(&hookContext{sessionID: "s1"}, "", t.TempDir(), hookTree, "test", cfg, "")
	assert.Assert(t, cache != nil, "expected a cache for a named command in hook mode")
	assert.Assert(t, key != "")
}

// TestHookResultCacheTargetAffectsKey pins the wiring: a different execution
// target has to reach the key, or a run validated on one sidecar reports a hit
// for another.
func TestHookResultCacheTargetAffectsKey(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.ProjectConfig{Commands: []config.Command{{Name: "test", Run: "go test ./..."}}}
	hook := &hookContext{sessionID: "s1"}

	_, local := hookResultCache(hook, "", dir, hookTree, "test", cfg, "")
	_, remote := hookResultCache(hook, "", dir, hookTree, "test", cfg, "sidecar-a\x00")
	assert.Assert(t, local != remote, "target must participate in the cache key")
}

// --- execTarget ---

func TestExecTarget(t *testing.T) {
	withImage := &config.ProjectConfig{Validation: &config.ValidationConfig{SidecarImage: "snap-1"}}

	tests := []struct {
		name   string
		opts   *validateOpts
		cfg    *config.ProjectConfig
		active *sidecar.ActiveSidecar
		want   string
	}{
		{name: "local run", opts: &validateOpts{}, cfg: &config.ProjectConfig{}, want: ""},
		{
			name: "explicit sidecar id",
			opts: &validateOpts{sidecarID: "sc-1"},
			cfg:  &config.ProjectConfig{},
			want: "sc-1\x00",
		},
		{
			name:   "active sidecar",
			opts:   &validateOpts{},
			cfg:    &config.ProjectConfig{},
			active: &sidecar.ActiveSidecar{SidecarIDs: []string{"sc-2"}},
			want:   "sc-2\x00",
		},
		{
			name:   "explicit id wins over active",
			opts:   &validateOpts{sidecarID: "sc-1"},
			cfg:    &config.ProjectConfig{},
			active: &sidecar.ActiveSidecar{SidecarIDs: []string{"sc-2"}},
			want:   "sc-1\x00",
		},
		{
			name: "configured image with no sidecar yet",
			opts: &validateOpts{},
			cfg:  withImage,
			want: "\x00snap-1",
		},
		{
			name:   "image and active sidecar",
			opts:   &validateOpts{},
			cfg:    withImage,
			active: &sidecar.ActiveSidecar{SidecarIDs: []string{"sc-2"}},
			want:   "sc-2\x00snap-1",
		},
		{name: "nil config", opts: &validateOpts{}, cfg: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, execTarget(tt.opts, tt.cfg, tt.active), tt.want)
		})
	}
}

// gitSetup initialises a minimal git repo at dir on the given branch name.
func gitSetup(t *testing.T, dir, branch string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", branch)
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	_ = os.WriteFile(filepath.Join(dir, "README"), []byte("init"), 0o644)
	run("add", ".")
	run("commit", "-m", "init")
}

func hashFor(sessionID, branch string) string {
	sum := sha256.Sum256([]byte(sessionID + ":" + branch))
	return fmt.Sprintf("%x", sum[:4])
}

// Tests with a session ID: branch must be hashed, never appear raw.

func TestSidecarAutoNameWithSessionAndBranch(t *testing.T) {
	dir := t.TempDir()
	gitSetup(t, dir, "main")
	ctx := session.WithID(context.Background(), "sess-1")
	got := sidecarAutoName(ctx, dir)
	want := filepath.Base(dir) + "-sess-1-" + hashFor("sess-1", "main")
	assert.Equal(t, got, want)
}

func TestSidecarAutoNameWithSessionBranchWithSlashes(t *testing.T) {
	dir := t.TempDir()
	gitSetup(t, dir, "main")
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("checkout", "-b", "feature/my-branch")
	ctx := session.WithID(context.Background(), "sess-2")
	got := sidecarAutoName(ctx, dir)
	want := filepath.Base(dir) + "-sess-2-" + hashFor("sess-2", "feature/my-branch")
	assert.Equal(t, got, want)
	assert.Assert(t, !strings.Contains(got, "feature"), "raw branch must not appear in name, got %q", got)
	assert.Assert(t, !strings.Contains(got, "my-branch"), "raw branch must not appear in name, got %q", got)
}

func TestSidecarAutoNameWithSessionNoBranch(t *testing.T) {
	dir := t.TempDir()
	// No git repo → no branch.
	ctx := session.WithID(context.Background(), "sess-3")
	got := sidecarAutoName(ctx, dir)
	assert.Equal(t, got, filepath.Base(dir)+"-sess-3")
}

func TestSidecarAutoNameDifferentBranchesDifferentNames(t *testing.T) {
	dir := t.TempDir()
	gitSetup(t, dir, "main")
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	ctx := session.WithID(context.Background(), "sess-x")
	n1 := sidecarAutoName(ctx, dir)
	run("checkout", "-b", "other-branch")
	n2 := sidecarAutoName(ctx, dir)
	assert.Assert(t, n1 != n2, "different branches must produce different names: %q vs %q", n1, n2)
}

// Tests without a session ID: legacy sanitised-branch fallback.

func TestSidecarAutoNameNoSessionBranchPresent(t *testing.T) {
	dir := t.TempDir()
	gitSetup(t, dir, "main")
	got := sidecarAutoName(context.Background(), dir)
	assert.Equal(t, got, filepath.Base(dir)+"-main-validate")
}

func TestSidecarAutoNameNoSessionBranchAbsent(t *testing.T) {
	dir := t.TempDir()
	// No git repo → falls back to old format.
	got := sidecarAutoName(context.Background(), dir)
	assert.Equal(t, got, filepath.Base(dir)+"-validate")
}

func TestSidecarAutoNameNoSessionBranchWithSlashes(t *testing.T) {
	dir := t.TempDir()
	gitSetup(t, dir, "main")
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("checkout", "-b", "feature/my-branch")
	got := sidecarAutoName(context.Background(), dir)
	assert.Equal(t, got, filepath.Base(dir)+"-feature-my-branch-validate")
}

func TestSidecarAutoNameNoSessionLongBranch(t *testing.T) {
	dir := t.TempDir()
	long := "abcdefghijklmnopqrstuvwxyz012345" // 32 chars
	gitSetup(t, dir, "main")
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("checkout", "-b", long)
	got := sidecarAutoName(context.Background(), dir)
	// branch truncated to 30 chars
	assert.Equal(t, got, filepath.Base(dir)+"-"+long[:30]+"-validate")
}

// runMarkRemoteCLI runs "validate --mark-remote" against workDir.
func runValidateListCLI(t *testing.T, workDir string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"validate", "--list", "--project", workDir})
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func runMarkRemoteCLI(t *testing.T, workDir string, extraArgs ...string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"validate", "--mark-remote", "--project", workDir}, extraArgs...))
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestValidateMarkRemoteNamedCommand(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "install", Run: "go mod download"},
			{Name: "test", Run: "go test ./..."},
		},
	}))

	_, stderr, err := runMarkRemoteCLI(t, dir, "test")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(stderr, "test"), "got: %q", stderr)

	cfg, err := config.LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Assert(t, cfg.FindCommand("test").Remote)
	assert.Assert(t, !cfg.FindCommand("install").Remote)
}

func TestValidateMarkRemoteAllCommands(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "install", Run: "go mod download"},
			{Name: "lint", Run: "task lint"},
		},
	}))

	_, _, err := runMarkRemoteCLI(t, dir)
	assert.NilError(t, err)

	cfg, err := config.LoadProjectConfig(dir)
	assert.NilError(t, err)
	for _, c := range cfg.Commands {
		assert.Assert(t, c.Remote, c.Name)
	}
}

func TestValidateMarkRemoteUnknownCommand(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{{Name: "test", Run: "go test ./..."}},
	}))

	_, _, err := runMarkRemoteCLI(t, dir, "nope")
	assert.Assert(t, err != nil)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "expected userError, got %T", err)
	assert.Assert(t, strings.Contains(ue.Suggestion(), "--list"), "got: %q", ue.Suggestion())

	// A miss must not rewrite the file.
	cfg, err := config.LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Assert(t, !cfg.FindCommand("test").Remote)
}

func TestValidateMarkRemoteNoCommandsConfigured(t *testing.T) {
	isolateConfig(t)
	_, _, err := runMarkRemoteCLI(t, t.TempDir())
	assert.Assert(t, err != nil)
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "expected userError, got %T", err)
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk init"), "got: %q", ue.Suggestion())
}

// A malformed config must not be overwritten: the commands it holds are
// invisible, so marking one would discard the rest.
func TestValidateMarkRemoteRefusesMalformedConfig(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	chunkDir := filepath.Join(dir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	original := `{"commands": [{"name": "test", "run": "task test"}`
	path := filepath.Join(chunkDir, "config.json")
	assert.NilError(t, os.WriteFile(path, []byte(original), 0o644))

	_, _, err := runMarkRemoteCLI(t, dir, "test")
	assert.Assert(t, err != nil)

	data, readErr := os.ReadFile(path)
	assert.NilError(t, readErr)
	assert.Equal(t, string(data), original)
}

// The unnamed sweep must leave formatters local: on the sidecar they rewrite
// files that never come back to the working tree. The skills tell agents to run
// the bare form, so this is the path that has to be safe.
func TestValidateMarkRemoteSkipsAutofix(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "test", Run: "task test", Role: config.RoleGate},
			{Name: "format", Run: "task fmt", Role: config.RoleAutofix, Local: true},
		},
	}))

	_, stderr, err := runMarkRemoteCLI(t, dir)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(stderr, "left local"), "skip should be reported: %q", stderr)
	assert.Assert(t, strings.Contains(stderr, "format"), "got: %q", stderr)

	cfg, err := config.LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Assert(t, cfg.FindCommand("test").Remote)
	assert.Assert(t, !cfg.FindCommand("format").Remote)

	// Naming it overrides the skip.
	_, _, err = runMarkRemoteCLI(t, dir, "format")
	assert.NilError(t, err)
	cfg, err = config.LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Assert(t, cfg.FindCommand("format").Remote)
	assert.Assert(t, !cfg.FindCommand("format").Local)
}

func TestValidateMarkRemoteDoesNotCallUnspecifiedAutofixLocal(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{{Name: "format", Run: "task fmt", Role: config.RoleAutofix}},
	}))

	_, stderr, err := runMarkRemoteCLI(t, dir)

	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(stderr, "left local"), "unspecified commands default remote: %q", stderr)
}

// --list has to show what the skills tell agents to inspect before marking.
func TestValidateListShowsRoutingAndRole(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "test", Run: "task test", Role: config.RoleGate, Remote: true},
			{Name: "format", Run: "task fmt", Role: config.RoleAutofix, Local: true},
			{Name: "bare", Run: "echo hi"},
		},
	}))

	stdout, stderr, err := runValidateListCLI(t, dir)
	assert.NilError(t, err)
	out := stdout + stderr
	for _, want := range []string{"test [remote, gate]", "format [local, autofix]", "bare [remote]"} {
		assert.Assert(t, strings.Contains(out, want), "missing %q in:\n%s", want, out)
	}
}

func TestFailBeforeRunClosesTheRun(t *testing.T) {
	dir := t.TempDir()
	log, err := eventlog.Open(dir)
	assert.NilError(t, err)

	var reported string
	rec := log.Recorder(func(_ iostream.Level, msg string) { reported = msg }, eventlog.OpValidate, "", "", "")

	inErr := errors.New("bundle sync: agent: failed to sign challenge")
	assert.Equal(t, failBeforeRun(rec, time.Now(), inErr), inErr)

	events, err := log.Recent(10)
	assert.NilError(t, err)
	assert.Equal(t, len(events), 1)
	assert.Equal(t, events[0].Level, "error")
	assert.Assert(t, strings.Contains(events[0].Msg, "setup failed"), "got %q", events[0].Msg)
	assert.Assert(t, strings.Contains(events[0].Msg, inErr.Error()), "got %q", events[0].Msg)
	assert.Equal(t, reported, events[0].Msg)

	// Nothing ran, so the run closes on a 0/0 tally.
	passed, total, ok := events[0].Outcome()
	assert.Assert(t, ok)
	assert.Equal(t, passed, 0)
	assert.Equal(t, total, 0)
}

// A released run reports where the answer will come from and nothing else:
// there is no verdict yet, so claiming one either way would be a lie.
func TestReportDelegatedValidateAnnouncesABackgroundRun(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	streams := iostream.Streams{Out: &outBuf, Err: &errBuf}

	err := reportDelegatedValidate(watchd.ValidateResponse{
		TaskID: "0192cf6e-1b9f-7c3e-8a11-2b3c4d5e6f70",
		Reason: "small change, 42 lines",
		// Ignored: a released run has not produced these, and a daemon that
		// sends them anyway must not have them read as a result.
		ExitCode: 1,
		Stderr:   "should not be shown",
	}, nil, streams)

	assert.NilError(t, err)
	assert.Equal(t, outBuf.String(), "")
	assert.Assert(t, strings.Contains(errBuf.String(), "validating in the background"), "got %q", errBuf.String())
	assert.Assert(t, strings.Contains(errBuf.String(), "0192cf6e"), "the task ID was not reported: %q", errBuf.String())
	assert.Assert(t, strings.Contains(errBuf.String(), "small change, 42 lines"), "got %q", errBuf.String())
	assert.Assert(t, !strings.Contains(errBuf.String(), "should not be shown"), "output of a run that never happened was printed")
}

// A run that was offered to the background and held says why, then behaves
// exactly as a delegated run always has.
func TestReportDelegatedValidateExplainsAHeldRun(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	streams := iostream.Streams{Out: &outBuf, Err: &errBuf}

	err := reportDelegatedValidate(watchd.ValidateResponse{
		Reason:   "large change, 912 lines, over the 500-line limit",
		ExitCode: 2,
		Stdout:   "on stdout",
		Stderr:   "on stderr",
	}, nil, streams)

	var silent *silentExitError
	assert.Assert(t, errors.As(err, &silent), "a failing run must carry its exit code, got %v", err)
	assert.Equal(t, silent.code, 2)
	assert.Equal(t, outBuf.String(), "on stdout")
	assert.Assert(t, strings.Contains(errBuf.String(), "validating now: large change"), "got %q", errBuf.String())
	assert.Assert(t, strings.Contains(errBuf.String(), "on stderr"), "got %q", errBuf.String())
}

// A caller that never offered to be released is told nothing extra.
func TestReportDelegatedValidateSaysNothingWhenThereWasNoDecision(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	streams := iostream.Streams{Out: &outBuf, Err: &errBuf}

	assert.NilError(t, reportDelegatedValidate(watchd.ValidateResponse{Stderr: "1/1 passed"}, nil, streams))
	assert.Equal(t, errBuf.String(), "1/1 passed")
}

// The commit gate and the end-of-turn hook arrive here looking alike, and only
// one of them can be released: a released commit gate is a commit that went
// through without the checks it exists to run.
func TestOnlyTheStopHookMayRunInBackground(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    bool
	}{
		{`{"session_id":"s","hook_event_name":"Stop"}`, true},
		{`{"session_id":"s","hook_event_name":"PreToolUse"}`, false},
		{`{"session_id":"s","hook_event_name":"UserPromptSubmit"}`, false},
		// No event name: an older Claude Code, or another agent's hook runner.
		// An unrecognised hook is not evidence of one that can wait.
		{`{"session_id":"s"}`, false},
	} {
		hook := detectHook(strings.NewReader(tc.payload))
		assert.Assert(t, hook != nil, "payload was not read as a hook: %s", tc.payload)
		assert.Equal(t, mayRunInBackground(hook), tc.want, "payload: %s", tc.payload)
	}

	// Not a hook at all — a developer at a terminal, with no next turn to hear
	// the answer on.
	assert.Equal(t, mayRunInBackground(nil), false)
}

func TestDetectHookReadsTheEventName(t *testing.T) {
	hook := detectHook(strings.NewReader(`{"session_id":"abc","stop_hook_active":true,"hook_event_name":"Stop"}`))
	assert.Assert(t, hook != nil)
	assert.Equal(t, hook.sessionID, "abc")
	assert.Equal(t, hook.stopHookActive, true)
	assert.Equal(t, hook.event, "Stop")
}

// A low-risk change gets no score line: it is the common case, and a number
// printed every turn is a number nobody reads. Advice is printed whenever
// there is any.
func TestReportDelegatedValidatePrintsRiskOnlyWhenItMatters(t *testing.T) {
	var quiet bytes.Buffer
	assert.NilError(t, reportDelegatedValidate(watchd.ValidateResponse{
		Risk: &watchd.RiskSummary{Score: 12, Band: watchd.BandLow, Parts: []string{"12 lines (1)"}},
	}, nil, iostream.Streams{Out: &quiet, Err: &quiet}))
	assert.Equal(t, quiet.String(), "", "a low-risk change was narrated")

	var loud bytes.Buffer
	err := reportDelegatedValidate(watchd.ValidateResponse{
		Reason:   "large change, 2000 lines, over the 500-line limit",
		ExitCode: 1,
		Risk: &watchd.RiskSummary{
			Score:  92,
			Band:   watchd.BandHigh,
			Parts:  []string{"2000 lines (60)", "3 files (6)"},
			Advice: "committing it in parts would get each piece checked sooner",
		},
	}, nil, iostream.Streams{Out: &loud, Err: &loud})

	assert.Assert(t, err != nil)
	out := loud.String()
	assert.Assert(t, strings.Contains(out, "risk 92/100 high"), "got %q", out)
	assert.Assert(t, strings.Contains(out, "2000 lines (60)"), "got %q", out)
	assert.Assert(t, strings.Contains(out, "committing it in parts"), "got %q", out)
}

// A snapshot run validates a tree that was checked out from the state being
// validated, so git sees nothing changed in it. The clean-tree skip must not
// fire there: skipping would run no commands and report a pass, which is a
// green light for code nothing looked at.
//
// This is the shape of a real bug. The unit tests around the daemon stub the
// runner, so the skip lived below all of them and the first honest end-to-end
// run reported "passed" having executed nothing.
func TestASnapshotRunIsNotSkippedForBeingClean(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	// A clean repo: everything committed, exactly as a checked-out snapshot is.
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{{Name: "test", Run: "touch " + marker}},
	}))
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "t@t.co"}, {"config", "user.name", "t"},
		{"add", "-A"}, {"commit", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, "git %v: %s", args, out)
	}

	run := func(extra ...string) {
		var outBuf, errBuf bytes.Buffer
		root := newTestRootCmd()
		root.SetOut(&outBuf)
		root.SetErr(&errBuf)
		root.SetIn(strings.NewReader(hookPayload))
		root.SetArgs(append([]string{"--insecure-storage", "validate", "--local", "--project", dir}, extra...))
		_ = root.Execute()
	}

	// Without the attribution, a clean tree is skipped — the behaviour every
	// ordinary hook run relies on.
	run()
	_, err := os.Stat(marker)
	assert.Assert(t, err != nil, "a clean ordinary run should have been skipped")

	// With it, the commands run.
	run("--attribute-to", dir)
	_, err = os.Stat(marker)
	assert.NilError(t, err, "a snapshot run was skipped and reported without running anything")
}

func TestFinishValidateFinalizesEventLog(t *testing.T) {
	tests := []struct {
		name      string
		execErr   error
		wantLevel string
	}{
		{name: "success", wantLevel: "done"},
		{name: "failure", execErr: errors.New("test failed"), wantLevel: "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, err := eventlog.Open(t.TempDir())
			assert.NilError(t, err)
			recorder := log.Recorder(func(iostream.Level, string) {}, eventlog.OpValidate, "sb-1", "sidecar", "main")
			result := validate.Result{Passed: 1, Total: 2}

			err = finishValidate(
				&cobra.Command{}, nil, tt.execErr, time.Now(), &config.ProjectConfig{}, result, "",
				recorder, recorder.Status, iostream.Streams{Out: io.Discard, Err: io.Discard}, nil,
			)
			if tt.execErr == nil {
				assert.NilError(t, err)
			} else {
				assert.ErrorIs(t, err, tt.execErr)
			}

			events, err := log.Recent(10)
			assert.NilError(t, err)
			assert.Equal(t, len(events), 1)
			assert.Equal(t, events[0].Level, tt.wantLevel)
			passed, total, final := events[0].Outcome()
			assert.Assert(t, final)
			assert.Equal(t, passed, result.Passed)
			assert.Equal(t, total, result.Total)
		})
	}
}

// A passing hook run names what it ran, so the user can tell a full check from
// a single command.
func TestValidateHookPassNamesTheCommands(t *testing.T) {
	isolateConfig(t)
	dir := hookProject(t, "exit 0")

	stdout, _, err := runActiveStopHookOutput(t, dir)
	assert.NilError(t, err)
	var response hookResponse
	assert.NilError(t, json.Unmarshal([]byte(stdout), &response))
	assert.Assert(t, strings.HasPrefix(response.SystemMessage, "chunk validate passed: test ("),
		"got %q", response.SystemMessage)
}

// Codex shows a systemMessage as a warning, so a pass says nothing there. The
// client detects Codex from the payload and the daemon subprocess is told by
// flag; both must reach the same answer.
func TestValidateHookPassIsQuietUnderCodex(t *testing.T) {
	for name, tc := range map[string]struct {
		stdin string
		args  []string
	}{
		"payload": {
			stdin: `{"session_id":"test-session-001","stop_hook_active":true,"hook_event_name":"Stop","turn_id":"turn-1"}`,
		},
		"forwarded": {
			args: []string{"--hook-session-id", "test-session-001", "--stop-hook-active", "--hook-codex"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			isolateConfig(t)
			dir := hookProject(t, "exit 0")

			var outBuf, errBuf bytes.Buffer
			root := newTestRootCmd()
			root.SetOut(&outBuf)
			root.SetErr(&errBuf)
			root.SetIn(strings.NewReader(tc.stdin))
			root.SetArgs(append([]string{"validate", "--local", "--no-daemon", "--project", dir}, tc.args...))
			assert.NilError(t, root.Execute())

			assert.Equal(t, outBuf.String(), "", "a pass under Codex must not write a hook response")
			assert.Assert(t, strings.Contains(errBuf.String(), "1/1 passed"), "the run did not happen: %q", errBuf.String())
		})
	}
}

func TestDetectHookRecognisesCodex(t *testing.T) {
	hook := detectHook(strings.NewReader(`{"session_id":"abc","hook_event_name":"Stop","turn_id":"turn-1"}`))
	assert.Assert(t, hook != nil)
	assert.Assert(t, hook.codex)

	hook = detectHook(strings.NewReader(`{"session_id":"abc","hook_event_name":"Stop"}`))
	assert.Assert(t, hook != nil)
	assert.Assert(t, !hook.codex)
}

// fakeValidateDaemon stands up a Unix socket answering /ping with build and
// /validate with a pass, and returns a channel of the validate requests it got.
func fakeValidateDaemon(t *testing.T, build string) <-chan watchd.ValidateRequest {
	t.Helper()
	// Not t.TempDir(): a unix socket path is capped at 104 bytes on darwin.
	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_WATCHD_DIR", dir)
	t.Setenv("CHUNK_WATCHD_REMOTE_ADDR", "")

	reqs := make(chan watchd.ValidateRequest, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, build)
	})
	mux.HandleFunc("/validate", func(w http.ResponseWriter, r *http.Request) {
		var req watchd.ValidateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reqs <- req
		_ = json.NewEncoder(w).Encode(watchd.ValidateResponse{})
	})

	ln, err := net.Listen("unix", filepath.Join(dir, "watchd.sock"))
	assert.NilError(t, err)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return reqs
}

// The daemon subprocess learns it is under Codex only from the request, so the
// client must say so for a Codex payload and only for one. It says so in a field
// and never as a flag: a remote daemon's build cannot be checked, and one that
// predates the flag would reject the run with a non-blocking exit.
func TestRunValidateViaDaemonForwardsCodex(t *testing.T) {
	for name, tc := range map[string]struct {
		hook  *hookContext
		codex bool
	}{
		"claude": {hook: &hookContext{sessionID: "abc", event: hookEventStop}},
		"codex":  {hook: &hookContext{sessionID: "abc", event: hookEventStop, codex: true}, codex: true},
	} {
		t.Run(name, func(t *testing.T) {
			reqs := fakeValidateDaemon(t, watchd.BuildID())

			var outBuf, errBuf bytes.Buffer
			err := runValidateViaDaemon(t.TempDir(), []string{"validate"}, "", "", tc.hook,
				iostream.Streams{Out: &outBuf, Err: &errBuf})
			assert.NilError(t, err)

			req := <-reqs
			assert.Equal(t, req.HookCodex, tc.codex)
			assert.Assert(t, !slices.Contains(req.Args, "--hook-codex"), "args: %q", req.Args)
		})
	}
}

// A daemon from another build may not know the hook flags, and rejects a run
// carrying one with a non-blocking exit — a commit gate would pass having
// checked nothing. The hook runs inline instead.
func TestTryHookDelegateSkipsADaemonFromAnotherBuild(t *testing.T) {
	reqs := fakeValidateDaemon(t, "some-older-build")
	hook := &hookContext{sessionID: "abc", event: "PreToolUse", codex: true}

	delegated, err := tryHookDelegate(&cobra.Command{}, hook, t.TempDir(), false, iostream.Streams{})
	assert.NilError(t, err)
	assert.Assert(t, !delegated)
	assert.Equal(t, len(reqs), 0, "the run was sent to the mismatched daemon")
}

// A hook released to the background says so in its response, under Codex too:
// otherwise the hook finishes with nothing to show it checked anything.
func TestReportDelegatedValidateTellsTheHookAboutABackgroundRun(t *testing.T) {
	for name, hook := range map[string]*hookContext{
		"claude": {sessionID: "abc", event: hookEventStop},
		"codex":  {sessionID: "abc", event: hookEventStop, codex: true},
	} {
		t.Run(name, func(t *testing.T) {
			var outBuf, errBuf bytes.Buffer
			err := reportDelegatedValidate(watchd.ValidateResponse{
				TaskID: "0192cf6e-1b9f-7c3e-8a11-2b3c4d5e6f70",
				Reason: "small change, 42 lines",
			}, hook, iostream.Streams{Out: &outBuf, Err: &errBuf})
			assert.NilError(t, err)

			var response hookResponse
			assert.NilError(t, json.Unmarshal(outBuf.Bytes(), &response))
			assert.Equal(t, response.SystemMessage, "chunk validate: running in the background (small change, 42 lines)")
		})
	}
}

func TestDetectHookReadsToolCommand(t *testing.T) {
	hook := detectHook(strings.NewReader(`{"session_id":"s","hook_event_name":"PreToolUse","tool_input":{"command":"git commit -m x"}}`))
	assert.Equal(t, hook.toolCommand, "git commit -m x")

	// Codex's unified exec may send argv rather than a command line, including a
	// shell running one.
	hook = detectHook(strings.NewReader(`{"session_id":"s","hook_event_name":"PreToolUse","tool_input":{"command":["bash","-lc","git commit -m x"]}}`))
	assert.Assert(t, hook.skipsCommitGate() == false, "argv running git commit through a shell must run the gate")

	hook = detectHook(strings.NewReader(`{"session_id":"s","hook_event_name":"Stop"}`))
	assert.Equal(t, hook.toolCommand, "")
}

func TestSkipsCommitGate(t *testing.T) {
	cases := []struct {
		payload string
		want    bool
	}{
		{`{"session_id":"s","hook_event_name":"PreToolUse","tool_input":{"command":"ls -la"}}`, true},
		{`{"session_id":"s","hook_event_name":"PreToolUse","tool_input":{"command":["ls","-la"]}}`, true},
		{`{"session_id":"s","hook_event_name":"PreToolUse","tool_input":{"command":"cd sub && git commit -m x"}}`, false},
		// A command chunk cannot read runs the gate rather than risk missing a commit.
		{`{"session_id":"s","hook_event_name":"PreToolUse"}`, false},
		{`{"session_id":"s","hook_event_name":"PreToolUse","tool_input":{"command":42}}`, false},
		// Only the commit gate is filtered; the Stop hook always runs.
		{`{"session_id":"s","hook_event_name":"Stop","tool_input":{"command":"ls"}}`, false},
	}
	for _, tc := range cases {
		hook := detectHook(strings.NewReader(tc.payload))
		assert.Equal(t, hook.skipsCommitGate(), tc.want, tc.payload)
	}
	assert.Assert(t, !(*hookContext)(nil).skipsCommitGate())
}

// Codex has no per-entry "if", so its commit gate starts before every Bash
// call. Anything but a git commit must end the run at once, writing nothing and
// running nothing.
func TestValidateCommitGateRunsOnlyForCommits(t *testing.T) {
	for name, tc := range map[string]struct {
		command string
		wantRun bool
	}{
		"not a commit": {command: "ls -la", wantRun: false},
		"commit":       {command: "git add . && git commit -m x", wantRun: true},
	} {
		t.Run(name, func(t *testing.T) {
			isolateConfig(t)
			marker := filepath.Join(t.TempDir(), "ran")
			dir := hookProject(t, "touch "+marker)

			payload, err := json.Marshal(map[string]any{
				"session_id":      "test-session-001",
				"hook_event_name": "PreToolUse",
				"turn_id":         "turn-1",
				"tool_name":       "Bash",
				"tool_input":      map[string]string{"command": tc.command},
			})
			assert.NilError(t, err)

			var outBuf, errBuf bytes.Buffer
			root := newTestRootCmd()
			root.SetOut(&outBuf)
			root.SetErr(&errBuf)
			root.SetIn(bytes.NewReader(payload))
			root.SetArgs([]string{"validate", "--local", "--no-daemon", "--project", dir})
			assert.NilError(t, root.Execute(), "stderr: %s", errBuf.String())

			_, statErr := os.Stat(marker)
			assert.Equal(t, statErr == nil, tc.wantRun, "stderr: %s", errBuf.String())
			if !tc.wantRun {
				assert.Equal(t, outBuf.String(), "")
				assert.Equal(t, errBuf.String(), "")
			}
		})
	}
}

// Codex runs hooks in the session's working directory, which may sit below the
// project that holds .chunk/config.json.
func TestHookProjectRoot(t *testing.T) {
	root := t.TempDir()
	gitSetup(t, root, "main")
	assert.NilError(t, config.SaveProjectConfig(root, &config.ProjectConfig{
		Commands: []config.Command{{Name: "test", Run: "true"}},
	}))
	sub := filepath.Join(root, "internal", "pkg")
	assert.NilError(t, os.MkdirAll(sub, 0o755))

	assert.Equal(t, hookProjectRoot(root), root)
	assert.Equal(t, hookProjectRoot(sub), root)

	// A project nested in a repository is found before the repository root.
	nested := filepath.Join(root, "services", "api")
	assert.NilError(t, config.SaveProjectConfig(nested, &config.ProjectConfig{
		Commands: []config.Command{{Name: "test", Run: "true"}},
	}))
	assert.Equal(t, hookProjectRoot(nested), nested)

	// With no config up to the git root, the search stops there and the
	// directory is used as given.
	other := t.TempDir()
	gitSetup(t, other, "main")
	otherSub := filepath.Join(other, "sub")
	assert.NilError(t, os.MkdirAll(otherSub, 0o755))
	assert.Equal(t, hookProjectRoot(otherSub), otherSub)
}

// A formatter has to run before the gates check the tree, wherever it sits in
// the config: the remote batch syncs the tree when it starts, so a formatter
// run after it would change nothing any gate saw.
func TestRunValidationPlanRunsAutofixFirst(t *testing.T) {
	workDir := t.TempDir()
	plan := validate.Plan{
		LocalCommands: []config.Command{
			{Name: "lint", Run: "echo lint >> order"},
			{Name: "format", Run: "echo format >> order", Role: config.RoleAutofix},
			{Name: "test", Run: "echo test >> order"},
		},
	}

	result, err := runValidationPlan(
		context.Background(), nil, plan, config.ResolvedConfig{}, workDir, workDir, nil, nil,
		func(iostream.Level, string) {}, iostream.Streams{Out: io.Discard, Err: io.Discard},
	)

	assert.NilError(t, err)
	assert.Equal(t, result, validate.Result{Passed: 3, Total: 3})
	data, err := os.ReadFile(filepath.Join(workDir, "order"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "format\nlint\ntest\n")
}
