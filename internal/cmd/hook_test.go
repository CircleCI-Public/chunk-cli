package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func runHooksCmd(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd("test")
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	allArgs := append([]string{"hook"}, args...)
	allArgs = append(allArgs, "--project", dir)
	root.SetArgs(allArgs)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestHooksEnable_CreatesPrePushHook(t *testing.T) {
	dir := initGitRepo(t)
	_, errOut, err := runHooksCmd(t, dir, "enable")
	assert.NilError(t, err)

	data, statErr := os.ReadFile(filepath.Join(dir, ".git", "hooks", "pre-push"))
	assert.NilError(t, statErr, "expected pre-push hook to be created")
	assert.Assert(t, strings.Contains(string(data), "chunk validate"))

	assert.Assert(t, strings.Contains(errOut, "Installed"), "got: %s", errOut)
}

func TestHooksEnable_NoopWhenAlreadyEnabled(t *testing.T) {
	dir := initGitRepo(t)

	_, _, err := runHooksCmd(t, dir, "enable")
	assert.NilError(t, err)

	_, errOut, err := runHooksCmd(t, dir, "enable")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(errOut, "already"), "got: %s", errOut)
}

func TestHooksEnable_AppendsToExistingHook(t *testing.T) {
	dir := initGitRepo(t)
	hooksDir := filepath.Join(dir, ".git", "hooks")
	assert.NilError(t, os.MkdirAll(hooksDir, 0o755))

	existing := "#!/bin/sh\necho pre-push\n"
	assert.NilError(t, os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(existing), 0o755))

	_, errOut, err := runHooksCmd(t, dir, "enable")
	assert.NilError(t, err)

	data, _ := os.ReadFile(filepath.Join(hooksDir, "pre-push"))
	content := string(data)
	assert.Assert(t, strings.HasPrefix(content, existing), "existing content must be preserved")
	assert.Assert(t, strings.Contains(content, "chunk validate"))
	assert.Assert(t, strings.Contains(errOut, "Updated"), "got: %s", errOut)
}

func TestHooksDisable_RemovesChunkManagedHook(t *testing.T) {
	dir := initGitRepo(t)

	_, _, err := runHooksCmd(t, dir, "enable")
	assert.NilError(t, err)

	_, errOut, err := runHooksCmd(t, dir, "disable")
	assert.NilError(t, err)

	_, statErr := os.Stat(filepath.Join(dir, ".git", "hooks", "pre-push"))
	assert.Assert(t, os.IsNotExist(statErr), "expected pre-push hook to be removed")
	assert.Assert(t, strings.Contains(errOut, "Removed"), "got: %s", errOut)
}

func TestHooksDisable_RemovesOnlyChunkLineFromMixedHook(t *testing.T) {
	dir := initGitRepo(t)
	hooksDir := filepath.Join(dir, ".git", "hooks")
	assert.NilError(t, os.MkdirAll(hooksDir, 0o755))

	content := "#!/bin/sh\necho pre-push\nchunk validate\n"
	assert.NilError(t, os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(content), 0o755))

	_, _, err := runHooksCmd(t, dir, "disable")
	assert.NilError(t, err)

	data, readErr := os.ReadFile(filepath.Join(hooksDir, "pre-push"))
	assert.NilError(t, readErr, "mixed hook file should remain (not deleted)")
	assert.Assert(t, !strings.Contains(string(data), "chunk validate"), "chunk validate must be removed")
	assert.Assert(t, strings.Contains(string(data), "echo pre-push"), "other content must be preserved")
}

func TestHooksDisable_NoopWhenNotInstalled(t *testing.T) {
	dir := initGitRepo(t)
	_, errOut, err := runHooksCmd(t, dir, "disable")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(errOut, "No pre-push hook"), "got: %s", errOut)
}

func TestHooksStatus_Enabled(t *testing.T) {
	dir := initGitRepo(t)

	_, _, err := runHooksCmd(t, dir, "enable")
	assert.NilError(t, err)

	out, _, err := runHooksCmd(t, dir, "status")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "enabled"), "got: %s", out)
}

func TestHooksStatus_DisabledWhenNoHook(t *testing.T) {
	dir := initGitRepo(t)
	out, _, err := runHooksCmd(t, dir, "status")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "disabled"), "got: %s", out)
}

func TestHooksStatus_DisabledWhenNoGitRepo(t *testing.T) {
	dir := t.TempDir()
	out, _, err := runHooksCmd(t, dir, "status")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "disabled"), "got: %s", out)
}
