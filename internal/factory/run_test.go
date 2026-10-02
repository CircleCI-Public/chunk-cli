package factory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
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
