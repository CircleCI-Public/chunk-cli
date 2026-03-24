package acceptance

import (
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/acceptance/testutil"
	"github.com/CircleCI-Public/chunk-cli/acceptance/testutil/fakes"
)

func TestAuthStatusWithEnvKey(t *testing.T) {
	anthropic := fakes.NewFakeAnthropic()
	srv := httptest.NewServer(anthropic)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.AnthropicURL = srv.URL

	result := testutil.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t,
		strings.Contains(combined, "Environment variable") || strings.Contains(combined, "environment variable"),
		"expected output to mention environment variable, got: %s", combined)
}

func TestAuthStatusNoKey(t *testing.T) {
	env := testutil.NewTestEnv(t)
	env.AnthropicKey = ""

	result := testutil.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Not authenticated") || strings.Contains(combined, "not authenticated"),
		"expected output to indicate no auth, got: %s", combined)
}

func TestAuthLogoutNoStoredKey(t *testing.T) {
	env := testutil.NewTestEnv(t)
	env.AnthropicKey = ""

	result := testutil.RunCLI(t, []string{"auth", "logout"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "No API key"),
		"expected output to indicate no stored key, got: %s", combined)
}
