package sidecar_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// TestSync_StaleBaselineBootstrapsAndReturnsRemoteBaseError verifies that Sync
// triggers Bootstrap when no baseline is stored, and that Bootstrap surfaces a
// RemoteBaseError when the branch is not pushed and MergeBase fails (no upstream
// tracking branch). Crucially, no rm -rf must be issued at any point.
func TestSync_StaleBaselineBootstrapsAndReturnsRemoteBaseError(t *testing.T) {
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)

	// SSH server: all commands succeed (exitCode 0), so mkdir-p, test-d, fetch
	// all pass. Bootstrap then calls gitutil.MergeBase() locally, which fails
	// because the test repo has no upstream tracking branch.
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("", 0)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	// Isolated HOME so OpenSession resolves known_hosts to a writable temp path.
	t.Setenv(config.EnvHome, t.TempDir())
	// Isolated XDG_DATA_HOME so sidecar state files don't bleed across tests.
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	repoDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	t.Chdir(repoDir)

	cl := newClient(t, srv.URL)
	noopStatus := iostream.StatusFunc(func(_ iostream.Level, _ string) {})

	err := sidecar.Sync(context.Background(), cl, "sb-1", keyFile, "", "", repoDir, noopStatus)

	// Sync must return an error (MergeBase failed — no upstream branch).
	assert.Assert(t, err != nil, "expected Sync to return an error")

	// The error must be a RemoteBaseError.
	var remoteBaseErr *sidecar.RemoteBaseError
	assert.Assert(t, errors.As(err, &remoteBaseErr),
		"expected RemoteBaseError, got: %T %v", err, err)

	// No rm -rf must have been sent at any point.
	for _, cmd := range sshSrv.Commands() {
		assert.Assert(t, !strings.Contains(cmd, "rm -rf"),
			"Sync must not send rm -rf; got command: %q", cmd)
	}
}
