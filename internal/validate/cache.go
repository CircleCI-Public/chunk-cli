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
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// maxDigestBytes caps the total file content hashed into one working-tree
// digest. Because the digest enumerates untracked files individually (see
// worktreeDigest), a large un-gitignored tree — a build directory nobody
// remembered to ignore — would otherwise be re-read on every hook invocation.
// Past the budget the digest fails closed: no key, no cache, commands run.
// A var rather than a const so tests can shrink it.
var maxDigestBytes int64 = 64 << 20

// ResultCache is a read/write store for validate run outcomes.
type ResultCache interface {
	Get(key string) (CachedResult, bool)
	Put(key string, r CachedResult) error
}

// CachedResult records the timestamp of a successful validate run. Only
// successful runs are cached; failures are never stored so the agent always
// retries after a fix, even when the working tree has not changed.
//
// CachedAt is informational: presence of the entry is what marks a run as
// successful, and nothing currently reads the timestamp. It exists as a
// debugging breadcrumb and a hook for a future TTL.
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

// CacheKeyInputs collects everything outside the working tree that can change
// the outcome of a validate run.
type CacheKeyInputs struct {
	// WorkDir is the directory the run was invoked from; git state is resolved
	// relative to it.
	WorkDir string
	// CommandName is the single command being run, or "" when all commands run.
	CommandName string
	// Commands is the configured command set.
	Commands []config.Command
	// Target identifies where the commands execute: "" for a local run,
	// otherwise an opaque description of the sidecar. Sidecar routing depends on
	// mutable state outside the repo, so it has to participate in the key — a
	// working tree validated against one sidecar says nothing about another.
	Target string
}

// BuildCacheKey constructs the cache key for a validate run from the serialized
// commands, the execution target, the HEAD commit SHA, and a digest of the
// working tree.
//
// The second return value is false when the git state cannot be established
// (not a repo, no commits yet, a changed path that cannot be read, or a working
// tree too large to hash within maxDigestBytes). Callers must not read or write
// the cache in that case: with no trustworthy git state the key would depend
// only on the config and would therefore stay stable across code changes,
// turning every subsequent run into a false cache hit.
func BuildCacheKey(in CacheKeyInputs) (string, bool) {
	head, err := gitutil.HeadRef(in.WorkDir)
	if err != nil {
		return "", false
	}
	tree, ok := worktreeDigest(in.WorkDir)
	if !ok {
		return "", false
	}
	cfgBytes, err := json.Marshal(in.Commands)
	if err != nil {
		return "", false
	}
	return CacheKey(in.CommandName, sha256hex(cfgBytes), in.Target, head, tree), true
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
	out, ok := gitOut(workDir, "rev-parse", "--show-toplevel")
	if !ok {
		return "", false
	}
	root := strings.TrimSpace(out)

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
	remaining := maxDigestBytes
	for _, rel := range changedPaths(status) {
		writePart(h, rel)
		n, ok := hashFile(h, filepath.Join(root, rel), remaining)
		if !ok {
			return "", false
		}
		remaining -= n
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
// part boundaries unambiguous, and reports how many bytes it read. A missing
// file is not a failure: deletions and renames are already recorded in the
// status entry. Anything else — an unreadable file, or a non-regular path such
// as a dirty submodule — means the digest would not reflect that path's state,
// so it reports false.
//
// A file larger than remaining is refused without being read, so exceeding the
// digest budget costs a stat rather than a full pass over the file.
func hashFile(h io.Writer, path string, remaining int64) (int64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, errors.Is(err, os.ErrNotExist)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	if info.Size() > remaining {
		return 0, false
	}
	fh := sha256.New()
	n, err := io.Copy(fh, f)
	if err != nil {
		return 0, false
	}
	_, _ = h.Write(fh.Sum(nil))
	return n, true
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
