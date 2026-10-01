package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// setupFactoryProject is setupReviewProject with an intent to implement.
func setupFactoryProject(t *testing.T, resp *fakes.ExecResponse) (*testenv.TestEnv, *fakes.FakeCircleCI, string) {
	t.Helper()
	env, cci, workDir := setupReviewProject(t, resp, "correctness")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "INTENT.md"), []byte("Build a hello world app."), 0o644))
	return env, cci, workDir
}

func TestFactoryMissingIntent(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, nil, "correctness")

	result := binary.RunCLI(t, []string{"factory"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Could not read INTENT.md"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0,
		"no sidecar should boot without an intent")
}

// A --reviews path that does not exist is a typo, so it is named as one.
func TestFactoryExplicitReviewsDirMissing(t *testing.T) {
	env, cci, workDir := setupFactoryProject(t, nil)

	result := binary.RunCLI(t, []string{"factory", "--reviews", ".chunk/review"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Could not read prompts"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
}

// The worker runs before any review, so it is the first to meet a rejected
// credential and must report it as one.
func TestFactoryWorkerCredentialRejected(t *testing.T) {
	env, cci, workDir := setupFactoryProject(t, &fakes.ExecResponse{
		CommandID: "cmd-1",
		Stdout:    "Failed to authenticate. API Error: 401 API key is invalid.",
		ExitCode:  1,
	})

	result := binary.RunCLI(t, []string{"factory"}, env, workDir)

	assert.Equal(t, result.ExitCode, 3, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "ANTHROPIC_API_KEY"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(cci.Sidecars), 0, "the run's sidecars should be deleted")
}

// A worker that changes nothing must not let the untouched tree pass.
func TestFactoryWorkerNoChanges(t *testing.T) {
	env, cci, workDir := setupFactoryProject(t, &fakes.ExecResponse{CommandID: "cmd-1"})

	result := binary.RunCLI(t, []string{"factory"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "without changing anything"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "Branch:   chunk/factory-"), "stdout: %s", result.Stdout)
	assert.Equal(t, len(cci.Sidecars), 0, "the run's sidecars should be deleted")

	// The work happened in a worktree; the developer's tree is untouched.
	entries, err := os.ReadDir(filepath.Join(workDir, ".chunk", "worktrees"))
	assert.NilError(t, err)
	assert.Assert(t, len(entries) == 2, "want .gitignore and one worktree, got %v", entries)
	_, err = os.Stat(filepath.Join(workDir, ".chunk", "factory-worker-pool.json"))
	assert.Assert(t, os.IsNotExist(err), "pool state belongs in the worktree")
}
