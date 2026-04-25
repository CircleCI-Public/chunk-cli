package sidecar

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestSaveAndLoadActive(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

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

	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "expected nil when no active sidecar file")
}

func TestLoadActiveWalksUpToParent(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "sub", "dir")
	assert.NilError(t, os.MkdirAll(child, 0o755))

	// Mark parent as a git root so the walk doesn't escape it.
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".git"), 0o755))

	// Write .chunk/sidecar in parent
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".chunk"), 0o755))
	data := []byte(`{"sidecar_id":"sb-parent","name":"parent-box"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(parent, ".chunk", "sidecar.json"), data, 0o644))

	// cd into child — should still find the parent's file
	t.Chdir(child)

	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-parent")
}

func TestLoadActiveStopsAtGitRoot(t *testing.T) {
	// grandparent has .chunk/sidecar; parent has .git; cwd is child of parent.
	// The walk should stop at parent and not find grandparent's file.
	grandparent := t.TempDir()
	parent := filepath.Join(grandparent, "repo")
	child := filepath.Join(parent, "sub")
	assert.NilError(t, os.MkdirAll(child, 0o755))

	// .git lives in parent (the repo root)
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".git"), 0o755))

	// .chunk/sidecar lives in grandparent (above the repo root)
	assert.NilError(t, os.MkdirAll(filepath.Join(grandparent, ".chunk"), 0o755))
	data := []byte(`{"sidecar_id":"sb-grandparent"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(grandparent, ".chunk", "sidecar.json"), data, 0o644))

	t.Chdir(child)

	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "walk should not cross the git root boundary")
}

func TestLoadActiveNoGitRepo(t *testing.T) {
	// No .git anywhere — only the cwd itself is checked.
	parent := t.TempDir()
	child := filepath.Join(parent, "sub")
	assert.NilError(t, os.MkdirAll(child, 0o755))

	// .chunk/sidecar in parent but no .git anywhere
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".chunk"), 0o755))
	data := []byte(`{"sidecar_id":"sb-parent"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(parent, ".chunk", "sidecar.json"), data, 0o644))

	t.Chdir(child)

	// Without .git the walk stops at cwd, so the parent file is not found.
	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "without a git repo the walk should not go above cwd")
}

func TestClearActive(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-xyz"}))

	// File should exist
	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)

	assert.NilError(t, ClearActive())

	// Should be gone now
	got, err = LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got == nil)
}

func TestSessionKeyedSidecar(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

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

	assert.NilError(t, ClearActive())
}

func TestSaveActiveUpdatesParentFile(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "sub")
	assert.NilError(t, os.MkdirAll(child, 0o755))

	// Mark parent as the git root so the walk can reach it.
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".git"), 0o755))

	// Write an existing .chunk/sidecar in parent
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".chunk"), 0o755))
	initial := []byte(`{"sidecar_id":"sb-old"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(parent, ".chunk", "sidecar.json"), initial, 0o644))

	// Save from child — should update parent's file, not create child's
	t.Chdir(child)
	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-new"}))

	// Child should have no .chunk dir
	_, err := os.Stat(filepath.Join(child, ".chunk"))
	assert.Assert(t, os.IsNotExist(err), "expected no .chunk in child")

	// Parent's file should be updated
	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-new")
}

func TestWorkspaceFieldRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

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

	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-1"}))

	data, err := os.ReadFile(filepath.Join(dir, ".chunk", sidecarFileName()))
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(data), "workspace"), "empty workspace should be omitted from JSON")
}

func TestResolveWorkspaceCLIFlagWins(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-1", Workspace: "/workspace/saved"}))

	got := resolveWorkspace("/workspace/override", "myrepo")
	assert.Equal(t, got, "/workspace/override")
}

func TestResolveWorkspaceSidecarFallback(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-1", Workspace: "/workspace/saved"}))

	got := resolveWorkspace("", "myrepo")
	assert.Equal(t, got, "/workspace/saved")
}

func TestResolveWorkspaceDefaultFallback(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	got := resolveWorkspace("", "myrepo")
	assert.Equal(t, got, "./workspace/myrepo")
}

func TestLoadForSessionFindsSessionFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".chunk"), 0o755))
	data := []byte(`{"sidecar_id":"sb-sess","name":"session-box"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".chunk", "sidecar.sess-123.json"), data, 0o644))

	got, err := LoadForSession("sess-123")
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-sess")
}

func TestLoadForSessionDoesNotSeeGenericFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// Only a generic sidecar.json exists — session-specific lookup should return nil.
	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-generic"}))

	got, err := LoadForSession("sess-abc")
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "session lookup should not see the generic sidecar file")
}

func TestLoadForSessionReturnsNilWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	got, err := LoadForSession("sess-xyz")
	assert.NilError(t, err)
	assert.Assert(t, got == nil)
}

func TestLoadForSessionEmptyIDDelegatesToLoadActive(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	assert.NilError(t, SaveActive(ActiveSidecar{SidecarID: "sb-generic"}))

	got, err := LoadForSession("")
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-generic")
}
