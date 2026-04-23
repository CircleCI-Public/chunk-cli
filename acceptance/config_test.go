package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

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
	assert.Check(t, cmp.Contains(combined, "claude-haiku-4-5-20251001"),
		"expected config show to contain model name")
}

func TestConfigShowDefaults(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "claude-sonnet-4-6"),
		"expected default model value in config show")
	assert.Check(t, cmp.Contains(combined, "Default"),
		"expected '(Default)' source annotation")
}

// anthropicAPIKey last 4 chars shown, not first 4
func TestConfigShowMasksLastFourChars(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = "sk-ant-api03-AAAA-middle-ZZZZ"

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "ZZZZ"),
		"expected last 4 chars of API key to be visible")
	assert.Check(t, !strings.Contains(combined, "sk-a"),
		"expected first chars of API key to be masked, got: %s", combined)
}

// API key stored in config file (no env var) is resolved and shown
func TestConfigShowFromConfigFile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.AnthropicKey = "" // no env var

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	err := config.Save(config.UserConfig{AnthropicAPIKey: "sk-ant-stored-key-ZZZZ"})
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "user config"),
		"expected apiKey source to be 'user config'")
	assert.Check(t, cmp.Contains(combined, "ZZZZ"),
		"expected last 4 chars of stored key visible")
}

// config show must not display analyzeModel or promptModel
func TestConfigShowNoModelConstants(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, !strings.Contains(combined, "analyzeModel"),
		"analyzeModel should not appear in config show, got: %s", combined)
	assert.Check(t, !strings.Contains(combined, "promptModel"),
		"promptModel should not appear in config show, got: %s", combined)
}

// config set rejects invalid keys
func TestConfigSetInvalidKey(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"config", "set", "badkey", "somevalue"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 1, "expected exit code 1 for invalid key\nstdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t,
		strings.Contains(combined, "Unknown config key") || strings.Contains(combined, "not a recognized"),
		"expected error about invalid key, got: %s", combined)
}

// verify config file permissions are 0o600 and dir is 0o700
func TestConfigFilePermissions(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"config", "set", "model", "test-model"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "config set failed\nstdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	configDir := filepath.Join(env.HomeDir, ".config", "chunk")
	configFile := filepath.Join(configDir, "config.json")

	dirInfo, err := os.Stat(configDir)
	assert.NilError(t, err, "config dir should exist at %s", configDir)
	dirPerm := dirInfo.Mode().Perm()
	assert.Check(t, cmp.Equal(dirPerm, os.FileMode(0o700)))

	fileInfo, err := os.Stat(configFile)
	assert.NilError(t, err, "config file should exist at %s", configFile)
	filePerm := fileInfo.Mode().Perm()
	assert.Check(t, cmp.Equal(filePerm, os.FileMode(0o600)))
}

func TestConfigShowModelFromEnvVar(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["CODE_REVIEW_CLI_MODEL"] = "claude-test-env-model"

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "claude-test-env-model"),
		"expected model from env var")
	assert.Check(t, cmp.Contains(combined, "Environment variable"),
		"expected 'Environment variable' source")
}

func TestConfigShowAPIKeyEnvPrecedenceOverFile(t *testing.T) {
	env := testenv.NewTestEnv(t)

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	err := config.Save(config.UserConfig{AnthropicAPIKey: "sk-ant-file-key-FILE"})
	assert.NilError(t, err)

	env.AnthropicKey = "sk-ant-env-key-ENVK"
	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "ENVK"),
		"expected env key last 4 chars")
	assert.Check(t, !strings.Contains(combined, "FILE"),
		"file key should not appear when env var is set, got: %s", combined)
	assert.Check(t, cmp.Contains(combined, "Environment variable"),
		"expected env var source")
}

