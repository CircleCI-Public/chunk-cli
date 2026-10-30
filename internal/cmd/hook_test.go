package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

func TestResolveHookRootHonoursCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := resolveHookRoot(ctx, "")
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
}

func runHooksCmd(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	root := newTestRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	allArgs := append([]string{"hook"}, args...)
	allArgs = append(allArgs, "--project", dir)
	root.SetArgs(allArgs)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// hookSentinels points the project data dir at a temp dir and returns the
// sentinel's new path and the in-repo path older versions wrote.
func hookSentinels(t *testing.T, dir string) (string, string) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	paths, err := validate.HooksDisabledPaths(dir)
	assert.NilError(t, err)
	return paths[0], paths[1]
}

func touch(t *testing.T, p string) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	assert.NilError(t, os.WriteFile(p, []byte{}, 0o644))
}

func TestHooksDisable_CreatesSentinel(t *testing.T) {
	dir := t.TempDir()
	sentinel, legacy := hookSentinels(t, dir)
	_, _, err := runHooksCmd(t, dir, "disable")
	assert.NilError(t, err)
	_, statErr := os.Stat(sentinel)
	assert.NilError(t, statErr, "expected sentinel file to exist")
	_, statErr = os.Stat(legacy)
	assert.Assert(t, os.IsNotExist(statErr), "expected nothing written in the repo")
}

func TestHooksEnable_RemovesSentinel(t *testing.T) {
	dir := t.TempDir()
	sentinel, legacy := hookSentinels(t, dir)
	touch(t, sentinel)
	touch(t, legacy)

	_, _, err := runHooksCmd(t, dir, "enable")
	assert.NilError(t, err)
	for _, p := range []string{sentinel, legacy} {
		_, statErr := os.Stat(p)
		assert.Assert(t, os.IsNotExist(statErr), "expected %s to be removed", p)
	}
}

func TestHooksEnable_NoopWhenAlreadyEnabled(t *testing.T) {
	dir := t.TempDir()
	hookSentinels(t, dir)
	_, _, err := runHooksCmd(t, dir, "enable")
	assert.NilError(t, err)
}

func TestHooksStatus_Disabled(t *testing.T) {
	dir := t.TempDir()
	sentinel, _ := hookSentinels(t, dir)
	touch(t, sentinel)

	out, _, err := runHooksCmd(t, dir, "status")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "disabled"), "got: %s", out)
}

func TestHooksStatus_DisabledByLegacySentinel(t *testing.T) {
	dir := t.TempDir()
	_, legacy := hookSentinels(t, dir)
	touch(t, legacy)

	out, _, err := runHooksCmd(t, dir, "status")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "disabled"), "got: %s", out)
}

func TestHooksStatus_Enabled(t *testing.T) {
	dir := t.TempDir()
	hookSentinels(t, dir)
	out, _, err := runHooksCmd(t, dir, "status")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "enabled"), "got: %s", out)
}
