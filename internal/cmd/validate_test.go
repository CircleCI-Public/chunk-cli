package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

// hookPayload is the JSON Claude Code sends to Stop hooks via stdin.
const hookPayload = `{"session_id":"test-session-001","stop_hook_active":false}`

func runValidateHook(t *testing.T, workDir string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := NewRootCmd("test")
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(hookPayload))
	root.SetArgs([]string{"validate", "--project", workDir})
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
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

// TestValidateHookRequiresAuthByDefault verifies that hook invocations now
// always require CircleCI auth — even when no commands are explicitly marked
// Remote:true — because remote is the default execution mode.
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

func TestValidateNeedsSidecarSidecarImage(t *testing.T) {
	cfg := &config.ProjectConfig{
		Validation: &config.ValidationConfig{SidecarImage: "my-snapshot-abc123"},
	}
	got := validateNeedsSidecar(false, cfg)
	assert.Assert(t, got, "expected validateNeedsSidecar=true with sidecarImage configured")
}

func TestHostForwardEnv(t *testing.T) {
	t.Run("returns nil when token is empty", func(t *testing.T) {
		assert.Assert(t, hostForwardEnv("") == nil)
	})

	t.Run("forwards token as CIRCLE_TOKEN", func(t *testing.T) {
		env := hostForwardEnv("abc123")
		assert.Equal(t, env[config.EnvCircleToken], "abc123")
		_, hasAlias := env[config.EnvCircleCIToken]
		assert.Assert(t, !hasAlias)
	})
}

func TestOpenAPIExecPassesEnvVars(t *testing.T) {
	isolateConfig(t)

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	envVars := map[string]string{"FOO": "bar", "BAZ": "qux"}
	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	execFn, _, err := newExecFn(context.Background(), client, "sidecar-123", "", envVars, config.ResolvedConfig{}, streams)
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
	root := NewRootCmd("test")
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
	root := NewRootCmd("test")
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
	root := NewRootCmd("test")
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
	root := NewRootCmd("test")
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"validate", "--local", "--project", dir})
	err := root.Execute()

	assert.NilError(t, err, "--local must bypass Remote:true config and run locally")
	combined := outBuf.String() + errBuf.String()
	assert.Assert(t, strings.Contains(combined, "ran-locally"),
		"--local must execute commands locally even when Remote:true, got: %q", combined)
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
func runActiveStopHook(t *testing.T, dir string) (stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := NewRootCmd("test")
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(activeStopHookPayload))
	root.SetArgs([]string{"validate", "--local", "--project", dir})
	err = root.Execute()
	return errBuf.String(), err
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

	second := run()
	assert.Assert(t, strings.Contains(second, skipMsg), "second run must hit the cache, got: %q", second)

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
			active: &sidecar.ActiveSidecar{SidecarID: "sc-2"},
			want:   "sc-2\x00",
		},
		{
			name:   "explicit id wins over active",
			opts:   &validateOpts{sidecarID: "sc-1"},
			cfg:    &config.ProjectConfig{},
			active: &sidecar.ActiveSidecar{SidecarID: "sc-2"},
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
			active: &sidecar.ActiveSidecar{SidecarID: "sc-2"},
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
	root := NewRootCmd("test")
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"validate", "--list", "--project", workDir})
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func runMarkRemoteCLI(t *testing.T, workDir string, extraArgs ...string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := NewRootCmd("test")
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
	assert.Assert(t, cfg.HasRemoteCommands())
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
			{Name: "format", Run: "task fmt", Role: config.RoleAutofix},
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
}

// --list has to show what the skills tell agents to inspect before marking.
func TestValidateListShowsRoutingAndRole(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "test", Run: "task test", Role: config.RoleGate, Remote: true},
			{Name: "format", Run: "task fmt", Role: config.RoleAutofix},
			{Name: "bare", Run: "echo hi"},
		},
	}))

	stdout, stderr, err := runValidateListCLI(t, dir)
	assert.NilError(t, err)
	out := stdout + stderr
	for _, want := range []string{"test [remote, gate]", "format [local, autofix]", "bare [local]"} {
		assert.Assert(t, strings.Contains(out, want), "missing %q in:\n%s", want, out)
	}
}
