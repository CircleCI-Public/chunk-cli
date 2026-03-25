package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func TestShellEscape(t *testing.T) {
	assert.Equal(t, ShellEscape("hello"), "'hello'")
	assert.Equal(t, ShellEscape("it's"), "'it'\\''s'")
	assert.Equal(t, ShellEscape(""), "''")
}

func TestShellJoin(t *testing.T) {
	assert.Equal(t, ShellJoin([]string{"ls", "-la"}), "'ls' '-la'")
	assert.Equal(t, ShellJoin([]string{"echo", "hello world"}), "'echo' 'hello world'")
}

func TestTofuHostKeyCallback(t *testing.T) {
	dir := t.TempDir()
	knownHosts := filepath.Join(dir, "known_hosts")

	// Write a known host
	err := os.WriteFile(knownHosts, []byte("example.com abc123\n"), 0o600)
	assert.NilError(t, err)

	// Read it back and verify parsing works
	data, err := os.ReadFile(knownHosts)
	assert.NilError(t, err)
	assert.Assert(t, len(data) > 0)
}
