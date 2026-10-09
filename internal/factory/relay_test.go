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

// newRelay returns a relay through dir whose sidecars are directories on this
// machine: the fake sidecar runs every command locally, so rsync and ssh run
// for real through the same WebSocket tunnel chunk uses, and a sidecar's
// workspace is a local path.
func newRelay(t *testing.T, dir string) *Relay {
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

	return NewRelay(client, dir, func(iostream.Level, string) {})
}

// localRepo returns an empty git repo to pull into, standing in for the run's
// worktree.
func localRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitOutput(t, dir, "init", "-q")
	return dir
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

// TestRelayCarriesWorkspaceToEveryReviewer runs a round's relay the way a run
// does: the implementer's files are pulled into the run's worktree, pushed from
// there to every reviewer, and the reviewers' new files marked, so each
// reviewer's `git diff HEAD` is the implementer's work.
func TestRelayCarriesWorkspaceToEveryReviewer(t *testing.T) {
	ctx := context.Background()
	root := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeFile(t, root, ".gitignore", "build/\n")
	writeFile(t, root, "main.go", "package main\n")
	gitrepo.AddFile(t, root, ".")
	gitOutput(t, root, "commit", "-m", "base")
	wt, err := CreateWorktree(ctx, root, filepath.Join(t.TempDir(), "wt"), "run-1", "add a flag")
	assert.NilError(t, err)
	r := newRelay(t, wt.Path)

	// Every pool member starts from the worktree's baseline, as the pool's
	// sync leaves them.
	member := func() string {
		dir := filepath.Join(t.TempDir(), "my-repo")
		gitOutput(t, root, "clone", "-q", "--branch", wt.Branch, root, dir)
		return dir
	}
	impl := member()
	reviewers := []*sidecar.PoolEntry{{ID: "rev-1", RepoPath: member()}, {ID: "rev-2", RepoPath: member()}}
	// A reviewer from an earlier round still holds a file the implementer has
	// since deleted.
	writeFile(t, reviewers[0].RepoPath, "stale.go", "package main\n")
	// What an implementer leaves behind: an edit, a new file, build output
	// that has no business leaving its sidecar, and git config it set.
	writeFile(t, impl, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, impl, "pkg/new.go", "package pkg\n")
	writeFile(t, impl, "build/app", "binary")
	gitOutput(t, impl, "config", "core.hooksPath", "/tmp/evil")
	// A nested repo's git config would run here too, so its .git stays put.
	gitOutput(t, impl, "init", "-q", "pkg")

	assert.NilError(t, r.Pull(ctx, "impl", impl))

	// The work lands in the worktree, which keeps its own git.
	assert.Equal(t, readFile(t, wt.Path, "main.go"), "package main\n\nfunc main() {}\n")
	info, err := os.Lstat(filepath.Join(wt.Path, ".git"))
	assert.NilError(t, err)
	assert.Assert(t, !info.IsDir(), "the implementer's .git replaced the worktree's")
	assert.Equal(t, gitOutput(t, wt.Path, "status", "--porcelain"), " M main.go\n?? pkg/")
	cmd := exec.Command("git", "config", "core.hooksPath")
	cmd.Dir = wt.Path
	assert.Assert(t, cmd.Run() != nil, "the implementer's git config reached this machine")
	_, err = os.Lstat(filepath.Join(wt.Path, "pkg", ".git"))
	assert.Assert(t, os.IsNotExist(err), "a nested repo's .git reached this machine")

	assert.NilError(t, r.Push(ctx, reviewers))
	s := &Sidecars{Exec: localExec(t.TempDir()), Reviewers: reviewers}
	assert.NilError(t, s.onReviewers(s.script(ctx, "git reset -q && git add -A -N")))

	for _, rev := range reviewers {
		assert.Equal(t, gitOutput(t, rev.RepoPath, "diff", "HEAD", "--name-only"), "main.go\npkg/new.go", rev.ID)
		assert.Equal(t, gitOutput(t, rev.RepoPath, "rev-parse", "HEAD"), wt.Baseline, "a reviewer's own history was replaced")
		_, err := os.Stat(filepath.Join(rev.RepoPath, "build"))
		assert.Assert(t, os.IsNotExist(err), "ignored build output reached %s", rev.ID)
		_, err = os.Stat(filepath.Join(rev.RepoPath, "stale.go"))
		assert.Assert(t, os.IsNotExist(err), "deleted file survived on %s", rev.ID)
	}
}

func TestRelayPullMirrorsDeletions(t *testing.T) {
	r := newRelay(t, localRepo(t))
	ctx := context.Background()

	impl := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeFile(t, impl, "keep.go", "package main\n")
	writeFile(t, impl, "drop.go", "package main\n")
	assert.NilError(t, r.Pull(ctx, "impl", impl))

	// The next round deletes a file; the worktree must not keep it, or it
	// would be pushed back to every reviewer.
	assert.NilError(t, os.Remove(filepath.Join(impl, "drop.go")))
	assert.NilError(t, r.Pull(ctx, "impl", impl))

	assert.Equal(t, readFile(t, r.dir, "keep.go"), "package main\n")
	_, err := os.Stat(filepath.Join(r.dir, "drop.go"))
	assert.Assert(t, os.IsNotExist(err))
}

// TestRelayPullKeepsTrackedIgnoredFiles covers a file tracked despite matching
// .gitignore: a sync never sends it, so its absence on the sidecar must not
// delete it from the worktree.
func TestRelayPullKeepsTrackedIgnoredFiles(t *testing.T) {
	dir := localRepo(t)
	writeFile(t, dir, ".gitignore", "*.env\n")
	writeFile(t, dir, "dev.env", "local")
	gitOutput(t, dir, "add", "-f", ".gitignore", "dev.env")
	r := newRelay(t, dir)

	impl := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeFile(t, impl, ".gitignore", "*.env\n")
	writeFile(t, impl, "main.go", "package main\n")
	assert.NilError(t, r.Pull(context.Background(), "impl", impl))

	assert.Equal(t, readFile(t, dir, "dev.env"), "local")
	assert.Equal(t, readFile(t, dir, "main.go"), "package main\n")
}

func TestRelayPushReportsEveryFailedReviewer(t *testing.T) {
	r := newRelay(t, localRepo(t))
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
	r := newRelay(t, localRepo(t))
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
		_, err := os.Stat(filepath.Join(r.dir, gone))
		assert.Assert(t, os.IsNotExist(err), "ignored %s was pulled", gone)
	}
	assert.Equal(t, readFile(t, r.dir, "keep.log"), "wanted")
	assert.Equal(t, readFile(t, r.dir, "cmd/build/main.go"), "package main\n")
	assert.Equal(t, readFile(t, r.dir, "what1.txt"), "kept")
}

func TestRelayPullRequiresGitWorkspace(t *testing.T) {
	r := newRelay(t, t.TempDir())
	err := r.Pull(context.Background(), "impl", t.TempDir())
	assert.ErrorContains(t, err, "list ignored files")
}
