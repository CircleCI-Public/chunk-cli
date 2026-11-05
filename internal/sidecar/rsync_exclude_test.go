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

// TestSyncFlags runs the local rsync with the flags a sync uses: in a git
// repo, what git ignores is left out, negations and tracked files included;
// outside one, rsync reads .gitignore itself.
func TestSyncFlags(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not on PATH")
	}
	tests := []struct {
		name    string
		notGit  bool
		files   map[string]string // in the source
		tracked []string          // added to git with -f
		onDest  map[string]string // already on the destination
		sent    []string          // must arrive, with the source's content
		notSent []string          // must not arrive
		kept    map[string]string // destination files left as they were
	}{
		{
			name:    "a negated file is sent",
			files:   map[string]string{".gitignore": "*.log\n!keep.log\n", "a.log": "x\n", "keep.log": "x\n"},
			sent:    []string{"keep.log"},
			notSent: []string{"a.log"},
		},
		{
			name: "a subdirectory's negation re-includes a parent's pattern",
			files: map[string]string{
				".gitignore": "*.log\n", "sub/.gitignore": "!b.log\n",
				"sub/b.log": "x\n", "sub/c.log": "x\n",
			},
			sent:    []string{"sub/b.log"},
			notSent: []string{"sub/c.log"},
		},
		{
			// web-ui-consolidated#9382
			name: "a negated directory is re-included",
			files: map[string]string{
				".gitignore": "test-results/\n!src/libs/api-hooks/api/v3/test-results/\n",
				"src/libs/api-hooks/api/v3/test-results/hooks.ts": "x\n",
				"src/libs/api-hooks/api/v3/test-results/new.ts":   "x\n",
				"test/e2e/test-results/out.json":                  "x\n",
			},
			tracked: []string{"src/libs/api-hooks/api/v3/test-results/hooks.ts"},
			sent: []string{
				"src/libs/api-hooks/api/v3/test-results/hooks.ts",
				"src/libs/api-hooks/api/v3/test-results/new.ts",
			},
			notSent: []string{"test/e2e/test-results/out.json"},
		},
		{
			name:    "a negation under an ignored directory does not re-include",
			files:   map[string]string{".gitignore": "build/\n!build/out.txt\n", "build/out.txt": "x\n"},
			notSent: []string{"build/out.txt"},
		},
		{
			name:    "a tracked file a .gitignore matches is sent",
			files:   map[string]string{".gitignore": "*.log\n", "forced.log": "x\n"},
			tracked: []string{"forced.log"},
			sent:    []string{"forced.log"},
		},
		{
			// macOS's openrsync drops a .gitignore's last line with no
			// newline, as uv's "*" in every .venv has.
			name: "ignored files on the sidecar are kept",
			files: map[string]string{
				"harness/.venv/.gitignore": "*",
				"harness/.venv/pyvenv.cfg": "home = /Users/me/python\n",
				"harness/tool.py":          "print()\n",
			},
			onDest: map[string]string{"harness/.venv/pyvenv.cfg": "home = /usr/bin\n"},
			sent:   []string{"harness/tool.py"},
			kept:   map[string]string{"harness/.venv/pyvenv.cfg": "home = /usr/bin\n"},
		},
		{
			name:    "outside a git repo rsync reads .gitignore, without negations",
			notGit:  true,
			files:   map[string]string{".gitignore": "*.log\n!keep.log\n", "a.log": "x\n", "keep.log": "x\n", "x.txt": "x\n"},
			sent:    []string{"x.txt"},
			notSent: []string{"a.log", "keep.log"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := t.TempDir()
			if !tt.notGit {
				src = gitrepo.SetupGitRepo(t, "my-org", "my-repo")
			}
			writeTree(t, src, tt.files)
			for _, f := range tt.tracked {
				gitIn(t, src, "add", "-f", f)
			}
			dst := t.TempDir()
			writeTree(t, dst, tt.onDest)

			gitRoot := src
			if tt.notGit {
				gitRoot = ""
			}
			flags, cleanup, err := syncFilterFlags(t.Context(), src, gitRoot)
			assert.NilError(t, err)
			t.Cleanup(cleanup)
			args := append(append([]string{}, flags...), src+"/", dst+"/")
			out, err := exec.CommandContext(t.Context(), "rsync", args...).CombinedOutput()
			assert.NilError(t, err, string(out))

			for _, f := range tt.sent {
				_, err := os.Stat(filepath.Join(dst, f))
				assert.Check(t, err, "%s is sent", f)
			}
			for _, f := range tt.notSent {
				_, err := os.Stat(filepath.Join(dst, f))
				assert.Check(t, os.IsNotExist(err), "%s is not sent", f)
			}
			for f, want := range tt.kept {
				got, err := os.ReadFile(filepath.Join(dst, f))
				assert.Check(t, err)
				assert.Check(t, cmp.Equal(string(got), want), "%s is kept", f)
			}
		})
	}
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
