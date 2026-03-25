package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

const (
	defaultKeyName = "chunk_ai"
	defaultSSHUser = "user"
	defaultSSHPort = 2222
	knownHostsFile = "chunk_ai_known_hosts"
)

// Session holds the info needed to SSH into a sandbox.
type Session struct {
	URL          string // sandbox domain
	IdentityFile string // path to SSH private key
	KnownHosts   string // path to known_hosts file
}

// OpenSession registers an SSH key with the sandbox and returns session info.
func OpenSession(ctx context.Context, client *circleci.Client, sandboxID, identityFile string) (*Session, error) {
	sshDir := filepath.Join(os.Getenv("HOME"), ".ssh")

	if identityFile == "" {
		identityFile = filepath.Join(sshDir, defaultKeyName)
	}

	if _, err := os.Stat(identityFile); err != nil {
		return nil, fmt.Errorf("SSH key not found: %s\nGenerate one with: ssh-keygen -t ed25519 -f %s\nOr pass --identity-file to use an existing key", identityFile, identityFile)
	}

	pubKeyPath := identityFile + ".pub"
	pubKeyData, err := os.ReadFile(pubKeyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("SSH public key not found: %s\nThe public key must be co-located with the private key.\nGenerate a new keypair with: ssh-keygen -t ed25519 -f %s", pubKeyPath, identityFile)
		}
		return nil, fmt.Errorf("read public key: %w", err)
	}
	pubKey := strings.TrimSpace(string(pubKeyData))

	resp, err := client.AddSSHKey(ctx, sandboxID, pubKey)
	if err != nil {
		return nil, fmt.Errorf("register SSH key: %w", err)
	}

	return &Session{
		URL:          resp.URL,
		IdentityFile: identityFile,
		KnownHosts:   filepath.Join(sshDir, knownHostsFile),
	}, nil
}
