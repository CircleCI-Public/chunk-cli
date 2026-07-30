package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestOrgCreateHappyPath(t *testing.T) {
	isolateConfig(t)

	fake := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	t.Setenv(config.EnvCircleToken, "test-token")
	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)

	cmd := newOrgCreateCmd()
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

	cmd := newOrgCreateCmd()
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

	cmd := newOrgCreateCmd()
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
	cmd := newOrgCreateCmd()
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

	cmd := newOrgListCmd()
	cmd.Flags().Bool("insecure-storage", false, "")
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

	cmd := newOrgListCmd()
	cmd.Flags().Bool("insecure-storage", false, "")
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

	cmd := newOrgListCmd()
	cmd.Flags().Bool("insecure-storage", false, "")
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

	cmd := newOrgListCmd()
	cmd.Flags().Bool("insecure-storage", false, "")
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

	cmd := newOrgListCmd()
	cmd.Flags().Bool("insecure-storage", false, "")
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	assert.Assert(t, err != nil)
}
