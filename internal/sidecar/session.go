package sidecar

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/closer"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

const (
	defaultKeyName = "chunk_ai"
	defaultSSHUser = "user"
	knownHostsFile = "chunk_ai_known_hosts"
)

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

// OpenSession registers an SSH key with the sidecar and returns session info.
// authSock is the SSH_AUTH_SOCK path; when non-empty and no identityFile is
// given, the agent is tried first.
func OpenSession(ctx context.Context, client *circleci.Client, sidecarID, identityFile, authSock string) (*Session, error) {
	sshDir := filepath.Join(os.Getenv(config.EnvHome), ".ssh")

	// When no identity file is specified, try the ssh-agent first.
	if identityFile == "" && authSock != "" {
		pubKey, err := agentPublicKey(ctx, authSock)
		if err == nil {
			resp, err := client.AddSSHKey(ctx, sidecarID, pubKey)
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
		// Agent not available — fall back to default key file.
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

	resp, err := client.AddSSHKey(ctx, sidecarID, pubKey)
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
