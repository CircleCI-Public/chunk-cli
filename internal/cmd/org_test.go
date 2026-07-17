package cmd

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

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
