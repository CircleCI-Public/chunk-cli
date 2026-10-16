package sidecar

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/envspec"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func newProvisionClient(t *testing.T, cci *fakes.FakeCircleCI) *circleci.Client {
	t.Helper()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return client
}

func provisionOptions(t *testing.T, client *circleci.Client) ProvisionOptions {
	t.Helper()
	var out, errOut bytes.Buffer
	return ProvisionOptions{
		Dir:     t.TempDir(),
		Client:  client,
		Sidecar: func(context.Context) (string, error) { return "sidecar-123", nil },
		Streams: iostream.Streams{Out: &out, Err: &errOut},
		Status:  func(iostream.Level, string) {},
	}
}

func TestProvisionWrapsSyncFailureWithErrSync(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	// The SSH key is generated fine, but registering it with the sidecar fails,
	// so the sync cannot open a session.
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyStatusCode = 500

	opts := provisionOptions(t, newProvisionClient(t, cci))
	err := Provision(context.Background(), opts, &envbuilder.Environment{})
	assert.Assert(t, err != nil)
	assert.Assert(t, errors.Is(err, ErrSync), "got: %v", err)
	assert.Assert(t, !errors.Is(err, ErrSSHKey), "got: %v", err)
}

func TestProvisionWrapsKeyFailureWithErrSSHKey(t *testing.T) {
	// DefaultKeyPath is $HOME/.ssh/chunk_ai; with HOME a regular file, statting
	// or creating anything beneath it fails with ENOTDIR.
	home := filepath.Join(t.TempDir(), "home")
	assert.NilError(t, os.WriteFile(home, []byte("not a directory"), 0o600))
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	opts := provisionOptions(t, newProvisionClient(t, fakes.NewFakeCircleCI()))
	err := Provision(context.Background(), opts, &envbuilder.Environment{})
	assert.Assert(t, err != nil)
	assert.Assert(t, errors.Is(err, ErrSSHKey), "got: %v", err)
	assert.Assert(t, !errors.Is(err, ErrSync), "got: %v", err)
}

func TestProvisionSkipsTheTestStep(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	sshSrv := fakes.NewSSHServerAcceptingAnyKey(t)
	sshSrv.SetResult("", 0)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()

	opts := provisionOptions(t, newProvisionClient(t, cci))
	opts.SkipSync = true
	env := &envbuilder.Environment{Setup: []envbuilder.Step{
		{Name: "install", Command: "npm install"},
		{Name: envspec.StepTest, Command: "npm test"},
	}}

	assert.NilError(t, Provision(context.Background(), opts, env))

	cmds := sshSrv.Commands()
	assert.Equal(t, len(cmds), 1, "got: %v", cmds)
	assert.Assert(t, strings.Contains(cmds[0], "npm install"), "got: %s", cmds[0])
	assert.Assert(t, !strings.Contains(cmds[0], "npm test"), "got: %s", cmds[0])
}

func TestProvisionRunsInTheSavedWorkspaceOnlyForItsSidecar(t *testing.T) {
	for _, tc := range []struct {
		name     string
		activeID string
		wantCD   bool
	}{
		{name: "active sidecar", activeID: "sidecar-123", wantCD: true},
		{name: "another sidecar", activeID: "sidecar-other", wantCD: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.EnvHome, t.TempDir())
			t.Setenv(config.EnvXDGDataHome, t.TempDir())
			ctx := context.Background()
			assert.NilError(t, SaveActive(ctx, ActiveSidecar{SidecarIDs: []string{tc.activeID}, Workspace: "/home/user/widget"}))

			sshSrv := fakes.NewSSHServerAcceptingAnyKey(t)
			sshSrv.SetResult("", 0)
			cci := fakes.NewFakeCircleCI()
			cci.AddKeyURL = sshSrv.Addr()

			opts := provisionOptions(t, newProvisionClient(t, cci))
			opts.SkipSync = true
			env := &envbuilder.Environment{Setup: []envbuilder.Step{{Name: "install", Command: "npm install"}}}
			assert.NilError(t, Provision(ctx, opts, env))

			cmds := sshSrv.Commands()
			assert.Equal(t, len(cmds), 1, "got: %v", cmds)
			assert.Equal(t, strings.Contains(cmds[0], "/home/user/widget"), tc.wantCD, "got: %s", cmds[0])
		})
	}
}
