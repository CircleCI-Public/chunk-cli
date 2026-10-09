package factory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// newProject returns a repository with one commit and, on top of it, the
// developer's work in progress: an edit and an untracked file.
func newProject(t *testing.T) string {
	t.Helper()
	root := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeFile(t, root, "main.go", "package main\n")
	gitrepo.AddFile(t, root, "main.go")
	gitOutput(t, root, "commit", "-m", "base")
	writeFile(t, root, "main.go", "package main\n\n// in progress\n")
	writeFile(t, root, "mine.go", "package main\n")
	return root
}

func TestCreateWorktreeStartsFromTheDevelopersFiles(t *testing.T) {
	ctx := context.Background()
	root := newProject(t)
	before := gitOutput(t, root, "status", "--porcelain")
	branch := gitOutput(t, root, "rev-parse", "--abbrev-ref", "HEAD")

	wt, err := CreateWorktree(ctx, root, filepath.Join(t.TempDir(), "wt"), "run-1", "Add a --verbose flag")
	assert.NilError(t, err)

	assert.Equal(t, wt.Branch, "chunk/factory/add-a-verbose-flag/run-1")
	assert.Equal(t, gitOutput(t, wt.Path, "rev-parse", "HEAD"), wt.Baseline)
	assert.Equal(t, wt.Head, gitOutput(t, root, "rev-parse", "HEAD"))
	assert.Assert(t, wt.Baseline != wt.Head, "uncommitted work was not committed as the baseline")
	// Uncommitted work is in the baseline, untracked files included, so it is
	// not part of the change under review.
	assert.Equal(t, readFile(t, wt.Path, "main.go"), "package main\n\n// in progress\n")
	assert.Equal(t, readFile(t, wt.Path, "mine.go"), "package main\n")
	assert.Equal(t, gitOutput(t, wt.Path, "status", "--porcelain"), "")
	// The developer's checkout is left exactly as it was.
	assert.Equal(t, gitOutput(t, root, "status", "--porcelain"), before)
	assert.Equal(t, gitOutput(t, root, "rev-parse", "--abbrev-ref", "HEAD"), branch)
}

func TestCreateWorktreeFromACleanTreeStartsAtHEAD(t *testing.T) {
	ctx := context.Background()
	root := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeFile(t, root, "main.go", "package main\n")
	gitrepo.AddFile(t, root, "main.go")
	gitOutput(t, root, "commit", "-m", "base")

	wt, err := CreateWorktree(ctx, root, filepath.Join(t.TempDir(), "wt"), "run-1", "add a flag")
	assert.NilError(t, err)
	assert.Equal(t, wt.Baseline, gitOutput(t, root, "rev-parse", "HEAD"))
	assert.Equal(t, wt.Head, wt.Baseline)
}

func TestWorktreeCommitKeepsTheWorktree(t *testing.T) {
	ctx := context.Background()
	root := newProject(t)
	wt, err := CreateWorktree(ctx, root, filepath.Join(t.TempDir(), "wt"), "run-1", "add a flag")
	assert.NilError(t, err)

	commit, err := wt.Commit(ctx, "nothing")
	assert.NilError(t, err)
	assert.Equal(t, commit, "", "nothing changed, so nothing is committed")

	writeFile(t, wt.Path, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, wt.Path, "helper.go", "package main\n")
	commit, err = wt.Commit(ctx, CommitMessage("add a main\n\nwith detail", "run-1", Outcome{Result: ResultPassed, Rounds: 2}))
	assert.NilError(t, err)

	assert.Equal(t, gitOutput(t, root, "rev-parse", wt.Branch), commit)
	assert.Equal(t, gitOutput(t, root, "log", "-1", "--format=%s", wt.Branch), "add a main")
	assert.Equal(t, gitOutput(t, root, "log", "-1", "--format=%b", wt.Branch),
		"with detail\n\nWritten by chunk factory run run-1 (passed after 2 round(s)).", "the subject is not repeated in the body")
	stat, err := wt.Stat(ctx)
	assert.NilError(t, err)
	assert.Equal(t, stat, "2 files changed, 2 insertions(+), 1 deletion(-)")
	_, err = os.Stat(wt.Path)
	assert.NilError(t, err, "the worktree is kept for the developer")
}

func TestWorktreeRemove(t *testing.T) {
	ctx := context.Background()
	root := newProject(t)
	wt, err := CreateWorktree(ctx, root, filepath.Join(t.TempDir(), "wt"), "run-1", "add a flag")
	assert.NilError(t, err)

	assert.NilError(t, wt.Remove(ctx, root))
	_, err = os.Stat(wt.Path)
	assert.Assert(t, os.IsNotExist(err))
	assert.Equal(t, gitOutput(t, root, "branch", "--list", wt.Branch), "")
	assert.Equal(t, readFile(t, root, "mine.go"), "package main\n", "the developer's own files are untouched")
}

func TestCommitMessageDoesNotRepeatTheSubject(t *testing.T) {
	assert.Equal(t, CommitMessage("  add a flag\n", "run-1", Outcome{}),
		"add a flag\n\nWritten by chunk factory run run-1.\n")
	assert.Equal(t, CommitMessage("add a flag\n\nso users can opt out\n", "run-1", Outcome{Result: ResultPassed, Rounds: 1}),
		"add a flag\n\nso users can opt out\n\nWritten by chunk factory run run-1 (passed after 1 round(s)).\n")
	assert.Equal(t, CommitMessage("add a flag\r\n\r\n    go test ./...\r\n", "run-1", Outcome{}),
		"add a flag\n\n    go test ./...\n\nWritten by chunk factory run run-1.\n")
}

func TestBranchSlug(t *testing.T) {
	for _, tc := range []struct {
		prompt string
		want   string
	}{
		{prompt: "Add a factory status command", want: "add-a-factory-status-command"},
		{prompt: "# Fix --continue lookup\n\nMore detail", want: "fix-continue-lookup"},
		{prompt: "  Spaces, punctuation & CAPS!  ", want: "spaces-punctuation-caps"},
		{prompt: "!!!", want: "change"},
		{prompt: "Make this deliberately long branch name stop at a readable boundary eventually", want: "make-this-deliberately-long-branch-name-stop-at"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, branchSlug(tc.prompt), tc.want)
		})
	}
}
