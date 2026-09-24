package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func TestOrgCreateHappyPath(t *testing.T) {
	isolateConfig(t)

	fake := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	t.Setenv(config.EnvCircleToken, "test-token")
	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)

	cmd := insecureStorageCmd(newOrgCreateCmd())
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"my-new-org"})

	err := cmd.Execute()
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out.String(), "my-new-org"),
		"expected org name in output, got: %s", out.String())
	assert.Assert(t, strings.Contains(out.String(), "ID:"),
		"expected ID field in output, got: %s", out.String())
	assert.Assert(t, strings.Contains(out.String(), "Slug:"),
		"expected Slug field in output, got: %s", out.String())
}

func TestOrgCreateAPIError(t *testing.T) {
	isolateConfig(t)

	fake := fakes.NewFakeCircleCI()
	fake.CreateOrgStatusCode = 500
	srv := httptest.NewServer(fake)
	defer srv.Close()

	t.Setenv(config.EnvCircleToken, "test-token")
	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)

	cmd := insecureStorageCmd(newOrgCreateCmd())
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"failing-org"})

	err := cmd.Execute()
	assert.Assert(t, err != nil)

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.UserMessage(), "failing-org"),
		"expected org name in error message, got: %s", ue.UserMessage())
}

func TestOrgCreateRequiresAuth(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	cmd := insecureStorageCmd(newOrgCreateCmd())
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"my-org"})

	err := cmd.Execute()
	assert.Assert(t, err != nil)

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.UserMessage(), "CircleCI authentication required."),
		"expected auth error, got: %s", ue.UserMessage())
}

func TestOrgCreateRequiresName(t *testing.T) {
	cmd := insecureStorageCmd(newOrgCreateCmd())
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	assert.Assert(t, err != nil, "expected error when no name argument given")
}

func setupOrgListFake(t *testing.T) *fakes.FakeCircleCI {
	t.Helper()
	isolateConfig(t)
	fake := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	t.Setenv(config.EnvCircleToken, "test-token")
	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)
	return fake
}

func TestOrgListTableOutput(t *testing.T) {
	fake := setupOrgListFake(t)
	fake.Collaborations = []fakes.Collaboration{
		{ID: "org-abc", Name: "myorg", VCSType: "github"},
		{ID: "org-def", Name: "otherorg", VCSType: "circleci"},
	}

	cmd := insecureStorageCmd(newOrgListCmd())
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err := cmd.Execute()
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out.String(), "org-abc"), "expected org ID in output: %s", out.String())
	assert.Assert(t, strings.Contains(out.String(), "myorg"), "expected org name in output: %s", out.String())
	assert.Assert(t, strings.Contains(out.String(), "org-def"), "expected second org ID in output: %s", out.String())
}

func TestOrgListJSONOutput(t *testing.T) {
	fake := setupOrgListFake(t)
	fake.Collaborations = []fakes.Collaboration{
		{ID: "org-abc", Name: "myorg", VCSType: "github"},
	}

	cmd := insecureStorageCmd(newOrgListCmd())
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--json"})

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	assert.NilError(t, err)

	var collabs []circleci.Collaboration
	assert.NilError(t, json.Unmarshal(out.Bytes(), &collabs))
	assert.Equal(t, len(collabs), 1)
	assert.Equal(t, collabs[0].ID, "org-abc")
}

func TestOrgListJSONEmpty(t *testing.T) {
	setupOrgListFake(t)

	cmd := insecureStorageCmd(newOrgListCmd())
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--json"})

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	assert.NilError(t, err)

	var collabs []circleci.Collaboration
	assert.NilError(t, json.Unmarshal(out.Bytes(), &collabs), "empty list should decode as [], not null")
	assert.Equal(t, len(collabs), 0)
}

func TestOrgListEmpty(t *testing.T) {
	setupOrgListFake(t)

	cmd := insecureStorageCmd(newOrgListCmd())
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err := cmd.Execute()
	assert.NilError(t, err)
	assert.Equal(t, out.Len(), 0, "no table output expected for empty list")
	assert.Assert(t, strings.Contains(errOut.String(), "No organizations found"), "expected warning: %s", errOut.String())
}

func TestOrgListRequiresAuth(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	cmd := insecureStorageCmd(newOrgListCmd())
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	assert.Assert(t, err != nil)
}

// stubPromptOrgName replaces the org name prompt for the duration of a test.
func stubPromptOrgName(t *testing.T, fn func(iostream.Streams) (string, error)) {
	t.Helper()
	orig := promptOrgName
	promptOrgName = fn
	t.Cleanup(func() { promptOrgName = orig })
}

