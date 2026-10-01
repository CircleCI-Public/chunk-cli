package factory

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// newRelay returns a relay whose sidecars are directories on this machine: the
// fake sidecar runs every command locally, so rsync and ssh run for real through
// the same WebSocket tunnel chunk uses, and a sidecar's workspace is a local path.
func newRelay(t *testing.T) *Relay {
	t.Helper()
	for _, bin := range []string{"rsync", "ssh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}

	homeDir := t.TempDir()
	t.Setenv(config.EnvHome, homeDir)
	// chunk generates its own key here, as it does on a developer's machine:
	// the system ssh rsync runs must accept the format chunk writes.
	sshSrv := fakes.NewSSHServerAcceptingAnyKey(t)
	sshSrv.RunLocally()

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	api := httptest.NewServer(cci)
	t.Cleanup(api.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: api.URL})
	assert.NilError(t, err)

	r, err := NewRelay(client, func(iostream.Level, string) {})
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, r.Close()) })
	return r
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	assert.NilError(t, os.WriteFile(path, []byte(content), 0o644))
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	assert.NilError(t, err)
	return string(b)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitrepo.GitEnv(dir)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))
	return strings.TrimRight(string(out), "\n")
}

func TestRelayCarriesWorkspaceToEveryReviewer(t *testing.T) {
	r := newRelay(t)
	ctx := context.Background()

	impl := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeFile(t, impl, ".gitignore", "build/\n")
	writeFile(t, impl, "main.go", "package main\n")
	gitrepo.AddFile(t, impl, ".")
	gitOutput(t, impl, "commit", "-m", "base")
	// What an implementer leaves behind: an edit, a new file, and build output
	// that reviewers have no business seeing.
	writeFile(t, impl, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, impl, "pkg/new.go", "package pkg\n")
	writeFile(t, impl, "build/app", "binary")

	reviewers := []*sidecar.PoolEntry{
		{ID: "rev-1", RepoPath: filepath.Join(t.TempDir(), "my-repo")},
		{ID: "rev-2", RepoPath: filepath.Join(t.TempDir(), "my-repo")},
	}
	// A reviewer reused from an earlier round still holds a file the
	// implementer has since deleted.
	writeFile(t, reviewers[0].RepoPath, "stale.go", "package main\n")

	assert.NilError(t, r.Pull(ctx, "impl", impl))
	assert.NilError(t, r.Push(ctx, reviewers))

	for _, rev := range reviewers {
		assert.Equal(t, readFile(t, rev.RepoPath, "main.go"), "package main\n\nfunc main() {}\n")
		assert.Equal(t, readFile(t, rev.RepoPath, "pkg/new.go"), "package pkg\n")
		_, err := os.Stat(filepath.Join(rev.RepoPath, "build"))
		assert.Assert(t, os.IsNotExist(err), "ignored build output reached %s", rev.ID)
		_, err = os.Stat(filepath.Join(rev.RepoPath, "stale.go"))
		assert.Assert(t, os.IsNotExist(err), "deleted file survived on %s", rev.ID)

		// Reviewers read the change through git, so history must arrive too.
		assert.Equal(t, gitOutput(t, rev.RepoPath, "rev-parse", "HEAD"), gitOutput(t, impl, "rev-parse", "HEAD"))
		assert.Equal(t, gitOutput(t, rev.RepoPath, "status", "--porcelain"), " M main.go\n?? pkg/")
	}
}

func TestRelayPullMirrorsDeletions(t *testing.T) {
	r := newRelay(t)
	ctx := context.Background()

	impl := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeFile(t, impl, "keep.go", "package main\n")
	writeFile(t, impl, "drop.go", "package main\n")
	assert.NilError(t, r.Pull(ctx, "impl", impl))

	// The next round deletes a file; the staging copy must not keep it, or it
	// would be pushed back to every reviewer.
	assert.NilError(t, os.Remove(filepath.Join(impl, "drop.go")))
	assert.NilError(t, r.Pull(ctx, "impl", impl))

	assert.Equal(t, readFile(t, r.Dir(), "keep.go"), "package main\n")
	_, err := os.Stat(filepath.Join(r.Dir(), "drop.go"))
	assert.Assert(t, os.IsNotExist(err))
}

func TestRelayPushReportsEveryFailedReviewer(t *testing.T) {
	r := newRelay(t)
	ctx := context.Background()

	impl := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeFile(t, impl, "main.go", "package main\n")
	assert.NilError(t, r.Pull(ctx, "impl", impl))

	// A workspace under a regular file cannot be created, so both pushes fail.
	blocker := filepath.Join(t.TempDir(), "file")
	writeFile(t, filepath.Dir(blocker), "file", "")
	good := &sidecar.PoolEntry{ID: "rev-ok", RepoPath: filepath.Join(t.TempDir(), "my-repo")}
	err := r.Push(ctx, []*sidecar.PoolEntry{
		{ID: "rev-1", RepoPath: filepath.Join(blocker, "a")},
		good,
		{ID: "rev-2", RepoPath: filepath.Join(blocker, "b")},
	})

	assert.ErrorContains(t, err, "push to rev-1")
	assert.ErrorContains(t, err, "push to rev-2")
	assert.Assert(t, !strings.Contains(err.Error(), "rev-ok"))
	// One reviewer failing must not stop the others being synced.
	assert.Equal(t, readFile(t, good.RepoPath, "main.go"), "package main\n")
}

// TestRelayPullHonoursGitIgnoreRules covers ignore rules rsync's own .gitignore
// merge gets wrong or never reads, which is why the pull asks git instead.
func TestRelayPullHonoursGitIgnoreRules(t *testing.T) {
	r := newRelay(t)
	ctx := context.Background()

	impl := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeFile(t, impl, ".gitignore", "*.log\n!keep.log\n")
	writeFile(t, impl, ".git/info/exclude", "scratch/\n")
	writeFile(t, impl, "debug.log", "noise")
	writeFile(t, impl, "keep.log", "wanted")
	writeFile(t, impl, "scratch/notes.md", "agent notes")
	// Only the root build/ is ignored; a nested one of the same name is source.
	writeFile(t, impl, ".gitignore", "*.log\n!keep.log\n/build/\n")
	writeFile(t, impl, "build/app", "binary")
	writeFile(t, impl, "cmd/build/main.go", "package main\n")
	// A name rsync would otherwise read as a wildcard must match only itself.
	writeFile(t, impl, ".gitignore", "*.log\n!keep.log\n/build/\n/what\\[1\\].txt\n")
	writeFile(t, impl, "what[1].txt", "ignored")
	writeFile(t, impl, "what1.txt", "kept")

	assert.NilError(t, r.Pull(ctx, "impl", impl))

	for _, gone := range []string{"debug.log", "scratch", "build", "what[1].txt"} {
		_, err := os.Stat(filepath.Join(r.Dir(), gone))
		assert.Assert(t, os.IsNotExist(err), "ignored %s was pulled", gone)
	}
	assert.Equal(t, readFile(t, r.Dir(), "keep.log"), "wanted")
	assert.Equal(t, readFile(t, r.Dir(), "cmd/build/main.go"), "package main\n")
	assert.Equal(t, readFile(t, r.Dir(), "what1.txt"), "kept")
}

func TestRelayPullRequiresGitWorkspace(t *testing.T) {
	r := newRelay(t)
	err := r.Pull(context.Background(), "impl", t.TempDir())
	assert.ErrorContains(t, err, "list ignored files")
}
