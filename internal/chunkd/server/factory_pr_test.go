package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// prDaemon is a daemon whose project's origin is a bare repository on this
// machine that parses as acme/widgets on GitHub, and whose GitHub is a fake.
type prDaemon struct {
	*daemon
	root   string
	origin string
	gh     *fakes.FakeGitHub
	ran    bool
}

// newPRDaemon builds a daemon whose factory run commits real work in a real
// worktree and ends with result.
func newPRDaemon(t *testing.T, result factory.Result) *prDaemon {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	pd := &prDaemon{gh: fakes.NewFakeGitHub()}
	pd.daemon, pd.root = newFactoryDaemon(t, func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		pd.ran = true
		wt, err := factory.CreateWorktree(ctx, opts.Root, filepath.Join(t.TempDir(), "wt"), "run-1", opts.Prompt)
		if err != nil {
			return factory.Report{}, err
		}
		opts.OnStart("run-1", wt)
		assert.NilError(t, os.WriteFile(filepath.Join(wt.Path, "flag.go"), []byte("package main\n"), 0o644))
		_, err = wt.Commit(ctx, "add a flag")
		assert.NilError(t, err)
		checks := []factory.Check{{Name: "bugs", Kind: factory.KindReview, Status: factory.StatusPassed}}
		return factory.Report{
			RunID: "run-1", Worktree: wt, Started: true, Committed: true, Stat: "1 file changed",
			Outcome: factory.Outcome{Result: result, Rounds: 1, Checks: checks},
		}, nil
	})

	pd.origin = filepath.Join(t.TempDir(), "github.com", "acme", "widgets.git")
	assert.NilError(t, os.MkdirAll(pd.origin, 0o755))
	git(t, pd.origin, "init", "-q", "--bare")
	git(t, pd.root, "remote", "add", "origin", pd.origin)
	git(t, pd.root, "push", "-q", "origin", "main")

	srv := httptest.NewServer(pd.gh)
	t.Cleanup(srv.Close)
	client, err := github.New(github.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	pd.daemon.gh = client
	return pd
}

func (pd *prDaemon) run(t *testing.T, req chunkd.FactoryRequest) chunkd.SessionDetail {
	t.Helper()
	req.ProjectRoot, req.Prompt = pd.root, "Add a --verbose flag\n\nAnd document it."
	sess, err := pd.startFactory(req)
	assert.NilError(t, err)
	return waitForSessionEnd(t, pd.daemon, sess.ID)
}

func (pd *prDaemon) remoteHas(t *testing.T, branch string) bool {
	t.Helper()
	return git(t, pd.origin, "branch", "--list", branch) != ""
}

func TestFactoryRunThatPassesPushesItsBranchAndOpensAPullRequest(t *testing.T) {
	pd := newPRDaemon(t, factory.ResultPassed)

	detail := pd.run(t, chunkd.FactoryRequest{OpenPR: true})

	pr := detail.Factory.PullRequest
	assert.Assert(t, pr != nil)
	assert.DeepEqual(t, *pr, chunkd.PullRequest{
		State: chunkd.PROpened, Base: "main", URL: "https://github.com/acme/widgets/pull/1", Number: 1,
	})
	assert.Assert(t, pd.remoteHas(t, detail.Factory.Branch), "the branch was not pushed")
	pulls := pd.gh.PullRequests()
	assert.Equal(t, len(pulls), 1)
	got := pulls[0]
	assert.Equal(t, got.Owner+"/"+got.Repo, "acme/widgets")
	assert.Equal(t, got.Head, detail.Factory.Branch)
	assert.Equal(t, got.Base, "main")
	assert.Equal(t, got.Title, "Add a --verbose flag")
	assert.Equal(t, got.Draft, false)
	assert.Assert(t, strings.Contains(got.Body, "> Add a --verbose flag"), got.Body)
	assert.Assert(t, strings.Contains(got.Body, "1 file changed"), got.Body)
	assert.Assert(t, strings.Contains(got.Body, "- bugs (review)"), got.Body)
	// Opening it does not change how the run ended.
	assert.Equal(t, detail.State, chunkd.SessionDone)
	assert.Equal(t, detail.Factory.Result, chunkd.ResultPassed)
}

func TestFactoryRunOpensADraftWhenAskedTo(t *testing.T) {
	pd := newPRDaemon(t, factory.ResultPassed)

	detail := pd.run(t, chunkd.FactoryRequest{OpenPR: true, Draft: true})

	assert.Equal(t, detail.Factory.PullRequest.State, chunkd.PROpened)
	assert.Assert(t, detail.Factory.PullRequest.Draft)
	assert.Equal(t, pd.gh.PullRequests()[0].Draft, true)
}

func TestFactoryRunNeverAskedForAPullRequestPushesNothing(t *testing.T) {
	pd := newPRDaemon(t, factory.ResultPassed)

	detail := pd.run(t, chunkd.FactoryRequest{})

	assert.Assert(t, detail.Factory.PullRequest == nil)
	assert.Assert(t, !pd.remoteHas(t, detail.Factory.Branch), "the branch was pushed unasked")
	assert.Equal(t, len(pd.gh.PullRequests()), 0)
}

