package cmd

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
)

func setupSidecarCreateFake(t *testing.T) *fakes.FakeCircleCI {
	t.Helper()
	isolateConfig(t)
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	t.Setenv(config.EnvCircleToken, "test-token")
	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)
	return cci
}

func TestSidecarCreateUsesImageFromConfig(t *testing.T) {
	cci := setupSidecarCreateFake(t)

	dir := t.TempDir()
	t.Chdir(dir)
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		OrgID:      "org-abc",
		Validation: &config.ValidationConfig{SidecarImage: "snap-from-config"},
	}))

	cmd := newSidecarCreateCmd()
	cmd.Flags().Bool("insecure-storage", false, "")
	_ = cmd.Flags().Set("insecure-storage", "true")
	cmd.SetArgs([]string{"--org-id", "org-abc"})
	assert.NilError(t, cmd.Execute())

	assert.Equal(t, len(cci.Sidecars), 1)
	assert.Equal(t, cci.Sidecars[0].Image, "snap-from-config")
}

func TestSnapshotCreateNameTooLong(t *testing.T) {
	cmd := newSidecarSnapshotCreateCmd()
	cmd.SetOut(nil)
	cmd.SetErr(nil)

	longName := strings.Repeat("a", 256)
	cmd.SetArgs([]string{"--name", longName})

	err := cmd.Execute()
	assert.ErrorContains(t, err, "255 characters or fewer")
	assert.ErrorContains(t, err, "256")
}

func TestResolveOrgID_FlagTakesPrecedence(t *testing.T) {
	called := false
	got, err := resolveOrgID("from-flag", t.TempDir(), func() (string, error) {
		called = true
		return "picker-org", nil
	})
	assert.NilError(t, err)
	assert.Equal(t, got, "from-flag")
	assert.Assert(t, !called, "pickOrg should not be called when flag is set")
}

func TestResolveOrgID_EnvVar(t *testing.T) {
	t.Setenv(config.EnvCircleCIOrgID, "env-org")
	called := false
	got, err := resolveOrgID("", t.TempDir(), func() (string, error) {
		called = true
		return "picker-org", nil
	})
	assert.NilError(t, err)
	assert.Equal(t, got, "env-org")
	assert.Assert(t, !called, "pickOrg should not be called when env var is set")
}

func TestResolveOrgID_ProjectConfig(t *testing.T) {
	t.Setenv(config.EnvCircleCIOrgID, "")
	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{OrgID: "config-org"}))
	called := false
	got, err := resolveOrgID("", dir, func() (string, error) {
		called = true
		return "picker-org", nil
	})
	assert.NilError(t, err)
	assert.Equal(t, got, "config-org")
	assert.Assert(t, !called, "pickOrg should not be called when project config has org ID")
}

func TestResolveOrgID_FallsBackToPickOrg(t *testing.T) {
	t.Setenv(config.EnvCircleCIOrgID, "")
	got, err := resolveOrgID("", t.TempDir(), func() (string, error) {
		return "picker-org", nil
	})
	assert.NilError(t, err)
	assert.Equal(t, got, "picker-org")
}

func TestResolveOrgID_PickOrgError(t *testing.T) {
	t.Setenv(config.EnvCircleCIOrgID, "")
	pickErr := errors.New("network failure")
	_, err := resolveOrgID("", t.TempDir(), func() (string, error) {
		return "", pickErr
	})
	assert.ErrorIs(t, err, pickErr)
}

func newOrgPickerClient(t *testing.T, cci *fakes.FakeCircleCI) *circleci.Client {
	t.Helper()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return client
}

func TestOrgPicker_APIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.CollaborationsStatusCode = 500
	client := newOrgPickerClient(t, cci)

	_, err := orgPicker(context.Background(), client)()
	assert.Assert(t, err != nil)
}

func TestOrgPicker_NoOrgs(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	client := newOrgPickerClient(t, cci)

	_, err := orgPicker(context.Background(), client)()
	assert.ErrorContains(t, err, "no organizations found")
}

func TestOrgPicker_SingleOrg(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Collaborations = []fakes.Collaboration{{ID: "org-abc", Name: "myorg"}}
	client := newOrgPickerClient(t, cci)

	got, err := orgPicker(context.Background(), client)()
	assert.NilError(t, err)
	assert.Equal(t, got, "org-abc")
}

func TestOrgPicker_MultipleOrgs_NoTTY(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Collaborations = []fakes.Collaboration{
		{ID: "org-1", Name: "first"},
		{ID: "org-2", Name: "second"},
	}
	client := newOrgPickerClient(t, cci)

	_, err := orgPicker(context.Background(), client)()
	assert.Assert(t, errors.Is(err, tui.ErrNoTTY), "expected ErrNoTTY, got: %v", err)
}

func TestOrgPicker_MultipleOrgs_NonInteractive(t *testing.T) {
	t.Setenv("CI", "true")

	cci := fakes.NewFakeCircleCI()
	cci.Collaborations = []fakes.Collaboration{
		{ID: "org-1", Name: "first"},
		{ID: "org-2", Name: "second"},
	}
	client := newOrgPickerClient(t, cci)

	_, err := orgPicker(context.Background(), client)()
	assert.Assert(t, errors.Is(err, tui.ErrNoTTY), "expected ErrNoTTY in non-interactive mode, got: %v", err)
}

func TestSnapshotCreateNameAtLimit(t *testing.T) {
	cmd := newSidecarSnapshotCreateCmd()

	exactName := strings.Repeat("a", 255)
	cmd.SetArgs([]string{"--name", exactName})

	// Passes name validation; fails later on sidecar ID resolution (no active sidecar).
	// We just confirm it does NOT return the length error.
	err := cmd.Execute()
	if err != nil {
		assert.Assert(t, !strings.Contains(err.Error(), "255 characters or fewer"),
			"unexpected length validation error for 255-char name: %v", err)
	}
}
