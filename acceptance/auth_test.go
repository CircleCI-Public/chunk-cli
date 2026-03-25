package acceptance

import (
	"net/http"
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
	assert.Assert(t, strings.Contains(combined, "API key is valid"),
		"expected validation success message, got: %s", combined)
}

func TestAuthStatusNoKey(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Not authenticated") || strings.Contains(combined, "not authenticated"),
		"expected output to indicate no auth, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "chunk auth login"),
		"expected help text about login command, got: %s", combined)
}

func TestAuthStatusInvalidKey(t *testing.T) {
	// Fake server that rejects all keys with 401
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = srv.URL
	env.AnthropicKey = "sk-ant-invalid-key-0000"

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 1, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "validation failed"),
		"expected validation failure message, got: %s", combined)
}

func TestAuthStatusShowsHeader(t *testing.T) {
	anthropic := fakes.NewFakeAnthropic()
	srv := httptest.NewServer(anthropic)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = srv.URL

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Authentication Status"),
		"expected header in output, got: %s", combined)
}

// env var takes priority over config file when both are set
func TestAuthStatusEnvOverridesConfig(t *testing.T) {
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
	assert.Assert(t, strings.Contains(combined, "Environment variable"),
		"expected env to take priority over config file, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "Config file"),
		"expected env source, not config file, got: %s", combined)
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

func TestAuthLogoutNoStoredKeyWithEnvVar(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = "sk-ant-env-only-key"

	// No config file key, but env var is set
	result := binary.RunCLI(t, []string{"auth", "logout"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "No API key"),
		"expected no stored key message, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "ANTHROPIC_API_KEY"),
		"expected env var note, got: %s", combined)
}
