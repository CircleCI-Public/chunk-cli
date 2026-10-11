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

		cmd := exec.Command("go", "build", "-o", binaryPath, ".")
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

var (
	versionedMu     sync.Mutex
	versionedBinary = map[string]string{}
)

// BuildVersioned compiles the chunk CLI with a release version baked in, as
// goreleaser does, and returns its path. The default binary reports "dev", which
// `chunk upgrade` and the update notice treat as a source build; tests of those
// need a binary that looks like a release.
func BuildVersioned(t *testing.T, version string) string {
	t.Helper()
	versionedMu.Lock()
	defer versionedMu.Unlock()
	if path, ok := versionedBinary[version]; ok {
		return path
	}

	dir, err := os.MkdirTemp("", "chunk-acceptance-"+version+"-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	path := filepath.Join(dir, "chunk")
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", path, ".")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	versionedBinary[version] = path
	return path
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

	// Prepend --insecure-storage so acceptance tests never touch the system keychain.
	allArgs := append([]string{"--insecure-storage"}, args...)
	bin := binaryPath
	if e.Binary != "" {
		bin = e.Binary
	}
	cmd := exec.CommandContext(ctx, bin, allArgs...)
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
