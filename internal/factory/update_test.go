package factory

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/testing/gitrepo"
)

// updateProject is a finished run whose project has a bare origin holding main.
// The run changed work.go on its branch; root is the developer's checkout.
func updateProject(t *testing.T) (root, origin string, rec Record) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	ctx := context.Background()
	root = gitrepo.SetupGitRepo(t, "o", "r")
	writeFile(t, root, "shared.go", "package main\n\nvar a = 1\n")
	gitrepo.AddFile(t, root, "shared.go")
	gitOutput(t, root, "commit", "-m", "base")
	gitOutput(t, root, "branch", "-M", "main")
	origin = filepath.Join(t.TempDir(), "origin.git")
	gitOutput(t, root, "init", "--bare", origin)
	gitOutput(t, root, "remote", "set-url", "origin", origin)
	gitOutput(t, root, "push", "origin", "main")

	author := config.CommitAuthor{Name: "Run Author", Email: "run@example.com"}
	wt, err := CreateWorktree(ctx, WorktreeOptions{Root: root, Path: filepath.Join(t.TempDir(), "wt"), RunID: "run-1", Prompt: "add work", Author: author})
	assert.NilError(t, err)
	writeFile(t, wt.Path, "work.go", "package main\n")
	_, err = wt.Commit(ctx, "add work")
	assert.NilError(t, err)
	rec = Record{RunID: "run-1", Prompt: "add work", Worktree: wt.Path, Branch: wt.Branch, Baseline: wt.Baseline,
		Head: wt.Head, BaseBranch: "main", DefaultBranch: "main", Result: ResultPassed, Rounds: 1}
	return root, origin, rec
}

// landOnMain commits a change to name on main and pushes it, as a merged PR would.
func landOnMain(t *testing.T, root, name, content string) {
	t.Helper()
	gitOutput(t, root, "checkout", "--quiet", "main")
	writeFile(t, root, name, content)
	gitrepo.AddFile(t, root, name)
	gitOutput(t, root, "commit", "-m", "change "+name)
	gitOutput(t, root, "push", "origin", "main")
}

var runAuthor = config.CommitAuthor{Name: "Run Author", Email: "run@example.com"}

func TestUpdateBranchMergesCleanly(t *testing.T) {
	root, _, rec := updateProject(t)
	landOnMain(t, root, "other.go", "package main\n")

	rep, err := UpdateBranch(context.Background(), root, rec, UpdateOptions{Author: runAuthor})
	assert.NilError(t, err)
	assert.Equal(t, rep.Result, UpdateClean)
	assert.Equal(t, rep.MergedCommits, 1)
	assert.Equal(t, gitOutput(t, rec.Worktree, "status", "--porcelain"), "")
	assert.Equal(t, len(strings.Fields(gitOutput(t, rec.Worktree, "rev-list", "--parents", "-1", "HEAD"))), 3, "a merge commit has two parents")
	assert.Equal(t, gitOutput(t, rec.Worktree, "log", "-1", "--format=%an <%ae>"), "Run Author <run@example.com>")
	assert.Equal(t, readFile(t, rec.Worktree, "other.go"), "package main\n")
}

func TestUpdateBranchUpToDate(t *testing.T) {
	root, _, rec := updateProject(t)
	before := gitOutput(t, rec.Worktree, "rev-parse", "HEAD")

	rep, err := UpdateBranch(context.Background(), root, rec, UpdateOptions{Author: runAuthor})
	assert.NilError(t, err)
	assert.Equal(t, rep.Result, UpdateUpToDate)
	assert.Equal(t, gitOutput(t, rec.Worktree, "rev-parse", "HEAD"), before)
}

