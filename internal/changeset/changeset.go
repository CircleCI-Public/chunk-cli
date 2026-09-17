// Package changeset holds the shape of an answer to one question: how far has a
// working tree moved from an earlier state.
//
// It is a type and nothing else, in its own package, because there is more than
// one way to find the answer and they must agree on what the answer looks like.
// gitutil measures with git, which knows what is ignored and how much of a file
// an edit touched. filestate measures by walking and hashing, for a tree git
// cannot answer for. A caller deciding what to do about a change should not have
// to know which of them produced it.
package changeset

const (
	// BaselineHead is the Baseline of a change measured against the last commit.
	BaselineHead = "HEAD"
	// BaselineWholeTree is the Baseline of a change measured against nothing at
	// all: there is no earlier state to compare with, so every file counts. It
	// is what a repository with no commits yet, or no git at all, produces the
	// first time it is measured.
	BaselineWholeTree = "whole tree"
)

// Changes summarises how far a working tree has moved from a baseline: which
// paths changed, and how much of them.
//
// It answers a different question from a fingerprint. A fingerprint says
// whether the tree is the same tree as before; this says how big the change is
// and what kind of files it touched — which is what a caller deciding how much
// caution a change deserves needs to know.
type Changes struct {
	// Paths is every path reported as changed, relative to the repo root.
	// Rename and copy entries name the destination only, since that is the file
	// now on disk.
	Paths []string
	// Lines is how many lines the change touches: insertions plus deletions
	// against the baseline for tracked files, and every line of a new file, all
	// of which are new. Binary content contributes no lines — there are none to
	// count — so a change can name paths and still report zero.
	Lines int
	// Baseline names what the change was measured against: BaselineHead, or an
	// opaque identifier for an earlier state. A caller reporting a number has to
	// be able to say what it is a number of, and the answers mean different
	// things.
	Baseline string
}

// Empty reports whether nothing has changed relative to the baseline.
func (c Changes) Empty() bool { return len(c.Paths) == 0 }

// Incremental reports whether the change was measured from an earlier state of
// this same working tree, rather than from a commit or from nothing.
//
// It is the difference between "you have changed 40 lines since the last time
// the checks passed" and "you are 40 lines from the last commit", which are the
// same number about different things.
func (c Changes) Incremental() bool {
	return c.Baseline != "" && c.Baseline != BaselineHead && c.Baseline != BaselineWholeTree
}
