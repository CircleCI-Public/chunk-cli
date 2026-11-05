package sidecar

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

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
	writeTree(t, src, map[string]string{
		"harness/.venv/.gitignore": "*",
		"harness/.venv/pyvenv.cfg": "home = /Users/me/python\n",
		"harness/tool.py":          "print()\n",
	})
	dst := t.TempDir()
	writeTree(t, dst, map[string]string{"harness/.venv/pyvenv.cfg": "home = /usr/bin\n"})

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

func TestGitIgnoredPathsDescendsIntoNestedRepos(t *testing.T) {
	src := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeTree(t, src, map[string]string{
		".gitignore": "*.log\ndeps/\n",
		"a.log":      "x\n",
		"x.txt":      "x\n",
	})

	// A nested repo the outer repo ignores whole: listed as deps/ alone.
	depsX := filepath.Join(src, "deps", "x")
	err := os.MkdirAll(depsX, 0o755)
	assert.NilError(t, err)
	gitIn(t, depsX, "init", "-q")
	writeTree(t, depsX, map[string]string{".gitignore": "*.o\n", "a.o": "x\n"})

	// A submodule: a committed repo the outer repo records as a gitlink.
	lib := filepath.Join(src, "vendor", "lib")
	err = os.MkdirAll(lib, 0o755)
	assert.NilError(t, err)
	gitIn(t, lib, "init", "-q")
	writeTree(t, lib, map[string]string{
		".gitignore":        "node_modules/\n*.tmp\n!keep.tmp\n",
		"index.js":          "x\n",
		"node_modules/x.js": "x\n",
		"a.tmp":             "x\n",
		"keep.tmp":          "x\n",
	})
	gitIn(t, lib, "add", ".gitignore", "index.js")
	gitIn(t, lib, "commit", "-q", "-m", "init")
	gitIn(t, src, "add", "vendor/lib") // records mode 160000

	// A second level: an untracked repo inside the submodule.
	inner := filepath.Join(lib, "inner")
	err = os.MkdirAll(inner, 0o755)
	assert.NilError(t, err)
	gitIn(t, inner, "init", "-q")
	writeTree(t, inner, map[string]string{".gitignore": "dist/\n", "dist/x": "x\n"})

	// An uninitialized submodule: a gitlink whose directory is empty.
	head := gitIn(t, src, "rev-parse", "HEAD")
	err = os.MkdirAll(filepath.Join(src, "vendor", "empty"), 0o755)
	assert.NilError(t, err)
	gitIn(t, src, "update-index", "--add", "--cacheinfo", "160000,"+head+",vendor/empty")

	// An untracked nested repo, inside an untracked directory.
	tool := filepath.Join(src, "tools", "gen")
	err = os.MkdirAll(tool, 0o755)
	assert.NilError(t, err)
	gitIn(t, tool, "init", "-q")
	writeTree(t, tool, map[string]string{
		".gitignore": "out/\n",
		"main.go":    "x\n",
		"out/bin":    "x\n",
	})

	got, err := gitIgnoredPaths(t.Context(), src)
	assert.NilError(t, err)
	sort.Strings(got)
	assert.Check(t, cmp.DeepEqual(got, []string{
		"a.log",
		"deps/",
		"tools/gen/out/",
		"vendor/lib/a.tmp",
		"vendor/lib/inner/dist/",
		"vendor/lib/node_modules/",
	}))
}

// A nested repo git cannot read stops the list, rather than letting its
// ignored files through.
func TestGitIgnoredPathsFailsOnBrokenNestedRepo(t *testing.T) {
	src := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	head := gitIn(t, src, "rev-parse", "HEAD")
	writeTree(t, src, map[string]string{"vendor/bad/.git": "gitdir: /nonexistent\n"})
	gitIn(t, src, "update-index", "--add", "--cacheinfo", "160000,"+head+",vendor/bad")

	_, err := gitIgnoredPaths(t.Context(), src)
	assert.Check(t, cmp.ErrorContains(err, "vendor/bad"))
}

// gitIn runs git in dir with the test's isolated environment.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitrepo.GitEnv(dir)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		assert.NilError(t, err)
		err = os.WriteFile(path, []byte(content), 0o644)
		assert.NilError(t, err)
	}
}
