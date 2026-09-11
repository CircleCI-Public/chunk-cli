// Package filestate measures a working tree without asking git anything.
//
// It exists because git is not always there to ask. A repository with no
// commits has no HEAD to diff against, and a directory that was never a
// repository has nothing at all — and both are trees an agent can be working
// in. More of them will be: keeping track of what changed without a commit
// history is becoming its own problem, and a resident daemon watching a
// filesystem is the shape of the answer.
//
// Where git can answer, it should: it knows what is ignored, and it knows how
// much of a modified file an edit actually touched. This does not. See Changes
// for what that costs and which way it errs.
package filestate

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/changeset"
	"github.com/CircleCI-Public/chunk-cli/internal/hashutil"
)

// Walk budgets. Past either of them Build fails rather than returning a partial
// index, because a partial index measures a smaller change than the real one —
// and a change that reads smaller than it is, is a change that gets waved
// through. Failing makes the caller wait instead.
var (
	maxFiles       = 20000
	maxBytes int64 = 64 << 20
)

// ErrWalkBudget reports that the tree holds more than the walk is willing to read.
var ErrWalkBudget = errors.New("working tree exceeds the walk budget")

// Entry is one file's contribution to the state of a tree.
type Entry struct {
	// Hash is the SHA-256 of the file's contents, which is what makes a
	// modification detectable without keeping the contents themselves.
	Hash string
	// Lines is how many lines the file has. Binary content reports none.
	Lines int
}

// Index is the state of a working tree: every file it holds, by path relative
// to the root, in slash form on every platform.
type Index map[string]Entry

// Build walks the tree at root and hashes it.
//
// Skipping is deliberately minimal: .git, and whatever the root .gitignore
// names in one of the three forms that cannot be misread — a bare name, a
// directory, or *.ext. Anything it is unsure about is walked and counted. That
// is the wrong way round for speed and the right way round for safety, since
// every file wrongly skipped makes the change look smaller than it is. The
// budgets above are what keep the honest version bounded.
func Build(root string) (Index, error) {
	// Checked before the walk, because WalkDir reports a missing root through the
	// same callback as a missing file deep in the tree — and the tree's own
	// absence must not come back as "nothing has changed here".
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("read %s: not a directory", root)
	}

	skip := loadIgnores(root)
	idx := make(Index)

	var (
		files int
		bytes int64
	)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that cannot be read is skipped rather than fatal: a
			// permissions hole somewhere in a tree should not stop the rest of it
			// being measured.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			if skip.dir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || skip.file(d.Name()) {
			return nil
		}

		files++
		if files > maxFiles {
			return ErrWalkBudget
		}
		hash, lines, n, hashErr := hashAndCount(path, maxBytes-bytes)
		if hashErr != nil {
			return hashErr
		}
		bytes += n
		idx[rel] = Entry{Hash: hash, Lines: lines}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	return idx, nil
}

// Changes reports how far this index has moved from an earlier one. A nil
// before means there is no earlier state, so the whole tree is the change.
//
// A modified file counts its whole length rather than the lines that actually
// changed. Without the earlier contents there is no way to know how much of it
// moved — only that it did — so it counts the most it could have, which
// over-measures a one-line edit in a long file as the whole file. That is the
// direction to be wrong in: an over-measured change is waited for, and an
// under-measured one is waved through. Where git can answer, it answers better;
// this is for the trees it cannot.
func (i Index) Changes(before Index) changeset.Changes {
	baseline := changeset.BaselineWholeTree
	if before != nil {
		baseline = "earlier state"
	}
	ch := changeset.Changes{Baseline: baseline}

	for path, now := range i {
		was, existed := before[path]
		switch {
		case !existed:
			ch.Paths = append(ch.Paths, path)
			ch.Lines += now.Lines
		case was.Hash != now.Hash:
			ch.Paths = append(ch.Paths, path)
			ch.Lines += max(was.Lines, now.Lines)
		}
	}
	for path, was := range before {
		if _, still := i[path]; !still {
			ch.Paths = append(ch.Paths, path)
			ch.Lines += was.Lines
		}
	}
	// Sorted so the same change always reports the same way: map order is
	// random, and a path list that shuffles between runs is unreadable in output
	// and untestable in a test.
	sort.Strings(ch.Paths)
	return ch
}

