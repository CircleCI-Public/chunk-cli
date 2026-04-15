package acceptance

import (
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestSandboxResetHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "reset",
		"--sandbox-id", "sb-111",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "sb-111"),
		"expected sandbox ID in success message, got: %s", result.Stderr)
}

func TestSandboxResetSendsCorrectSandboxID(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "reset",
		"--sandbox-id", "sb-target",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	resetReqs := filterByMethod(reqs, "POST", "/api/v2/sandbox/instances/sb-target/reset")
	assert.Equal(t, len(resetReqs), 1, "expected 1 reset request for sb-target")
}

func TestSandboxResetUsesActiveSandbox(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	workDir := t.TempDir()

	// create sets active sandbox to "sandbox-new-123"
	result := binary.RunCLI(t, []string{
		"sandbox", "create",
		"--org-id", "org-aaa",
		"--name", "test-box",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "create stderr: %s", result.Stderr)

	// reset without --sandbox-id should use the active sandbox
	result = binary.RunCLI(t, []string{
		"sandbox", "reset",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "reset stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	resetReqs := filterByMethod(reqs, "POST", "/api/v2/sandbox/instances/sandbox-new-123/reset")
	assert.Equal(t, len(resetReqs), 1, "expected reset request for active sandbox ID")
}

func TestSandboxResetNoActiveSandbox(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := t.TempDir()

	result := binary.RunCLI(t, []string{
		"sandbox", "reset",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit with no sandbox ID")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sandbox-id") || strings.Contains(combined, "active sandbox"),
		"expected helpful error, got: %s", combined)
}

func TestSandboxResetAPIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ResetStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "reset",
		"--sandbox-id", "sb-111",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 500 response")
}

func TestSandboxResetMissingToken(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"sandbox", "reset",
		"--sandbox-id", "sb-111",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit without token")
}
