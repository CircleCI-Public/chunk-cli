package sidecar

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// A sync leaves out what git ignores even where the local rsync would not:
// macOS's openrsync drops a .gitignore's last line when it has no newline, as
// uv's "*" in every .venv has, which sent a Mac's venv to the sidecar to be
// rebuilt by every run there. The sidecar's own copy is kept.
func TestSyncFlagsLeaveOutWhatGitIgnores(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not on PATH")
	}
	src := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	write := func(dir, name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		assert.NilError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	write(src, "harness/.venv/.gitignore", "*")
	write(src, "harness/.venv/pyvenv.cfg", "home = /Users/me/python\n")
	write(src, "harness/tool.py", "print()\n")
	dst := t.TempDir()
	write(dst, "harness/.venv/pyvenv.cfg", "home = /usr/bin\n")

	excludeFile, err := localExcludeFile(t.Context(), src)
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.Remove(excludeFile) })
	args := append(mirrorFlags(), "--exclude-from="+excludeFile, src+"/", dst+"/")
	out, err := exec.CommandContext(t.Context(), "rsync", args...).CombinedOutput()
	assert.NilError(t, err, string(out))

	got, err := os.ReadFile(filepath.Join(dst, "harness/.venv/pyvenv.cfg"))
	assert.NilError(t, err)
	assert.Equal(t, string(got), "home = /usr/bin\n", "the sidecar's venv is kept, not replaced by the local one")
	_, err = os.Stat(filepath.Join(dst, "harness/tool.py"))
	assert.NilError(t, err, "files git does not ignore are synced")
}
