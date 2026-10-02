package factory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// A run that fails before the implementer starts leaves nothing behind: its
// worktree and branch are removed, and the developer's checkout is untouched.
func TestRunThatFailsBeforeTheImplementerLeavesNothingBehind(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	root := newProject(t)
	before := gitOutput(t, root, "status", "--porcelain")

	cci := fakes.NewFakeCircleCI()
	cci.CreateStatusCode = http.StatusInternalServerError
	api := httptest.NewServer(cci)
	t.Cleanup(api.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: api.URL})
	assert.NilError(t, err)

	rep, err := Run(context.Background(), RunOptions{
		Root:     root,
		Prompt:   "add a --verbose flag",
		Attempts: 1,
		Client:   client,
		OrgID:    "org-1",
		Status:   func(iostream.Level, string) {},
	})

	assert.ErrorContains(t, err, "create the run's sidecars: ")
	assert.Assert(t, !rep.Started)
	assert.Assert(t, rep.Worktree.Path != "", "the worktree was never created")
	_, statErr := os.Stat(rep.Worktree.Path)
	assert.Assert(t, os.IsNotExist(statErr), "the worktree was left behind")
	assert.Equal(t, gitOutput(t, root, "branch", "--list", rep.Worktree.Branch), "")
	assert.Equal(t, gitOutput(t, root, "status", "--porcelain"), before)
}

func TestReviewerCount(t *testing.T) {
	prompts := []review.Prompt{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	assert.Equal(t, ReviewerCount(0, prompts), 3, "one per prompt by default")
	assert.Equal(t, ReviewerCount(2, prompts), 2)
	assert.Equal(t, ReviewerCount(5, prompts), 3, "never more than one per prompt")
	assert.Equal(t, ReviewerCount(2, nil), 0, "none without prompts")
}

// The work is committed when the run was cancelled and the last pull fails:
// what earlier rounds brought back is still kept on the run's branch.
func TestCommitWorkCommitsAfterACancelAndAFailedPull(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	root := newProject(t)
	ctx := context.Background()
	wt, err := CreateWorktree(ctx, root, t.TempDir()+"/wt", "run-1")
	assert.NilError(t, err)
	writeFile(t, wt.Path, "flag.go", "package main\n")

	api := httptest.NewServer(fakes.NewFakeCircleCI())
	t.Cleanup(api.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: api.URL})
	assert.NilError(t, err)
	steps := &Sidecars{
		Implementer: &Implementer{Entry: &sidecar.PoolEntry{ID: "no-such-sidecar", RepoPath: "/workspace"}},
		Relay:       NewRelay(client, wt.Path, nil),
	}
	var warnings []string
	status := func(level iostream.Level, msg string) {
		if level == iostream.LevelWarn {
			warnings = append(warnings, msg)
		}
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	assert.Assert(t, commitWork(cancelled, steps, wt, "chunk factory: add a flag", status))

	assert.Equal(t, len(warnings), 1, "%v", warnings)
	assert.Assert(t, strings.HasPrefix(warnings[0], "could not bring back the implementer's last changes"), warnings[0])
	assert.Equal(t, gitOutput(t, root, "show", "--format=", "--name-only", wt.Branch), "flag.go")
}
