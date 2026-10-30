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

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

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

// TestSaveActivePoolStatePersistsEveryMember guards the fix that lets a pool
// grown beyond one sidecar be fully reused later: without it, only the first
// member chunk validate ever created was remembered, so every run needing
// more than one sidecar recreated the rest from scratch and orphaned the
// previous run's extras.
// useLocalSidecar makes the fake sidecar run commands on this machine, so the
// rsync a pooled run does is real, with the sidecar's home redirected to a temp
// dir instead of /home/user.
func useLocalSidecar(t *testing.T, sshSrv *fakes.SSHServer) {
	t.Helper()
	for _, bin := range []string{"rsync", "ssh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	sshSrv.RunLocally()
	t.Setenv("CHUNK_SIDECAR_HOME", t.TempDir())
}

func TestSaveActivePoolStatePersistsEveryMember(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv(config.EnvHome, homeDir)
	pubKey := fakes.GenerateSSHKeypairAt(t, filepath.Join(homeDir, ".ssh", "chunk_ai"))
	sshSrv := fakes.NewSSHServer(t, pubKey)
	useLocalSidecar(t, sshSrv)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	t.Chdir(workDir)

	pool, err := sidecar.NewPool(context.Background(), client, sidecar.PoolOptions{
		Size:     2,
		Name:     "validate",
		OrgID:    "org-1",
		Image:    "ubuntu:22.04",
		WorkDir:  workDir,
		RepoPath: sidecar.DefaultWorkspace("my-repo"),
	}, func(iostream.Level, string) {})
	assert.NilError(t, err)

	a, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	b, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	pool.Release(a)
	pool.Release(b)

	assert.NilError(t, saveActivePoolState(context.Background(), pool, "org-1"))

	active, err := sidecar.LoadActive(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, len(active.SidecarIDs), 2)
	assert.Assert(t, slices.Contains(active.SidecarIDs, a.ID))
	assert.Assert(t, slices.Contains(active.SidecarIDs, b.ID))
	assert.Equal(t, active.OrgID, "org-1")
}

func TestOpenAPIExecPassesEnvVars(t *testing.T) {
	isolateConfig(t)

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	// Point the daemon socket at an empty dir. Without this the command
	// registration would reach a chunk daemon actually running on the developer's
	// machine, which is neither hermetic nor polite.
	t.Setenv("CHUNK_DAEMON_DIR", t.TempDir())

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
// chunk daemon, which reads this same on-disk log. Registering the project is
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

func TestValidateParallelism(t *testing.T) {
	assert.Equal(t, validateParallelism(0, ""), defaultValidateParallelism)
	assert.Equal(t, validateParallelism(1, ""), 1)
	assert.Equal(t, validateParallelism(5, ""), 5)

	// An explicit --sidecar-id names one target, not a pool to grow around it.
	assert.Equal(t, validateParallelism(0, "sidecar-123"), 1)
	assert.Equal(t, validateParallelism(5, "sidecar-123"), 1)
}

// TestPlanValidationExecutionSpreadsMultipleRemoteCommands confirms several
// independent remote commands get more than one sidecar by default, and that
// --parallel both raises and caps how many are used.
func TestPlanValidationExecutionSpreadsMultipleRemoteCommands(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "test", Run: "task test"},
		{Name: "acceptance-test", Run: "task acceptance-test"},
		{Name: "lint", Run: "task lint"},
		{Name: "format", Run: "task fmt", Local: true},
	}}

	t.Run("defaults to the cap when unset", func(t *testing.T) {
		plan := planValidationExecution(cfg, &validateOpts{}, "")
		assert.Equal(t, len(plan.RemoteCommands), 3)
		assert.Equal(t, plan.PoolSize, defaultValidateParallelism)
	})

	t.Run("parallel flag raises the cap up to the remote command count", func(t *testing.T) {
		plan := planValidationExecution(cfg, &validateOpts{parallel: 10}, "")
		assert.Equal(t, plan.PoolSize, 3)
	})

	t.Run("parallel flag can also lower it", func(t *testing.T) {
		plan := planValidationExecution(cfg, &validateOpts{parallel: 1}, "")
		assert.Equal(t, plan.PoolSize, 1)
	})

	t.Run("explicit sidecar-id pins to one sidecar even with parallel set", func(t *testing.T) {
		plan := planValidationExecution(cfg, &validateOpts{parallel: 10, sidecarID: "sidecar-123"}, "")
		assert.Equal(t, len(plan.RemoteCommands), 3)
		assert.Equal(t, plan.PoolSize, 1)
	})
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

	err := reportDelegatedValidate(chunkd.ValidateResponse{
		TaskID: "0192cf6e-1b9f-7c3e-8a11-2b3c4d5e6f70",
		Reason: "small change, 42 lines",
		// Ignored: a released run has not produced these, and a daemon that
		// sends them anyway must not have them read as a result.
		ExitCode: 1,
		Stderr:   "should not be shown",
	}, streams)

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

	err := reportDelegatedValidate(chunkd.ValidateResponse{
		Reason:   "large change, 912 lines, over the 500-line limit",
		ExitCode: 2,
		Stdout:   "on stdout",
		Stderr:   "on stderr",
	}, streams)

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

	err := reportDelegatedValidate(chunkd.ValidateResponse{Stderr: "1/1 passed"}, streams)
	assert.NilError(t, err)
	assert.Equal(t, errBuf.String(), "1/1 passed")
}

