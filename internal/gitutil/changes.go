package gitutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// maxCountBytes caps the total untracked content read to count lines. Untracked
// files have to be read to be measured — there is no diff to ask git for — and a
// large un-gitignored tree would otherwise be walked in full on every call. Past
// the budget the count fails, and callers fall back to whatever they do when the
// tree cannot be measured. A var rather than a const so tests can shrink it.
var maxCountBytes int64 = 16 << 20

// ErrCountBudget reports that the changed files hold more content than the line
// count is willing to read.
var ErrCountBudget = errors.New("changed files exceed the line count budget")

// Changes summarises how far a working tree has moved from HEAD: which paths
// changed, and how much of them.
//
// It answers a different question from Worktree. A fingerprint says whether the
// tree is the same tree as before; this says how big the change is and what kind
// of files it touched — which is what a caller deciding how much caution a
// change deserves needs to know.
type Changes struct {
	// Paths is every path git reports as changed, relative to the repo root.
	// Rename and copy entries name the destination only, since that is the file
	// now on disk.
	Paths []string
	// Lines is how many lines the change touches: insertions plus deletions
	// against HEAD for tracked files, and every line of an untracked file, all
	// of which are new. Binary content contributes no lines — there are none to
	// count — so a change can name paths and still report zero.
	Lines int
}

// Empty reports whether git sees no change at all relative to HEAD.
func (c Changes) Empty() bool { return len(c.Paths) == 0 }

// WorkingChanges measures the working tree at dir against HEAD.
//
// Tracked files are measured with `git diff --shortstat HEAD`, which counts
// staged and unstaged edits together — the same total a developer would see
// running the command by hand. Untracked files are counted here instead, because
// git will not diff a file it does not know about, and a new file is exactly the
// change a caller most wants counted.
//
// The error is non-nil when the tree's size cannot be established: dir is not a
// repo, the repo has no commits yet, or the untracked content exceeds the read
// budget. The returned Changes is then the zero value, which reports no paths
// and no lines — never mistake it for a small change, since it is the same
// value a clean tree produces.
func WorkingChanges(dir string) (Changes, error) {
	out, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Changes{}, fmt.Errorf("resolve repo root: %w", err)
	}
	root := strings.TrimSpace(out)

	// Same status invocation Fingerprint uses, and for the same reasons: -z
	// leaves exotic paths openable, and -uall lists untracked files one by one
	// rather than collapsing a directory into a single entry.
	status, err := gitOut(dir, "status", "--porcelain", "-z", "-uall")
	if err != nil {
		return Changes{}, fmt.Errorf("read git status: %w", err)
	}
	entries := parseStatus(status)
	if len(entries) == 0 {
		return Changes{}, nil
	}

	ch := Changes{Paths: make([]string, 0, len(entries))}
	remaining := maxCountBytes
	for _, e := range entries {
		ch.Paths = append(ch.Paths, e.Path)
		if e.X != '?' || e.Y != '?' {
			continue
		}
		lines, read, err := countLines(filepath.Join(root, e.Path), remaining)
		if err != nil {
			return Changes{}, fmt.Errorf("count %s: %w", e.Path, err)
		}
		remaining -= read
		ch.Lines += lines
	}

	tracked, err := trackedLines(dir)
	if err != nil {
		return Changes{}, err
	}
	ch.Lines += tracked
	return ch, nil
}

// shortstatCounts pulls the insertion and deletion totals out of shortstat
// output. Either half is absent when the change is all one direction.
var shortstatCounts = regexp.MustCompile(`(\d+) (insertion|deletion)`)

// trackedLines reports how many lines the tracked changes at dir touch,
// insertions and deletions together.
//
// Deletions count towards the total even though removing code rarely breaks a
// build, because the alternative is treating a change that deletes a thousand
// lines as no change at all. A caller wanting the two apart would need the
// numbers separately, and nothing yet does.
func trackedLines(dir string) (int, error) {
	out, err := gitOut(dir, "diff", "--shortstat", "HEAD")
	if err != nil {
		return 0, fmt.Errorf("read git diff: %w", err)
	}
	total := 0
	for _, m := range shortstatCounts.FindAllStringSubmatch(out, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			// Unreachable for real shortstat output, and a malformed count is no
			// reason to fail the whole measurement.
			continue
		}
		total += n
	}
	return total, nil
}

// countLines reports the number of lines in path and how many bytes it read to
// find out.
//
// A missing file is not a failure: status records deletions, and a path can go
// away between the status call and this read. A non-regular path — a symlink, a
// socket, a dirty submodule — has no lines of its own, and is not read.
//
// Binary content reports no lines. Counting newlines in a compiled binary
// produces a number with no meaning, and a caller weighing "how large is this
// change" is better served by a zero it can reason about than by a byte tally
// dressed up as lines. Detection matches git's own rule of thumb: a NUL byte
// near the start of the file.
func countLines(path string, remaining int64) (int, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, 0, nil
	}
	if info.Size() > remaining {
		return 0, 0, ErrCountBudget
	}

	var (
		buf      = make([]byte, 64<<10)
		lines    int
		read     int64
		first    = true
		lastByte byte
	)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if first {
				first = false
				if bytes.IndexByte(chunk, 0) >= 0 {
					return 0, read + int64(n), nil
				}
			}
			lines += bytes.Count(chunk, []byte{'\n'})
			lastByte = chunk[len(chunk)-1]
			read += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, read, err
		}
	}
	// A final line with no newline after it is still a line.
	if read > 0 && lastByte != '\n' {
		lines++
	}
	return lines, read, nil
}