func TestConfigShowModelEnvPrecedenceOverFile(t *testing.T) {
	env := testenv.NewTestEnv(t)

	setResult := binary.RunCLI(t, []string{"config", "set", "model", "file-model"}, env, env.HomeDir)
	assert.Equal(t, setResult.ExitCode, 0, "config set failed")

	env.Extra["CODE_REVIEW_CLI_MODEL"] = "env-model"
	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "env-model"),
		"expected env model")
	assert.Check(t, !strings.Contains(combined, "file-model"),
		"file model should not appear when env var is set, got: %s", combined)
	assert.Check(t, !strings.Contains(combined, "Default"),
		"default source should not appear, got: %s", combined)
	assert.Check(t, cmp.Contains(combined, "Environment variable"),
		"expected env var source")
}

// --- Precedence tests: env > config file (remaining providers) ---

func TestConfigShowCircleCITokenEnvPrecedenceOverFile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.CircleToken = "" // clear default

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	err := config.Save(config.UserConfig{CircleCIToken: "file-circle-token-FILE"})
	assert.NilError(t, err)

	env.CircleToken = "env-circle-token-ENVT"
	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "ENVT"),
		"expected env token last 4 chars")
	assert.Check(t, !strings.Contains(combined, "FILE"),
		"file token should not appear when env var is set, got: %s", combined)
	assert.Check(t, cmp.Contains(combined, "Environment variable"),
		"expected env var source")
}

func TestConfigShowCircleCITokenFromFile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.CircleToken = "" // no env var

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	err := config.Save(config.UserConfig{CircleCIToken: "stored-circle-FTOK"})
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "FTOK"),
		"expected file token last 4 chars")
	assert.Check(t, cmp.Contains(combined, "user config"),
		"expected config file source")
}

func TestConfigShowCircleTokenEnvPrecedenceOverCircleCIToken(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.CircleToken = "" // clear the default CIRCLE_TOKEN

	env.Extra["CIRCLE_TOKEN"] = "circle-token-CTOK"
	env.Extra["CIRCLECI_TOKEN"] = "circleci-token-CITK"

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "CTOK"),
		"expected CIRCLE_TOKEN value")
	assert.Check(t, !strings.Contains(combined, "CITK"),
		"CIRCLECI_TOKEN should not win over CIRCLE_TOKEN, got: %s", combined)
	assert.Check(t, cmp.Contains(combined, "CIRCLE_TOKEN"),
		"expected CIRCLE_TOKEN source label")
}

func TestConfigShowGitHubTokenEnvPrecedenceOverFile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.GithubToken = "" // clear default

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	err := config.Save(config.UserConfig{GitHubToken: "file-github-token-FILE"})
	assert.NilError(t, err)

	env.GithubToken = "env-github-token-ENVG"
	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "ENVG"),
		"expected env token last 4 chars")
	assert.Check(t, !strings.Contains(combined, "FILE"),
		"file token should not appear when env var is set, got: %s", combined)
	assert.Check(t, cmp.Contains(combined, "Environment variable"),
		"expected env var source")
}

func TestConfigShowGitHubTokenFromFile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.GithubToken = "" // no env var

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.HomeDir, ".config"))
	err := config.Save(config.UserConfig{GitHubToken: "stored-github-GHTK"})
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "GHTK"),
		"expected file token last 4 chars")
	assert.Check(t, cmp.Contains(combined, "user config"),
		"expected config file source")
}

func TestConfigShowModelFileOverDefault(t *testing.T) {
	env := testenv.NewTestEnv(t)

	setResult := binary.RunCLI(t, []string{"config", "set", "model", "custom-file-model"}, env, env.HomeDir)
	assert.Equal(t, setResult.ExitCode, 0, "config set failed")

	result := binary.RunCLI(t, []string{"config", "show"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Check(t, cmp.Contains(combined, "custom-file-model"),
		"expected file model")
	assert.Check(t, !strings.Contains(combined, "Default"),
		"should not show Default source, got: %s", combined)
	assert.Check(t, cmp.Contains(combined, "user config"),
		"expected config file source")
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
