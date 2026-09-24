package gitexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func setupRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"checkout", "-b", "main"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		gitRun(t, dir, args...)
	}
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		fmt.Sprintf("HOME=%s", dir),
		fmt.Sprintf("PATH=%s", os.Getenv("PATH")),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRunnerUsesDir(t *testing.T) {
	dir := setupRepo(t)
	runner := Runner{Dir: dir}

	out, err := runner.Output(context.Background(), "rev-parse", "--show-toplevel")
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(string(out)), dir)

	assert.NilError(t, runner.Run(context.Background(), "branch", "runner-test"))
	out, err = runner.Output(context.Background(), "show-ref", "--verify", "refs/heads/runner-test")
	assert.NilError(t, err)
	assert.Assert(t, strings.HasSuffix(strings.TrimSpace(string(out)), " refs/heads/runner-test"))
}

func TestRunnerReportsFailure(t *testing.T) {
	runner := Runner{Dir: setupRepo(t)}

	_, err := runner.Output(context.Background(), "rev-parse", "--verify", "refs/heads/missing")
	assert.Assert(t, err != nil)
	out, err := runner.CombinedOutput(context.Background(), "rev-parse", "--verify", "refs/heads/missing")
	assert.Assert(t, err != nil)
	assert.Assert(t, len(out) > 0)
	assert.Assert(t, runner.Run(context.Background(), "rev-parse", "--verify", "refs/heads/missing") != nil)
}

func TestRunnerUsesExplicitEnvironment(t *testing.T) {
	dir := setupRepo(t)
	runner := Runner{
		Dir: dir,
		Env: append(os.Environ(),
			"GIT_AUTHOR_NAME=Runner Test",
			"GIT_AUTHOR_EMAIL=runner@test.example",
		),
	}

	out, err := runner.Output(context.Background(), "var", "GIT_AUTHOR_IDENT")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(out), "Runner Test <runner@test.example>"))
}

func TestRunnerHonoursCanceledContext(t *testing.T) {
	runner := Runner{Dir: setupRepo(t)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.Output(ctx, "status")
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
	_, err = runner.CombinedOutput(ctx, "status")
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
	err = runner.Run(ctx, "status")
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
}
