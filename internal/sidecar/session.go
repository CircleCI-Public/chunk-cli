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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/closer"
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

// Session holds the info needed to SSH into a sidecar.
// It is a plain value type with no open connections or resources to close.
// Each call to ExecOverSSH opens and closes its own SSH connection.
type Session struct {
	URL          string // WebSocket tunnel URL (ws:// or wss://)
	IdentityFile string // path to SSH private key (empty when using agent)
	KnownHosts   string // path to known_hosts file
	UseAgent     bool   // true when authenticating via ssh-agent
	AuthSock     string // SSH_AUTH_SOCK path (only used when UseAgent is true)
}

// readProbeKey resolves the SSH public key to use for a staleness probe.
func readProbeKey(authSock, identityFile string) (string, error) {
	if identityFile == "" && authSock != "" {
		if pubKey, err := agentPublicKey(context.Background(), authSock); err == nil {
			return pubKey, nil
		}
	}
	if identityFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		identityFile = filepath.Join(home, ".ssh", defaultKeyName)
	}
	data, err := os.ReadFile(identityFile + ".pub")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// IsDefinitelyStale probes sidecarID with a single AddSSHKey attempt under a
// short timeout. It returns true only when the provisioner responds 404.
func IsDefinitelyStale(ctx context.Context, client *circleci.Client, sidecarID, identityFile, authSock string) bool {
	pubKey, err := readProbeKey(authSock, identityFile)
	if err != nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err = client.AddSSHKey(probeCtx, sidecarID, pubKey)
	if err == nil {
		return false
	}
	var se *circleci.StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
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

// OpenSession registers an SSH key with the sidecar and returns session info.
// authSock is the SSH_AUTH_SOCK path; when non-empty and no identityFile is
// given, the agent is tried first. retryOn404 should be true only for freshly
// created sidecars where a 404 can be transient.
func OpenSession(ctx context.Context, client *circleci.Client, sidecarID, identityFile, authSock string, retryOn404 bool) (*Session, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	sshDir := filepath.Join(home, ".ssh")

	if identityFile == "" && authSock != "" {
		pubKey, err := agentPublicKey(ctx, authSock)
		if err == nil {
			resp, err := addSSHKey(ctx, client, sidecarID, pubKey, retryOn404)
			if err != nil {
				return nil, fmt.Errorf("register SSH key: %w", err)
			}
			return &Session{
				URL:        resp.URL,
				UseAgent:   true,
				AuthSock:   authSock,
				KnownHosts: filepath.Join(sshDir, knownHostsFile),
			}, nil
		}
	}

	if identityFile == "" {
		identityFile = filepath.Join(sshDir, defaultKeyName)
	}

	if _, err := os.Stat(identityFile); err != nil {
		return nil, &KeyNotFoundError{Path: identityFile}
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

// agentPublicKey returns the first public key from the running ssh-agent
// in authorized_keys format, or an error if the agent is unavailable.
func agentPublicKey(ctx context.Context, authSock string) (_ string, err error) {
	ag, conn, err := dialAgent(ctx, authSock)
	if err != nil {
		return "", err
	}
	defer closer.ErrorHandler(conn, &err)

	keys, err := ag.List()
	if err != nil {
		return "", fmt.Errorf("list agent keys: %w", err)
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("ssh-agent has no keys")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(keys[0]))), nil
}

// dialAgent connects to the ssh-agent at the given socket path and returns
// the agent client and the underlying connection. The caller must close conn.
func dialAgent(ctx context.Context, authSock string) (agent.ExtendedAgent, net.Conn, error) {
	if authSock == "" {
		return nil, nil, ErrAuthSockNotSet
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", authSock)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to ssh-agent: %w", err)
	}
	return agent.NewClient(conn), conn, nil
}
