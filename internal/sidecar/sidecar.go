package sidecar

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func List(ctx context.Context, client *circleci.Client, orgID string) ([]circleci.Sidecar, error) {
	return client.ListSidecars(ctx, orgID)
}

func Create(ctx context.Context, client *circleci.Client, orgID, name, provider, image string) (*circleci.Sidecar, error) {
	return client.CreateSidecar(ctx, orgID, name, provider, image)
}

func Exec(ctx context.Context, client *circleci.Client, sidecarID, command string, args []string) (*circleci.ExecResponse, error) {
	return client.Exec(ctx, sidecarID, command, args)
}

func AddSSHKey(ctx context.Context, client *circleci.Client, sidecarID, publicKey, publicKeyFile string) (*circleci.AddSSHKeyResponse, error) {
	if publicKey != "" && publicKeyFile != "" {
		return nil, ErrMutuallyExclusiveKeys
	}
	if publicKey == "" && publicKeyFile == "" {
		return nil, ErrPublicKeyRequired
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
		return nil, ErrPrivateKeyProvided
	}

	return client.AddSSHKey(ctx, sidecarID, key)
}

// SSH opens a session and either runs a command or starts an interactive shell.
// stdin is forwarded to the remote command when non-nil; callers should pass
// os.Stdin when the process stdin is a pipe, nil otherwise.
func SSH(ctx context.Context, client *circleci.Client, sidecarID, identityFile, authSock string, args []string, envVars map[string]string, io iostream.Streams, stdin io.Reader) error {
	session, err := OpenSession(ctx, client, sidecarID, identityFile, authSock)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return InteractiveShell(ctx, session, envVars)
	}

	command := ShellJoin(args)
	result, err := ExecOverSSH(ctx, session, command, stdin, envVars)
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
		return fmt.Errorf("%q exited with status %d", command, result.ExitCode)
	}
	return nil
}
