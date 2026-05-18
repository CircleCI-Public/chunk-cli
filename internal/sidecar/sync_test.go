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

// TestSync_NonApplyFailureReturnsImmediately verifies that Sync does not send a
// "rm -rf" cleanup command when syncWorkspace fails for a reason other than a
// git-apply failure. MUT-013 caught this gap by inverting the errApplyFailed
// check, which caused Sync to retry (and rm -rf the remote workspace) for all
// failure types, not just patch-apply failures.
func TestSync_NonApplyFailureReturnsImmediately(t *testing.T) {
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)

	// SSH server: mkdir-p and test-d succeed; git rev-parse fails (exit 1) to
	// simulate a sidecar where origin/HEAD is not configured — a RemoteBaseError,
	// which is a non-errApplyFailed error and must not trigger rm -rf.
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResultFunc(func(cmd string) (string, int) {
		if strings.Contains(cmd, "rev-parse") {
			return "fatal: ambiguous argument 'origin/HEAD'", 1
		}
		return "", 0
	})

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

	err := sidecar.Sync(context.Background(), cl, "sb-1", keyFile, "", "", noopStatus)

	// Sync must return an error (MergeBase failed — no upstream branch).
	assert.Assert(t, err != nil, "expected Sync to return an error")

	// The error must be a RemoteBaseError, not an apply failure.
	var remoteBaseErr *sidecar.RemoteBaseError
	assert.Assert(t, errors.As(err, &remoteBaseErr),
		"expected RemoteBaseError, got: %T %v", err, err)

	// Critically: no rm -rf must have been sent. With MUT-013, Sync would treat
	// the RemoteBaseError as a retryable apply failure and issue a rm -rf before
	// the second syncWorkspace attempt.
	for _, cmd := range sshSrv.Commands() {
		assert.Assert(t, !strings.Contains(cmd, "rm -rf"),
			"Sync must not send rm -rf for non-apply failures; got command: %q", cmd)
	}
}
