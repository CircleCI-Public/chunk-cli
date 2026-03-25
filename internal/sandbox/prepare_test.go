package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func TestDetectPackageManager(t *testing.T) {
	t.Run("pnpm", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte("lockfile: true"), 0o644)
		pm := detectPackageManager(dir)
		assert.Assert(t, pm != nil)
		assert.Equal(t, pm.name, "pnpm")
		assert.Equal(t, pm.installCommand, "pnpm install")
	})

	t.Run("npm", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o644)
		pm := detectPackageManager(dir)
		assert.Assert(t, pm != nil)
		assert.Equal(t, pm.name, "npm")
	})

	t.Run("none", func(t *testing.T) {
		dir := t.TempDir()
		pm := detectPackageManager(dir)
		assert.Assert(t, pm == nil)
	})
}

func TestGatherRepoContext(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test"}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test"), 0o644)

	ctx := gatherRepoContext(dir)
	assert.Assert(t, len(ctx) > 0)
	assert.Assert(t, containsSubstring(ctx, "package.json"))
	assert.Assert(t, containsSubstring(ctx, "go.mod"))
}

func TestGatherDockerfiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "Dockerfile.chunk"), []byte("FROM node"), 0o644) // should be skipped

	result := gatherDockerfiles(dir)
	assert.Assert(t, containsSubstring(result, "FROM alpine"))
	assert.Assert(t, !containsSubstring(result, "FROM node"))
}

func TestUniqueDockerfileName(t *testing.T) {
	dir := t.TempDir()
	assert.Equal(t, uniqueDockerfileName(dir), "Dockerfile.chunk")

	_ = os.WriteFile(filepath.Join(dir, "Dockerfile.chunk"), []byte(""), 0o644)
	assert.Equal(t, uniqueDockerfileName(dir), "Dockerfile.chunk.1")
}

func TestStripMarkdownFences(t *testing.T) {
	assert.Equal(t, stripMarkdownFences("```json\n[]\n```"), "[]")
	assert.Equal(t, stripMarkdownFences("[]"), "[]")
	assert.Equal(t, stripMarkdownFences("```\nhello\n```"), "hello")
}

func containsSubstring(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
