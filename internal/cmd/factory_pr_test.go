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

	_, stderr, err := runFactoryBuildCmd(t, "add a --verbose flag")

	assert.NilError(t, err, stderr)
	// The progress said so as it happened, and the summary does not say it again.
	assert.Equal(t, strings.Count(stderr, "Opened pull request https://github.com/acme/widgets/pull/1"), 1, stderr)
	pulls := gh.PullRequests()
	assert.Equal(t, len(pulls), 1)
	assert.Equal(t, pulls[0].Title, "add a --verbose flag")
	assert.Equal(t, pulls[0].Base, "main")
	assert.Equal(t, pulls[0].Head, "chunk/factory/add-a-verbose-flag/run-1")
	assert.Equal(t, pulls[0].Draft, false)
}

func TestFactoryOpensADraftPullRequestWithDraft(t *testing.T) {
	_, gh := prProject(t, factory.ResultPassed)

	_, stderr, err := runFactoryBuildCmd(t, "--draft", "add a --verbose flag")

	assert.NilError(t, err, stderr)
	assert.Equal(t, len(gh.PullRequests()), 1)
	assert.Equal(t, gh.PullRequests()[0].Draft, true)
}

func TestFactoryWithNoOpenPRDoesNotPush(t *testing.T) {
	_, gh := prProject(t, factory.ResultPassed)

	_, stderr, err := runFactoryBuildCmd(t, "--no-open-pr", "add a --verbose flag")

	assert.NilError(t, err, stderr)
	assert.Assert(t, !strings.Contains(stderr, "pull request"), stderr)
	assert.Equal(t, len(gh.PullRequests()), 0)
}

// Asking for a pull request when the checks did not pass opens none, and the
// exit is the run's own, not a second failure.
func TestFactoryWhoseChecksFailOpensNoPullRequest(t *testing.T) {
	_, gh := prProject(t, factory.ResultExhausted)

	_, stderr, err := runFactoryBuildCmd(t, "--max-attempts", "1", "add a --verbose flag")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), "Checks still failed after 1 round(s).")
	assert.Assert(t, strings.Contains(stderr, "No pull request opened: checks still failed after 1 round(s)"), stderr)
	assert.Equal(t, len(gh.PullRequests()), 0)
}

// Opening a pull request is part of the command's work, so not getting one is
// an error even though the checks passed, and the message says where the work
// is.
func TestFactoryThatCannotOpenThePullRequestExitsWithAnError(t *testing.T) {
	_, gh := prProject(t, factory.ResultPassed)
	gh.RefusePullRequests(http.StatusForbidden, `{"message":"Resource not accessible by personal access token"}`)

	_, stderr, err := runFactoryBuildCmd(t, "add a --verbose flag")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.HasPrefix(ue.UserMessage(), "All checks passed, but the pull request could not be opened"), ue.UserMessage())
	assert.Assert(t, strings.Contains(ue.UserMessage(), "Pull requests: write"), ue.UserMessage())
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk/factory/add-a-verbose-flag/run-1"), ue.Suggestion())
	// Said once as it happened and once as the error, not a third time in the summary.
	assert.Equal(t, strings.Count(stderr, "Resource not accessible by personal access token"), 1, stderr)
	assert.Assert(t, strings.Contains(stderr, "could not open the pull request"), stderr)
}

func TestFactoryJSONCarriesThePullRequest(t *testing.T) {
	prProject(t, factory.ResultPassed)

	stdout, stderr, err := runFactoryBuildCmd(t, "--json", "add a --verbose flag")

	assert.NilError(t, err, stderr)
	var detail chunkd.SessionDetail
	assert.NilError(t, json.Unmarshal([]byte(stdout), &detail), stdout)
	assert.DeepEqual(t, *detail.Factory.PullRequest, chunkd.PullRequest{
		State: chunkd.PROpened, Base: "main", URL: "https://github.com/acme/widgets/pull/1", Number: 1,
	})
}

// --open-pr insists on the pull request, so a daemon that could not open one
// refuses the run before it costs anything.
func TestFactoryOpenPRIsRefusedBeforeRunningWithoutAGitHubToken(t *testing.T) {
	factoryProject(t)
	startSessionDaemon(t, fakeFactoryConfig(t, factory.ResultPassed))

	_, _, err := runFactoryBuildCmd(t, "--open-pr", "add a --verbose flag")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), "GitHub token"), ue.UserMessage())
}

// Without --open-pr the same daemon runs the work anyway and says why it
// opened nothing: the change is worth having either way.
func TestFactoryWithoutAGitHubTokenStillRunsAndSaysWhyItOpenedNoPullRequest(t *testing.T) {
	factoryProject(t)
	startSessionDaemon(t, fakeFactoryConfig(t, factory.ResultPassed))

	_, stderr, err := runFactoryBuildCmd(t, "add a --verbose flag")

	assert.NilError(t, err, stderr)
	assert.Assert(t, strings.Contains(stderr, "No pull request opened:"), stderr)
	assert.Assert(t, strings.Contains(stderr, "GITHUB_TOKEN"), stderr)
}

// On a project that can open one, --open-pr changes nothing but the moment a
// problem would be reported: the run still goes ahead and opens it.
func TestFactoryWithOpenPRStillOpensThePullRequest(t *testing.T) {
	_, gh := prProject(t, factory.ResultPassed)

	_, stderr, err := runFactoryBuildCmd(t, "--open-pr", "add a --verbose flag")

	assert.NilError(t, err, stderr)
	assert.Equal(t, len(gh.PullRequests()), 1)
	assert.Equal(t, gh.PullRequests()[0].Head, "chunk/factory/add-a-verbose-flag/run-1")
}

func TestFactoryRefusesPullRequestFlagsThatContradict(t *testing.T) {
	tests := []struct {
		name string
		args []string
		msg  string
	}{
		{
			name: "both ways at once",
			args: []string{"--open-pr", "--no-open-pr"},
			msg:  "--open-pr and --no-open-pr ask for opposite things.",
		},
		{
			name: "a draft of nothing",
			args: []string{"--draft", "--no-open-pr"},
			msg:  "--draft has nothing to open as a draft with --no-open-pr.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factoryProject(t)

			_, _, err := runFactoryBuildCmd(t, append(tt.args, "add a --verbose flag")...)

			var ue *userError
			assert.Assert(t, errors.As(err, &ue), "got %v", err)
			assert.Equal(t, ue.UserMessage(), tt.msg)
			assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
		})
	}
}

func TestFactoryHelpDescribesOpenPR(t *testing.T) {
	var out bytes.Buffer
	cmd := newFactoryBuildCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	assert.NilError(t, cmd.Execute())

	for _, want := range []string{"--open-pr", "--no-open-pr", "--draft", "ready for review"} {
		assert.Assert(t, strings.Contains(out.String(), want), "missing %q in:\n%s", want, out.String())
	}
}

// A remote run never reaches the daemon that opens pull requests, so asking
// for one is refused rather than quietly dropped.
func TestFactoryRefusesPullRequestFlagsWithRemote(t *testing.T) {
	for _, flag := range []string{"--open-pr", "--draft"} {
		t.Run(flag, func(t *testing.T) {
			factoryProject(t)

			_, _, err := runFactoryBuildCmd(t, "--remote", flag, "add a --verbose flag")

			var ue *userError
			assert.Assert(t, errors.As(err, &ue), "got %v", err)
			assert.Equal(t, ue.UserMessage(), "--open-pr and --draft do not work with --remote yet.")
			assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
		})
	}
}
