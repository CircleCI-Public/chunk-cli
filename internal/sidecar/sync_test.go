package sidecar_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// setupRepoWithOriginHEAD creates a git repo whose remote URL looks like
// github.com/org/repo and whose refs/remotes/origin/HEAD points to the current
// HEAD SHA, so that gitutil.MergeBase can resolve a base commit.
func setupRepoWithOriginHEAD(t *testing.T, org, repo string) string {
	t.Helper()
	dir := gitrepo.SetupGitRepo(t, org, repo)
	env := gitrepo.GitEnv(dir)

	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	head := strings.TrimSpace(string(out))

	for _, args := range [][]string{
		{"update-ref", "refs/remotes/origin/main", head},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// TestSync_NonApplyFailureReturnsImmediately verifies that Sync does not send a
// "rm -rf" cleanup command when syncWorkspace fails for a reason other than a
// git-apply failure. MUT-013 caught this gap by inverting the errApplyFailed
// check, which caused Sync to retry (and rm -rf the remote workspace) for all
// failure types, not just patch-apply failures.
func TestSync_NonApplyFailureReturnsImmediately(t *testing.T) {
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)

	// SSH server: all commands succeed (exitCode 0), so mkdir-p and test-d pass.
	// syncWorkspace then calls gitutil.MergeBase(), which fails because the test
	// repo has no upstream tracking branch — a non-errApplyFailed error.
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

// TestSync_FetchBeforeReset verifies that Sync issues a git fetch origin on the
// sidecar before git reset --hard. This ensures the sidecar can recover when
// its clone is stale — i.e. the merge-base commit exists on GitHub but is
// absent from the sidecar's local object store (e.g. when booted from an older
// snapshot).
func TestSync_FetchBeforeReset(t *testing.T) {
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)

	// SSH server: all commands succeed, so the full sync path runs through to
	// git apply without triggering the rm-rf retry.
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("", 0)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	// Repo with origin/HEAD set so MergeBase resolves successfully.
	repoDir := setupRepoWithOriginHEAD(t, "my-org", "my-repo")
	// Write an untracked file so GeneratePatch produces a non-empty patch and
	// Sync reaches the reset+apply steps rather than returning early.
	assert.NilError(t, os.WriteFile(filepath.Join(repoDir, "change.txt"), []byte("change"), 0o644))
	t.Chdir(repoDir)

	cl := newClient(t, srv.URL)
	noopStatus := iostream.StatusFunc(func(_ iostream.Level, _ string) {})

	err := sidecar.Sync(context.Background(), cl, "sb-1", keyFile, "", "", noopStatus)
	assert.NilError(t, err)

	// Find the positions of the fetch and reset commands in the recorded sequence.
	cmds := sshSrv.Commands()
	fetchIdx, resetIdx := -1, -1
	for i, cmd := range cmds {
		if strings.Contains(cmd, "fetch origin") {
			fetchIdx = i
		}
		if strings.Contains(cmd, "reset --hard") {
			resetIdx = i
		}
	}

	assert.Assert(t, fetchIdx >= 0, "expected a fetch origin command; got: %v", cmds)
	assert.Assert(t, resetIdx >= 0, "expected a reset --hard command; got: %v", cmds)
	assert.Assert(t, fetchIdx < resetIdx,
		"fetch (index %d) must come before reset (index %d); commands: %v", fetchIdx, resetIdx, cmds)
}
