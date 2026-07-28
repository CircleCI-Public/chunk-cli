package filecache_test

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/filecache"
)

type entry struct {
	Value string `json:"value"`
}

func TestFileCache_PutAndGet(t *testing.T) {
	c := filecache.FileCache[entry]{Dir: t.TempDir()}

	assert.NilError(t, c.Put("key1", entry{Value: "hello"}))

	got, ok := c.Get("key1")
	assert.Assert(t, ok)
	assert.Equal(t, got.Value, "hello")
}

func TestFileCache_Miss(t *testing.T) {
	c := filecache.FileCache[entry]{Dir: t.TempDir()}

	_, ok := c.Get("nonexistent")
	assert.Assert(t, !ok)
}

func TestFileCache_CorruptFile_ReportsMiss(t *testing.T) {
	dir := t.TempDir()
	c := filecache.FileCache[entry]{Dir: dir}

	assert.NilError(t, c.Put("key1", entry{Value: "ok"}))

	// Find and corrupt the entry file.
	entries, err := os.ReadDir(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 1)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, entries[0].Name()), []byte("{bad json"), 0o600))

	_, ok := c.Get("key1")
	assert.Assert(t, !ok, "corrupt entry should be treated as a miss")
}

func TestFileCache_DifferentKeys_DifferentEntries(t *testing.T) {
	c := filecache.FileCache[entry]{Dir: t.TempDir()}

	assert.NilError(t, c.Put("keyA", entry{Value: "a"}))
	assert.NilError(t, c.Put("keyB", entry{Value: "b"}))

	a, ok := c.Get("keyA")
	assert.Assert(t, ok)
	assert.Equal(t, a.Value, "a")

	b, ok := c.Get("keyB")
	assert.Assert(t, ok)
	assert.Equal(t, b.Value, "b")
}

func TestFileCache_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cache")
	c := filecache.FileCache[entry]{Dir: dir}

	assert.NilError(t, c.Put("k", entry{Value: "v"}))

	_, ok := c.Get("k")
	assert.Assert(t, ok)
}
