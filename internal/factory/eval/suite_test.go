package eval

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitrepo.GitEnv(dir)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	assert.NilError(t, os.WriteFile(path, []byte(content), 0o644))
}

// newRepo returns a repository with one commit of main.go, and the commit.
func newRepo(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-q")
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-q", "-m", "base")
	return root, git(t, root, "rev-parse", "HEAD")
}

// diffOf returns the patch that edit makes to root's HEAD, leaving root as it
// was.
func diffOf(t *testing.T, root string, edit func()) string {
	t.Helper()
	edit()
	git(t, root, "add", "-A", "-N")
	diff := git(t, root, "diff", "HEAD") + "\n"
	git(t, root, "reset", "-q", "--hard")
	git(t, root, "clean", "-q", "-fd")
	return diff
}

// writeSuite writes cases and their patches, by case name, to a new suite
// directory.
func writeSuite(t *testing.T, cases []Case, patches map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, patch := range patches {
		writeFile(t, dir, filepath.Join("patches", name+".patch"), patch)
	}
	data, err := json.Marshal(map[string]any{"cases": cases})
	assert.NilError(t, err)
	writeFile(t, dir, SuiteFile, string(data))
	return dir
}

func TestLoadSuite(t *testing.T) {
	dir := writeSuite(t, []Case{
		{Name: "flag", Revision: "abc", Request: "add a flag", Patch: "patches/flag.patch",
			Expect: []Expected{{File: "main.go", Lines: []int{3, 4}, About: "bug"}}},
		{Name: "clean", Revision: "abc", Request: "tidy", Patch: "patches/clean.patch"},
	}, map[string]string{"flag": "diff --git a/x b/x\n", "clean": "diff --git a/y b/y\n"})

	s, err := LoadSuite(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(s.Cases), 2)
	assert.Assert(t, !s.Cases[0].Clean())
	assert.Assert(t, s.Cases[1].Clean())
	assert.Equal(t, string(s.Cases[0].patch), "diff --git a/x b/x\n")
}

func TestLoadSuiteRejectsBadCases(t *testing.T) {
	ok := Case{Name: "ok", Revision: "abc", Request: "r", Patch: "patches/ok.patch"}
	patches := map[string]string{"ok": "diff\n", "empty": "  \n"}
	for _, tc := range []struct {
		name string
		edit func(*Case)
		want string
	}{
		{"bad name", func(c *Case) { c.Name = "Has Spaces" }, "name must be lowercase"},
		{"no revision", func(c *Case) { c.Revision = "" }, "no revision"},
		{"no request", func(c *Case) { c.Request = " " }, "no request"},
		{"absolute patch", func(c *Case) { c.Patch = "/etc/passwd" }, "inside the suite directory"},
		{"missing patch", func(c *Case) { c.Patch = "patches/nope.patch" }, "read patch"},
		{"empty patch", func(c *Case) { c.Patch = "patches/empty.patch" }, "patch is empty"},
		{"no file", func(c *Case) { c.Expect = []Expected{{About: "x"}} }, "names no file"},
		{"backwards lines", func(c *Case) { c.Expect = []Expected{{File: "a.go", Lines: []int{5, 2}}} }, "must be [first, last]"},
		{"one line", func(c *Case) { c.Expect = []Expected{{File: "a.go", Lines: []int{5}}} }, "must be [first, last]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ok
			tc.edit(&c)
			_, err := LoadSuite(writeSuite(t, []Case{c}, patches))
			assert.ErrorContains(t, err, tc.want)
		})
	}

	t.Run("repeated name", func(t *testing.T) {
		_, err := LoadSuite(writeSuite(t, []Case{ok, ok}, patches))
		assert.ErrorContains(t, err, "name is used twice")
	})
	t.Run("no cases", func(t *testing.T) {
		_, err := LoadSuite(writeSuite(t, nil, nil))
		assert.ErrorContains(t, err, "has no cases")
	})
}

func TestCheckFindsCasesThatCannotRun(t *testing.T) {
	ctx := context.Background()
	root, base := newRepo(t)
	good := diffOf(t, root, func() { writeFile(t, root, "main.go", "package main\n\nfunc main() { run() }\n") })
	writeFile(t, root, "main.go", "package main\n\n// moved on\n")
	git(t, root, "commit", "-q", "-am", "later")
	stale := diffOf(t, root, func() { writeFile(t, root, "main.go", "package main\n\n// moved on again\n") })

	dir := writeSuite(t, []Case{
		{Name: "good", Revision: base, Request: "r", Patch: "patches/good.patch"},
		{Name: "stale", Revision: base, Request: "r", Patch: "patches/stale.patch"},
		{Name: "gone", Revision: strings.Repeat("0", 40), Request: "r", Patch: "patches/good.patch"},
	}, map[string]string{"good": good, "stale": stale})
	s, err := LoadSuite(dir)
	assert.NilError(t, err)

	err = s.Check(ctx, root)
	assert.ErrorContains(t, err, "case stale: patch patches/stale.patch does not apply")
	assert.ErrorContains(t, err, "case gone: revision 0000000000000000000000000000000000000000 is not in this repository")
	assert.Assert(t, !strings.Contains(err.Error(), "case good"), err.Error())
	// The check leaves the repository as it was.
	assert.Equal(t, git(t, root, "status", "--porcelain"), "")
}

// The suite checked in for chunk-cli must stay loadable, and runnable here
// when the clone has its history.
func TestChunkCLISuite(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "harness", "suites", "chunk-cli")
	s, err := LoadSuite(dir)
	assert.NilError(t, err)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	assert.NilError(t, err)
	for _, c := range s.Cases {
		if exec.Command("git", "-C", root, "cat-file", "-e", c.Revision+"^{commit}").Run() != nil {
			t.Skipf("revision %s is not in this clone", c.Revision)
		}
	}
	assert.NilError(t, s.Check(context.Background(), root))
}
