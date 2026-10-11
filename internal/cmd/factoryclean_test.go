package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/factory"
)

// finishedRun makes a real worktree and branch for a run in project and a
// record saying it passed.
func finishedRun(t *testing.T, project, runID string) factory.Worktree {
	t.Helper()
	dataDir, err := config.ProjectDataDir(config.CanonicalProjectRoot(project))
	assert.NilError(t, err)
	wt, err := factory.CreateWorktree(context.Background(), factory.WorktreeOptions{Root: project, Path: filepath.Join(dataDir, "factory", runID), RunID: runID, Prompt: "do the thing"})
	assert.NilError(t, err)
	rec, err := json.Marshal(factory.Record{RunID: runID, Prompt: "do the thing", Worktree: wt.Path, Branch: wt.Branch, Result: factory.ResultPassed, Rounds: 2})
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(dataDir, "factory", runID+".json"), rec, 0o600))
	return wt
}

func runFactoryCleanCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newFactoryCleanCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	cmd.SetContext(t.Context())
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func branchExists(t *testing.T, project, branch string) bool {
	t.Helper()
	c := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	c.Dir = project
	return c.Run() == nil
}

func TestFactoryCleanDryRunRemovesNothing(t *testing.T) {
	project := factoryProject(t)
	wt := finishedRun(t, project, "20261010-100000")

	out, _, err := runFactoryCleanCmd(t, "--dry-run")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "20261010-100000"), out)
	assert.Assert(t, strings.Contains(out, "finished"), out)
	assert.Assert(t, branchExists(t, project, wt.Branch))
	_, statErr := os.Stat(wt.Path)
	assert.NilError(t, statErr)
}

func TestFactoryCleanNonInteractiveNeedsYes(t *testing.T) {
	project := factoryProject(t)
	wt := finishedRun(t, project, "20261010-100000")

	_, _, err := runFactoryCleanCmd(t)
	assert.ErrorContains(t, err, "terminal")
	var ue *userError
	assert.Assert(t, asUserError(err, &ue))
	assert.Assert(t, strings.Contains(ue.suggestion, "--yes"))
	assert.Assert(t, branchExists(t, project, wt.Branch))
}

func TestFactoryCleanUnknownRunIsAUserError(t *testing.T) {
	project := factoryProject(t)
	finishedRun(t, project, "20261010-100000")

	_, _, err := runFactoryCleanCmd(t, "--yes", "20200101-000000")
	var ue *userError
	assert.Assert(t, asUserError(err, &ue))
	assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
}

func TestFactoryCleanAcceptsRunIDAndBranch(t *testing.T) {
	project := factoryProject(t)
	a := finishedRun(t, project, "20261010-100000")
	b := finishedRun(t, project, "20261010-110000")
	c := finishedRun(t, project, "20261010-120000")

	out, _, err := runFactoryCleanCmd(t, "--yes", "20261010-100000", b.Branch)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "removed 20261010-100000 (branch, worktree, record)"), out)
	assert.Assert(t, strings.Contains(out, "Removed 2 run(s); skipped 0."), out)
	assert.Assert(t, !branchExists(t, project, a.Branch))
	assert.Assert(t, !branchExists(t, project, b.Branch))
	assert.Assert(t, branchExists(t, project, c.Branch))
}

func TestFactoryCleanSkipsDirtyUnlessForced(t *testing.T) {
	project := factoryProject(t)
	wt := finishedRun(t, project, "20261010-100000")
	assert.NilError(t, os.WriteFile(filepath.Join(wt.Path, "wip.txt"), []byte("x"), 0o644))

	out, errOut, err := runFactoryCleanCmd(t, "--yes")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(errOut, "skipped 20261010-100000: worktree has uncommitted changes (use --force to discard them)"), errOut)
	assert.Assert(t, strings.Contains(out, "Removed 0 run(s); skipped 1."), out)
	assert.Assert(t, branchExists(t, project, wt.Branch))

	_, _, err = runFactoryCleanCmd(t, "--yes", "--force")
	assert.NilError(t, err)
	assert.Assert(t, !branchExists(t, project, wt.Branch))
}

func TestRunAge(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		start time.Time
		want  string
	}{
		{time.Time{}, "-"},
		{now.Add(-5 * time.Minute), "5m"},
		{now.Add(-2 * time.Hour), "2h"},
		{now.Add(-72 * time.Hour), "3d"},
	} {
		assert.Equal(t, runAge(tc.start, now), tc.want)
	}
}

func asUserError(err error, target **userError) bool { return errors.As(err, target) }

func runFactoryListCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newFactoryListCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	cmd.SetContext(t.Context())
	err := cmd.Execute()
	return out.String(), err
}

func TestFactoryListNoRuns(t *testing.T) {
	factoryProject(t)

	out, err := runFactoryListCmd(t)
	assert.NilError(t, err)
	assert.Equal(t, out, "No factory runs.\n")

	out, err = runFactoryListCmd(t, "--json")
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(out), "[]")
}

func TestFactoryListShowsAFinishedRun(t *testing.T) {
	project := factoryProject(t)
	wt := finishedRun(t, project, "20261010-100000")

	out, err := runFactoryListCmd(t)
	assert.NilError(t, err)
	for _, want := range []string{"RUN", "20261010-100000", "finished", "passed", "clean", "do the thing"} {
		assert.Assert(t, strings.Contains(out, want), "missing %q in:\n%s", want, out)
	}

	out, err = runFactoryListCmd(t, "--json")
	assert.NilError(t, err)
	var entries []factory.RunEntry
	assert.NilError(t, json.Unmarshal([]byte(out), &entries))
	assert.Equal(t, len(entries), 1)
	assert.DeepEqual(t, entries[0].RunIDs, []string{"20261010-100000"})
	assert.Equal(t, entries[0].State, factory.StateFinished)
	assert.Equal(t, entries[0].Branch, wt.Branch)
	assert.Assert(t, entries[0].WorktreeExists)
}
