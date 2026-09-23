package sidecar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
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

// TestWSSHTTPClientRespectsProxyFromEnvironment verifies that the transport
// used to dial the sidecar's wss:// tunnel routes through HTTP_PROXY/
// HTTPS_PROXY like the rest of the codebase, rather than defaulting to no
// proxy the way a bare &http.Transport{} literal otherwise would.
//
// This checks wiring directly (via the Proxy field's function pointer)
// instead of exercising a real dial with the env vars set: http.Transport
// caches the parsed proxy environment for the lifetime of the process the
// first time any transport's Proxy func is invoked, so a live end-to-end
// check here would be liable to observe a stale cache poisoned by whichever
// test happened to dial first.
func TestWSSHTTPClientRespectsProxyFromEnvironment(t *testing.T) {
	client := wssHTTPClient()

	transport, ok := client.Transport.(*http.Transport)
	assert.Assert(t, ok, "expected *http.Transport, got %T", client.Transport)
	assert.Assert(t, transport.Proxy != nil, "expected Proxy to be set so HTTP_PROXY/HTTPS_PROXY are honored")
	assert.Equal(t,
		reflect.ValueOf(transport.Proxy).Pointer(),
		reflect.ValueOf(http.ProxyFromEnvironment).Pointer(),
		"expected Proxy to be http.ProxyFromEnvironment",
	)

	assert.Assert(t, transport.TLSClientConfig != nil)
	assert.Assert(t, transport.TLSClientConfig.InsecureSkipVerify)
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

// Env vars wiring TestHelperEnsureKeyPair up as a child process of
// TestEnsureKeyPairAcrossProcesses.
const (
	keyGenHelperPathEnv = "CHUNK_TEST_KEYGEN_PATH"
	keyGenHelperAtEnv   = "CHUNK_TEST_KEYGEN_AT"
)

// TestHelperEnsureKeyPair is the child half of
// TestEnsureKeyPairAcrossProcesses. It skips during a normal run.
func TestHelperEnsureKeyPair(t *testing.T) {
	keyPath := os.Getenv(keyGenHelperPathEnv)
	if keyPath == "" {
		t.Skip("child process helper for TestEnsureKeyPairAcrossProcesses")
	}

	// Spin to a deadline the parent shares with every sibling, so the processes
	// contend rather than lining up behind each other's startup cost.
	if at, err := strconv.ParseInt(os.Getenv(keyGenHelperAtEnv), 10, 64); err == nil {
		time.Sleep(time.Until(time.Unix(0, at)))
	}

	generated, err := EnsureKeyPair(keyPath)
	assert.NilError(t, err)
	fmt.Printf("GENERATED=%v\n", generated)
}

// TestEnsureKeyPairAcrossProcesses guards the race an in-process mutex cannot
// close: two chunk invocations sharing one HOME on a machine with no key yet.
// Before the lock file they both saw a missing key and both generated, so a
// sidecar that had registered the first public key rejected the private key that
// survived — and with the two writes interleaved, the pair left on disk was
// permanently mismatched, since the existence check only ever tests the private
// half.
func TestEnsureKeyPairAcrossProcesses(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), ".ssh", "chunk_ai")
	startAt := time.Now().Add(500 * time.Millisecond).UnixNano()

	const procs = 6
	cmds := make([]*exec.Cmd, procs)
	outs := make([]*bytes.Buffer, procs)
	for i := range procs {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperEnsureKeyPair$", "-test.v")
		cmd.Env = append(os.Environ(),
			keyGenHelperPathEnv+"="+keyPath,
			keyGenHelperAtEnv+"="+strconv.FormatInt(startAt, 10),
		)
		out := &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = out, out
		cmds[i], outs[i] = cmd, out
	}

	for _, cmd := range cmds {
		assert.NilError(t, cmd.Start())
	}
	generated := 0
	for i, cmd := range cmds {
		assert.NilError(t, cmd.Wait(), outs[i].String())
		if strings.Contains(outs[i].String(), "GENERATED=true") {
			generated++
		}
	}

	assert.Equal(t, generated, 1, "exactly one process should generate the keypair")
	assertKeyPairMatches(t, keyPath)
	assertNoStrayFiles(t, keyPath)
}

// assertKeyPairMatches checks the public key on disk is the one belonging to the
// private key on disk. A mismatch is the silent, permanent failure mode: every
// later run stats the private key, finds it, and never regenerates.
func assertKeyPairMatches(t *testing.T, keyPath string) {
	t.Helper()

	privData, err := os.ReadFile(keyPath)
	assert.NilError(t, err)
	signer, err := ssh.ParsePrivateKey(privData)
	assert.NilError(t, err)

	pubData, err := os.ReadFile(keyPath + ".pub")
	assert.NilError(t, err)

	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	assert.Equal(t, strings.TrimSpace(string(pubData)), want,
		"public key on disk does not belong to the private key on disk")
}

