package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func runHooksCmd(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd("test")
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	allArgs := append([]string{"hooks"}, args...)
	allArgs = append(allArgs, "--project", dir)
	root.SetArgs(allArgs)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestHooksDisable_CreatesSentinel(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runHooksCmd(t, dir, "disable")
	assert.NilError(t, err)
	_, statErr := os.Stat(filepath.Join(dir, ".chunk", "hooks-disabled"))
	assert.NilError(t, statErr, "expected sentinel file to exist")
}

func TestHooksEnable_RemovesSentinel(t *testing.T) {
	dir := t.TempDir()
	chunkDir := filepath.Join(dir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "hooks-disabled"), []byte{}, 0o644))

	_, _, err := runHooksCmd(t, dir, "enable")
	assert.NilError(t, err)
	_, statErr := os.Stat(filepath.Join(chunkDir, "hooks-disabled"))
	assert.Assert(t, os.IsNotExist(statErr), "expected sentinel file to be removed")
}

func TestHooksEnable_NoopWhenAlreadyEnabled(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runHooksCmd(t, dir, "enable")
	assert.NilError(t, err)
}

func TestHooksStatus_Disabled(t *testing.T) {
	dir := t.TempDir()
	chunkDir := filepath.Join(dir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "hooks-disabled"), []byte{}, 0o644))

	out, _, err := runHooksCmd(t, dir, "status")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "disabled"), "got: %s", out)
}

func TestHooksStatus_Enabled(t *testing.T) {
	dir := t.TempDir()
	out, _, err := runHooksCmd(t, dir, "status")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "enabled"), "got: %s", out)
}

// TestValidateHookPath_HooksDisabled verifies that when CHUNK_HOOKS_DISABLED is
// set the validate hook path exits 1 and prints a clear message to stderr.
func TestValidateHookPath_HooksDisabled(t *testing.T) {
	t.Setenv(config.EnvChunkHooksDisabled, "1")

	dir := t.TempDir()
	root := NewRootCmd("test")
	var errOut bytes.Buffer
	root.SetErr(&errOut)
	root.SetArgs([]string{"validate", "--project", dir})

	// Provide a valid Stop hook JSON payload so detectHook returns non-nil.
	hookPayload := `{"session_id":"test-hooks-disabled","stop_hook_active":false}`
	root.SetIn(strings.NewReader(hookPayload))

	err := root.Execute()
	type exitCoder interface{ ExitCode() int }
	ec, ok := err.(exitCoder)
	assert.Assert(t, ok, "expected ExitCode() method on error, got: %v", err)
	assert.Equal(t, ec.ExitCode(), 1)
	assert.Assert(t, strings.Contains(errOut.String(), "hooks are disabled"), "got: %s", errOut.String())
}
