package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

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
	assert.Assert(t, strings.Contains(combined, "model"),
		"expected config show to mention model, got: %s", combined)
}

// apiKey.slice(-4) not .slice(0,4) — verify last 4 chars shown, not first 4
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

	// Store key via config set
	setResult := binary.RunCLI(t, []string{"config", "set", "apiKey", "sk-ant-stored-key-ZZZZ"}, env, env.HomeDir)
	assert.Equal(t, setResult.ExitCode, 0, "config set failed\nstdout: %s\nstderr: %s", setResult.Stdout, setResult.Stderr)

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
	assert.Equal(t, result.ExitCode, 2, "expected exit code 2 for invalid key\nstdout: %s\nstderr: %s", result.Stdout, result.Stderr)

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
