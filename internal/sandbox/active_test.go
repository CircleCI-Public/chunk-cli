package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func TestSaveAndLoadActive(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	want := ActiveSandbox{SandboxID: "sb-abc", Name: "my-box"}
	err := SaveActive(want)
	assert.NilError(t, err)

	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil, "expected non-nil ActiveSandbox")
	assert.Equal(t, got.SandboxID, want.SandboxID)
	assert.Equal(t, got.Name, want.Name)
}

func TestLoadActiveReturnsNilWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "expected nil when no active sandbox file")
}

func TestLoadActiveWalksUpToParent(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "sub", "dir")
	assert.NilError(t, os.MkdirAll(child, 0o755))

	// Mark parent as a git root so the walk doesn't escape it.
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".git"), 0o755))

	// Write .chunk/sandbox in parent
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".chunk"), 0o755))
	data := []byte(`{"sandbox_id":"sb-parent","name":"parent-box"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(parent, ".chunk", "sandbox"), data, 0o644))

	// cd into child — should still find the parent's file
	t.Chdir(child)

	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SandboxID, "sb-parent")
}

func TestLoadActiveStopsAtGitRoot(t *testing.T) {
	// grandparent has .chunk/sandbox; parent has .git; cwd is child of parent.
	// The walk should stop at parent and not find grandparent's file.
	grandparent := t.TempDir()
	parent := filepath.Join(grandparent, "repo")
	child := filepath.Join(parent, "sub")
	assert.NilError(t, os.MkdirAll(child, 0o755))

	// .git lives in parent (the repo root)
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".git"), 0o755))

	// .chunk/sandbox lives in grandparent (above the repo root)
	assert.NilError(t, os.MkdirAll(filepath.Join(grandparent, ".chunk"), 0o755))
	data := []byte(`{"sandbox_id":"sb-grandparent"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(grandparent, ".chunk", "sandbox"), data, 0o644))

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

	// .chunk/sandbox in parent but no .git anywhere
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".chunk"), 0o755))
	data := []byte(`{"sandbox_id":"sb-parent"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(parent, ".chunk", "sandbox"), data, 0o644))

	t.Chdir(child)

	// Without .git the walk stops at cwd, so the parent file is not found.
	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "without a git repo the walk should not go above cwd")
}

func TestClearActive(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	assert.NilError(t, SaveActive(ActiveSandbox{SandboxID: "sb-xyz"}))

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

func TestSessionKeyedSandbox(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// Save without a session — generic file.
	assert.NilError(t, SaveActive(ActiveSandbox{SandboxID: "sb-generic"}))

	// With a session ID set, load should return nil (isolated from the generic file).
	t.Setenv("CLAUDE_SESSION_ID", "sess-abc")
	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "session-keyed load should not see generic file")

	// Save under the session.
	assert.NilError(t, SaveActive(ActiveSandbox{SandboxID: "sb-session"}))

	got, err = LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SandboxID, "sb-session")

	// Without the session env var, the original generic file is still intact.
	t.Setenv("CLAUDE_SESSION_ID", "")
	got, err = LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SandboxID, "sb-generic")
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

	// Write an existing .chunk/sandbox in parent
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".chunk"), 0o755))
	initial := []byte(`{"sandbox_id":"sb-old"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(parent, ".chunk", "sandbox"), initial, 0o644))

	// Save from child — should update parent's file, not create child's
	t.Chdir(child)
	assert.NilError(t, SaveActive(ActiveSandbox{SandboxID: "sb-new"}))

	// Child should have no .chunk dir
	_, err := os.Stat(filepath.Join(child, ".chunk"))
	assert.Assert(t, os.IsNotExist(err), "expected no .chunk in child")

	// Parent's file should be updated
	got, err := LoadActive()
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SandboxID, "sb-new")
}
