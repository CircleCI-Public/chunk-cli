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
// of the raw key string. Concurrent writes to the same key are safe: the
// content-hash naming makes them idempotent (same bytes, same filename).
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
// as needed. Files are written with mode 0600.
func (c FileCache[T]) Put(key string, v T) error {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(c.entryPath(key), data, 0o600)
}

func (c FileCache[T]) entryPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.Dir, hex.EncodeToString(sum[:])+".json")
}
