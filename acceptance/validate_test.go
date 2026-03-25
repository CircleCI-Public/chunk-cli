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

func writeProjectConfig(t *testing.T, workDir string, installCmd, testCmd string) {
	t.Helper()
	chunkDir := filepath.Join(workDir, ".chunk")
	err := os.MkdirAll(chunkDir, 0o755)
	assert.NilError(t, err)

	type command struct {
		Name string `json:"name"`
		Run  string `json:"run"`
	}
	var commands []command
	if installCmd != "" {
		commands = append(commands, command{Name: "install", Run: installCmd})
	}
	if testCmd != "" {
		commands = append(commands, command{Name: "test", Run: testCmd})
	}

	config := map[string]interface{}{"commands": commands}
	data, err := json.Marshal(config)
	assert.NilError(t, err)
	err = os.WriteFile(filepath.Join(chunkDir, "config.json"), data, 0o644)
	assert.NilError(t, err)
}

func TestValidateRunDryRun(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo install", "echo test")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "--dry-run",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "echo install"),
		"expected install command in dry-run output, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "echo test"),
		"expected test command in dry-run output, got: %s", combined)
}

func TestValidateRunDryRunTestOnly(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "", "echo test-only")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "--dry-run",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "echo test-only"),
		"expected test command, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "install"),
		"should not contain install command, got: %s", combined)
}

func TestValidateRunDryRunNoConfig(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	// No .chunk/config.json

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "--dry-run",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "No validate commands") || strings.Contains(combined, "validate init"),
		"expected no-commands-configured error, got: %s", combined)
}

func TestValidateRunLocal(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo installed", "echo tested")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "echo installed"),
		"expected install command output, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "echo tested"),
		"expected test command output, got: %s", combined)
}

func TestValidateRunLocalFailure(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "true", "false") // false exits non-zero

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code for failing test command")
}

func TestValidateRunLocalSkipsAfterFailure(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	// install fails, so test should be skipped
	writeProjectConfig(t, workDir, "false", "echo should-not-run")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "skipped"),
		"expected skipped indicator for test command, got: %s", combined)
}

func TestValidateRunRemoteMissingOrgId(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo install", "echo test")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "--sandbox-id", "sandbox-123",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "org-id") || strings.Contains(combined, "org_id"),
		"expected missing org-id error, got: %s", combined)
}

func TestValidateRunRemote(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{
		CommandID: "cmd-456",
		PID:       100,
		Stdout:    "remote output\n",
		Stderr:    "",
		ExitCode:  0,
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo install", "echo test")

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"validate",
		"--sandbox-id", "sandbox-123",
		"--org-id", "org-456",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// Verify exec was called (2 commands: install + test)
	reqs := cci.Recorder.AllRequests()
	execReqs := filterByPath(reqs, "/api/v2/sandbox/instances/sandbox-123/exec")
	assert.Equal(t, len(execReqs), 2, "expected 2 exec requests (install + test)")

	// Verify Circle-Token auth on exec requests
	for _, req := range execReqs {
		assert.Assert(t, req.Header.Get("Circle-Token") != "",
			"expected Circle-Token auth on exec request")
	}
}

func TestValidateInitNotGitRepo(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "init",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "git"),
		"expected git repo error, got: %s", combined)
}

func TestValidateInitInvalidProfile(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "init", "--profile", "bogus",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Invalid profile") || strings.Contains(combined, "Valid profiles"),
		"expected invalid profile error, got: %s", combined)
}

func TestValidateInitExistingConfigNoForce(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo install", "echo test")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "init", "--skip-env",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "already exists") || strings.Contains(combined, "--force"),
		"expected existing config message, got: %s", combined)
}

func TestValidateInitHappyPath(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	anthropic := fakes.NewFakeAnthropic("bun test")
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
		"validate", "init", "--skip-env",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// Verify anthropic-version header was sent
	anthropicReqs := anthropic.Recorder.AllRequests()
	for _, req := range anthropicReqs {
		assert.Equal(t, req.Header.Get("Anthropic-Version"), "2023-06-01",
			"expected anthropic-version header on Anthropic request to %s", req.URL.Path)
	}

	// Verify config.json was created
	configPath := filepath.Join(workDir, ".chunk", "config.json")
	data, err := os.ReadFile(configPath)
	assert.NilError(t, err, "expected .chunk/config.json to exist")
	assert.Assert(t, strings.Contains(string(data), `"name":"test"`) || strings.Contains(string(data), `"name": "test"`),
		"expected test command entry in config, got: %s", string(data))

	// Verify hook config was created
	hookConfigPath := filepath.Join(workDir, ".chunk", "hook", "config.yml")
	_, err = os.Stat(hookConfigPath)
	assert.NilError(t, err, "expected .chunk/hook/config.yml to exist")
}

func TestValidateInitForce(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo old-install", "echo old-test")

	anthropic := fakes.NewFakeAnthropic("npm test")
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	env := testenv.NewTestEnv(t)
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
		"validate", "init", "--force", "--skip-env",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// Verify config was overwritten with the detected test command
	configPath := filepath.Join(workDir, ".chunk", "config.json")
	data, err := os.ReadFile(configPath)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "npm test"),
		"expected overwritten test command in config, got: %s", string(data))
}

func TestValidateInitMissingApiKey(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{
		"validate", "init", "--skip-env",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "API key") || strings.Contains(combined, "ANTHROPIC_API_KEY"),
		"expected API key error, got: %s", combined)
}
