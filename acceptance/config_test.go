package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
)

func TestConfigSetAndShow(t *testing.T) {
	env := testenv.NewTestEnv(t)

	setResult := binary.RunCLI(t, []string{"config", "set", "model", "claude-haiku-4-5-20251001"}, env, env.HomeDir)
	assert.Equal(t, setResult.ExitCode, 0, "config set failed\nstdout: %s\nstderr: %s", setResult.Stdout, setResult.Stderr)

	showResult := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, showResult.ExitCode, 0, "config show failed\nstdout: %s\nstderr: %s", showResult.Stdout, showResult.Stderr)

	combined := showResult.Stdout + showResult.Stderr
	assert.Assert(t, strings.Contains(combined, "claude-haiku-4-5-20251001"),
		"expected config show to contain model name, got: %s", combined)
}

func TestConfigShowDefaults(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "claude-sonnet-4-6"),
		"expected default model value in config show, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "Default"),
		"expected '(Default)' source annotation, got: %s", combined)
}

// anthropicAPIKey last 4 chars shown, not first 4
func TestConfigShowMasksLastFourChars(t *testing.T) {
	env := testenv.NewTestEnv(t)
	// Use a key where the first 4 and last 4 chars are distinct
	env.AnthropicKey = "sk-ant-api03-AAAA-middle-ZZZZ"

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	// Last 4 chars "ZZZZ" should be visible
	assert.Assert(t, strings.Contains(combined, "ZZZZ"),
		"expected last 4 chars of API key to be visible, got: %s", combined)
	// First 4 chars "sk-a" should NOT appear unmasked (they should be replaced by *)
	// The masked key should look like "****...**ZZZZ"
	assert.Assert(t, !strings.Contains(combined, "sk-a"),
		"expected first chars of API key to be masked, got: %s", combined)
}

// API key stored in config file (no env var) is resolved and shown
func TestConfigShowFromConfigFile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = "" // no env var

	// Store key directly in config file
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	assert.NilError(t, config.Save(config.UserConfig{AnthropicAPIKey: "sk-ant-stored-key-ZZZZ"}))

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "user config"),
		"expected apiKey source to be 'user config', got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "ZZZZ"),
		"expected last 4 chars of stored key visible, got: %s", combined)
}

// config show must not display analyzeModel or promptModel
func TestConfigShowNoModelConstants(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, !strings.Contains(combined, "analyzeModel"),
		"analyzeModel should not appear in config show, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "promptModel"),
		"promptModel should not appear in config show, got: %s", combined)
}

// config set rejects invalid keys
func TestConfigSetInvalidKey(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"config", "set", "badkey", "somevalue"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 1, "expected exit code 1 for invalid key\nstdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t,
		strings.Contains(combined, "Unknown config key") || strings.Contains(combined, "not a recognized"),
		"expected error about invalid key, got: %s", combined)
}

// verify config file permissions are 0o600 and dir is 0o700
func TestConfigFilePermissions(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"config", "set", "model", "test-model"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "config set failed\nstdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// Find the config file — it should be at XDG_CONFIG_HOME/chunk/config.json
	configDir := filepath.Join(env.HomeDir, ".config", "chunk")
	configFile := filepath.Join(configDir, "config.json")

	// Check directory permissions
	dirInfo, err := os.Stat(configDir)
	assert.NilError(t, err, "config dir should exist at %s", configDir)
	dirPerm := dirInfo.Mode().Perm()
	assert.Equal(t, dirPerm, os.FileMode(0o700),
		fmt.Sprintf("expected config dir perm 0700, got %04o", dirPerm))

	// Check file permissions
	fileInfo, err := os.Stat(configFile)
	assert.NilError(t, err, "config file should exist at %s", configFile)
	filePerm := fileInfo.Mode().Perm()
	assert.Equal(t, filePerm, os.FileMode(0o600),
		fmt.Sprintf("expected config file perm 0600, got %04o", filePerm))
}

func TestConfigShowModelFromEnvVar(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["CODE_REVIEW_CLI_MODEL"] = "claude-test-env-model"

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "claude-test-env-model"),
		"expected model from env var, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "Environment variable"),
		"expected 'Environment variable' source, got: %s", combined)
}

func TestConfigShowAPIKeyEnvPrecedenceOverFile(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// Store key directly in config file
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	assert.NilError(t, config.Save(config.UserConfig{AnthropicAPIKey: "sk-ant-file-key-FILE"}))

	// Set env var — it should win
	env.AnthropicKey = "sk-ant-env-key-ENVK"
	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "ENVK"),
		"expected env key last 4 chars, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "FILE"),
		"file key should not appear when env var is set, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "Environment variable"),
		"expected env var source, got: %s", combined)
}

func TestConfigShowModelEnvPrecedenceOverFile(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// Set model via config file
	setResult := binary.RunCLI(t, []string{"config", "set", "model", "file-model"}, env, env.HomeDir)
	assert.Equal(t, setResult.ExitCode, 0, "config set failed")

	// Set env var — it should win
	env.Extra["CODE_REVIEW_CLI_MODEL"] = "env-model"
	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "env-model"),
		"expected env model, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "file-model"),
		"file model should not appear when env var is set, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "Environment variable"),
		"expected env var source, got: %s", combined)
}

func TestConfigSetMissingValue(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// "config set model" with no value — cobra ExactArgs(2) should reject
	result := binary.RunCLI(t, []string{"config", "set", "model"}, env, env.HomeDir)
	assert.Assert(t, result.ExitCode != 0,
		"expected non-zero exit for missing value\nstdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

func TestConfigSetMissingKeyAndValue(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// "config set" with no args — cobra ExactArgs(2) should reject
	result := binary.RunCLI(t, []string{"config", "set"}, env, env.HomeDir)
	assert.Assert(t, result.ExitCode != 0,
		"expected non-zero exit for missing args\nstdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}