func TestUpdateBranchCommitsConflictsWithTheirMarkers(t *testing.T) {
	ctx := context.Background()
	root, _, rec := updateProject(t)
	writeFile(t, rec.Worktree, "shared.go", "package main\n\nvar a = 2\n")
	gitOutput(t, rec.Worktree, "commit", "-am", "change a on the branch")
	landOnMain(t, root, "shared.go", "package main\n\nvar a = 3\n")

	rep, err := UpdateBranch(ctx, root, rec, UpdateOptions{Author: runAuthor})
	assert.NilError(t, err)
	assert.Equal(t, rep.Result, UpdateConflicts)
	assert.DeepEqual(t, rep.Conflicts, []string{"shared.go"})
	assert.Assert(t, strings.Contains(readFile(t, rec.Worktree, "shared.go"), "<<<<<<<"))
	assert.Equal(t, gitOutput(t, rec.Worktree, "status", "--porcelain"), "", "nothing is left in progress")
	assert.Equal(t, gitOutput(t, rec.Worktree, "log", "-1", "--format=%s"),
		"Merge main into "+rec.Branch+" (conflicts to resolve: shared.go)")
	assert.Assert(t, strings.Contains(gitOutput(t, rec.Worktree, "show", "HEAD:shared.go"), "======="))

	saved, err := LoadRecord(root, rec.RunID)
	assert.NilError(t, err)
	assert.DeepEqual(t, saved.Conflicts, []string{"shared.go"})

	_, err = openWorktree(ctx, root, saved, "run-2", runAuthor)
	assert.NilError(t, err)
}

func TestUpdateBranchRefusesADirtyWorktree(t *testing.T) {
	root, _, rec := updateProject(t)
	landOnMain(t, root, "other.go", "package main\n")
	writeFile(t, rec.Worktree, "scratch.go", "package main\n")

	_, err := UpdateBranch(context.Background(), root, rec, UpdateOptions{Author: runAuthor})
	assert.ErrorContains(t, err, "uncommitted changes")
}

func TestMarkersGateFailsOnlyWhileMarkersRemain(t *testing.T) {
	tests := []struct {
		name    string
		content string
		pass    bool
	}{
		{"clean", "package main\n", true},
		{"ours marker", "<<<<<<< HEAD\nx\n", false},
		{"separator", "a\n=======\nb\n", false},
		{"theirs marker", ">>>>>>> origin/main\n", false},
		{"bare marker", "<<<<<<<\n", false},
		{"indented is not a marker", "    =======\n", true},
		{"longer rule is not a marker", "==========\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			gitOutput(t, dir, "init", "--quiet")
			writeFile(t, dir, "f.txt", tt.content)
			gitOutput(t, dir, "add", "f.txt")
			cmd := exec.Command("sh", "-c", MarkersGate.Run)
			cmd.Dir = dir
			cmd.Env = gitrepo.GitEnv(dir)
			assert.Equal(t, cmd.Run() == nil, tt.pass)
		})
	}
}

func TestContinuationPromptNamesTheConflictedFiles(t *testing.T) {
	c := &Continuation{From: Record{Prompt: "add work", BaseBranch: "main", Conflicts: []string{"a.go", "b.go"}}}
	p := c.prompt()
	for _, want := range []string{"merged with `main`", "a.go, b.go", "<<<<<<<", "remove the markers"} {
		assert.Assert(t, strings.Contains(p, want), "%q missing from %s", want, p)
	}
	c.From.Conflicts = nil
	assert.Assert(t, !strings.Contains(c.prompt(), "conflict markers"))
}

func TestFinishedContinuedRunClearsTheConflicts(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	from := Record{RunID: "run-1", Conflicts: []string{"a.go"}}
	assert.NilError(t, saveRecord(dataDir, from))

	(&Continuation{From: from}).resolved(dataDir, func(iostream.Level, string) {})

	got, err := LoadRecord(root, "run-1")
	assert.NilError(t, err)
	assert.Equal(t, len(got.Conflicts), 0)
}

func TestLoadChecksAddsTheMarkersGate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "style.md", "Check the style.\n")
	cfg := &config.ProjectConfig{Commands: []config.Command{{Name: "test", Run: "go test ./..."}}}

	_, commands, err := LoadChecks(dir, false, cfg, false)
	assert.NilError(t, err)
	assert.DeepEqual(t, commands, []config.Command{{Name: "test", Run: "go test ./..."}, MarkersGate})

	_, commands, err = LoadChecks(dir, false, cfg, true)
	assert.NilError(t, err)
	assert.DeepEqual(t, commands, []config.Command{MarkersGate}) // the gate survives --no-validate

	_, _, err = LoadChecks(t.TempDir(), true, &config.ProjectConfig{}, false)
	assert.ErrorIs(t, err, ErrNothingToCheck, "the gate alone is not something to check with")
}
