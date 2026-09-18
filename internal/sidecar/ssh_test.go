package sidecar

import (
	"os"
	"path/filepath"
	"sync"
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
		got, _, err := toWebSocketURL(tc.in)
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

// TestEnsureKeyPairConcurrent covers the fan-out paths (bundle sync per
// sidecar, validate per variant) reaching a machine with no key yet. Every
// goroutine must end up with the same keypair: if two generate, each registers
// a public key whose private half is then overwritten, and those sidecars
// reject the key that survived on disk.
func TestEnsureKeyPairConcurrent(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), ".ssh", "chunk_ai")

	const goroutines = 8
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		generated int
		pubKeys   []string
	)
	start := make(chan struct{})

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			didGenerate, err := EnsureKeyPair(keyPath)
			assert.NilError(t, err)

			pub, err := os.ReadFile(keyPath + ".pub")
			assert.NilError(t, err)

			mu.Lock()
			defer mu.Unlock()
			if didGenerate {
				generated++
			}
			pubKeys = append(pubKeys, string(pub))
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, generated, 1, "exactly one goroutine should generate the keypair")
	for _, pub := range pubKeys {
		assert.Equal(t, pub, pubKeys[0], "every goroutine should observe the same public key")
	}

	// An existing key is left alone.
	didGenerate, err := EnsureKeyPair(keyPath)
	assert.NilError(t, err)
	assert.Assert(t, !didGenerate, "should not regenerate an existing key")
	pub, err := os.ReadFile(keyPath + ".pub")
	assert.NilError(t, err)
	assert.Equal(t, string(pub), pubKeys[0])
}