// assertNoStrayFiles checks generation left no temp or lock files behind.
func assertNoStrayFiles(t *testing.T, keyPath string) {
	t.Helper()

	entries, err := os.ReadDir(filepath.Dir(keyPath))
	assert.NilError(t, err)
	base := filepath.Base(keyPath)
	for _, e := range entries {
		assert.Assert(t, e.Name() == base || e.Name() == base+".pub",
			"unexpected leftover file: %s", e.Name())
	}
}

func TestGenerateKeyPairWritesMatchingPair(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), ".ssh", "chunk_ai")
	assert.NilError(t, GenerateKeyPair(keyPath))

	assertKeyPairMatches(t, keyPath)
	assertNoStrayFiles(t, keyPath)

	info, err := os.Stat(keyPath)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600), "private key must stay owner-only")
}

// TestEnsureKeyPairBreaksStaleLock covers a process killed mid-generation: its
// lock file outlives it, and without recovery every later run would wait out the
// timeout and then fail rather than generating.
func TestEnsureKeyPairBreaksStaleLock(t *testing.T) {
	defer func(d time.Duration) { keyGenLockTimeout = d }(keyGenLockTimeout)
	keyGenLockTimeout = 50 * time.Millisecond

	dir := t.TempDir()
	keyPath := filepath.Join(dir, ".ssh", "chunk_ai")
	assert.NilError(t, os.MkdirAll(filepath.Dir(keyPath), 0o700))
	assert.NilError(t, os.WriteFile(keyPath+".lock", nil, 0o600))

	generated, err := EnsureKeyPair(keyPath)
	assert.NilError(t, err)
	assert.Assert(t, generated, "should break the abandoned lock and generate")
	assertKeyPairMatches(t, keyPath)
	assertNoStrayFiles(t, keyPath)
}

// TestEnsureKeyPairAdoptsKeyPublishedByLockHolder covers the waiter's side: it
// must use the holder's key rather than generating a second one.
func TestEnsureKeyPairAdoptsKeyPublishedByLockHolder(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, ".ssh", "chunk_ai")
	lockPath := keyPath + ".lock"
	assert.NilError(t, os.MkdirAll(filepath.Dir(keyPath), 0o700))
	assert.NilError(t, os.WriteFile(lockPath, nil, 0o600))

	// Stand in for the holder finishing while we wait.
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = GenerateKeyPair(keyPath)
		_ = os.Remove(lockPath)
	}()

	generated, err := EnsureKeyPair(keyPath)
	assert.NilError(t, err)
	assert.Assert(t, !generated, "should adopt the key the holder published")
	assertKeyPairMatches(t, keyPath)
}

// TestSSHAuthEncryptedKey checks a passphrase-protected key surfaces as a typed
// error. With --identity-file and the ssh-agent path both gone this key cannot
// authenticate at all, so the cmd layer needs to recognise it to name the fix.
func TestSSHAuthEncryptedKey(t *testing.T) {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen not available")
	}

	keyPath := filepath.Join(t.TempDir(), "chunk_ai")
	out, err := exec.Command(keygen, "-t", "ed25519", "-N", "hunter2", "-f", keyPath, "-q").CombinedOutput()
	assert.NilError(t, err, string(out))

	_, _, err = sshAuth(context.Background(), &Session{IdentityFile: keyPath})
	var encErr *EncryptedKeyError
	assert.Assert(t, errors.As(err, &encErr), "want EncryptedKeyError, got %v", err)
	assert.Equal(t, encErr.Path, keyPath)
}

// TestSessionCloserAfterCleanExit verifies that closing a session whose remote
// end has already exited cleanly is not reported as an error. ssh.Session.Close
// returns io.EOF in that state, which previously surfaced from `chunk sidecar
// ssh` as "An unknown error occurred" after exiting an interactive shell.
func TestSessionCloserAfterCleanExit(t *testing.T) {
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("", 0)

	session := &Session{
		URL:          sshSrv.Addr(),
		IdentityFile: keyFile,
		KnownHosts:   filepath.Join(t.TempDir(), "known_hosts"),
	}

	client, err := dialSSH(context.Background(), session)
	assert.NilError(t, err)
	defer func() { _ = client.Close() }()

	t.Run("raw close returns io.EOF", func(t *testing.T) {
		sess, err := client.NewSession()
		assert.NilError(t, err)
		assert.NilError(t, sess.Run("exit"))
		assert.ErrorIs(t, sess.Close(), io.EOF)
	})

	t.Run("wrapped close returns nil", func(t *testing.T) {
		sess, err := client.NewSession()
		assert.NilError(t, err)
		assert.NilError(t, sess.Run("exit"))
		assert.NilError(t, sessionCloser{sess}.Close())
	})
}

func TestSessionCloserPropagatesOtherErrors(t *testing.T) {
	sentinel := errors.New("boom")
	err := sessionCloser{closeFunc(func() error { return sentinel })}.Close()
	assert.ErrorIs(t, err, sentinel)
}

type closeFunc func() error

func (f closeFunc) Close() error { return f() }
