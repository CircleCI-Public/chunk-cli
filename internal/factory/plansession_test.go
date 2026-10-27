package factory

import (
	"context"
	"errors"
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

func TestPlanSessionRecordsNoneAtFirst(t *testing.T) {
	root := newSessionProject(t)
	s, err := OpenPlanSession(root, "plan-1")
	assert.NilError(t, err)
	defer s.Close()

	id, err := s.ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, ""))
}

func TestPlanSessionKeepsTheRecordedSessionForTheNextOpen(t *testing.T) {
	root := newSessionProject(t)
	s, err := OpenPlanSession(root, "plan-1")
	assert.NilError(t, err)
	assert.NilError(t, s.Record("s-1"))
	assert.NilError(t, s.Close())

	again, err := OpenPlanSession(root, "plan-1")
	assert.NilError(t, err)
	defer again.Close()
	id, err := again.ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, "s-1"))

	other, err := OpenPlanSession(root, "plan-2")
	assert.NilError(t, err)
	defer other.Close()
	id, err = other.ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, ""), "another plan's record is its own")
}

func TestPlanSessionIsOpenInOneProcessAtATime(t *testing.T) {
	root := newSessionProject(t)
	s, err := OpenPlanSession(root, "plan-1")
	assert.NilError(t, err)

	_, err = OpenPlanSession(root, "plan-1")
	assert.Check(t, errors.Is(err, ErrPlanSessionOpen))

	assert.NilError(t, s.Close())
	again, err := OpenPlanSession(root, "plan-1")
	assert.NilError(t, err)
	assert.NilError(t, again.Close())
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
		s, err := OpenPlanSession(root, id)
		assert.NilError(t, err)
		assert.NilError(t, s.Record("s-"+id))
		assert.NilError(t, s.Close())
	}

	err := PrunePlanWorktrees(ctx, root, func(_ context.Context, id string) (bool, error) {
		return id == "done", nil
	})
	assert.NilError(t, err)

	done, err := planSessionPath(root, "done")
	assert.NilError(t, err)
	_, err = os.Stat(done)
	assert.Check(t, errors.Is(err, os.ErrNotExist), "the ended plan's session record is still there")
	s, err := OpenPlanSession(root, "open")
	assert.NilError(t, err)
	defer s.Close()
	id, err := s.ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, "s-open"))
}