func newOrgClient(t *testing.T, fake *fakes.FakeCircleCI) *circleci.Client {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return client
}

func TestCreateFirstOrg_CreatesNamedOrg(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) { return "  acme  ", nil })
	client := newOrgClient(t, fakes.NewFakeCircleCI())

	var errOut bytes.Buffer
	id, err := createFirstOrg(context.Background(), client, iostream.Streams{Out: io.Discard, Err: &errOut})
	assert.NilError(t, err)
	assert.Equal(t, id, "org-new-1")
	assert.Assert(t, strings.Contains(errOut.String(), `Organization "acme" created.`), errOut.String())
}

func TestCreateFirstOrg_NoTTYSuggestsOrgCreate(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) { return "", ui.ErrNoTTY })
	client := newOrgClient(t, fakes.NewFakeCircleCI())

	_, err := createFirstOrg(context.Background(), client, iostream.Streams{Out: io.Discard, Err: io.Discard})
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Equal(t, ue.UserMessage(), "No organizations found.")
	assert.Assert(t, strings.Contains(ue.suggestion, "chunk org create"), ue.suggestion)
}

func TestCreateFirstOrg_Cancelled(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) { return "", ui.ErrCancelled })
	client := newOrgClient(t, fakes.NewFakeCircleCI())

	_, err := createFirstOrg(context.Background(), client, iostream.Streams{Out: io.Discard, Err: io.Discard})
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Equal(t, ue.UserMessage(), "No organization created.")
}

func TestCreateFirstOrg_EmptyName(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) { return "   ", nil })
	client := newOrgClient(t, fakes.NewFakeCircleCI())

	_, err := createFirstOrg(context.Background(), client, iostream.Streams{Out: io.Discard, Err: io.Discard})
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Equal(t, ue.UserMessage(), "No organization created.")
}

func TestCreateFirstOrg_APIError(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) { return "taken", nil })
	fake := fakes.NewFakeCircleCI()
	fake.CreateOrgStatusCode = 422
	client := newOrgClient(t, fake)

	_, err := createFirstOrg(context.Background(), client, iostream.Streams{Out: io.Discard, Err: io.Discard})
	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.UserMessage(), `"taken"`), ue.UserMessage())
}

func TestOrgPicker_NoOrgs_CreatesOrg(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) { return "acme", nil })
	client := newOrgClient(t, fakes.NewFakeCircleCI())

	id, err := orgPicker(context.Background(), client, "", iostream.Streams{Out: io.Discard, Err: io.Discard})()
	assert.NilError(t, err)
	assert.Equal(t, id, "org-new-1")
}

func TestEnsureOrgAfterSignup_NoOrgs(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) { return "acme", nil })
	srv := httptest.NewServer(fakes.NewFakeCircleCI())
	t.Cleanup(srv.Close)

	var errOut bytes.Buffer
	ensureOrgAfterSignup(context.Background(), iostream.Streams{Out: io.Discard, Err: &errOut}, srv.URL, "test-token")
	assert.Assert(t, strings.Contains(errOut.String(), `Organization "acme" created.`), errOut.String())
}

func TestEnsureOrgAfterSignup_HasOrgs(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) {
		t.Fatal("must not prompt when the account already has an organization")
		return "", nil
	})
	fake := fakes.NewFakeCircleCI()
	fake.Collaborations = []fakes.Collaboration{{ID: "org-abc", Name: "existing"}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var errOut bytes.Buffer
	ensureOrgAfterSignup(context.Background(), iostream.Streams{Out: io.Discard, Err: &errOut}, srv.URL, "test-token")
	assert.Equal(t, errOut.String(), "")
}

func TestEnsureOrgAfterSignup_NoTTYWarns(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) { return "", ui.ErrNoTTY })
	srv := httptest.NewServer(fakes.NewFakeCircleCI())
	t.Cleanup(srv.Close)

	var errOut bytes.Buffer
	ensureOrgAfterSignup(context.Background(), iostream.Streams{Out: io.Discard, Err: &errOut}, srv.URL, "test-token")
	assert.Assert(t, strings.Contains(errOut.String(), "chunk org create"), errOut.String())
}

func TestEnsureOrgAfterSignup_CreateFailedShowsCause(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) { return "acme", nil })
	fake := fakes.NewFakeCircleCI()
	fake.CreateOrgStatusCode = 500
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	var errOut bytes.Buffer
	ensureOrgAfterSignup(context.Background(), iostream.Streams{Out: io.Discard, Err: &errOut}, srv.URL, "test-token")
	assert.Assert(t, strings.Contains(errOut.String(), `Failed to create organization "acme".`), errOut.String())
	assert.Assert(t, strings.Contains(errOut.String(), "500"), errOut.String())
}
