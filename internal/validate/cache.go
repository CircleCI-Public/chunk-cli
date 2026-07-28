package validate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// ResultCache is a read/write store for validate run outcomes.
type ResultCache interface {
	Get(key string) (CachedResult, bool)
	Put(key string, r CachedResult) error
}

// CachedResult records the timestamp of a successful validate run. Only
// successful runs are cached; failures are never stored so the agent always
// retries after a fix, even when the working tree has not changed.
type CachedResult struct {
	CachedAt time.Time `json:"cached_at"`
}

// CacheKey builds a content-addressed key from a name and ordered content
// parts. Parts are length-prefixed before hashing to prevent boundary
// collisions (["ab","c"] and ["a","bc"] produce different keys).
func CacheKey(name string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		writePart(h, p)
	}
	return name + "\x00" + hex.EncodeToString(h.Sum(nil))
}

// BuildCacheKey constructs the cache key for a validate run from the
// serialized commands, the HEAD commit SHA, and a digest of the working tree.
// commandName is "" when all commands are run.
//
// The second return value is false when the git state cannot be established
// (not a repo, no commits yet, or a changed path that cannot be read). Callers
// must not read or write the cache in that case: with no trustworthy git state
// the key would depend only on the config and would therefore stay stable
// across code changes, turning every subsequent run into a false cache hit.
func BuildCacheKey(workDir, commandName string, commands []config.Command) (string, bool) {
	head, ok := gitOut(workDir, "rev-parse", "HEAD")
	if !ok {
		return "", false
	}
	tree, ok := worktreeDigest(workDir)
	if !ok {
		return "", false
	}
	cfgBytes, err := json.Marshal(commands)
	if err != nil {
		return "", false
	}
	return CacheKey(commandName, sha256hex(cfgBytes), strings.TrimSpace(head), tree), true
}

// worktreeDigest hashes the state of every path git reports as changed: the
// porcelain status entries, which record adds, deletions, renames and mode
// changes, plus the current contents of each changed path.
//
// Hashing contents is what makes the key sensitive to repeated edits. Porcelain
// output for a modified file is byte-identical before and after a further edit
// (both report " M path"), so status alone would let the second edit reuse the
// first run's result — exactly the loop a Stop hook runs in.
func worktreeDigest(workDir string) (string, bool) {
	root, ok := gitOut(workDir, "rev-parse", "--show-toplevel")
	if !ok {
		return "", false
	}

	// Porcelain paths are relative to the repo root regardless of workDir. -z
	// leaves exotic paths unquoted so they can be opened, and -uall lists
	// untracked files individually rather than collapsing them into a single
	// "dir/" entry, so their contents are hashed too.
	status, ok := gitOut(workDir, "status", "--porcelain", "-z", "-uall")
	if !ok {
		return "", false
	}

	h := sha256.New()
	writePart(h, status)
	for _, rel := range changedPaths(status) {
		writePart(h, rel)
		if !hashFile(h, filepath.Join(strings.TrimSpace(root), rel)) {
			return "", false
		}
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// changedPaths extracts the paths from -z porcelain status output. Entries are
// "XY <path>\x00"; rename and copy entries carry an extra "<origPath>\x00"
// field that is skipped, since nothing exists under the original name.
func changedPaths(status string) []string {
	fields := strings.Split(status, "\x00")
	var paths []string
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		// Every entry is at least "XY " plus one path character; anything
		// shorter is the empty string trailing the final separator.
		if len(f) < 4 {
			continue
		}
		paths = append(paths, f[3:])
		if f[0] == 'R' || f[0] == 'C' || f[1] == 'R' || f[1] == 'C' {
			i++
		}
	}
	return paths
}

// hashFile mixes the contents of path into h as a fixed-width digest, keeping
// part boundaries unambiguous. A missing file is not a failure: deletions and
// renames are already recorded in the status entry. Anything else — an
// unreadable file, or a non-regular path such as a dirty submodule — means the
// digest would not reflect that path's state, so it reports false.
func hashFile(h io.Writer, path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	fh := sha256.New()
	if _, err := io.Copy(fh, f); err != nil {
		return false
	}
	_, _ = h.Write(fh.Sum(nil))
	return true
}

func writePart(h io.Writer, s string) {
	_, _ = fmt.Fprintf(h, "%d:", len(s))
	_, _ = io.WriteString(h, s)
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func gitOut(workDir string, args ...string) (string, bool) {
	cmdArgs := append([]string{"-C", workDir}, args...)
	out, err := exec.Command("git", cmdArgs...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}
