package factory

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
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

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	assert.NilError(t, err)
	return string(b)
}