// A low-risk change gets no score line: it is the common case, and a number
// printed every turn is a number nobody reads. Advice is printed whenever
// there is any.
func TestReportDelegatedValidatePrintsRiskOnlyWhenItMatters(t *testing.T) {
	var quiet bytes.Buffer
	err := reportDelegatedValidate(chunkd.ValidateResponse{
		Risk: &chunkd.RiskSummary{Score: 12, Band: chunkd.BandLow, Parts: []string{"12 lines (1)"}},
	}, iostream.Streams{Out: &quiet, Err: &quiet})
	assert.NilError(t, err)
	assert.Equal(t, quiet.String(), "", "a low-risk change was narrated")

	var loud bytes.Buffer
	err = reportDelegatedValidate(chunkd.ValidateResponse{
		Reason:   "large change, 2000 lines, over the 500-line limit",
		ExitCode: 1,
		Risk: &chunkd.RiskSummary{
			Score:  92,
			Band:   chunkd.BandHigh,
			Parts:  []string{"2000 lines (60)", "3 files (6)"},
			Advice: "committing it in parts would get each piece checked sooner",
		},
	}, iostream.Streams{Out: &loud, Err: &loud})

	assert.Assert(t, err != nil)
	out := loud.String()
	assert.Assert(t, strings.Contains(out, "risk 92/100 high"), "got %q", out)
	assert.Assert(t, strings.Contains(out, "2000 lines (60)"), "got %q", out)
	assert.Assert(t, strings.Contains(out, "committing it in parts"), "got %q", out)
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

			err = finishValidate(tt.execErr, time.Now(), result, recorder, recorder.Status, nil)
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

// fakeValidateDaemon stands up a Unix socket answering /ping with build and
// /validate with a pass, and returns a channel of the validate requests it got.
func fakeValidateDaemon(t *testing.T, build string) <-chan chunkd.ValidateRequest {
	t.Helper()
	// Not t.TempDir(): a unix socket path is capped at 104 bytes on darwin.
	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_DAEMON_DIR", dir)
	t.Setenv("CHUNK_DAEMON_REMOTE_ADDR", "")

	reqs := make(chan chunkd.ValidateRequest, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, build)
	})
	mux.HandleFunc("/validate", func(w http.ResponseWriter, r *http.Request) {
		var req chunkd.ValidateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reqs <- req
		_ = json.NewEncoder(w).Encode(chunkd.ValidateResponse{})
	})

	ln, err := net.Listen("unix", filepath.Join(dir, "daemon.sock"))
	assert.NilError(t, err)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return reqs
}

// The daemon boots the run's sidecar before the subprocess starts, so the image
// a --no-daemon run would use has to travel in the request.
func TestRunValidateViaDaemonForwardsSidecarImage(t *testing.T) {
	reqs := fakeValidateDaemon(t, chunkd.BuildID())

	var outBuf, errBuf bytes.Buffer
	err := runValidateViaDaemon(t.TempDir(), []string{"validate", "test"}, "", "org-1", "snap-test",
		iostream.Streams{Out: &outBuf, Err: &errBuf})
	assert.NilError(t, err)

	req := <-reqs
	assert.Equal(t, req.SidecarImage, "snap-test")
}

// A hook payload on stdin changes nothing about delegation: the run goes to the
// daemon synchronously, never offered to the background, and carries no hook
// context.
func TestValidateWithStopPayloadDelegatesSynchronously(t *testing.T) {
	isolateConfig(t)
	reqs := fakeValidateDaemon(t, chunkd.BuildID())
	dir := t.TempDir()
	err := config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{{Name: "test", Run: "true"}},
	})
	assert.NilError(t, err)

	var outBuf, errBuf bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(`{"session_id":"s1","hook_event_name":"Stop"}`))
	root.SetArgs([]string{"validate", "--local", "--project", dir})
	err = root.Execute()
	assert.NilError(t, err, "stderr: %s", errBuf.String())

	var req chunkd.ValidateRequest
	select {
	case req = <-reqs:
	default:
		t.Fatalf("the run was not delegated to the daemon; stderr: %s", errBuf.String())
	}
	assert.Check(t, !req.AllowAsync)
	assert.Check(t, !slices.Contains(req.Args, "--hook-session-id"), "args: %q", req.Args)
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
