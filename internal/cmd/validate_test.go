package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
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
	t.Run("returns nil when no tokens are set", func(t *testing.T) {
		t.Setenv(config.EnvCircleToken, "")
		t.Setenv(config.EnvCircleCIToken, "")

		assert.Assert(t, hostForwardEnv() == nil)
	})

	t.Run("forwards CIRCLE_TOKEN when set", func(t *testing.T) {
		t.Setenv(config.EnvCircleToken, "abc123")
		t.Setenv(config.EnvCircleCIToken, "")

		env := hostForwardEnv()
		assert.Equal(t, env[config.EnvCircleToken], "abc123")
		_, hasAlias := env[config.EnvCircleCIToken]
		assert.Assert(t, !hasAlias)
	})

	t.Run("forwards CIRCLECI_TOKEN when set", func(t *testing.T) {
		t.Setenv(config.EnvCircleToken, "")
		t.Setenv(config.EnvCircleCIToken, "def456")

		env := hostForwardEnv()
		assert.Equal(t, env[config.EnvCircleCIToken], "def456")
		_, hasCanonical := env[config.EnvCircleToken]
		assert.Assert(t, !hasCanonical)
	})

	t.Run("forwards both when both are set", func(t *testing.T) {
		t.Setenv(config.EnvCircleToken, "abc123")
		t.Setenv(config.EnvCircleCIToken, "def456")

		env := hostForwardEnv()
		assert.Equal(t, env[config.EnvCircleToken], "abc123")
		assert.Equal(t, env[config.EnvCircleCIToken], "def456")
	})
}
