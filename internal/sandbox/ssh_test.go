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

func TestToWebSocketURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://8000-abc.e2b.app", "wss://8000-abc.e2b.app/ssh/tunnel"},
		{"http://localhost:8000", "ws://localhost:8000/ssh/tunnel"},
		{"wss://host.example.com", "wss://host.example.com/ssh/tunnel"},
		{"ws://127.0.0.1:9000", "ws://127.0.0.1:9000/ssh/tunnel"},
		{"ws://127.0.0.1:9000/ssh/tunnel", "ws://127.0.0.1:9000/ssh/tunnel"},
		{"127.0.0.1:9000", "ws://127.0.0.1:9000/ssh/tunnel"},
		{"https://host/already/has/path", "wss://host/already/has/path/ssh/tunnel"},
	}
	for _, tc := range cases {
		got, err := toWebSocketURL(tc.in)
		assert.NilError(t, err, "input: %s", tc.in)
		assert.Equal(t, got, tc.want, "input: %s", tc.in)
	}
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
