package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// prProject is a factory project whose origin is a bare repository on this
// machine that parses as acme/widgets on GitHub, with a daemon whose GitHub is
// a fake.
func prProject(t *testing.T, result factory.Result) (project string, gh *fakes.FakeGitHub) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	project = factoryProject(t)
	origin := filepath.Join(t.TempDir(), "github.com", "acme", "widgets.git")
	assert.NilError(t, os.MkdirAll(origin, 0o755))
	for _, c := range []struct {
		dir  string
		args []string
	}{
		{origin, []string{"init", "-q", "--bare"}},
		{project, []string{"remote", "set-url", "origin", origin}},
		{project, []string{"push", "-q", "origin", "main"}},
	} {
		cmd := exec.Command("git", c.args...)
		cmd.Dir = c.dir
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, string(out))
	}

	gh = fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	client, err := github.New(github.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	startSessionDaemonWithGitHub(t, fakeFactoryConfig(t, result), client)
	return project, gh
}

func TestFactoryOpensAPullRequestOnceTheChecksPass(t *testing.T) {
	_, gh := prProject(t, factory.ResultPassed)

	_, stderr, err := runFactoryBuildCmd(t, "--open-pr", "add a --verbose flag")

	assert.NilError(t, err, stderr)
	assert.Assert(t, strings.Contains(stderr, "Opened pull request https://github.com/acme/widgets/pull/1"), stderr)
	pulls := gh.PullRequests()
	assert.Equal(t, len(pulls), 1)
	assert.Equal(t, pulls[0].Title, "add a --verbose flag")
	assert.Equal(t, pulls[0].Base, "main")
	assert.Equal(t, pulls[0].Head, "chunk/factory/add-a-verbose-flag/run-1")
	assert.Equal(t, pulls[0].Draft, false)
}

func TestFactoryOpensADraftPullRequestWithDraft(t *testing.T) {
	_, gh := prProject(t, factory.ResultPassed)

	_, stderr, err := runFactoryBuildCmd(t, "--open-pr", "--draft", "add a --verbose flag")

	assert.NilError(t, err, stderr)
	assert.Equal(t, len(gh.PullRequests()), 1)
	assert.Equal(t, gh.PullRequests()[0].Draft, true)
}

func TestFactoryWithoutOpenPRDoesNotPush(t *testing.T) {
	_, gh := prProject(t, factory.ResultPassed)

	_, stderr, err := runFactoryBuildCmd(t, "add a --verbose flag")

	assert.NilError(t, err, stderr)
	assert.Assert(t, !strings.Contains(stderr, "pull request"), stderr)
	assert.Equal(t, len(gh.PullRequests()), 0)
}

// Asking for a pull request when the checks did not pass opens none, and the
// exit is the run's own, not a second failure.
func TestFactoryWhoseChecksFailOpensNoPullRequest(t *testing.T) {
	_, gh := prProject(t, factory.ResultExhausted)

	_, stderr, err := runFactoryBuildCmd(t, "--open-pr", "--max-attempts", "1", "add a --verbose flag")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), "Checks still failed after 1 round(s).")
	assert.Assert(t, strings.Contains(stderr, "No pull request opened: checks still failed after 1 round(s)"), stderr)
	assert.Equal(t, len(gh.PullRequests()), 0)
}

// The person asked for a pull request, so not getting one is an error even
// though the checks passed, and the message says where the work is.
func TestFactoryThatCannotOpenThePullRequestExitsWithAnError(t *testing.T) {
	_, gh := prProject(t, factory.ResultPassed)
	gh.RefusePullRequests(http.StatusForbidden, `{"message":"Resource not accessible by personal access token"}`)

	_, stderr, err := runFactoryBuildCmd(t, "--open-pr", "add a --verbose flag")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.HasPrefix(ue.UserMessage(), "All checks passed, but the pull request could not be opened"), ue.UserMessage())
	assert.Assert(t, strings.Contains(ue.UserMessage(), "Pull requests: write"), ue.UserMessage())
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk/factory/add-a-verbose-flag/run-1"), ue.Suggestion())
	assert.Assert(t, strings.Contains(stderr, "Could not open a pull request"), stderr)
}

func TestFactoryJSONCarriesThePullRequest(t *testing.T) {
	prProject(t, factory.ResultPassed)

	stdout, stderr, err := runFactoryBuildCmd(t, "--json", "--open-pr", "add a --verbose flag")

	assert.NilError(t, err, stderr)
	var detail chunkd.SessionDetail
	assert.NilError(t, json.Unmarshal([]byte(stdout), &detail), stdout)
	assert.DeepEqual(t, *detail.Factory.PullRequest, chunkd.PullRequest{
		State: chunkd.PROpened, Base: "main", URL: "https://github.com/acme/widgets/pull/1", Number: 1,
	})
}

func TestFactoryOpenPRIsRefusedBeforeRunningWithoutAGitHubToken(t *testing.T) {
	factoryProject(t)
	startSessionDaemon(t, fakeFactoryConfig(t, factory.ResultPassed))

	_, _, err := runFactoryBuildCmd(t, "--open-pr", "add a --verbose flag")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), "GitHub token"), ue.UserMessage())
}

func TestFactoryRefusesDraftWithoutOpenPR(t *testing.T) {
	factoryProject(t)

	_, _, err := runFactoryBuildCmd(t, "--draft", "add a --verbose flag")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), "--draft needs --open-pr.")
	assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
}

func TestFactoryHelpDescribesOpenPR(t *testing.T) {
	var out bytes.Buffer
	cmd := newFactoryBuildCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	assert.NilError(t, cmd.Execute())

	for _, want := range []string{"--open-pr", "--draft", "ready for review"} {
		assert.Assert(t, strings.Contains(out.String(), want), "missing %q in:\n%s", want, out.String())
	}
}
