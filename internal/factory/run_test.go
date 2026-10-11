package factory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/circleci"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/review"
	"github.com/smeltdev/smelt/internal/sidecar"
	"github.com/smeltdev/smelt/internal/testing/fakes"
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
	_, err = LoadRecord(root, rep.RunID)
	assert.ErrorIs(t, err, ErrNoRecord, "a run with no work left a record to continue")
}

// A continued run that fails before the implementer starts keeps the earlier
// run's worktree, with the work's files back in it, and its branch.
func TestContinuedRunThatFailsBeforeTheImplementerKeepsTheWork(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	root, rec := finishedRun(t)
	tip := gitOutput(t, root, "rev-parse", rec.Branch)

	cci := fakes.NewFakeCircleCI()
	cci.CreateStatusCode = http.StatusInternalServerError
	api := httptest.NewServer(cci)
	t.Cleanup(api.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: api.URL})
	assert.NilError(t, err)

	rep, err := Run(context.Background(), RunOptions{
		Root:     root,
		Continue: &Continuation{From: rec},
		Attempts: 1,
		Client:   client,
		OrgID:    "org-1",
		Status:   func(iostream.Level, string) {},
	})

	assert.ErrorContains(t, err, "create the run's sidecars: ")
	assert.Equal(t, rep.Worktree.Path, rec.Worktree)
	assert.Equal(t, readFile(t, rec.Worktree, "flag.go"), "package main\n", "the work's files were not put back")
	assert.Equal(t, gitOutput(t, rec.Worktree, "status", "--porcelain"), "")
	assert.Equal(t, gitOutput(t, root, "rev-parse", rec.Branch), tip)
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
	wt, err := CreateWorktree(ctx, WorktreeOptions{Root: root, Path: t.TempDir() + "/wt", RunID: "run-1", Prompt: "add a flag"})
	assert.NilError(t, err)
	writeFile(t, wt.Path, "flag.go", "package main\n")

	api := httptest.NewServer(fakes.NewFakeCircleCI())
	t.Cleanup(api.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: api.URL})
	assert.NilError(t, err)
	steps := &Sidecars{
		Implementer: &Implementer{Entry: &sidecar.PoolEntry{ID: "no-such-sidecar", RepoPath: "/workspace"}},
		Relay:       NewRelay(sidecar.NewCircleCIBackend(client, ""), wt.Path, nil),
	}
	var warnings []string
	status := func(level iostream.Level, msg string) {
		if level == iostream.LevelWarn {
			warnings = append(warnings, msg)
		}
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	assert.Assert(t, commitWork(cancelled, steps, wt, "smelt factory: add a flag", status))

	assert.Equal(t, len(warnings), 1, "%v", warnings)
	assert.Assert(t, strings.HasPrefix(warnings[0], "could not bring back the implementer's last changes"), warnings[0])
	assert.Equal(t, gitOutput(t, root, "show", "--format=", "--name-only", wt.Branch), "flag.go")
}

// A run with a log records itself there, its failure included, and keeps it
// open until its cleanup has reported too.
func TestRunKeepsItsLog(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	root := newProject(t)
	cci := fakes.NewFakeCircleCI()
	cci.CreateStatusCode = http.StatusInternalServerError
	api := httptest.NewServer(cci)
	t.Cleanup(api.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: api.URL})
	assert.NilError(t, err)

	path := filepath.Join(t.TempDir(), "run.log")
	var shown []string
	rep, err := Run(context.Background(), RunOptions{
		Root:     root,
		Prompt:   "add a --verbose flag",
		Attempts: 1,
		Client:   client,
		OrgID:    "org-1",
		Log:      path,
		Status:   func(_ iostream.Level, msg string) { shown = append(shown, msg) },
	})
	assert.ErrorContains(t, err, "create the run's sidecars: ")
	assert.Equal(t, rep.Log, path)

	b, err := os.ReadFile(path)
	assert.NilError(t, err)
	got := string(b)
	for _, want := range []string{
		"info  run " + rep.RunID + "\n",
		"info  prompt:\n    add a --verbose flag\n",
		"info  Factory run: backend=circleci image=default attempts=1 reviewers=0 model=default\n",
		"step  Preparing an implementer sidecar and 0 reviewer sidecar(s)...\n",
		"end   stopped: create the run's sidecars: ",
	} {
		assert.Assert(t, strings.Contains(got, want), "missing %q in:\n%s", want, got)
	}
	assert.Assert(t, strings.Index(got, "info  Factory run:") < strings.Index(got, "step  Preparing"), got)
	// Every status line the caller saw is in the log.
	for _, msg := range shown {
		assert.Assert(t, strings.Contains(got, msg), "status %q is not in the log:\n%s", msg, got)
	}
	// The end is the last thing written.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	assert.Assert(t, strings.Contains(lines[len(lines)-1], " end "), got)
}

func TestRunSummary(t *testing.T) {
	tests := []struct {
		name      string
		backend   string
		image     string
		attempts  int
		reviewers int
		model     string
		want      string
	}{
		{"all fields", "docker", "smelt-sandbox:local", 5, 2, "opus", "Factory run: backend=docker image=smelt-sandbox:local attempts=5 reviewers=2 model=opus"},
		{"empty image and model", "docker", "", 5, 2, "", "Factory run: backend=docker image=default attempts=5 reviewers=2 model=default"},
		{"empty backend", "", "img", 1, 1, "m", "Factory run: backend=circleci image=img attempts=1 reviewers=1 model=m"},
		{"zero reviewers", "docker", "img", 1, 0, "m", "Factory run: backend=docker image=img attempts=1 reviewers=0 model=m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, runSummary(tc.backend, tc.image, tc.attempts, tc.reviewers, tc.model), tc.want)
		})
	}
}

func TestRunOptionsSummaryNamesTheBackend(t *testing.T) {
	assert.Equal(t, (&RunOptions{Attempts: 3, Reviewers: 2}).summary(),
		"Factory run: backend=circleci image=default attempts=3 reviewers=2 model=default")
	got := (&RunOptions{Backend: sidecar.NewCircleCIBackend(nil, ""), Image: "img", Model: "m"}).summary()
	assert.Equal(t, got, "Factory run: backend=docker image=img attempts=0 reviewers=0 model=m")
}
