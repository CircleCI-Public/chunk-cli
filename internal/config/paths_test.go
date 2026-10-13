package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func TestRepoDataDirIsSharedByEveryCheckout(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv(EnvXDGDataHome, xdg)
	repo := gitrepo.SetupGitRepo(t, "org", "repo")
	sub := filepath.Join(repo, "sub")
	assert.NilError(t, os.Mkdir(sub, 0o755))
	wt := filepath.Join(t.TempDir(), "wt")
	cmd := exec.Command("git", "worktree", "add", wt)
	cmd.Dir, cmd.Env = repo, gitrepo.GitEnv(repo)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))

	want, err := RepoDataDir(repo)
	assert.NilError(t, err)
	assert.Assert(t, strings.HasPrefix(want, filepath.Join(xdg, "chunk", "repo-")), want)
	for _, dir := range []string{sub, wt} {
		got, err := RepoDataDir(dir)
		assert.NilError(t, err)
		assert.Equal(t, got, want, dir)
	}

	other, err := RepoDataDir(gitrepo.SetupGitRepo(t, "org", "other"))
	assert.NilError(t, err)
	assert.Assert(t, other != want)
}

// TestRepoDataDirThroughASymlinkedSubdirectory covers git answering
// --git-common-dir relative to the real working directory: joined to the
// symlinked spelling instead, "../../.git" lands outside the repository.
func TestRepoDataDirThroughASymlinkedSubdirectory(t *testing.T) {
	t.Setenv(EnvXDGDataHome, t.TempDir())
	repo := gitrepo.SetupGitRepo(t, "org", "repo")
	sub := filepath.Join(repo, "a", "b")
	assert.NilError(t, os.MkdirAll(sub, 0o755))
	link := filepath.Join(t.TempDir(), "link")
	assert.NilError(t, os.Symlink(sub, link))

	want, err := RepoDataDir(repo)
	assert.NilError(t, err)
	got, err := RepoDataDir(link)
	assert.NilError(t, err)
	assert.Equal(t, got, want)
}

func TestRepoDataDirOutsideARepoIsAnError(t *testing.T) {
	t.Setenv(EnvXDGDataHome, t.TempDir())
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	_, err := RepoDataDir(t.TempDir())
	assert.Assert(t, err != nil)
	_, err = RepoDataDir("")
	assert.Assert(t, err != nil)
}
