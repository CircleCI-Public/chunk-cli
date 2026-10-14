package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
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

	cmd := insecureStorageCmd(newSidecarCreateCmd())
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

	_, err := orgPicker(context.Background(), client, "", iostream.Streams{Out: io.Discard, Err: io.Discard})()
	assert.Assert(t, err != nil)
}

func TestOrgPicker_NoOrgs(t *testing.T) {
	stubPromptOrgName(t, func(iostream.Streams) (string, error) { return "", ui.ErrNoTTY })
	cci := fakes.NewFakeCircleCI()
	client := newOrgPickerClient(t, cci)

	_, err := orgPicker(context.Background(), client, "", iostream.Streams{Out: io.Discard, Err: io.Discard})()
	assert.ErrorContains(t, err, "no organizations found")
}

func TestOrgPicker_SingleOrg(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Collaborations = []fakes.Collaboration{{ID: "org-abc", Name: "myorg"}}
	client := newOrgPickerClient(t, cci)

	got, err := orgPicker(context.Background(), client, "", iostream.Streams{Out: io.Discard, Err: io.Discard})()
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

	_, err := orgPicker(context.Background(), client, "", iostream.Streams{Out: io.Discard, Err: io.Discard})()
	assert.Assert(t, errors.Is(err, ui.ErrNoTTY), "expected ErrNoTTY, got: %v", err)
}

func TestOrgPicker_MultipleOrgs_NonInteractive(t *testing.T) {
	t.Setenv("CI", "true")

	cci := fakes.NewFakeCircleCI()
	cci.Collaborations = []fakes.Collaboration{
		{ID: "org-1", Name: "first"},
		{ID: "org-2", Name: "second"},
	}
	client := newOrgPickerClient(t, cci)

	_, err := orgPicker(context.Background(), client, "", iostream.Streams{Out: io.Discard, Err: io.Discard})()
	assert.Assert(t, errors.Is(err, ui.ErrNoTTY), "expected ErrNoTTY in non-interactive mode, got: %v", err)
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

func TestRecordSnapshotImage(t *testing.T) {
	never := func(string, bool) (bool, error) {
		t.Fatal("asked to confirm")
		return false, nil
	}
	answer := func(ok bool, err error) confirmFunc {
		return func(string, bool) (bool, error) { return ok, err }
	}

	tests := []struct {
		name    string
		current string // "" leaves validation.sidecarImage unset
		confirm confirmFunc
		want    string
		wantErr string // a line stderr must contain
	}{
		{name: "unset is recorded", confirm: never, want: "snap-new", wantErr: "Recorded snap-new"},
		{name: "same image is left alone", current: "snap-new", confirm: never, want: "snap-new"},
		{name: "replaced when confirmed", current: "snap-old", confirm: answer(true, nil), want: "snap-new", wantErr: "Recorded snap-new"},
		{name: "kept when declined", current: "snap-old", confirm: answer(false, nil), want: "snap-old", wantErr: "Kept validation.sidecarImage snap-old"},
		{name: "kept without a terminal", current: "snap-old", confirm: answer(false, ui.ErrNoTTY), want: "snap-old", wantErr: "chunk config set validation.sidecarImage snap-new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := &config.ProjectConfig{OrgID: "org-1"}
			if tt.current != "" {
				cfg.Validation = &config.ValidationConfig{SidecarImage: tt.current}
			}
			assert.NilError(t, config.SaveProjectConfig(dir, cfg))

			confirm := func(label string, defaultYes bool) (bool, error) {
				assert.Check(t, defaultYes, "pressing Enter should replace the image")
				return tt.confirm(label, defaultYes)
			}
			var errOut strings.Builder
			recordSnapshotImage(dir, "snap-new", iostream.Streams{Out: io.Discard, Err: &errOut}, confirm)

			got, err := config.LoadProjectConfig(dir)
			assert.NilError(t, err)
			assert.Equal(t, got.Validation.SidecarImage, tt.want)
			assert.Equal(t, got.OrgID, "org-1", "the rest of the config is kept")
			assert.Assert(t, strings.Contains(errOut.String(), tt.wantErr), "stderr: %s", errOut.String())
		})
	}
}

func TestRecordSnapshotImageWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	var errOut strings.Builder
	recordSnapshotImage(dir, "snap-new", iostream.Streams{Out: io.Discard, Err: &errOut}, nil)

	_, err := config.LoadProjectConfig(dir)
	assert.Assert(t, errors.Is(err, os.ErrNotExist), "no config is created: %v", err)
	assert.Assert(t, strings.Contains(errOut.String(), "chunk config set validation.sidecarImage snap-new"), "stderr: %s", errOut.String())
}

func TestRecordSnapshotImageWithInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".chunk", "config.json")
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	const bad = "{not json"
	assert.NilError(t, os.WriteFile(path, []byte(bad), 0o644))

	var errOut strings.Builder
	recordSnapshotImage(dir, "snap-new", iostream.Streams{Out: io.Discard, Err: &errOut}, nil)

	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, string(data), bad, "the invalid config is left untouched")
	assert.Assert(t, strings.Contains(errOut.String(), "Warning: could not record"), "stderr: %s", errOut.String())
	assert.Assert(t, strings.Contains(errOut.String(), "chunk config set validation.sidecarImage snap-new"), "stderr: %s", errOut.String())
}

func TestSidecarSetupError(t *testing.T) {
	assert.NilError(t, sidecarSetupError(nil, ""))

	phrased := &userError{msg: "Could not create the sidecar."}
	assert.Equal(t, sidecarSetupError(phrased, ""), error(phrased))

	step, ok := errors.AsType[*userError](sidecarSetupError(&sidecar.SetupStepError{Step: "install", ExitCode: 1}, ""))
	assert.Assert(t, ok)
	assert.Equal(t, step.msg, `Setup step "install" exited with status 1.`)

	denied, ok := errors.AsType[*userError](sidecarSetupError(fmt.Errorf("sync files to the sidecar: %w", circleci.ErrNotAuthorized), "env"))
	assert.Assert(t, ok)
	assert.Equal(t, denied.msg, "Not authorized to sync files.")
}

func TestLoadEnvironmentUsesSavedEnvironment(t *testing.T) {
	dir := t.TempDir()
	streams, _, errOut := testStreams()

	saved := &envbuilder.Environment{Stack: "go", Image: "cimg/go", ImageVersion: "1.25.3"}
	cfg := &config.ProjectConfig{Environment: saved}
	env, cached, err := loadEnvironment(context.Background(), dir, cfg, false, newStatusFunc(streams))
	assert.NilError(t, err)
	assert.Assert(t, cached)
	assert.Equal(t, env, saved)
	assert.Assert(t, strings.Contains(errOut.String(), "Using environment from .chunk/config.json"))
	// Nothing is saved; that is left to the caller.
	_, err = os.Stat(filepath.Join(dir, ".chunk", "config.json"))
	assert.Assert(t, os.IsNotExist(err))
}

func TestLoadEnvironmentDetectsWhenForced(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/widget\n\ngo 1.25\n"), 0o644))
	streams, _, _ := testStreams()

	saved := &envbuilder.Environment{Stack: "python"}
	cfg := &config.ProjectConfig{Environment: saved}
	env, cached, err := loadEnvironment(context.Background(), dir, cfg, true, newStatusFunc(streams))
	assert.NilError(t, err)
	assert.Assert(t, !cached)
	assert.Equal(t, env.Stack, "go")
	assert.Equal(t, cfg.Environment.Stack, "go")
	_, err = os.Stat(filepath.Join(dir, ".chunk", "config.json"))
	assert.Assert(t, os.IsNotExist(err), "loadEnvironment must leave saving to the caller")
}
