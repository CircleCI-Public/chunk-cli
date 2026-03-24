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

func TestAuthStatusWithEnvKey(t *testing.T) {
	anthropic := fakes.NewFakeAnthropic()
	srv := httptest.NewServer(anthropic)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = srv.URL

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t,
		strings.Contains(combined, "Environment variable") || strings.Contains(combined, "environment variable"),
		"expected output to mention environment variable, got: %s", combined)
}

func TestAuthStatusNoKey(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Not authenticated") || strings.Contains(combined, "not authenticated"),
		"expected output to indicate no auth, got: %s", combined)
}

// config takes priority over env var when both are set
func TestAuthStatusConfigPriority(t *testing.T) {
	anthropic := fakes.NewFakeAnthropic()
	srv := httptest.NewServer(anthropic)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = srv.URL
	env.AnthropicKey = "sk-ant-env-key-EEEE"

	// Store a different key in config file
	setResult := binary.RunCLI(t, []string{"config", "set", "apiKey", "sk-ant-config-key-CCCC"}, env, env.HomeDir)
	assert.Equal(t, setResult.ExitCode, 0, "config set failed\nstdout: %s\nstderr: %s", setResult.Stdout, setResult.Stderr)

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Config file"),
		"expected config source to take priority over env, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "Environment variable"),
		"expected config source, not env, got: %s", combined)
}

// auth status masks all but last 4 chars of API key
func TestAuthStatusMaskExactlyFourChars(t *testing.T) {
	anthropic := fakes.NewFakeAnthropic()
	srv := httptest.NewServer(anthropic)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = srv.URL
	// Key where last-4 and chars-5-to-8-from-end are distinct
	env.AnthropicKey = "sk-ant-AAAA-BBBB-CCCC-DDDD"

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "DDDD"),
		"expected last 4 chars visible, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "CCCC"),
		"expected chars 5-8 from end to be masked, got: %s", combined)
}

func TestAuthLogoutNoStoredKey(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{"auth", "logout"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "No API key"),
		"expected output to indicate no stored key, got: %s", combined)
}
