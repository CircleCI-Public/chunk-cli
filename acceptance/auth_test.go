package acceptance

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// --- auth status ---

func TestAuthStatusWithEnvKey(t *testing.T) {
	anthropic := fakes.NewFakeAnthropic()
	srv := httptest.NewServer(anthropic)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = srv.URL
	env.CircleToken = "" // Anthropic-only test
	env.GithubToken = ""

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t,
		strings.Contains(combined, "Environment variable") || strings.Contains(combined, "environment variable"),
		"expected output to mention environment variable, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "Valid"),
		"expected validation success message, got: %s", combined)
}

func TestAuthStatusNoKey(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""
	env.CircleToken = ""
	env.GithubToken = ""

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	// All three sections are always shown, each reporting "Not set" when unconfigured
	assert.Assert(t, strings.Contains(combined, "CircleCI"),
		"expected CircleCI section, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "Anthropic"),
		"expected Anthropic section, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "GitHub"),
		"expected GitHub section, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "Not set"),
		"expected Not set in output, got: %s", combined)
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
	env.CircleToken = "" // Anthropic-only test

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
	env.CircleToken = "" // Anthropic-only test
	env.GithubToken = ""

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
	env.CircleToken = "" // Anthropic-only test
	env.GithubToken = ""

	// Store a different key in config file
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	assert.NilError(t, config.Save(config.UserConfig{AnthropicAPIKey: "sk-ant-config-key-CCCC"}))

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
	env.AnthropicKey = "sk-ant-AAAA-BBBB-CCCC-DDDD"
	env.CircleToken = "" // Anthropic-only test
	env.GithubToken = ""

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "DDDD"),
		"expected last 4 chars visible, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "CCCC"),
		"expected chars 5-8 from end to be masked, got: %s", combined)
}

// auth status reads key from config file when no env var is set
func TestAuthStatusFromConfigFile(t *testing.T) {
	anthropic := fakes.NewFakeAnthropic()
	srv := httptest.NewServer(anthropic)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = srv.URL
	env.AnthropicKey = "" // no env var
	env.CircleToken = ""  // Anthropic-only test
	env.GithubToken = ""

	// Store key in config file
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	assert.NilError(t, config.Save(config.UserConfig{AnthropicAPIKey: "sk-ant-config-only-XYZW"}))

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Config file"),
		"expected config file source, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "Valid"),
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
	env.CircleToken = "" // Anthropic-only test
	env.GithubToken = ""

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

// auth status with all three providers configured shows all sections
func TestAuthStatusAllProviders(t *testing.T) {
	anthropic := fakes.NewFakeAnthropic()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	circleci := fakes.NewFakeCircleCI()
	circleCISrv := httptest.NewServer(circleci)
	defer circleCISrv.Close()

	gh := fakes.NewFakeGitHub()
	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = anthropicSrv.URL
	env.CircleCIURL = circleCISrv.URL
	env.GithubURL = ghSrv.URL

	result := binary.RunCLI(t, []string{"auth", "status"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "CircleCI"),
		"expected CircleCI section, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "Anthropic"),
		"expected Anthropic section, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "GitHub"),
		"expected GitHub section, got: %s", combined)
}

// auth set with an unrecognised provider prints an error and exits non-zero
func TestAuthSetInvalidProvider(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"auth", "set", "invalid-provider"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for invalid provider")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "unknown provider"),
		"expected error about unknown provider, got: %s", combined)
}

// --- auth remove ---

func TestAuthRemoveNoStoredKey(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{"auth", "remove", "anthropic"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "No API key"),
		"expected output to indicate no stored key, got: %s", combined)
}

func TestAuthRemoveNoStoredKeyWithEnvVar(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = "sk-ant-env-only-key"

	// No config file key, but env var is set
	result := binary.RunCLI(t, []string{"auth", "remove", "anthropic"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "No API key"),
		"expected no stored key message, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "ANTHROPIC_API_KEY"),
		"expected env var note, got: %s", combined)
}

// auth remove anthropic with a stored config key prompts for confirmation.
// Without a TTY the confirmation prompt fails and remove is cancelled.
func TestAuthRemoveWithStoredKey(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = "" // no env var

	// Store a key in config
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	assert.NilError(t, config.Save(config.UserConfig{AnthropicAPIKey: "sk-ant-stored-key-1234"}))

	result := binary.RunCLI(t, []string{"auth", "remove", "anthropic"}, env, env.HomeDir)

	// Without a TTY, confirm prompt returns error and remove is cancelled
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	// The command should detect the stored key and mention the config path
	assert.Assert(t,
		strings.Contains(combined, "remove") || strings.Contains(combined, "Cancelled"),
		"expected removal prompt or cancellation, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, env.HomeDir), "expected config path in output, got: %s", combined)

	// Key should not have been removed — cancelled remove leaves config intact.
	showResult := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, showResult.ExitCode, 0, "config show failed after cancelled remove: %s", showResult.Stderr)
	assert.Assert(t, strings.Contains(showResult.Stdout, "1234"), "expected stored key (masked) in config output, got: %s", showResult.Stdout)
}

// auth remove with both env var and config key.
// Without a TTY the confirmation fails, but the output proves the stored key was detected.
func TestAuthRemoveWithEnvAndConfigKey(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = "sk-ant-env-key-EEEE"

	// Store a different key in config
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	assert.NilError(t, config.Save(config.UserConfig{AnthropicAPIKey: "sk-ant-config-key-CCCC"}))

	result := binary.RunCLI(t, []string{"auth", "remove", "anthropic"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t,
		strings.Contains(combined, "remove") || strings.Contains(combined, "Cancelled"),
		"expected removal prompt or cancellation, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, env.HomeDir), "expected config path in output, got: %s", combined)

	// Key should not have been removed — cancelled remove leaves config intact.
	showResult := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, showResult.ExitCode, 0, "config show failed after cancelled remove: %s", showResult.Stderr)
	assert.Assert(t, strings.Contains(showResult.Stdout, "EEEE"), "expected env key (masked) in config output, got: %s", showResult.Stdout)
}

// auth remove requires a provider argument
func TestAuthRemoveRequiresProvider(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"auth", "remove"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit when provider omitted")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "remove") || strings.Contains(combined, "accepts"),
		"expected usage error, got: %s", combined)
}

// auth remove circleci removes a stored CircleCI token (cancelled without TTY)
func TestAuthRemoveCircleCI(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.CircleToken = "" // no env var

	// Store a CircleCI token via the config package instead of raw JSON
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	assert.NilError(t, config.Save(config.UserConfig{CircleCIToken: "circle-tok-1234"}))

	result := binary.RunCLI(t, []string{"auth", "remove", "circleci"}, env, env.HomeDir)

	// Without a TTY, confirm prompt cancels
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t,
		strings.Contains(combined, "remove") || strings.Contains(combined, "Cancelled"),
		"expected removal prompt or cancellation, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, env.HomeDir), "expected config path in output, got: %s", combined)
}

// --- auth set ---

// auth set anthropic without TTY exits cleanly
func TestAuthSetAnthropicNoTTYExitsCleanly(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{"auth", "set", "anthropic"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "API Key Setup"),
		"expected Anthropic login header, got: %s", combined)
}

// auth set circleci without TTY exits cleanly
func TestAuthSetCircleCI(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.CircleToken = ""
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{"auth", "set", "circleci"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "CircleCI Token Setup"),
		"expected CircleCI setup header, got: %s", combined)
}
