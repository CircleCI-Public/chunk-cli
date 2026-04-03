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
	assert.Assert(t, strings.Contains(combined, "no validate commands") || strings.Contains(combined, "chunk init"),
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

// --- Named command execution ---

func TestValidateRunNamed(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo installed", "echo tested")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "test",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "echo tested"),
		"expected test command in output, got: %s", combined)
	// Should not run install
	assert.Assert(t, !strings.Contains(combined, "echo installed"),
		"should not run install when running named test command, got: %s", combined)
}

func TestValidateRunNamedNotConfiguredNonTTY(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "", "echo test")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "nonexistent",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code for unknown command")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "not configured"),
		"expected 'not configured' error, got: %s", combined)
}

// --- Inline command (--cmd) ---

func TestValidateInlineCmd(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "--cmd", "echo inline-output",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "inline-output"),
		"expected inline command output, got: %s", combined)
}

func TestValidateInlineCmdDryRun(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "--cmd", "echo should-not-run", "--dry-run",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "echo should-not-run"),
		"expected command in dry-run output, got: %s", combined)
}

func TestValidateInlineCmdSave(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	// Create .chunk dir so config can be saved
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "lint", "--cmd", "echo linting", "--save",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// Verify command was saved to config
	data, err := os.ReadFile(filepath.Join(chunkDir, "config.json"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "lint"),
		"expected 'lint' in saved config, got: %s", string(data))
	assert.Assert(t, strings.Contains(string(data), "echo linting"),
		"expected command in saved config, got: %s", string(data))
}

// --- --list ---

func TestValidateList(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "echo install", "echo test")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "--list",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "install"),
		"expected 'install' in list output, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "test"),
		"expected 'test' in list output, got: %s", combined)
}

func TestValidateListNoConfig(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "--list",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "No commands configured"),
		"expected 'No commands configured' message, got: %s", combined)
}

// --- --status ---

func TestValidateStatus(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "", "echo test")

	env := testenv.NewTestEnv(t)

	// No prior run, so no cached result
	result := binary.RunCLI(t, []string{
		"validate", "--status",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "no cached result"),
		"expected 'no cached result' for fresh repo, got: %s", combined)
}

func TestValidateStatusAfterRun(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeProjectConfig(t, workDir, "", "echo test")

	env := testenv.NewTestEnv(t)

	// Run first to populate cache
	result := binary.RunCLI(t, []string{"validate"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "initial run stderr: %s", result.Stderr)

	// Now check status
	result = binary.RunCLI(t, []string{
		"validate", "--status",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "status stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "PASS"),
		"expected cached PASS status, got: %s", combined)
}

// --- Caching ---

func TestValidateCacheHitSkipsExecution(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	// Use a marker file to detect whether the command actually ran
	marker := filepath.Join(workDir, "marker.txt")
	writeProjectConfig(t, workDir, "", "echo ran >> "+marker)

	env := testenv.NewTestEnv(t)

	// First run: command executes
	result := binary.RunCLI(t, []string{"validate"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "first run stderr: %s", result.Stderr)

	data, err := os.ReadFile(marker)
	assert.NilError(t, err)
	assert.Equal(t, strings.Count(string(data), "ran"), 1,
		"expected command to run once, got: %s", string(data))

	// Second run: should be cached, command does not execute again
	result = binary.RunCLI(t, []string{"validate"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "second run stderr: %s", result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "cached"),
		"expected 'cached' in output for second run, got: %s", combined)

	// Marker file should still have only one "ran"
	data, err = os.ReadFile(marker)
	assert.NilError(t, err)
	assert.Equal(t, strings.Count(string(data), "ran"), 1,
		"expected command to still have run only once, got: %s", string(data))
}

func TestValidateCacheInvalidationOnGitChange(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	marker := filepath.Join(workDir, "marker.txt")
	writeProjectConfig(t, workDir, "", "echo ran >> "+marker)

	env := testenv.NewTestEnv(t)

	// First run: populates cache
	result := binary.RunCLI(t, []string{"validate"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "first run stderr: %s", result.Stderr)

	// Stage a new file so it appears in git diff HEAD
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "new-file.txt"), []byte("change"), 0o644))
	gitrepo.AddFile(t, workDir, "new-file.txt")

	// Second run: cache should be invalidated due to git diff change
	result = binary.RunCLI(t, []string{"validate"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "second run stderr: %s", result.Stderr)

	// Marker should show the command ran twice
	data, err := os.ReadFile(marker)
	assert.NilError(t, err)
	assert.Equal(t, strings.Count(string(data), "ran"), 2,
		"expected command to run twice after git change, got: %s", string(data))
}

// --- --force-run ---

func TestValidateForceRunBypassesCache(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	marker := filepath.Join(workDir, "marker.txt")
	writeProjectConfig(t, workDir, "", "echo ran >> "+marker)

	env := testenv.NewTestEnv(t)

	// First run: populates cache
	result := binary.RunCLI(t, []string{"validate"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "first run stderr: %s", result.Stderr)

	// Second run with --force-run: should bypass cache
	result = binary.RunCLI(t, []string{"validate", "--force-run"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "force run stderr: %s", result.Stderr)

	// Marker should show the command ran twice
	data, err := os.ReadFile(marker)
	assert.NilError(t, err)
	assert.Equal(t, strings.Count(string(data), "ran"), 2,
		"expected command to run twice with --force-run, got: %s", string(data))
}

// --- fileExt scoping ---

func TestValidateCacheFileExtScoping(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	marker := filepath.Join(workDir, "marker.txt")

	// Write config with fileExt scoping
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	type extCmd struct {
		Name    string `json:"name"`
		Run     string `json:"run"`
		FileExt string `json:"fileExt"`
	}
	configData, err := json.Marshal(map[string]interface{}{
		"commands": []extCmd{{Name: "gotest", Run: "echo ran >> " + marker, FileExt: ".go"}},
	})
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), configData, 0o644))

	env := testenv.NewTestEnv(t)

	// First run
	result := binary.RunCLI(t, []string{"validate"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "first run stderr: %s", result.Stderr)

	// Stage a non-.go file: cache should still be valid (fileExt scopes to .go)
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "readme.txt"), []byte("text change"), 0o644))
	gitrepo.AddFile(t, workDir, "readme.txt")

	result = binary.RunCLI(t, []string{"validate"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "second run stderr: %s", result.Stderr)

	data, err := os.ReadFile(marker)
	assert.NilError(t, err)
	assert.Equal(t, strings.Count(string(data), "ran"), 1,
		"expected command to run only once (non-.go change should not invalidate), got: %s", string(data))

	// Stage a .go file: cache should be invalidated
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0o644))
	gitrepo.AddFile(t, workDir, "main.go")

	result = binary.RunCLI(t, []string{"validate"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "third run stderr: %s", result.Stderr)

	data, err = os.ReadFile(marker)
	assert.NilError(t, err)
	assert.Equal(t, strings.Count(string(data), "ran"), 2,
		"expected command to run twice after .go change, got: %s", string(data))
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
