package factoryplan

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func TestPlanWorktreeRemovesThoseOfEndedPlans(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	origin := filepath.Join(t.TempDir(), "origin.git")
	for _, args := range [][]string{
		{"init", "--bare", origin},
		{"remote", "set-url", "origin", origin},
		{"push", "--quiet", "origin", "HEAD:main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = root, gitrepo.GitEnv(root)
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, string(out))
	}
	client, cci := testClient(t)
	ctx := context.Background()

	merged := NewStore(client)
	err := merged.Create(ctx, "a merged plan")
	assert.NilError(t, err)
	mergedDir, err := planWorktree(ctx, root, merged)
	assert.NilError(t, err)
	cci.SetFactoryPlanState(merged.ID(), "Done")

	store := NewStore(client)
	err = store.Create(ctx, "a plan")
	assert.NilError(t, err)
	dir, err := planWorktree(ctx, root, store)
	assert.NilError(t, err)

	assert.Check(t, cmp.Equal(filepath.Base(dir), store.ID()))
	assert.Check(t, dir != root)
	_, err = os.Stat(dir)
	assert.Check(t, err)
	_, err = os.Stat(mergedDir)
	assert.Check(t, errors.Is(err, os.ErrNotExist), "the merged plan's worktree is still there")
}
