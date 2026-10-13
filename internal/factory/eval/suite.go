// Package eval scores a directory of review prompts against a suite of changes
// whose problems are known, so prompts can be compared by what they catch and
// what they send back that they should not.
//
// Each case is a change to a repository at a pinned revision: a patch, the
// request it answers, and the problems a good review should find in it. A case
// that expects none is clean, and any high or medium finding on it sends sound
// work back for nothing. Reviews run as factory runs them in its first round,
// against the patch as uncommitted work, but with no implementer: the score is
// the reviews' alone.
package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

// SuiteFile is the file in a suite directory that lists its cases.
const SuiteFile = "suite.json"

// caseNameRe is a case's name: it names files of results, so it is kept plain.
var caseNameRe = regexp.MustCompile(`^[a-z0-9]+([-_][a-z0-9]+)*$`)

// Suite is a set of cases on one repository.
type Suite struct {
	Dir   string
	Cases []Case
}

// Case is one change to review.
type Case struct {
	Name string `json:"name"`
	// Revision is the commit the patch applies to.
	Revision string `json:"revision"`
	// Request is the change the patch answers, which reviewers are given as
	// the specification, as factory gives them the developer's prompt.
	Request string `json:"request"`
	// Patch is the change, a path to a git diff relative to the suite
	// directory.
	Patch string `json:"patch"`
	// Source says where the case came from, such as a pull request or a
	// mutation, for whoever reads the results.
	Source string `json:"source,omitempty"`
	// Expect are the problems a review should find. Empty means the change is
	// sound and should come back with no high or medium findings.
	Expect []Expected `json:"expect,omitempty"`

	// patch is the patch's content, read by LoadSuite.
	patch []byte
}

// Expected is one problem a case's change has.
type Expected struct {
	// File is the repository-relative path the problem is in.
	File string `json:"file"`
	// Lines is the range of new-file lines the problem is on, first and last.
	// Empty means anywhere in the file.
	Lines []int `json:"lines,omitempty"`
	// About says what the problem is, for whoever reads the results.
	About string `json:"about"`
}

// Clean reports whether the case expects no problems.
func (c Case) Clean() bool { return len(c.Expect) == 0 }

// LoadSuite reads and checks the suite in dir: every case is named uniquely,
// has a request and a non-empty patch, and every expected problem names a file
// and a sensible line range.
func LoadSuite(dir string) (Suite, error) {
	data, err := os.ReadFile(filepath.Join(dir, SuiteFile))
	if err != nil {
		return Suite{}, fmt.Errorf("read suite: %w", err)
	}
	var file struct {
		Cases []Case `json:"cases"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return Suite{}, fmt.Errorf("parse %s: %w", SuiteFile, err)
	}
	if len(file.Cases) == 0 {
		return Suite{}, fmt.Errorf("%s has no cases", SuiteFile)
	}
	seen := map[string]bool{}
	var errs []error
	for i := range file.Cases {
		c := &file.Cases[i]
		if err := c.check(dir, seen); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Suite{}, err
	}
	return Suite{Dir: dir, Cases: file.Cases}, nil
}

func (c *Case) check(dir string, seen map[string]bool) error {
	if !caseNameRe.MatchString(c.Name) {
		return fmt.Errorf("case %q: name must be lowercase words joined by - or _", c.Name)
	}
	if seen[c.Name] {
		return fmt.Errorf("case %s: name is used twice", c.Name)
	}
	seen[c.Name] = true
	if strings.TrimSpace(c.Revision) == "" {
		return fmt.Errorf("case %s: no revision", c.Name)
	}
	if strings.TrimSpace(c.Request) == "" {
		return fmt.Errorf("case %s: no request", c.Name)
	}
	if !filepath.IsLocal(c.Patch) {
		return fmt.Errorf("case %s: patch must be a path inside the suite directory", c.Name)
	}
	patch, err := os.ReadFile(filepath.Join(dir, c.Patch))
	if err != nil {
		return fmt.Errorf("case %s: read patch: %w", c.Name, err)
	}
	if len(strings.TrimSpace(string(patch))) == 0 {
		return fmt.Errorf("case %s: patch is empty", c.Name)
	}
	c.patch = patch
	for _, e := range c.Expect {
		if strings.TrimSpace(e.File) == "" {
			return fmt.Errorf("case %s: an expected problem names no file", c.Name)
		}
		switch {
		case len(e.Lines) == 0:
		case len(e.Lines) != 2 || e.Lines[0] < 1 || e.Lines[1] < e.Lines[0]:
			return fmt.Errorf("case %s: lines for %s must be [first, last], 1-based", c.Name, e.File)
		}
	}
	return nil
}

// Check makes sure every case can run in the repository at root: its revision
// is there and its patch applies to it. It reads only git's objects, so it
// costs nothing and runs before any sidecar starts.
func (s Suite) Check(ctx context.Context, root string) error {
	var errs []error
	for _, c := range s.Cases {
		if err := checkApplies(ctx, root, c); err != nil {
			errs = append(errs, fmt.Errorf("case %s: %w", c.Name, err))
		}
	}
	return errors.Join(errs...)
}

// checkApplies applies the case's patch to its revision in a scratch index,
// which leaves the repository's own index and working tree alone.
func checkApplies(ctx context.Context, root string, c Case) error {
	scratch, err := os.MkdirTemp("", "chunk-eval-check-")
	if err != nil {
		return fmt.Errorf("create scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	patch := filepath.Join(scratch, "patch")
	if err := os.WriteFile(patch, c.patch, 0o600); err != nil {
		return fmt.Errorf("write patch: %w", err)
	}

	git := gitexec.Runner{Dir: root, Env: append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(scratch, "index"))}
	if out, err := git.CombinedOutput(ctx, "read-tree", c.Revision+"^{tree}"); err != nil {
		return fmt.Errorf("revision %s is not in this repository: %w: %s", c.Revision, err, strings.TrimSpace(string(out)))
	}
	if out, err := git.CombinedOutput(ctx, "apply", "--cached", "--check", patch); err != nil {
		return fmt.Errorf("patch %s does not apply to %s: %w: %s", c.Patch, c.Revision, err, strings.TrimSpace(string(out)))
	}
	return nil
}
