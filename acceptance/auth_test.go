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

// auth status reads key from config file when no env var is set
func TestAuthStatusFromConfigFile(t *testing.T) {
	anthropic := fakes.NewFakeAnthropic()
	srv := httptest.NewServer(anthropic)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = srv.URL
	env.AnthropicKey = "" // no env var

	// Store key in config file
	setResult := binary.RunCLI(t, []string{"config", "set", "apiKey", "sk-ant-config-only-XYZW"}, env, env.HomeDir)
	assert.Equal(t, setResult.ExitCode, 0, "config set failed: %s", setResult.Stderr)

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Config file"),
		"expected config file source, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "API key is valid"),
		"expected key valid message, got: %s", combined)
	// Last 4 chars visible
	assert.Assert(t, strings.Contains(combined, "XYZW"),
		"expected last 4 chars of key, got: %s", combined)
}

// auth status validates via /v1/messages/count_tokens, not /v1/messages
func TestAuthStatusUsesCountTokensEndpoint(t *testing.T) {
	anthropic := fakes.NewFakeAnthropic()
	srv := httptest.NewServer(anthropic)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = srv.URL

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	requests := anthropic.Recorder.AllRequests()
	assert.Assert(t, len(requests) > 0, "expected at least one request to the API")

	// All requests should hit count_tokens, not messages
	for _, req := range requests {
		assert.Equal(t, req.URL.Path, "/v1/messages/count_tokens",
			"expected count_tokens endpoint, got: %s", req.URL.Path)
	}
}

// auth logout with a stored config key prompts for confirmation.
// Without a TTY the confirmation prompt fails and logout is cancelled,
// but the output shows the config file path, proving the key was detected.
func TestAuthLogoutWithStoredKey(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = "" // no env var

	// Store a key in config
	setResult := binary.RunCLI(t, []string{"config", "set", "apiKey", "sk-ant-stored-key-1234"}, env, env.HomeDir)
	assert.Equal(t, setResult.ExitCode, 0, "config set failed: %s", setResult.Stderr)

	result := binary.RunCLI(t, []string{"auth", "logout"}, env, env.HomeDir)

	// Without a TTY, confirm prompt returns error and logout is cancelled
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	// The command should detect the stored key and mention the config path
	assert.Assert(t,
		strings.Contains(combined, "remove") || strings.Contains(combined, "cancelled"),
		"expected removal prompt or cancellation, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, env.HomeDir), "expected config path in output, got: %s", combined)

	// Key should not have been removed — cancelled logout leaves config intact.
	showResult := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, showResult.ExitCode, 0, "config show failed after cancelled logout: %s", showResult.Stderr)
	assert.Assert(t, strings.Contains(showResult.Stdout, "1234"), "expected stored key (masked) in config output, got: %s", showResult.Stdout)
}

// auth logout with both env var and config key: running logout shows
// the stored key and (if confirmed) removes config key but env var remains.
// Without a TTY the confirmation fails, but the output proves both were detected.
func TestAuthLogoutWithEnvAndConfigKey(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = "sk-ant-env-key-EEEE"

	// Store a different key in config
	setResult := binary.RunCLI(t, []string{"config", "set", "apiKey", "sk-ant-config-key-CCCC"}, env, env.HomeDir)
	assert.Equal(t, setResult.ExitCode, 0, "config set failed: %s", setResult.Stderr)

	result := binary.RunCLI(t, []string{"auth", "logout"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	// Should detect the stored config key and show removal prompt
	assert.Assert(t,
		strings.Contains(combined, "remove") || strings.Contains(combined, "cancelled"),
		"expected removal prompt or cancellation, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, env.HomeDir), "expected config path in output, got: %s", combined)

	// Key should not have been removed — cancelled logout leaves config intact.
	showResult := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, showResult.ExitCode, 0, "config show failed after cancelled logout: %s", showResult.Stderr)
	assert.Assert(t, strings.Contains(showResult.Stdout, "EEEE"), "expected env key (masked) in config output, got: %s", showResult.Stdout)
}

// auth login without TTY: prompts for key but bubbletea fails without terminal,
// so command exits cleanly without storing anything.
func TestAuthLoginNoTTYExitsCleanly(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{"auth", "login"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "API Key Setup"),
		"expected login header, got: %s", combined)
}
