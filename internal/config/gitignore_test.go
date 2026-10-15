package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestEnsureGitignoreCreatesNew(t *testing.T) {
	dir := t.TempDir()

	added, err := EnsureGitignore(dir)
	assert.NilError(t, err)
	assert.Assert(t, added)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	assert.NilError(t, err)
	content := string(data)
	for _, pattern := range localStatePatterns {
		assert.Assert(t, strings.Contains(content, pattern), "missing %s", pattern)
	}
	assert.Assert(t, !strings.Contains(content, ".chunk/config.json"), "must not ignore config.json")
}

func TestEnsureGitignoreAppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	existing := "node_modules/\n.env\n"
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0o644))

	_, err := EnsureGitignore(dir)
	assert.NilError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	assert.NilError(t, err)
	content := string(data)
	assert.Assert(t, strings.HasPrefix(content, existing))
	for _, pattern := range localStatePatterns {
		assert.Assert(t, strings.Contains(content, pattern), "missing %s", pattern)
	}
}

func TestEnsureGitignoreIdempotent(t *testing.T) {
	dir := t.TempDir()

	_, err := EnsureGitignore(dir)
	assert.NilError(t, err)
	first, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	assert.NilError(t, err)

	added, err := EnsureGitignore(dir)
	assert.NilError(t, err)
	assert.Assert(t, !added)
	second, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	assert.NilError(t, err)

	assert.Equal(t, string(first), string(second))
}

func TestEnsureGitignoreAddsMissingNewline(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/"), 0o644))

	_, err := EnsureGitignore(dir)
	assert.NilError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	assert.NilError(t, err)
	content := string(data)
	assert.Assert(t, strings.HasPrefix(content, "node_modules/\n"), "missing newline before append")
	assert.Assert(t, strings.Contains(content, ".chunk/*-pool.json"))
}
