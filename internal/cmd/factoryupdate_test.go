package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/factory"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/smeltd"
	"github.com/smeltdev/smelt/internal/testing/gitrepo"
)

// updateProject is a run with work committed on its branch, a bare origin, and
// a main that has moved on: it gained other.go, or a conflicting shared.go.
func updateProject(t *testing.T, conflict bool) (root, origin string, rec factory.Record) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root = gitrepo.SetupGitRepo(t, "o", "r")
	writeTestFile(t, filepath.Join(root, "shared.txt"), "one\n")
	runGit(t, root, "add", "shared.txt")
	runGit(t, root, "commit", "-m", "base")
	runGit(t, root, "branch", "-M", "main")
	origin = filepath.Join(t.TempDir(), "origin.git")
	runGit(t, root, "init", "--bare", origin)
	runGit(t, root, "remote", "set-url", "origin", origin)
	runGit(t, root, "push", "origin", "main")
	wt, err := factory.CreateWorktree(context.Background(), factory.WorktreeOptions{Root: root, Path: filepath.Join(t.TempDir(), "wt"), RunID: "run-1", Prompt: "add a flag"})
	assert.NilError(t, err)
	writeTestFile(t, filepath.Join(wt.Path, "shared.txt"), "branch\n")
	runGit(t, wt.Path, "commit", "-am", "work")

	name, content := "other.txt", "x\n"
	if conflict {
		name, content = "shared.txt", "main\n"
	}
	writeTestFile(t, filepath.Join(root, name), content)
	runGit(t, root, "add", name)
	runGit(t, root, "commit", "-m", "main moves on")
	runGit(t, root, "push", "origin", "main")
	return root, origin, factory.Record{RunID: "run-1", Prompt: "add a flag", Worktree: wt.Path, Branch: wt.Branch,
		Baseline: wt.Baseline, Head: wt.Head, BaseBranch: "main", DefaultBranch: "main"}
}

func TestUpdateRunCleanJSONAndPush(t *testing.T) {
	root, origin, rec := updateProject(t, false)
	var out, errOut bytes.Buffer
	err := updateRun(t.Context(), iostream.Streams{Out: &out, Err: &errOut}, root, rec, updateFlags{json: true, push: true})
	assert.NilError(t, err)
	var got factory.UpdateReport
	assert.NilError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, got.Result, factory.UpdateClean)
	assert.Equal(t, got.MergedCommits, 1)
	assert.Equal(t, got.Base, "main")
	assert.Assert(t, pushed(t, origin, rec.Branch))
}

func TestUpdateRunWithConflictsDoesNotPush(t *testing.T) {
	root, origin, rec := updateProject(t, true)
	var out, errOut bytes.Buffer
	err := updateRun(t.Context(), iostream.Streams{Out: &out, Err: &errOut}, root, rec, updateFlags{push: true})
	assert.NilError(t, err)
	assert.Assert(t, !pushed(t, origin, rec.Branch))
	text := out.String()
	assert.Assert(t, strings.Contains(text, "Merged origin/main into "+rec.Branch+" with conflicts in 1 file(s):"), text)
	assert.Assert(t, strings.Contains(text, `smelt factory build --continue run-1 "Resolve the merge conflict markers in shared.txt"`), text)
	assert.Assert(t, strings.Contains(errOut.String(), "Not pushing"))
}

func TestUpdateRunWithoutPushLeavesTheBranchLocal(t *testing.T) {
	root, origin, rec := updateProject(t, false)
	var out, errOut bytes.Buffer
	assert.NilError(t, updateRun(t.Context(), iostream.Streams{Out: &out, Err: &errOut}, root, rec, updateFlags{}))
	assert.Assert(t, strings.Contains(out.String(), "clean (1 commits merged)"), out.String())
	assert.Assert(t, !pushed(t, origin, rec.Branch))
}

func TestUpdateRunNeverPushesANonFactoryBranch(t *testing.T) {
	root, origin, rec := updateProject(t, false)
	runGit(t, rec.Worktree, "branch", "-m", "feature")
	rec.Branch = "feature"
	var out, errOut bytes.Buffer
	err := updateRun(t.Context(), iostream.Streams{Out: &out, Err: &errOut}, root, rec, updateFlags{push: true})
	assert.ErrorContains(t, err, "not a smelt/")
	assert.Assert(t, !pushed(t, origin, "feature"))
}

func TestRefuseActiveRun(t *testing.T) {
	assert.NilError(t, refuseActiveRun(nil, "run-1"))
	assert.NilError(t, refuseActiveRun(&smeltd.Session{State: smeltd.SessionDone}, "run-1"))
	err := refuseActiveRun(&smeltd.Session{State: smeltd.SessionRunning}, "run-1")
	assert.ErrorContains(t, err, "still running")
}

func TestUpdateRunReportsAnUnknownRun(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	_, err := loadRunRecord(t.TempDir(), "nope")
	assert.ErrorContains(t, err, "No factory run nope")
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	assert.NilError(t, os.WriteFile(path, []byte(content), 0o644))
}