// Digest reduces an index to one value, so two states of a tree can be
// compared without keeping either of them.
//
// It is the git-free equivalent of a worktree fingerprint, for the same use:
// deciding whether a tree has moved while something else was looking away.
// Paths are folded in sorted order, since map iteration is random and a digest
// that depended on it would report every tree as changed.
func (i Index) Digest() string {
	paths := make([]string, 0, len(i))
	for path := range i {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, path := range paths {
		hashutil.WritePart(h, path)
		hashutil.WritePart(h, i[path].Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashAndCount reads path once, returning its content hash, its line count and
// how many bytes it read.
//
// One pass for both, because the file is being read anyway and a second pass to
// count lines would double the cost of every walk. Binary content reports no
// lines, on git's own rule of thumb — a NUL byte near the start — since counting
// newlines in a compiled object produces a number with no meaning.
func hashAndCount(path string, remaining int64) (string, int, int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, 0, nil // vanished between the walk and the read
	}
	if info.Size() > remaining {
		return "", 0, 0, ErrWalkBudget
	}

	f, err := os.Open(path)
	if err != nil {
		return "", 0, 0, nil // unreadable: counted as absent rather than fatal
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	var (
		buf      = make([]byte, 64<<10)
		lines    int
		read     int64
		first    = true
		binary   bool
		lastByte byte
	)
	reader := bufio.NewReader(f)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			_, _ = h.Write(chunk)
			if first {
				first = false
				binary = bytes.IndexByte(chunk, 0) >= 0
			}
			if !binary {
				lines += bytes.Count(chunk, []byte{'\n'})
				lastByte = chunk[len(chunk)-1]
			}
			read += int64(n)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", 0, read, readErr
		}
	}
	if !binary && read > 0 && lastByte != '\n' {
		lines++ // a final line with no newline after it is still a line
	}
	return hex.EncodeToString(h.Sum(nil)), lines, read, nil
}

// ignores is the small set of things a walk skips.
type ignores struct {
	dirs  map[string]bool
	names map[string]bool
	exts  map[string]bool
}

func (ig *ignores) dir(name string) bool {
	return name == ".git" || ig.dirs[name]
}

func (ig *ignores) file(name string) bool {
	return ig.names[name] || ig.exts[strings.ToLower(filepath.Ext(name))]
}

// loadIgnores reads the root .gitignore, taking only the patterns that cannot
// be misread.
//
// Negations, anchors, nested .gitignore files and ** globs are all skipped
// rather than half-implemented. A pattern understood wrongly either hides a
// file that changed — which is the failure mode this package must not have — or
// walks a directory it was told not to, which merely costs time.
func loadIgnores(root string) *ignores {
	ig := &ignores{
		dirs:  make(map[string]bool),
		names: make(map[string]bool),
		exts:  make(map[string]bool),
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return ig
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		if strings.Contains(line, "**") || strings.Count(line, "/") > 1 {
			continue
		}
		if after, ok := strings.CutSuffix(line, "/"); ok {
			if !strings.Contains(after, "*") {
				ig.dirs[strings.TrimPrefix(after, "/")] = true
			}
			continue
		}
		if strings.Contains(line, "/") {
			continue
		}
		if ext, ok := strings.CutPrefix(line, "*"); ok {
			if strings.HasPrefix(ext, ".") && !strings.Contains(ext, "*") {
				ig.exts[strings.ToLower(ext)] = true
			}
			continue
		}
		if !strings.ContainsAny(line, "*?[") {
			// A bare name is both a file name and a directory name in git, and it
			// is read as both here.
			ig.names[line] = true
			ig.dirs[line] = true
		}
	}
	return ig
}
