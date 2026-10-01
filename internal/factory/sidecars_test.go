package factory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func newPatchSidecars(t *testing.T) (*Sidecars, string) {
	t.Helper()
	dir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeFile(t, dir, "main.go", "package main\n")
	entry := &sidecar.PoolEntry{ID: "impl", RepoPath: dir}
	s := &Sidecars{
		Implementer: &Implementer{Entry: entry},
		ws:          workspace{exec: localExec(t.TempDir()), entry: entry},
	}
	assert.NilError(t, s.ws.commitBaseline(context.Background()))
	return s, dir
}

// TestWritePatchIncludesUncollectedNewFiles covers saving after a turn that
// failed: its new files were never marked intent-to-add by a collect, and the
// patch must still carry them.
func TestWritePatchIncludesUncollectedNewFiles(t *testing.T) {
	// Diff settings on the sidecar must not leak into the patch.
	gitConfig := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, filepath.Dir(gitConfig), filepath.Base(gitConfig), "[diff]\n\tnoprefix = true\n[color]\n\tdiff = always\n")
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)

	s, dir := newPatchSidecars(t)
	clean := filepath.Join(t.TempDir(), "clean")
	gitOutput(t, dir, "clone", "-q", dir, clean)

	writeFile(t, dir, "main.go", "package main\n\nfunc main() { helper() }\n")
	writeFile(t, dir, "helper.go", "package main\n\nfunc helper() {}\n")

	path := filepath.Join(t.TempDir(), "out", "work.patch")
	c, err := s.WritePatch(context.Background(), path)
	assert.NilError(t, err)
	assert.Equal(t, c.Stat, "2 files changed, 5 insertions(+)")

	gitOutput(t, clean, "apply", path)
	assert.Equal(t, readFile(t, clean, "main.go"), "package main\n\nfunc main() { helper() }\n")
	assert.Equal(t, readFile(t, clean, "helper.go"), "package main\n\nfunc helper() {}\n")
}

func TestWritePatchSkipsEmptyChange(t *testing.T) {
	s, _ := newPatchSidecars(t)

	path := filepath.Join(t.TempDir(), "work.patch")
	c, err := s.WritePatch(context.Background(), path)
	assert.NilError(t, err)
	assert.Assert(t, c.Empty())
	_, err = os.Stat(path)
	assert.Assert(t, os.IsNotExist(err), "an empty change wrote a patch")
}
