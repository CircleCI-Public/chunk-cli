package eval

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// commit commits everything in root as author with message, and returns the
// commit.
func commit(t *testing.T, root, author, message string) string {
	t.Helper()
	git(t, root, "add", "-A")
	cmd := exec.Command("git", "commit", "-q", "--allow-empty", "--author", author+" <a@example.com>", "-m", message)
	cmd.Dir = root
	cmd.Env = gitrepo.GitEnv(root)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))
	return git(t, root, "rev-parse", "HEAD")
}

func TestCommitsMakesCasesOfRecentCommits(t *testing.T) {
	ctx := context.Background()
	root, base := newRepo(t)
	writeFile(t, root, "pkg/a.go", "package pkg\n\nvar A = 1\n")
	first := commit(t, root, "Dev", "Add A\n\nA counts things.")
	writeFile(t, root, "README.md", "# docs\n")
	commit(t, root, "Dev", "Document the project")
	writeFile(t, root, "go.sum", "bumped\n")
	commit(t, root, "renovate[bot]", "Update module x")
	commit(t, root, "Dev", "Nothing at all")
	writeFile(t, root, "pkg/big.go", "package pkg\n\nvar Big = `"+strings.Repeat("x", maxCommitPatch)+"`\n")
	commit(t, root, "Dev", "Add a huge table")
	writeFile(t, root, "pkg/a.go", "package pkg\n\nvar A = 2\n")
	second := commit(t, root, "Dev", "Count from two")

	s, err := Commits(ctx, root, "HEAD", 5)
	assert.NilError(t, err)

	// Newest first, skipping documentation, bots, empty and oversized
	// commits, until the history runs out at the root.
	assert.Equal(t, len(s.Cases), 2)
	c := s.Cases[0]
	assert.Equal(t, c.Name, "commit-"+second[:12])
	assert.Equal(t, c.Revision, git(t, root, "rev-parse", second+"^"))
	assert.Equal(t, c.Request, "Count from two")
	assert.Assert(t, !c.Labeled())
	assert.Equal(t, s.Cases[1].Revision, base)
	assert.Equal(t, s.Cases[1].Request, "Add A\n\nA counts things.")
	assert.Equal(t, s.Cases[1].Source, "commit "+first[:12]+": Add A")
	assert.Assert(t, strings.Contains(string(s.Cases[1].patch), "+var A = 1"), string(s.Cases[1].patch))

	// Every case applies to its revision, as a loaded suite's must.
	assert.NilError(t, s.Check(ctx, root))

	s, err = Commits(ctx, root, "HEAD", 1)
	assert.NilError(t, err)
	assert.Equal(t, len(s.Cases), 1)
	assert.Equal(t, s.Cases[0].Name, "commit-"+second[:12])

	// From an earlier revision, later commits are not cases.
	s, err = Commits(ctx, root, second+"~1", 5)
	assert.NilError(t, err)
	assert.Equal(t, len(s.Cases), 1)
	assert.Equal(t, s.Cases[0].Name, "commit-"+first[:12])
}

func TestCommitsRefusesWhatIsNotAHistory(t *testing.T) {
	ctx := context.Background()
	root, _ := newRepo(t)

	_, err := Commits(ctx, root, "HEAD", 3)
	assert.ErrorContains(t, err, "no commit on HEAD makes a case")
	_, err = Commits(ctx, root, "--all", 3)
	assert.ErrorContains(t, err, "must not start with -")
	_, err = Commits(ctx, root, "nope", 3)
	assert.ErrorContains(t, err, "list the commits of nope")
	_, err = Commits(ctx, root, "HEAD", 0)
	assert.ErrorContains(t, err, "at least one commit")
}
