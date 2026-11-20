package factory

import (
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

// openChatSession opens the chat's session record for the rest of the test.
func openChatSession(t *testing.T, root string) *PlanSession {
	t.Helper()
	s, err := OpenChatSession(root)
	assert.NilError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestChatSessionRecordsNoneAtFirst(t *testing.T) {
	root := newSessionProject(t)
	s := openChatSession(t, root)

	id, err := s.ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, ""))
}

func TestChatSessionKeepsTheRecordedSessionForTheNextOpen(t *testing.T) {
	root := newSessionProject(t)
	s, err := OpenChatSession(root)
	assert.NilError(t, err)
	err = s.Record("s-1")
	assert.NilError(t, err)
	err = s.Close()
	assert.NilError(t, err)

	id, err := openChatSession(t, root).ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, "s-1"))
}

func TestChatSessionIsOpenInOneProcessAtATime(t *testing.T) {
	root := newSessionProject(t)
	s, err := OpenChatSession(root)
	assert.NilError(t, err)

	_, err = OpenChatSession(root)
	assert.Check(t, cmp.ErrorIs(err, ErrChatOpen))

	err = s.Record("c-1")
	assert.NilError(t, err)
	err = s.Close()
	assert.NilError(t, err)
	s, err = OpenChatSession(root)
	assert.NilError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	id, err := s.ID()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, "c-1"))
}
