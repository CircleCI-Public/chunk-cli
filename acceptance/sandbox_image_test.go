package acceptance

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

// TestSandboxImageCommands verifies that the commands chunk would run in the
// sandbox actually work inside the sandbox image.
//
// Enable with: CHUNK_SANDBOX_IMAGE_TEST=1
// Requires: docker accessible without sudo.
func TestSandboxImageCommands(t *testing.T) {
	if os.Getenv("CHUNK_SANDBOX_IMAGE_TEST") == "" {
		t.Skip("set CHUNK_SANDBOX_IMAGE_TEST=1 to run")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}

	repoRoot, err := filepath.Abs("..")
	assert.NilError(t, err)

	imageTag := "chunk-sandbox-image-test"

	t.Log("Building Dockerfile.sandbox...")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer buildCancel()

	buildCmd := exec.CommandContext(buildCtx, "docker", "build",
		"-f", "Dockerfile.sandbox",
		"-t", imageTag,
		".",
	)
	buildCmd.Dir = repoRoot
	out, err := buildCmd.CombinedOutput()
	assert.NilError(t, err, "docker build failed:\n%s", string(out))

	t.Cleanup(func() {
		_ = exec.Command("docker", "rmi", imageTag).Run()
	})

	tests := []struct {
		name       string
		command    []string
		wantOutput string
	}{
		{
			name:       "apt-get",
			command:    []string{"apt-get", "--version"},
			wantOutput: "apt",
		},
		{
			name:       "git",
			command:    []string{"git", "--version"},
			wantOutput: "git version",
		},
		{
			name:       "go",
			command:    []string{"go", "version"},
			wantOutput: "go version go",
		},
		{
			name:       "gofmt",
			command:    []string{"sh", "-c", `echo "package main" | gofmt`},
			wantOutput: "package main",
		},
		{
			name:       "task",
			command:    []string{"task", "--help"},
			wantOutput: "task",
		},
		{
			name:       "sh",
			command:    []string{"sh", "-c", "echo ok"},
			wantOutput: "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"run", "--rm", imageTag}, tt.command...)

			runCtx, runCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer runCancel()

			cmd := exec.CommandContext(runCtx, "docker", args...)
			var buf bytes.Buffer
			cmd.Stdout = &buf
			cmd.Stderr = &buf

			runErr := cmd.Run()
			exitCode := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				if errors.As(runErr, &exitErr) {
					exitCode = exitErr.ExitCode()
				}
			}

			assert.Equal(t, exitCode, 0,
				"%v exited %d\noutput: %s", tt.command, exitCode, buf.String())
			assert.Assert(t, strings.Contains(buf.String(), tt.wantOutput),
				"expected %q in output of %v\ngot: %s", tt.wantOutput, tt.command, buf.String())
		})
	}
}
