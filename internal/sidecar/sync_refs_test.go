package sidecar

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// syncFixture is a developer checkout cloned from a bare origin, plus a
// "sidecar" repo standing in for a snapshot image: a clone taken before the
// developer's work, still on main.
type syncFixture struct {
	origin, local, sidecar string
}

func newSyncFixture(t *testing.T) syncFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	f := syncFixture{
		origin:  filepath.Join(root, "origin.git"),
		local:   filepath.Join(root, "local"),
		sidecar: filepath.Join(root, "sidecar"),
	}
	seed := filepath.Join(root, "seed")
	runGit(t, root, "init", "-q", "-b", "main", seed)
	commitFile(t, seed, "a.txt", "a")
	runGit(t, root, "clone", "-q", "--bare", seed, f.origin)
	// The snapshot is cloned now, so its main and origin/main go stale once
	// origin moves on.
	runGit(t, root, "clone", "-q", f.origin, f.sidecar)
	commitFile(t, seed, "b.txt", "b")
	runGit(t, seed, "push", "-q", f.origin, "main")
	runGit(t, root, "clone", "-q", f.origin, f.local)
	return f
}

// sync runs the commands a bundle sync runs on a sidecar, locally against
// f.sidecar in place of SSH.
func (f syncFixture) sync(t *testing.T) *preparedBundleSync {
	t.Helper()
	prepared, err := prepareBundleSync(t.Context(), f.sidecar, f.local, "", 1, func(iostream.Level, string) {})
	assert.NilError(t, err)

	bundle := filepath.Join(t.TempDir(), "sync.bundle")
	assert.NilError(t, os.WriteFile(bundle, prepared.bundle, 0o644))
	runGit(t, f.sidecar, "fetch", "-q", bundle, "HEAD")
	runSh(t, checkoutScript(f.sidecar, prepared.resetRef, prepared.refs.branch))
	runGit(t, f.sidecar, "clean", "-fdq")
	if prepared.patch != "" {
		cmd := exec.Command("git", "-C", f.sidecar, "apply", "--whitespace=nowarn")
		cmd.Stdin = strings.NewReader(prepared.patch)
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, "git apply: %s", out)
	}
	if cmd := baseRefsScript(f.sidecar, prepared.refs); cmd != "" {
		runSh(t, cmd)
	}
	return prepared
}

func TestBundleSyncMirrorsBranchAndBase(t *testing.T) {
	f := newSyncFixture(t)
	runGit(t, f.local, "checkout", "-q", "-b", "feature")
	commitFile(t, f.local, "c.txt", "c")
	assert.NilError(t, os.WriteFile(filepath.Join(f.local, "untracked.txt"), []byte("u"), 0o644))

	f.sync(t)

	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "--abbrev-ref", "HEAD"), "feature")
	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "HEAD"), gitOut(t, f.local, "rev-parse", "HEAD"))
	base := gitOut(t, f.local, "rev-parse", "origin/main")
	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "origin/main"), base)
	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "origin/HEAD"), base)
	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "main"), base)
	assert.Equal(t, gitOut(t, f.sidecar, "log", "--format=%s", "origin/main..HEAD"), "add c.txt")
	assert.Equal(t, gitOut(t, f.sidecar, "status", "--porcelain"), "?? untracked.txt")
}

func TestBundleSyncOnDefaultBranchKeepsItAtHead(t *testing.T) {
	f := newSyncFixture(t)
	commitFile(t, f.local, "c.txt", "c")

	f.sync(t)

	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "--abbrev-ref", "HEAD"), "main")
	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "main"), gitOut(t, f.local, "rev-parse", "HEAD"))
	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "origin/main"), gitOut(t, f.local, "rev-parse", "origin/main"))
}

func TestBundleSyncDetachedHeadDetachesSidecar(t *testing.T) {
	f := newSyncFixture(t)
	commitFile(t, f.local, "c.txt", "c")
	runGit(t, f.local, "checkout", "-q", "--detach")

	prepared := f.sync(t)

	assert.Equal(t, prepared.refs.branch, "")
	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "--abbrev-ref", "HEAD"), "HEAD")
	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "HEAD"), gitOut(t, f.local, "rev-parse", "HEAD"))
}

// A reused sidecar can hold a branch from an earlier sync whose name clashes
// with the current one; the sync must still land on the right commit.
func TestBundleSyncBranchNameClashFallsBackToDetached(t *testing.T) {
	f := newSyncFixture(t)
	runGit(t, f.sidecar, "branch", "foo")
	runGit(t, f.local, "checkout", "-q", "-b", "foo/bar")
	commitFile(t, f.local, "c.txt", "c")

	f.sync(t)

	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "--abbrev-ref", "HEAD"), "HEAD")
	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "HEAD"), gitOut(t, f.local, "rev-parse", "HEAD"))
}

func TestBundleSyncWithoutRemoteHeadSkipsBase(t *testing.T) {
	f := newSyncFixture(t)
	runGit(t, f.local, "remote", "set-head", "origin", "--delete")
	runGit(t, f.local, "checkout", "-q", "-b", "feature")

	prepared := f.sync(t)

	assert.Equal(t, prepared.refs.baseSHA, "")
	assert.Equal(t, baseRefsScript(f.sidecar, prepared.refs), "")
	assert.Equal(t, gitOut(t, f.sidecar, "rev-parse", "--abbrev-ref", "HEAD"), "feature")
}

func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", "add "+name)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitOut(t, dir, args...)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitrepo.GitEnv(dir), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// runSh runs a script the way execScript does on a sidecar: piped to sh, since
// the sidecar's SSH server does not run commands through a shell.
func runSh(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("sh")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, "%s: %s", script, out)
}
