package sidecar

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestSaveActiveWritesToXDGDataPath(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv(config.EnvXDGDataHome, dataHome)

	dir := t.TempDir()
	t.Chdir(dir)

	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-1"}))

	// Must not appear inside the project's .chunk directory.
	_, err := os.Stat(filepath.Join(dir, ".chunk", "sidecar.json"))
	assert.Assert(t, os.IsNotExist(err), "sidecar.json must not be written inside .chunk/")

	// Must appear at the deterministic XDG data path.
	expected, err := config.ProjectDataDir(dir)
	assert.NilError(t, err)
	_, err = os.Stat(filepath.Join(expected, "sidecar.json"))
	assert.NilError(t, err)
}

func setupXDGData(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
}

func TestSaveAndLoadActive(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	want := ActiveSidecar{SidecarID: "sb-abc", Name: "my-box"}
	err := SaveActive(want)
	assert.NilError(t, err)

	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil, "expected non-nil ActiveSidecar")
	assert.Equal(t, got.SidecarID, want.SidecarID)
	assert.Equal(t, got.Name, want.Name)
}

func TestLoadActiveReturnsNilWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "expected nil when no active sidecar file")
}

func TestLoadActiveUsesGitRootAsKey(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "sub", "dir")
	assert.NilError(t, os.MkdirAll(child, 0o755))
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".git"), 0o755))

	setupXDGData(t)

	// Save from child — keyed to parent (git root).
	t.Chdir(child)
	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-git-root"}))

	// Load from child — should find it.
	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-git-root")

	// Load from parent (the git root) — same project, same file.
	t.Chdir(parent)
	got, err = LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-git-root")
}

func TestLoadActiveUsesCwdWhenNoGitRepo(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-cwd"}))

	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-cwd")
}

func TestClearActive(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-xyz"}))

	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)

	assert.NilError(t, ClearActive())

	got, err = LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got == nil)
}

func TestSessionKeyedSidecar(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	// Save without a session — generic file.
	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-generic"}))

	// With a session ID set, load should return nil (isolated from the generic file).
	t.Setenv(config.EnvClaudeSession, "sess-abc")
	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "session-keyed load should not see generic file")

	// Save under the session.
	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-session"}))

	got, err = LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-session")

	// Without the session env var, the original generic file is still intact.
	t.Setenv(config.EnvClaudeSession, "")
	got, err = LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-generic")
}

func TestClearActiveNoopWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	assert.NilError(t, ClearActive())
}

func TestWorkspaceFieldRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	want := ActiveSidecar{SidecarID: "sb-1", Name: "test", Workspace: "/workspace/myrepo"}
	assert.NilError(t, SaveActive(want))

	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.Workspace, want.Workspace)
	assert.Equal(t, got.SidecarID, want.SidecarID)
}

func TestWorkspaceOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-1"}))

	stateDir, err := saveDir()
	assert.NilError(t, err)
	data, err := os.ReadFile(filepath.Join(stateDir, sidecarFileName()))
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(data), "workspace"), "empty workspace should be omitted from JSON")
}

func TestResolveWorkspaceCLIFlagWins(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-1", Workspace: "/workspace/saved"}))

	got := resolveWorkspace("/workspace/override", "myrepo")
	assert.Equal(t, got, "/workspace/override")
}

func TestResolveWorkspaceSidecarFallback(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-1", Workspace: "/workspace/saved"}))

	got := resolveWorkspace("", "myrepo")
	assert.Equal(t, got, "/workspace/saved")
}

func TestResolveWorkspaceDefaultFallback(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	got := resolveWorkspace("", "myrepo")
	assert.Equal(t, got, "./workspace/myrepo")
}
