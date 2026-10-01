package commandutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitrepo.GitEnv(dir)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	assert.NilError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	assert.NilError(t, os.WriteFile(full, []byte(content), 0o644))
}

func TestExpandCommandListsChangedPackagesButNotDeletedOnes(t *testing.T) {
	dir := gitrepo.SetupGitRepo(t, "acme", "widgets")
	write(t, dir, "kept/a.go", "package kept\n")
	write(t, dir, "gone/b.go", "package gone\n")
	write(t, dir, "docs/readme.md", "docs\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "files")

	// A changed package, a deleted one, and a change that is not Go.
	write(t, dir, "kept/a.go", "package kept // changed\n")
	assert.NilError(t, os.RemoveAll(filepath.Join(dir, "gone")))
	write(t, dir, "docs/readme.md", "changed\n")

	assert.Equal(t, ExpandCommand(dir, "go test {{CHANGED_PACKAGES}}"), "go test ./kept")
}

func TestExpandCommandFallsBackToEverythingWhenOnlyDeletedPackagesChanged(t *testing.T) {
	dir := gitrepo.SetupGitRepo(t, "acme", "widgets")
	write(t, dir, "gone/b.go", "package gone\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "files")
	assert.NilError(t, os.RemoveAll(filepath.Join(dir, "gone")))

	assert.Equal(t, ExpandCommand(dir, "go test {{CHANGED_PACKAGES}}"), "go test ./...")
}
