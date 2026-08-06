package filecache_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

// backdate rewrites a file's modification time, standing in for an entry written
// on an earlier day.
func backdate(t *testing.T, path string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	assert.NilError(t, os.Chtimes(path, when, when))
}

// TestFileCache_Put_SweepsStaleEntries covers the growth that content-addressed
// keys cause: every new key writes a new file and nothing ever supersedes an old
// one in place, so without a sweep the directory accumulates one entry per write
// forever.
func TestFileCache_Put_SweepsStaleEntries(t *testing.T) {
	dir := t.TempDir()
	c := filecache.FileCache[entry]{Dir: dir, MaxAge: 24 * time.Hour}

	assert.NilError(t, c.Put("old", entry{Value: "old"}))
	entries, err := os.ReadDir(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 1)
	backdate(t, filepath.Join(dir, entries[0].Name()), 48*time.Hour)

	// The write that follows is what collects it.
	assert.NilError(t, c.Put("new", entry{Value: "new"}))

	_, ok := c.Get("old")
	assert.Assert(t, !ok, "an entry past MaxAge must be swept")
	got, ok := c.Get("new")
	assert.Assert(t, ok, "the entry just written must survive its own sweep")
	assert.Equal(t, got.Value, "new")
}

func TestFileCache_Put_KeepsFreshEntries(t *testing.T) {
	dir := t.TempDir()
	c := filecache.FileCache[entry]{Dir: dir, MaxAge: 24 * time.Hour}

	assert.NilError(t, c.Put("keep", entry{Value: "keep"}))
	assert.NilError(t, c.Put("other", entry{Value: "other"}))

	_, ok := c.Get("keep")
	assert.Assert(t, ok, "an entry within MaxAge must not be swept")
}

// TestFileCache_Put_SweepsOrphanedTempFiles covers a write interrupted between
// staging and rename — an interrupted Stop hook. The staged file is never named
// as an entry, so nothing would ever look at it again.
func TestFileCache_Put_SweepsOrphanedTempFiles(t *testing.T) {
	dir := t.TempDir()
	c := filecache.FileCache[entry]{Dir: dir}

	orphan := filepath.Join(dir, ".tmp-abandoned")
	assert.NilError(t, os.WriteFile(orphan, []byte(`{"value":"partial`), 0o600))
	backdate(t, orphan, 2*time.Hour)

	fresh := filepath.Join(dir, ".tmp-inflight")
	assert.NilError(t, os.WriteFile(fresh, []byte(`{"value":"partial`), 0o600))

	assert.NilError(t, c.Put("k", entry{Value: "v"}))

	_, err := os.Stat(orphan)
	assert.Assert(t, os.IsNotExist(err), "an orphaned staged file must be swept")
	// A staged file from a writer still running is seconds old; sweeping it would
	// break that concurrent Put.
	_, err = os.Stat(fresh)
	assert.NilError(t, err, "a staged file still being written must be left alone")
}

// TestFileCache_Put_LeavesForeignFilesAlone keeps the sweep to files it owns: the
// cache directory is shared with nothing today, but deleting an unrecognised file
// is not a cache's business.
func TestFileCache_Put_LeavesForeignFilesAlone(t *testing.T) {
	dir := t.TempDir()
	c := filecache.FileCache[entry]{Dir: dir, MaxAge: time.Hour}

	foreign := filepath.Join(dir, "README.txt")
	assert.NilError(t, os.WriteFile(foreign, []byte("notes"), 0o600))
	backdate(t, foreign, 30*24*time.Hour)

	assert.NilError(t, c.Put("k", entry{Value: "v"}))

	_, err := os.Stat(foreign)
	assert.NilError(t, err, "a file the cache did not write must be left alone")
}

func TestFileCache_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cache")
	c := filecache.FileCache[entry]{Dir: dir}

	assert.NilError(t, c.Put("k", entry{Value: "v"}))

	_, ok := c.Get("k")
	assert.Assert(t, ok)
}