func TestFactoryRunWhoseChecksFailOpensNoPullRequest(t *testing.T) {
	for _, result := range []factory.Result{factory.ResultExhausted, factory.ResultStuck, factory.ResultNoChange} {
		t.Run(string(result), func(t *testing.T) {
			pd := newPRDaemon(t, result)

			detail := pd.run(t, chunkd.FactoryRequest{OpenPR: true})

			pr := detail.Factory.PullRequest
			assert.Equal(t, pr.State, chunkd.PRSkipped)
			assert.Assert(t, pr.Message != "")
			assert.Assert(t, !pd.remoteHas(t, detail.Factory.Branch), "work that did not pass was pushed")
			assert.Equal(t, len(pd.gh.PullRequests()), 0)
		})
	}
}

// A pull request GitHub refuses leaves the run passed and its branch pushed,
// so the work can be opened by hand.
func TestFactoryRunWhoseTokenCannotOpenPullRequestsKeepsItsPushedBranch(t *testing.T) {
	pd := newPRDaemon(t, factory.ResultPassed)
	pd.gh.RefusePullRequests(http.StatusForbidden, `{"message":"Resource not accessible by personal access token"}`)

	detail := pd.run(t, chunkd.FactoryRequest{OpenPR: true})

	pr := detail.Factory.PullRequest
	assert.Equal(t, pr.State, chunkd.PRFailed)
	assert.Assert(t, strings.Contains(pr.Message, "Pull requests: write"), pr.Message)
	assert.Assert(t, pd.remoteHas(t, detail.Factory.Branch))
	assert.Equal(t, detail.Factory.Result, chunkd.ResultPassed)
	assert.Equal(t, detail.State, chunkd.SessionDone)
}

func TestFactoryRunTargetsTheDefaultBranchWhenItsOwnIsOnlyLocal(t *testing.T) {
	pd := newPRDaemon(t, factory.ResultPassed)
	pd.gh.SetDefaultBranch("trunk")
	git(t, pd.root, "checkout", "-q", "-b", "topic")

	detail := pd.run(t, chunkd.FactoryRequest{OpenPR: true})

	assert.Equal(t, detail.Factory.PullRequest.State, chunkd.PROpened)
	assert.Equal(t, detail.Factory.PullRequest.Base, "trunk")
	assert.Equal(t, pd.gh.PullRequests()[0].Base, "trunk")
}

func TestFactoryRunTargetsTheBranchItStartedFromWhenTheRemoteHasIt(t *testing.T) {
	pd := newPRDaemon(t, factory.ResultPassed)
	pd.gh.SetDefaultBranch("trunk")
	git(t, pd.root, "checkout", "-q", "-b", "topic")
	git(t, pd.root, "push", "-q", "origin", "topic")

	detail := pd.run(t, chunkd.FactoryRequest{OpenPR: true})

	assert.Equal(t, detail.Factory.PullRequest.Base, "topic")
}

// What a pull request needs is checked before a run that takes minutes starts.
func TestStartFactoryRefusesAPullRequestItCannotOpen(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, pd *prDaemon)
		req    chunkd.FactoryRequest
		status int
		msg    string
	}{
		{
			name:  "no GitHub token",
			setup: func(_ *testing.T, pd *prDaemon) { pd.daemon.gh = nil },
			req:   chunkd.FactoryRequest{OpenPR: true}, status: http.StatusServiceUnavailable, msg: "GITHUB_TOKEN",
		},
		{
			name: "an origin that is not on GitHub",
			setup: func(t *testing.T, pd *prDaemon) {
				git(t, pd.root, "remote", "set-url", "origin", "git@gitlab.com:acme/widgets.git")
			},
			req: chunkd.FactoryRequest{OpenPR: true}, status: http.StatusBadRequest, msg: "must be on GitHub",
		},
		{
			name:  "no origin",
			setup: func(t *testing.T, pd *prDaemon) { git(t, pd.root, "remote", "remove", "origin") },
			req:   chunkd.FactoryRequest{OpenPR: true}, status: http.StatusBadRequest, msg: "must be on GitHub",
		},
		{
			name: "a draft of nothing", setup: func(*testing.T, *prDaemon) {},
			req: chunkd.FactoryRequest{Draft: true}, status: http.StatusBadRequest, msg: "draft only applies",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pd := newPRDaemon(t, factory.ResultPassed)
			tt.setup(t, pd)
			tt.req.ProjectRoot, tt.req.Prompt = pd.root, "add a flag"

			_, err := pd.startFactory(tt.req)

			var ae *apiError
			assert.Assert(t, errors.As(err, &ae), "got %v", err)
			assert.Equal(t, ae.status, tt.status)
			assert.ErrorContains(t, ae, tt.msg)
			assert.Assert(t, !pd.ran, "a run started that could not open its pull request")
		})
	}
}
