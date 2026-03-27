package acceptance

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
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

// generateTestSSHKey writes an ed25519 keypair to identityFile and identityFile+".pub".
func generateTestSSHKey(t *testing.T, identityFile string) error {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privPEM, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return err
	}
	if err := os.WriteFile(identityFile, pem.EncodeToMemory(privPEM), 0o600); err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	return os.WriteFile(identityFile+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o644)
}

func TestValidateRunRemoteUsesSSH(t *testing.T) {
	// Verify that validate --sandbox-id uses the SSH path (AddSSHKey) rather than HTTP exec.
	// We can't complete the SSH handshake in this test, but we verify the code reaches
	// OpenSession (i.e. calls AddSSHKey) and never calls the HTTP exec endpoint.
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = "127.0.0.1" // will fail SSH handshake — no server at port 2222
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo install", "echo test")

	// Write a temporary SSH keypair so OpenSession can register a key.
	sshDir := filepath.Join(t.TempDir(), ".ssh")
	assert.NilError(t, os.MkdirAll(sshDir, 0o700))
	identityFile := filepath.Join(sshDir, "chunk_ai")
	assert.NilError(t, generateTestSSHKey(t, identityFile))

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"validate",
		"--sandbox-id", "sandbox-123",
		"--identity-file", identityFile,
	}, env, workDir)

	// SSH connection to 127.0.0.1:2222 will fail — that's expected.
	assert.Assert(t, result.ExitCode != 0, "expected failure because no SSH server is running")

	reqs := cci.Recorder.AllRequests()

	// AddSSHKey must be called — proves SSH path was taken.
	addKeyReqs := filterByPath(reqs, "/api/v2/sandbox/instances/sandbox-123/ssh/add-key")
	assert.Equal(t, len(addKeyReqs), 1, "expected 1 add-key request; got: %v", reqs)

	// HTTP exec must NOT be called — SSH is used instead.
	execReqs := filterByPath(reqs, "/api/v2/sandbox/instances/sandbox-123/exec")
	assert.Equal(t, len(execReqs), 0, "expected 0 HTTP exec requests (SSH should be used)")
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
