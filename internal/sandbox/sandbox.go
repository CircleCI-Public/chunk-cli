package sandbox

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// ExitError represents a remote command that exited with a non-zero status.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("command exited with status %d", e.Code)
}

func (e *ExitError) ExitCode() int { return e.Code }

func List(ctx context.Context, client *circleci.Client, orgID string) ([]circleci.Sandbox, error) {
	return client.ListSandboxes(ctx, orgID)
}

func Create(ctx context.Context, client *circleci.Client, orgID, name, provider, image string) (*circleci.Sandbox, error) {
	return client.CreateSandbox(ctx, orgID, name, provider, image)
}

func Exec(ctx context.Context, client *circleci.Client, sandboxID, command string, args []string) (*circleci.ExecResponse, error) {
	return client.Exec(ctx, sandboxID, command, args)
}

func AddSSHKey(ctx context.Context, client *circleci.Client, sandboxID, publicKey, publicKeyFile string) (*circleci.AddSSHKeyResponse, error) {
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

	return client.AddSSHKey(ctx, sandboxID, key)
}

// StepResult holds the output from executing a single setup step.
type StepResult struct {
	Name     string
	Stdout   string
	Stderr   string
	ExitCode int
}

// RunStep executes a single setup step and returns its result.
func RunStep(ctx context.Context, client *circleci.Client, sandboxID string, step envbuilder.Step) (*StepResult, error) {
	resp, err := client.Exec(ctx, sandboxID, "bash", []string{"-l", "-c", step.Command})
	if err != nil {
		return nil, fmt.Errorf("step %q: %w", step.Name, err)
	}
	return &StepResult{
		Name:     step.Name,
		Stdout:   resp.Stdout,
		Stderr:   resp.Stderr,
		ExitCode: resp.ExitCode,
	}, nil
}

// SSH opens a session and either runs a command or starts an interactive shell.
func SSH(ctx context.Context, client *circleci.Client, sandboxID, identityFile, authSock string, args []string, envVars map[string]string, io iostream.Streams) error {
	session, err := OpenSession(ctx, client, sandboxID, identityFile, authSock)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return InteractiveShell(ctx, session, envVars)
	}

	command := ShellJoin(args)
	result, err := ExecOverSSH(ctx, session, command, nil, envVars)
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
		return &ExitError{Code: result.ExitCode}
	}
	return nil
}
