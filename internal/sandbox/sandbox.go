package sandbox

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

func List(ctx context.Context, client *circleci.Client, orgID string) ([]circleci.Sandbox, error) {
	return client.ListSandboxes(ctx, orgID)
}

func Create(ctx context.Context, client *circleci.Client, orgID, name, image string) (*circleci.Sandbox, error) {
	return client.CreateSandbox(ctx, orgID, name, image)
}

func Exec(ctx context.Context, client *circleci.Client, orgID, sandboxID, command string, args []string) (*circleci.ExecResponse, error) {
	token, err := client.CreateAccessToken(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	return client.Exec(ctx, token, sandboxID, command, args)
}

func AddSshKey(ctx context.Context, client *circleci.Client, sandboxID, publicKey, publicKeyFile string) (*circleci.AddSshKeyResponse, error) {
	if publicKey != "" && publicKeyFile != "" {
		return nil, fmt.Errorf("--public-key and --public-key-file are mutually exclusive")
	}
	if publicKey == "" && publicKeyFile == "" {
		return nil, fmt.Errorf("either --public-key or --public-key-file is required")
	}

	key := publicKey
	if publicKeyFile != "" {
		data, err := os.ReadFile(publicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read public key file: %w", err)
		}
		key = strings.TrimSpace(string(data))
	}

	if strings.Contains(key, "PRIVATE KEY") {
		return nil, fmt.Errorf("the provided key appears to be a private key; please provide a public key instead")
	}

	token, err := client.CreateAccessToken(ctx, sandboxID)
	if err != nil {
		return nil, err
	}

	return client.AddSshKey(ctx, token, key)
}
