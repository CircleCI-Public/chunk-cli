package filecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultMaxAge is how long an entry survives when MaxAge is unset. Keys are
// content-addressed, so a superseded entry is never read again — it only has to
// outlive the chance of the caller returning to that exact state.
const DefaultMaxAge = 7 * 24 * time.Hour

// tmpPrefix marks a staged entry that has not been renamed into place yet, and
// entrySuffix a published one. Only files matching one of the two are swept, so
// anything else sharing the directory is left alone.
const (
	tmpPrefix   = ".tmp-"
	entrySuffix = ".json"
)

// tmpGrace is how long a staged file is left alone before the sweep treats it as
// orphaned. A staged file whose writer is still running is seconds old, so this
// only ever collects leftovers from a process that died mid-write. Unlike an
// entry, an orphan has no value at all, hence the much shorter window.
const tmpGrace = time.Hour

// FileCache is a generic JSON-on-disk cache keyed by arbitrary strings.
// Each entry is stored as a single file whose name is the hex-encoded SHA-256
// of the raw key string. Concurrent writes to the same key are safe: Put stages
// the entry in its own temporary file and renames it into place, so a reader
// sees either the previous entry or a complete new one, never a mixture. Two
// writers racing on one key both produce valid entries and the last rename
// wins.
//
// A new key means a new file, and nothing supersedes the entries it replaces, so
// with content-addressed keys the directory would grow one file per write forever.
// Put sweeps it; see MaxAge.
type FileCache[T any] struct {
	Dir string
	// MaxAge bounds how long an entry survives after it was written. Zero means
	// DefaultMaxAge. Nothing switches the sweep off: an unbounded cache
	// directory is a leak rather than a feature.
	MaxAge time.Duration
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
//
// Put then sweeps Dir, so the cache is bounded by writes to it rather than by
// anyone remembering to clean it out.
func (c FileCache[T]) Put(key string, v T) error {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(c.Dir, tmpPrefix+"*")
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
	c.sweep()
	return nil
}

// sweep removes entries older than MaxAge, along with staged files orphaned by a
// process that died between staging and rename — which is what happens when a
// Stop hook is interrupted mid-write.
//
// Best effort throughout: a failed sweep is not a failed Put, since the entry the
// caller asked for is already in place. The entry just written is the newest file
// in the directory, so it always survives.
func (c FileCache[T]) sweep() {
	maxAge := c.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		var cutoff time.Time
		switch {
		case strings.HasPrefix(name, tmpPrefix):
			cutoff = now.Add(-tmpGrace)
		case strings.HasSuffix(name, entrySuffix):
			cutoff = now.Add(-maxAge)
		default:
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(c.Dir, name))
	}
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
	return filepath.Join(c.Dir, hex.EncodeToString(sum[:])+entrySuffix)
}
