package gitutil

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// maxDigestBytes caps the total file content hashed into one fingerprint.
// Because the digest enumerates untracked files individually (see Fingerprint),
// a large un-gitignored tree — a build directory nobody remembered to ignore —
// would otherwise be re-read on every call. Past the budget fingerprinting
// fails, and callers fall back to doing the work they would have skipped.
// A var rather than a const so tests can shrink it.
var maxDigestBytes int64 = 64 << 20

// Worktree fingerprints the state of a git working tree at a point in time.
// Two Worktrees with equal Head and Digest describe trees with identical
// content, so callers can compare one against a previously recorded value to
// decide whether anything has changed since.
//
// The zero Worktree describes no tree at all. It reports the tree as not clean
// and is refused as a cache key, so a caller holding one falls back to doing
// whatever work it might otherwise have skipped.
type Worktree struct {
	// Head is the SHA of the current HEAD commit.
	Head string
	// Digest hashes every path git reports as changed, contents included.
	Digest string
	// Clean reports whether git sees no change at all relative to HEAD: nothing
	// modified, staged, or untracked.
	Clean bool
}

// Fingerprint captures the state of the working tree at dir: the HEAD commit
// plus a digest of the porcelain status entries — which record adds, deletions,
// renames and mode changes — and the current contents of every changed path.
//
// Hashing contents is what makes the fingerprint sensitive to repeated edits.
// Porcelain output for a modified file is byte-identical before and after a
// further edit (both report " M path"), so status alone would call the tree
// unchanged across that edit — exactly the loop a Stop hook runs in.
//
// The second return value is false when the tree's state cannot be established:
// dir is not a repo, the repo has no commits yet, a changed path cannot be read,
// or the tree exceeds the digest budget. The returned Worktree is then the zero
// value, which is safe to pass on: no caller can mistake it for a real tree.
func Fingerprint(dir string) (Worktree, bool) {
	head, err := HeadRef(dir)
	if err != nil {
		return Worktree{}, false
	}
	out, ok := gitOut(dir, "rev-parse", "--show-toplevel")
	if !ok {
		return Worktree{}, false
	}
	root := strings.TrimSpace(out)

	// Porcelain paths are relative to the repo root regardless of dir. -z leaves
	// exotic paths unquoted so they can be opened, and -uall lists untracked
	// files individually rather than collapsing them into a single "dir/" entry,
	// so their contents are hashed too.
	status, ok := gitOut(dir, "status", "--porcelain", "-z", "-uall")
	if !ok {
		return Worktree{}, false
	}

	digest, ok := digestTree(root, status)
	if !ok {
		return Worktree{}, false
	}
	return Worktree{Head: head, Digest: digest, Clean: status == ""}, true
}

// digestTree hashes the porcelain status and the contents of each path it names.
func digestTree(root, status string) (string, bool) {
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

// writePart mixes s into h length-prefixed, so that concatenations of different
// parts cannot collide (["ab","c"] and ["a","bc"] hash differently).
func writePart(h io.Writer, s string) {
	_, _ = fmt.Fprintf(h, "%d:", len(s))
	_, _ = io.WriteString(h, s)
}

func gitOut(dir string, args ...string) (string, bool) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}
