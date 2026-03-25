package binary

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/env"
)

var (
	binaryPath string
	buildOnce  sync.Once
	buildErr   error
)

// BuildBinary compiles the chunk CLI binary once and returns its path.
// Call from TestMain.
func BuildBinary() (string, error) {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "chunk-acceptance-*")
		if err != nil {
			buildErr = fmt.Errorf("create temp dir: %w", err)
			return
		}

		binaryPath = filepath.Join(dir, "chunk")

		// Find the repo root (parent of acceptance/)
		repoRoot, err := filepath.Abs(filepath.Join("..", ""))
		if err != nil {
			buildErr = fmt.Errorf("resolve repo root: %w", err)
			return
		}

		cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/chunk")
		cmd.Dir = repoRoot
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			buildErr = fmt.Errorf("go build failed: %w\nstderr: %s", err, stderr.String())
			return
		}
	})
	return binaryPath, buildErr
}

// Path returns the path to the compiled binary. Must call BuildBinary first.
func Path() string {
	return binaryPath
}

// CLIResult holds the output of a CLI invocation.
type CLIResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// RunCLI executes the chunk binary with the given args, env, and working directory.
func RunCLI(t *testing.T, args []string, e *env.TestEnv, workDir string) CLIResult {
	t.Helper()
	return RunCLIWithStdin(t, args, e, workDir, nil)
}

// RunCLIWithStdin executes the chunk binary with the given args, env, working directory,
// and optional stdin data.
func RunCLIWithStdin(t *testing.T, args []string, e *env.TestEnv, workDir string, stdin []byte) CLIResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = workDir
	cmd.Env = e.Environ()

	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to run CLI: %v", err)
		}
	}

	return CLIResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
}
