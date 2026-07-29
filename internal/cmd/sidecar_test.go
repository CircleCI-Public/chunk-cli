package cmd

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
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

func TestSidecarSetupPassesEnvImageToCreate(t *testing.T) {
	isolateConfig(t)
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	noop := iostream.StatusFunc(func(_ iostream.Level, _ string) {})
	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}

	id, err := sidecarSetupResolveSidecar(
		context.Background(), client,
		"org-abc", "my-sidecar", t.TempDir(),
		"cimg-go:1.26.5",
		noop, streams,
	)
	assert.NilError(t, err)
	assert.Assert(t, id != "")
	assert.Equal(t, len(cci.Sidecars), 1)
	assert.Equal(t, cci.Sidecars[0].Image, "cimg-go:1.26.5")
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
