package sandbox

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func List(ctx context.Context, client *circleci.Client, orgID string) ([]circleci.Sandbox, error) {
	return client.ListSandboxes(ctx, orgID)
}

func Create(ctx context.Context, client *circleci.Client, orgID, name, image string) (*circleci.Sandbox, error) {
	return client.CreateSandbox(ctx, orgID, name, image)
}

func Exec(ctx context.Context, client *circleci.Client, sandboxID, command string, args []string) (*circleci.ExecResponse, error) {
	return client.Exec(ctx, sandboxID, command, args)
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

	return client.AddSshKey(ctx, sandboxID, key)
}

// SSH opens a session and executes a command over SSH.
func SSH(ctx context.Context, client *circleci.Client, sandboxID, identityFile string, args []string, io iostream.Streams) error {
	session, err := OpenSession(ctx, client, sandboxID, identityFile)
	if err != nil {
		return err
	}

	command := ShellJoin(args)
	result, err := ExecOverSSH(session, command, nil)
	if err != nil {
		return err
	}

	if result.Stdout != "" {
		_, _ = fmt.Fprint(io.Out, result.Stdout)
	}
	if result.Stderr != "" {
		_, _ = fmt.Fprint(io.Err, result.Stderr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("command exited with status %d", result.ExitCode)
	}
	return nil
}
