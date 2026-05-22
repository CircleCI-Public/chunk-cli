package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
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
	assert.Assert(t, strings.Contains(stderr, "chunk auth set circleci"),
		"expected auth hint in stderr, got: %q", stderr)
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

	envVars := map[string]string{"FOO": "bar", "BAZ": "qux"}
	remoteExec, err := openSSHSession(context.Background(), "sidecar-123", keyFile, "", envVars, discardStreams())
	assert.NilError(t, err)

	_, _, _, err = remoteExec.ExecFn()(context.Background(), "echo hello")
	assert.NilError(t, err)

	got := sshSrv.EnvVars()
	assert.Equal(t, got["FOO"], "bar")
	assert.Equal(t, got["BAZ"], "qux")
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
