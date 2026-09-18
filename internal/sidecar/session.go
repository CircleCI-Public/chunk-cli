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
	if err := os.WriteFile(path, privPEM, 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("create public key: %w", err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}
	return nil
}

// keyGenMu serializes the check-then-generate below. Sidecar work fans out
// concurrently (bundle sync per sidecar, validate per variant) and every
// goroutine calls EnsureKeyPair. Without the lock they all see a missing key,
// each writes its own ed25519 material to the same path, and the sidecars that
// registered an overwritten public key reject the private key that survived.
var keyGenMu sync.Mutex

// EnsureKeyPair generates the keypair at path if it is not already there, and
// reports whether it generated one. Concurrent callers see a single generation.
func EnsureKeyPair(path string) (generated bool, err error) {
	keyGenMu.Lock()
	defer keyGenMu.Unlock()

	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat SSH key: %w", err)
	}
	if err := GenerateKeyPair(path); err != nil {
		return false, err
	}
	return true, nil
}

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
