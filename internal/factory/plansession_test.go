package factory

import (
	"context"
	"os"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func newSessionProject(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return gitrepo.SetupGitRepo(t, "my-org", "my-repo")
}

// openPlanSession opens plan planID's session record for the rest of the
// test.
func openPlanSession(t *testing.T, root, planID string) *PlanSession {
	t.Helper()
	s, err := OpenPlanSession(root, planID)
	assert.NilError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// recordPlanSession records id as plan planID's session, and lets go of the
// record.
func recordPlanSession(t *testing.T, root, planID, id string) {
	t.Helper()
	s, err := OpenPlanSession(root, planID)
	assert.NilError(t, err)
	err = s.Record(id)
	assert.NilError(t, err)
	err = s.Close()
	assert.NilError(t, err)
}

func TestPlanSessionRecordsNoneAtFirst(t *testing.T) {
	root := newSessionProject(t)
	s := openPlanSession(t, root, "plan-1")

	id, err := s.ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, ""))
}

func TestPlanSessionKeepsTheRecordedSessionForTheNextOpen(t *testing.T) {
	root := newSessionProject(t)
	recordPlanSession(t, root, "plan-1", "s-1")

	id, err := openPlanSession(t, root, "plan-1").ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, "s-1"))

	id, err = openPlanSession(t, root, "plan-2").ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, ""), "another plan's record is its own")
}

func TestPlanSessionIsOpenInOneProcessAtATime(t *testing.T) {
	root := newSessionProject(t)
	s, err := OpenPlanSession(root, "plan-1")
	assert.NilError(t, err)

	_, err = OpenPlanSession(root, "plan-1")
	assert.Check(t, cmp.ErrorIs(err, ErrPlanSessionOpen))

	err = s.Close()
	assert.NilError(t, err)
	openPlanSession(t, root, "plan-1")
}

func TestRecordedPlanSessionReadsWhileOpen(t *testing.T) {
	root := newSessionProject(t)
	s := openPlanSession(t, root, "plan-1")
	err := s.Record("s-1")
	assert.NilError(t, err)

	id, err := RecordedPlanSession(root, "plan-1")
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, "s-1"))
}

func TestRecordedPlanSessionIsEmptyAtFirst(t *testing.T) {
	root := newSessionProject(t)
	id, err := RecordedPlanSession(root, "plan-1")
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, ""))
}

func TestPlanSessionRefusesAnIDThatIsNotOneDirectory(t *testing.T) {
	root := newSessionProject(t)
	_, err := OpenPlanSession(root, "../escape")
	assert.Check(t, cmp.ErrorContains(err, "cannot name a directory"))
}

func TestPrunePlanWorktreesDropsAnEndedPlansSession(t *testing.T) {
	ctx := context.Background()
	root, _ := newPlanProject(t)
	for _, id := range []string{"done", "open"} {
		_, err := OpenPlanWorktree(ctx, root, id)
		assert.NilError(t, err)
		recordPlanSession(t, root, id, "s-"+id)
	}

	err := PrunePlanWorktrees(ctx, root, func(_ context.Context, id string) (bool, error) {
		return id == "done", nil
	})
	assert.NilError(t, err)

	done, err := planSessionPath(root, "done")
	assert.NilError(t, err)
	_, err = os.Stat(done)
	assert.Check(t, cmp.ErrorIs(err, os.ErrNotExist), "the ended plan's session record is still there")
	id, err := openPlanSession(t, root, "open").ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, "s-open"))
}

// A worktree kept for its changes keeps its session record, so the plan
// can still be reopened on its conversation.
func TestPrunePlanWorktreesKeepsTheSessionOfAWorktreeItKeeps(t *testing.T) {
	ctx := context.Background()
	root, _ := newPlanProject(t)
	path, err := OpenPlanWorktree(ctx, root, "done")
	assert.NilError(t, err)
	writeFile(t, path, "main.go", "package main\n\n// unsaved\n")
	recordPlanSession(t, root, "done", "s-done")

	err = PrunePlanWorktrees(ctx, root, func(context.Context, string) (bool, error) { return true, nil })
	assert.Check(t, cmp.ErrorContains(err, "remove the worktree of plan done"))

	id, err := openPlanSession(t, root, "done").ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, "s-done"))
}
