package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// fallbackEnv gives a project rooted in a temp dir with its own state and
// config directories, so nothing here reads or writes the developer's real
// sidecar state.
func fallbackEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv(config.EnvCircleCIOrgID, "")

	stateDir, err := sidecar.StateDir()
	assert.NilError(t, err)
	assert.NilError(t, os.MkdirAll(stateDir, 0o755))
	return dir
}

func fallbackClient(t *testing.T, cci *fakes.FakeCircleCI) *circleci.Client {
	t.Helper()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return client
}

// TestResolveSidecarReportsCreateRejection is the regression test for FACT-426.
//
// A token that cannot create sidecars used to produce a one-line warning and
// then a full local run: the commands the config marked remote were executed
// on the developer's machine and reported as if they had passed on a sidecar.
// The rejection must reach the caller instead.
func TestResolveSidecarReportsCreateRejection(t *testing.T) {
	workDir := fallbackEnv(t)
	cci := fakes.NewFakeCircleCI()
	cci.CreateStatusCode = http.StatusForbidden

	var sidecarID string
	created, err := resolveSidecar(
		context.Background(), fallbackClient(t, cci), &sidecarID,
		"org-1", "", workDir, nil,
		iostream.Streams{Out: io.Discard, Err: io.Discard},
	)

	assert.Assert(t, err != nil, "a rejected create must fail the run, not fall back to a local one")
	assert.Assert(t, !created)
	assert.Equal(t, sidecarID, "", "no sidecar ID may be reported when creation was refused")

	// The old code printed only the wrapped cause ("create sidecar: not
	// authorized"), discarding the message and suggestion that say what to do.
	ue, ok := errors.AsType[*userError](err)
	assert.Assert(t, ok, "want a structured userError, got %T: %v", err, err)
	assert.Equal(t, ue.ErrorCode(), "sidecar.not_authorized")
	assert.Assert(t, strings.Contains(ue.Detail(), "org-1"),
		"the detail must name the org that refused, got %q", ue.Detail())
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk org list"),
		"the suggestion must point at a next step, got %q", ue.Suggestion())
	assert.Equal(t, ue.UserExitCode(), ExitAuthError)
}

// TestResolveSidecarReportsUnpickableOrg covers the no-org-ID case: a fresh
// repo with nothing in .chunk/config.json, more than one collaboration to
// choose from, and no TTY to choose with. The picker's suggestion is the
// useful part and used to be swallowed with the error.
func TestResolveSidecarReportsUnpickableOrg(t *testing.T) {
	workDir := fallbackEnv(t)
	cci := fakes.NewFakeCircleCI()
	cci.Collaborations = []fakes.Collaboration{
		{ID: "org-aaa", Name: "circleci", VCSType: "github"},
		{ID: "org-bbb", Name: "circleci-public", VCSType: "github"},
	}

	var sidecarID string
	created, err := resolveSidecar(
		context.Background(), fallbackClient(t, cci), &sidecarID,
		"", "", workDir, nil,
		iostream.Streams{Out: io.Discard, Err: io.Discard},
	)

	assert.Assert(t, err != nil, "an unresolvable org must fail the run, not fall back to a local one")
	assert.Assert(t, !created)

	ue, ok := errors.AsType[*userError](err)
	assert.Assert(t, ok, "want a structured userError, got %T: %v", err, err)
	assert.Assert(t, strings.Contains(ue.Suggestion(), "orgID"),
		"the suggestion must name the setting that fixes this, got %q", ue.Suggestion())
}

// TestResolveSidecarUsesActiveSidecar guards the path that must stay quiet:
// when state already names a sidecar there is nothing to create and no error
// to report.
func TestResolveSidecarUsesActiveSidecar(t *testing.T) {
	workDir := fallbackEnv(t)
	cci := fakes.NewFakeCircleCI()
	cci.CreateStatusCode = http.StatusForbidden // must never be reached

	var sidecarID string
	created, err := resolveSidecar(
		context.Background(), fallbackClient(t, cci), &sidecarID,
		"org-1", "", workDir, &sidecar.ActiveSidecar{SidecarID: "sc-existing"},
		iostream.Streams{Out: io.Discard, Err: io.Discard},
	)

	assert.NilError(t, err)
	assert.Assert(t, !created, "reusing state is not a fresh provision")
	assert.Equal(t, sidecarID, "sc-existing")
}

