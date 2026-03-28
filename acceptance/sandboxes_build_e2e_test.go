package acceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
)

// TestSandboxesBuildEndToEnd clones real open-source repos, runs
// `chunk sandboxes build` to generate a Dockerfile.test, builds the image,
// and runs the tests inside the container.
//
// Enable with: CHUNK_ENV_BUILDER_ACCEPTANCE=1
// Requires: git and docker accessible without sudo.
func TestSandboxesBuildEndToEnd(t *testing.T) {
	if os.Getenv("CHUNK_ENV_BUILDER_ACCEPTANCE") == "" {
		t.Skip("set CHUNK_ENV_BUILDER_ACCEPTANCE=1 to run")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	repos := []struct {
		name string
		url  string
	}{
		{"flask", "https://github.com/pallets/flask.git"},
		{"serde", "https://github.com/serde-rs/serde.git"},
		{"gson", "https://github.com/google/gson.git"},
		{"zod", "https://github.com/colinhacks/zod.git"},
		{"lo", "https://github.com/samber/lo.git"},
		{"pydantic", "https://github.com/pydantic/pydantic.git"},
		{"black", "https://github.com/psf/black.git"},
		{"guava", "https://github.com/google/guava.git"},
		{"rxjs", "https://github.com/ReactiveX/rxjs.git"},
		{"rayon", "https://github.com/rayon-rs/rayon.git"},
		{"hugo", "https://github.com/gohugoio/hugo.git"},
	}

	cacheDir := os.Getenv("CHUNK_SANDBOX_CACHE_DIR")

	for _, repo := range repos {
		t.Run(repo.name, func(t *testing.T) {
			var cloneDir string
			if cacheDir != "" {
				cloneDir = cacheDir + "/chunk-sandbox-" + repo.name
			} else {
				cloneDir = t.TempDir()
			}
			e2eCloneRepo(t, repo.url, cloneDir)

			t.Log("Running chunk sandboxes env...")
			stdout, stderr, exitCode := e2eRunEnv(t, cloneDir)
			assert.Equal(t, exitCode, 0,
				"chunk sandboxes env failed\nstdout: %s\nstderr: %s", stdout, stderr)
			t.Log(stderr)
			t.Log(stdout)

			_, err := os.Stat(cloneDir + "/Dockerfile.test")
			assert.NilError(t, err, "Dockerfile.test not created")

			tag := "chunk-sandbox-" + repo.name + "-test"

			t.Log("Building Docker image...")
			buildOutput, buildExitCode := e2eRunBuild(t, cloneDir, tag)
			assert.Equal(t, buildExitCode, 0,
				"chunk sandboxes build failed\n%s", lastLines(buildOutput, 30))

			t.Log("Running tests in container...")
			testsOK, testOutput := e2eDockerRun(t, tag)
			assert.Assert(t, testsOK,
				"tests failed in container\n%s", lastLines(testOutput, 30))
		})
	}
}

func e2eCloneRepo(t *testing.T, url, dir string) {
	t.Helper()
	if _, err := os.Stat(dir + "/.git"); err == nil {
		t.Logf("using existing clone at %s", dir)
		return
	}
	t.Logf("cloning %s into %s...", url, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "clone", "--depth=1", url, dir).CombinedOutput()
	assert.NilError(t, err, "git clone failed: %s", out)
}

func e2eRunEnv(t *testing.T, dir string) (stdout, stderr string, exitCode int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary.Path(), "sandboxes", "env", "--dir", dir)
	cmd.Env = []string{
		fmt.Sprintf("PATH=%s", os.Getenv("PATH")),
		fmt.Sprintf("HOME=%s", os.Getenv("HOME")),
		"NO_COLOR=1",
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

func e2eRunBuild(t *testing.T, dir, tag string) (output string, exitCode int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary.Path(), "sandboxes", "build", "--dir", dir, "--tag", tag)
	cmd.Env = []string{
		fmt.Sprintf("PATH=%s", os.Getenv("PATH")),
		fmt.Sprintf("HOME=%s", os.Getenv("HOME")),
		"NO_COLOR=1",
	}
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return outBuf.String(), exitCode
}

func e2eDockerRun(t *testing.T, tag string) (bool, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	args := []string{"run", "--rm", tag}
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	return err == nil, string(out)
}

// lastLines returns the last n lines of s, for trimming long failure output.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	omitted := len(lines) - n
	return fmt.Sprintf("... (%d lines omitted) ...\n", omitted) + strings.Join(lines[len(lines)-n:], "\n")
}
