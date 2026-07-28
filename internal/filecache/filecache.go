package filecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// FileCache is a generic JSON-on-disk cache keyed by arbitrary strings.
// Each entry is stored as a single file whose name is the hex-encoded SHA-256
// of the raw key string. Concurrent writes to the same key are safe: Put stages
// the entry in its own temporary file and renames it into place, so a reader
// sees either the previous entry or a complete new one, never a mixture. Two
// writers racing on one key both produce valid entries and the last rename
// wins.
type FileCache[T any] struct {
	Dir string
}

// Get returns the cached value for key. Returns the zero value and false on a
// cache miss or any read/unmarshal error; errors are treated as misses so a
// corrupt entry never blocks execution.
func (c FileCache[T]) Get(key string) (T, bool) {
	var zero T
	data, err := os.ReadFile(c.entryPath(key))
	if err != nil {
		return zero, false
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return zero, false
	}
	return v, true
}

// Put writes v to the cache under key, creating Dir and any parent directories
// as needed. The entry is staged in a temporary file alongside its final path
// and renamed into place, which is atomic within a single directory. Entries
// have mode 0600, the mode os.CreateTemp applies and the rename preserves.
func (c FileCache[T]) Put(key string, v T) error {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(c.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := writeAndClose(f, data); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, c.entryPath(key)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// writeAndClose writes data to f and closes it, returning the first error. The
// close error matters here: on some filesystems a deferred write only surfaces
// then, and renaming a short file into place would publish a corrupt entry.
func writeAndClose(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (c FileCache[T]) entryPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.Dir, hex.EncodeToString(sum[:])+".json")
}
