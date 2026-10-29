package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// fakeClaude puts a claude on PATH that runs script, so a session runs
// without Claude Code or an API key. The script sees its input on stdin and
// its arguments in $@.
func fakeClaude(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"+script), 0o755)
	assert.NilError(t, err)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
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
