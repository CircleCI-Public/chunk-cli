package acceptance

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func TestInitWritesVCSConfig(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")

	cci := fakes.NewFakeCircleCI()
	cci.Collaborations = []fakes.Collaboration{
		{ID: "org-aaa", Name: "my-org"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.AnthropicKey = "" // skip claude

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate", "--skip-circleci",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	configPath := filepath.Join(workDir, ".chunk", "config.json")
	data, err := os.ReadFile(configPath)
	assert.NilError(t, err, "expected .chunk/config.json to exist")

	var cfg map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &cfg))

	vcs, ok := cfg["vcs"].(map[string]interface{})
	assert.Assert(t, ok, "expected vcs section in config, got: %s", string(data))
	assert.Equal(t, vcs["org"], "my-org", "expected org=my-org, got: %v", vcs["org"])
	assert.Equal(t, vcs["repo"], "my-repo", "expected repo=my-repo, got: %v", vcs["repo"])
}

func TestInitSkipAllWritesOnlyVCS(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate", "--skip-circleci",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	configPath := filepath.Join(workDir, ".chunk", "config.json")
	data, err := os.ReadFile(configPath)
	assert.NilError(t, err, "expected .chunk/config.json to exist")

	var cfg map[string]interface{}
	assert.NilError(t, json.Unmarshal(data, &cfg))

	vcs, ok := cfg["vcs"].(map[string]interface{})
	assert.Assert(t, ok, "expected vcs section, got: %s", string(data))
	assert.Equal(t, vcs["org"], "test-org")
	assert.Equal(t, vcs["repo"], "test-repo")

	_, hasCommands := cfg["commands"]
	assert.Assert(t, !hasCommands || cfg["commands"] == nil ||
		len(cfg["commands"].([]interface{})) == 0,
		"expected no commands with --skip-validate, got: %s", string(data))
}

func TestInitExistingConfigNoForce(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo install", "echo test")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate", "--skip-circleci",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "expected clean exit when config exists without --force\nstdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "already exists") || strings.Contains(combined, "--force"),
		"expected existing config message, got: %s", combined)
}

func TestInitExistingConfigWithForce(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo install", "echo test")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{
		"init", "--force", "--skip-hooks", "--skip-validate", "--skip-circleci",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

func TestInitNotGitRepo(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"init", "--skip-hooks", "--skip-validate", "--skip-circleci",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "git"),
		"expected git repo error, got: %s", combined)
}
