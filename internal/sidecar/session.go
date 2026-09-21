package sidecar

import (
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

const (
	defaultKeyName = "chunk_ai"
	defaultSSHUser = "user"
	knownHostsFile = "chunk_ai_known_hosts"
)

// DefaultKeyPath returns the default SSH private key path used by chunk.
func DefaultKeyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", defaultKeyName), nil
}

// GenerateKeyPair generates an ed25519 keypair and writes the private key to
// path and the public key to path+".pub". The .ssh directory is created if it
// does not exist.
func GenerateKeyPair(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create .ssh directory: %w", err)
	}

	pub, priv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("create public key: %w", err)
	}

	// Publish the public half first: callers test for the private key, so this
	// ordering means a visible private key always implies a readable .pub.
	if err := writeFileAtomic(path+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}
	if err := writeFileAtomic(path, privPEM, 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to a temp file in path's directory and renames it
// over path. A reader racing generation then sees either no file or a complete
// one, never the zero-length window os.WriteFile opens between truncate and
// write.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	writeErr := tmp.Chmod(perm)
	if writeErr == nil {
		_, writeErr = tmp.Write(data)
	}
	if closeErr := tmp.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", writeErr)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// keyGenMu serializes the check-then-generate below within this process. Sidecar
// work fans out concurrently (bundle sync per sidecar, validate per variant) and
// every goroutine calls EnsureKeyPair. Without the lock they all see a missing
// key, each writes its own ed25519 material to the same path, and the sidecars
// that registered an overwritten public key reject the private key that
// survived. The lock file in EnsureKeyPair extends the same guarantee across
// processes.
var keyGenMu sync.Mutex

// keyGenLockTimeout bounds how long a process waits for another process that
// already holds the generation lock. Generation is sub-millisecond, so waiting
// longer than this means the holder died and left its lock file behind. A var
// so tests can shorten it.
var keyGenLockTimeout = 5 * time.Second

// EnsureKeyPair generates the keypair at path if it is not already there, and
// reports whether it generated one. Concurrent callers see a single generation:
// goroutines in this process via keyGenMu, and other chunk processes sharing the
// same HOME via an O_EXCL lock file.
func EnsureKeyPair(path string) (generated bool, err error) {
	keyGenMu.Lock()
	defer keyGenMu.Unlock()

	exists, err := keyExists(path)
	if err != nil || exists {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create .ssh directory: %w", err)
	}

	// A bare stat-then-generate races two first-run chunk processes — parallel
	// agent sessions share one HOME — and either strands a private key whose
	// public half was never registered with the sidecar, or interleaves the two
	// writes into a mismatched pair that is never repaired, because the stat
	// above only ever tests the private key.
	release, acquired, err := acquireKeyGenLock(path + ".lock")
	if err != nil {
		return false, err
	}
	if !acquired {
		// Another process published the key while we waited.
		return false, nil
	}
	defer release()

	// Re-check under the lock: the previous holder may have finished between our
	// stat above and the lock being granted.
	if exists, err := keyExists(path); err != nil || exists {
		return false, err
	}
	if err := GenerateKeyPair(path); err != nil {
		return false, err
	}
	return true, nil
}

// keyExists reports whether the private key at path is present.
func keyExists(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat SSH key: %w", err)
	}
	return false, nil
}

// acquireKeyGenLock creates lockPath exclusively, making exactly one process the
// generator. When another process holds it, it waits for that process to publish
// the key and reports acquired=false. A lock held past keyGenLockTimeout is
// treated as abandoned and broken, so a process killed mid-generation cannot
// wedge every later run.
func acquireKeyGenLock(lockPath string) (release func(), acquired bool, err error) {
	keyPath := strings.TrimSuffix(lockPath, ".lock")
	deadline := time.Now().Add(keyGenLockTimeout)

	for {
		lock, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if openErr == nil {
			_ = lock.Close()
			return func() { _ = os.Remove(lockPath) }, true, nil
		}
		if !os.IsExist(openErr) {
			return nil, false, fmt.Errorf("acquire SSH key lock: %w", openErr)
		}

		exists, err := keyExists(keyPath)
		if err != nil {
			return nil, false, err
		}
		if exists {
			return nil, false, nil
		}

		if time.Now().After(deadline) {
			if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
				return nil, false, fmt.Errorf("clear stale SSH key lock: %w", err)
			}
			deadline = time.Now().Add(keyGenLockTimeout)
			continue
		}
		time.Sleep(keyGenPollInterval)
	}
}

