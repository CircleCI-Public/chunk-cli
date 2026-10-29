package factoryplan

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
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

	merged := newStore(client, "")
	err := merged.Create(ctx, "a merged plan")
	assert.NilError(t, err)
	mergedDir, err := planWorktree(ctx, root, merged)
	assert.NilError(t, err)
	cci.SetFactoryPlanState(merged.ID(), "Done")

	preFlow := newStore(client, "")
	err = preFlow.Create(ctx, "a plan made before flows")
	assert.NilError(t, err)
	preFlowDir, err := planWorktree(ctx, root, preFlow)
	assert.NilError(t, err)
	cci.DropFactoryFlow(preFlow.ID())

	deleted := newStore(client, "")
	err = deleted.Create(ctx, "a deleted plan")
	assert.NilError(t, err)
	deletedDir, err := planWorktree(ctx, root, deleted)
	assert.NilError(t, err)
	cci.DropFactoryFlow(deleted.ID())
	cci.DeleteFactoryPlan(deleted.ID())

	store := newStore(client, "")
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
	_, err = os.Stat(preFlowDir)
	assert.Check(t, err, "the live pre-flow plan's worktree was removed")
	_, err = os.Stat(deletedDir)
	assert.Check(t, errors.Is(err, os.ErrNotExist), "the deleted plan's worktree is still there")
}

func TestStoreEndedForEachOfFactorysEnds(t *testing.T) {
	client, cci := testClient(t)
	ctx := context.Background()
	for _, tc := range []struct {
		state string
		want  bool
	}{
		{
			state: "Done",
			want:  true,
		},
		{
			state: "Canceled",
			want:  true,
		},
		{
			state: "Won't Do",
			want:  true,
		},
		{
			state: "Duplicate",
			want:  true,
		},
		{
			state: "Planning - Human",
			want:  false,
		},
	} {
		t.Run(tc.state, func(t *testing.T) {
			store := newStore(client, "")
			err := store.Create(ctx, "a plan")
			assert.NilError(t, err)
			cci.SetFactoryPlanState(store.ID(), tc.state)

			got, err := store.ended(ctx, store.ID())
			assert.NilError(t, err)
			assert.Check(t, cmp.Equal(got, tc.want))
		})
	}
}

func TestStoreEndedForAPlanWithNoFlow(t *testing.T) {
	client, cci := testClient(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		delete bool // the plan is gone from factory too
		want   bool
	}{
		{
			name: "a live plan made before flows",
			want: false,
		},
		{
			name:   "a deleted plan",
			delete: true,
			want:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(client, "")
			err := store.Create(ctx, "a plan")
			assert.NilError(t, err)
			cci.DropFactoryFlow(store.ID())
			if tc.delete {
				cci.DeleteFactoryPlan(store.ID())
			}

			got, err := store.ended(ctx, store.ID())
			assert.NilError(t, err)
			assert.Check(t, cmp.Equal(got, tc.want))
		})
	}
}

// A failed read is an error and not ended, so nothing is pruned on it.
func TestStoreEndedErrsOnAFailedRead(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		fail func(cci *fakes.FakeCircleCI, id string)
		err  string
	}{
		{
			name: "the flow read fails",
			fail: func(cci *fakes.FakeCircleCI, _ string) {
				cci.FactoryStatusCode = http.StatusInternalServerError
			},
			err: "get the state of plan",
		},
		{
			name: "the plan read fails after a flow 404",
			fail: func(cci *fakes.FakeCircleCI, id string) {
				cci.DropFactoryFlow(id)
				cci.FactoryPlanStatusCode = http.StatusInternalServerError
			},
			err: "get plan",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cci := testClient(t)
			store := newStore(client, "")
			err := store.Create(ctx, "a plan")
			assert.NilError(t, err)
			tc.fail(cci, store.ID())

			got, err := store.ended(ctx, store.ID())
			assert.Check(t, cmp.ErrorContains(err, tc.err))
			assert.Check(t, !got)
		})
	}
}