// TestCannotCreateSidecarNamesOrgSource covers the ambiguity that made Claire's
// report hard to act on: four different places can supply the org ID, and the
// error never said which one had been used.
func TestCannotCreateSidecarNamesOrgSource(t *testing.T) {
	rejected := circleci.ErrNotAuthorized

	t.Run("names an explicit source", func(t *testing.T) {
		err := cannotCreateSidecar("org-1", "--org-id", rejected)
		assert.Assert(t, err != nil)
		ue, ok := errors.AsType[*userError](err)
		assert.Assert(t, ok)
		assert.Assert(t, strings.Contains(ue.Detail(), "--org-id"),
			"got %q", ue.Detail())
	})

	// An empty source means the org was auto-picked from a single
	// collaboration, which is exactly the case where the user never chose it.
	t.Run("omits the clause when auto-picked", func(t *testing.T) {
		err := cannotCreateSidecar("org-1", "", rejected)
		ue, ok := errors.AsType[*userError](err)
		assert.Assert(t, ok)
		assert.Assert(t, strings.Contains(ue.Detail(), "org-1"), "got %q", ue.Detail())
		assert.Assert(t, !strings.Contains(ue.Detail(), "org ID from"),
			"no source clause when there is no source, got %q", ue.Detail())
	})

	t.Run("passes through non-auth errors", func(t *testing.T) {
		assert.Assert(t, cannotCreateSidecar("org-1", "--org-id", io.EOF) == nil,
			"only authorization failures are this function's business")
	})
}

// TestUnreachableSidecarDoesNotDegradeToLocal covers the second half of
// FACT-426. Creation was only the first way to end up running remote-marked
// commands locally; a sidecar that already existed but could not be reached
// took the same warn-and-continue path and produced the same false green.
func TestUnreachableSidecarDoesNotDegradeToLocal(t *testing.T) {
	err := unreachableSidecar("sc-1", "test, lint", io.EOF)

	ue, ok := errors.AsType[*userError](err)
	assert.Assert(t, ok, "want a structured userError, got %T: %v", err, err)
	assert.Equal(t, ue.ErrorCode(), "sidecar.unreachable")
	assert.Equal(t, ue.UserExitCode(), ExitAPIError)
	assert.Assert(t, strings.Contains(ue.Detail(), "test, lint"),
		"the detail must name the commands that did not run, got %q", ue.Detail())
	// A sidecar that has gone unreachable will not fix itself, so "try again"
	// would send the user in a circle.
	assert.Assert(t, !strings.Contains(ue.Suggestion(), "Try again"),
		"got %q", ue.Suggestion())
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk sidecar"),
		"the suggestion must point at a next step, got %q", ue.Suggestion())
}

// TestMissingWorkspaceDoesNotDegradeToLocal covers the most reachable of the
// two: a sidecar that syncs fine but never had 'chunk sidecar env build' run
// still has no workspace to execute in.
func TestMissingWorkspaceDoesNotDegradeToLocal(t *testing.T) {
	err := missingWorkspace("sc-1", "/home/circleci/project", "test", io.EOF)

	ue, ok := errors.AsType[*userError](err)
	assert.Assert(t, ok, "want a structured userError, got %T: %v", err, err)
	assert.Equal(t, ue.ErrorCode(), "sidecar.workspace_missing")
	assert.Equal(t, ue.UserExitCode(), ExitNotFound)
	assert.Assert(t, strings.Contains(ue.Detail(), "/home/circleci/project"),
		"the detail must say where the workspace was expected, got %q", ue.Detail())
	assert.Assert(t, strings.Contains(ue.Detail(), "test"),
		"the detail must name the commands that did not run, got %q", ue.Detail())
	assert.Assert(t, strings.Contains(ue.Suggestion(), "env build"),
		"the suggestion must name the command that builds it, got %q", ue.Suggestion())
}

// TestSidecarSyncErrorKeepsSSHGuidance covers the other half of what made
// Claire's report hard to act on. sshSessionError already phrases a missing key
// with the ssh-keygen command that creates it, but the sync path wrapped the
// cause bare, so the suggestion was dropped and only "ssh key not found: <path>"
// reached the user.
func TestSidecarSyncErrorKeepsSSHGuidance(t *testing.T) {
	keyErr := &sidecar.KeyNotFoundError{Path: "/home/dev/.ssh/chunk_ai"}

	err := sidecarSyncError(
		context.Background(), nil, "sc-1", keyErr,
		iostream.Streams{Out: io.Discard, Err: io.Discard},
	)

	ue, ok := errors.AsType[*userError](err)
	assert.Assert(t, ok, "want a structured userError, got %T: %v", err, err)
	assert.Equal(t, ue.ErrorCode(), "ssh.key_not_found")
	assert.Assert(t, strings.Contains(ue.Suggestion(), "ssh-keygen"),
		"the suggestion must name the command that creates the key, got %q", ue.Suggestion())
	assert.Assert(t, strings.Contains(ue.Suggestion(), "/home/dev/.ssh/chunk_ai"),
		"the suggestion must name the path it expected, got %q", ue.Suggestion())
}

// A sync failure that is not an SSH problem must keep the sync framing rather
// than be forced through the SSH classifier.
func TestSidecarSyncErrorKeepsSyncFramingForOtherFailures(t *testing.T) {
	err := sidecarSyncError(
		context.Background(), nil, "sc-1", io.ErrUnexpectedEOF,
		iostream.Streams{Out: io.Discard, Err: io.Discard},
	)

	ue, ok := errors.AsType[*userError](err)
	assert.Assert(t, ok, "want a structured userError, got %T: %v", err, err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), "sync"),
		"got %q", ue.UserMessage())
}