// keyGenPollInterval is how often a waiting process re-tests the lock.
const keyGenPollInterval = 10 * time.Millisecond

// Session holds the info needed to SSH into a sidecar.
// It is a plain value type with no open connections or resources to close.
// Each call to ExecOverSSH opens and closes its own SSH connection.
type Session struct {
	URL          string // WebSocket tunnel URL (ws:// or wss://)
	IdentityFile string // path to SSH private key (~/.ssh/chunk_ai)
	KnownHosts   string // path to known_hosts file
}

// IsDefinitelyStale probes sidecarID with a single AddSSHKey attempt under a
// short timeout. It returns true only when the sidecar is gone or too old for
// the current API.
func IsDefinitelyStale(ctx context.Context, client *circleci.Client, sidecarID string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	keyPath, err := DefaultKeyPath()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return false
	}
	pubKey := strings.TrimSpace(string(data))

	_, err = client.AddSSHKey(probeCtx, sidecarID, pubKey)
	if err == nil {
		return false
	}
	return circleci.SidecarGone(err) || circleci.SidecarOutOfDate(err)
}

// addSSHKey registers a public key with the sidecar. When retryOn404 is true
// it retries on 404 to absorb provisioner replica lag after creation.
func addSSHKey(ctx context.Context, client *circleci.Client, sidecarID, pubKey string, retryOn404 bool) (*circleci.AddSSHKeyResponse, error) {
	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := client.AddSSHKey(ctx, sidecarID, pubKey)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var se *circleci.StatusError
		if !errors.As(err, &se) || se.StatusCode != http.StatusNotFound || !retryOn404 || attempt >= maxAttempts-1 {
			return nil, err
		}
		base := time.Duration(attempt+1) * 5 * time.Second
		jitter := time.Duration(rand.N(int64(2 * time.Second))) //nolint:gosec
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(base + jitter):
		}
	}
	return nil, lastErr
}

// OpenSession registers the default SSH key (~/.ssh/chunk_ai) with the sidecar
// and returns session info. The keypair is generated when it does not exist yet,
// so every path that reaches a sidecar gets one without the user creating keys
// by hand. retryOn404 should be true only for freshly created sidecars where a
// 404 can be transient.
func OpenSession(ctx context.Context, client *circleci.Client, sidecarID string, retryOn404 bool) (*Session, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	sshDir := filepath.Join(home, ".ssh")
	identityFile := filepath.Join(sshDir, defaultKeyName)

	if _, err := EnsureKeyPair(identityFile); err != nil {
		return nil, fmt.Errorf("generate SSH key: %w", err)
	}

	pubKeyPath := identityFile + ".pub"
	pubKeyData, err := os.ReadFile(pubKeyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &PublicKeyNotFoundError{KeyPath: pubKeyPath, IdentityFile: identityFile}
		}
		return nil, fmt.Errorf("read public key: %w", err)
	}
	pubKey := strings.TrimSpace(string(pubKeyData))

	resp, err := addSSHKey(ctx, client, sidecarID, pubKey, retryOn404)
	if err != nil {
		return nil, fmt.Errorf("register SSH key: %w", err)
	}

	return &Session{
		URL:          resp.URL,
		IdentityFile: identityFile,
		KnownHosts:   filepath.Join(sshDir, knownHostsFile),
	}, nil
}
