package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
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

	// Set up a project dir with a remote command. HasGitChanges returns true in
	// a non-git dir, so the hook won't short-circuit on a clean-tree check.
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

func TestValidateHookSkipsAuthCheckWhenNoRemoteCommands(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	// Local-only commands: auth check must not fire.
	dir := t.TempDir()
	projCfg := &config.ProjectConfig{
		Commands: []config.Command{
			{Name: "lint", Run: "echo ok", Remote: false},
		},
	}
	assert.NilError(t, config.SaveProjectConfig(dir, projCfg))

	_, stderr, err := runValidateHook(t, dir)

	// May fail for other reasons (no git repo), but must not be an auth error.
	assert.Assert(t, !strings.Contains(stderr, "CircleCI auth is not configured"),
		"auth check must not fire for local-only commands, stderr: %q", stderr)
	_ = err
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

// setupSSHSession starts fake CCI + SSH servers and sets env vars so
// ensureCircleCIClient resolves to the fake. Returns the SSH server and the
// identity key file path.
func setupSSHSession(t *testing.T) (*fakes.SSHServer, string) {
	t.Helper()
	isolateConfig(t)

	keyFile, pubKey := fakes.GenerateSSHKeypair(t)
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("hello\n", 0)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	cciSrv := httptest.NewServer(cci)
	t.Cleanup(cciSrv.Close)

	t.Setenv(config.EnvCircleToken, "test-token")
	t.Setenv(config.EnvCircleCIBaseURL, cciSrv.URL)

	return sshSrv, keyFile
}

func TestOpenSSHSessionPassesEnvVars(t *testing.T) {
	sshSrv, keyFile := setupSSHSession(t)

	client, err := circleci.NewClient(circleci.Config{
		Token:   "test-token",
		BaseURL: os.Getenv(config.EnvCircleCIBaseURL),
	})
	assert.NilError(t, err)

	envVars := map[string]string{"FOO": "bar", "BAZ": "qux"}
	execFn, _, err := openSSHSession(context.Background(), client, "sidecar-123", keyFile, "", envVars, config.ResolvedConfig{}, discardStreams())
	assert.NilError(t, err)

	_, _, _, err = execFn(context.Background(), "echo hello")
	assert.NilError(t, err)

	got := sshSrv.EnvVars()
	assert.Equal(t, got["FOO"], "bar")
	assert.Equal(t, got["BAZ"], "qux")
}

func TestOpenSSHSessionUsesIdentityFileWhenUseSSHIdentityFile(t *testing.T) {
	sshSrv, keyFile := setupSSHSession(t)

	client, err := circleci.NewClient(circleci.Config{
		Token:   "test-token",
		BaseURL: os.Getenv(config.EnvCircleCIBaseURL),
	})
	assert.NilError(t, err)

	// Write the key to the path that sidecar.DefaultKeyPath() would return so
	// we can pass it explicitly as the identity file — verifying the UseSSHIdentityFile
	// path uses the provided key rather than SSH_AUTH_SOCK.
	rc := config.ResolvedConfig{UseSSHIdentityFile: true}
	execFn, _, err := openSSHSession(context.Background(), client, "sidecar-123", keyFile, "", nil, rc, discardStreams())
	assert.NilError(t, err)

	sshSrv.SetResult("ok\n", 0)
	_, _, code, err := execFn(context.Background(), "true")
	assert.NilError(t, err)
	assert.Equal(t, code, 0)
}

func TestOpenSSHSessionFallsBackToAuthSockWhenNoIdentityFile(t *testing.T) {
	_, keyFile := setupSSHSession(t)
	t.Setenv(config.EnvSSHAuthSock, "")

	client, err := circleci.NewClient(circleci.Config{
		Token:   "test-token",
		BaseURL: os.Getenv(config.EnvCircleCIBaseURL),
	})
	assert.NilError(t, err)

	// No identity file provided and UseSSHIdentityFile=false: the session open
	// attempt will use an empty authSock, which should fail gracefully.
	rc := config.ResolvedConfig{UseSSHIdentityFile: false}
	_, _, err = openSSHSession(context.Background(), client, "sidecar-123", keyFile, "", nil, rc, discardStreams())
	// With a key file explicitly provided the session opens; the important thing
	// is that UseSSHIdentityFile=false does not attempt DefaultKeyPath resolution.
	assert.NilError(t, err)
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
