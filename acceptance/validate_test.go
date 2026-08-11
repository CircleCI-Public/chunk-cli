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

// hookPayload mirrors the Claude Code Stop hook JSON fields.
type hookPayload struct {
	SessionID      string `json:"session_id"`
	StopHookActive bool   `json:"stop_hook_active"`
}

func hookStdin(t *testing.T, sessionID string, stopHookActive bool) []byte {
	t.Helper()
	data, err := json.Marshal(hookPayload{SessionID: sessionID, StopHookActive: stopHookActive})
	assert.NilError(t, err)
	return data
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
	assert.Assert(t, strings.Contains(combined, "installed"),
		"expected install command output in result, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "tested"),
		"expected test command output in result, got: %s", combined)
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
	assert.Assert(t, strings.Contains(combined, "tested"),
		"expected test command output in result, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "installed"),
		"install command must not run when only 'test' is requested, got: %s", combined)
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
		"expected command output in result, got: %s", combined)
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
func TestValidateRunRemoteUsesSSH(t *testing.T) {
	// Verify that validate --sidecar-id uses the SSH path (AddSSHKey) rather than HTTP exec.
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
		"--sidecar-id", "sidecar-123",
		"--identity-file", identityFile,
	}, env, workDir)

	// SSH connection to 127.0.0.1:2222 will fail — that's expected.
	assert.Assert(t, result.ExitCode != 0, "expected failure because no SSH server is running")

	reqs := cci.Recorder.AllRequests()

	// AddSSHKey must be called — proves SSH path was taken.
	addKeyReqs := filterByPath(reqs, "/api/v3/sidecar/instances/sidecar-123/ssh/add-key")
	assert.Equal(t, len(addKeyReqs), 1, "expected 1 add-key request; got: %v", reqs)

	// HTTP exec must NOT be called — SSH is used instead.
	execReqs := filterByPath(reqs, "/api/v3/sidecar/instances/sidecar-123/exec")
	assert.Equal(t, len(execReqs), 0, "expected 0 HTTP exec requests (SSH should be used)")
}

func TestValidateSidecarImageNoActiveSidecarAutoCreates(t *testing.T) {
	// Plain chunk validate with sidecarImage configured but no active sidecar must
	// auto-create a sandbox — the configured image signals intent to run remotely.
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = "127.0.0.1"
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	cfg := map[string]interface{}{
		"commands": []map[string]interface{}{
			{"name": "test", "run": "echo test-output", "remote": true},
		},
		"validation": map[string]interface{}{
			"sidecarImage": "my-snapshot-abc123",
		},
	}
	data, err := json.Marshal(cfg)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), data, 0o644))

	sshDir := filepath.Join(t.TempDir(), ".ssh")
	assert.NilError(t, os.MkdirAll(sshDir, 0o700))
	identityFile := filepath.Join(sshDir, "chunk_ai")
	assert.NilError(t, generateTestSSHKey(t, identityFile))

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.Extra["CIRCLECI_ORG_ID"] = "org-aaa"

	result := binary.RunCLI(t, []string{"validate", "--identity-file", identityFile}, env, workDir)

	// No real SSH server is running so sync will fail — that's expected.
	assert.Assert(t, result.ExitCode != 0, "expected failure because no SSH server is running")

	reqs := cci.Recorder.AllRequests()

	// A sidecar must have been created with the configured image.
	createReqs := filterByPath(reqs, "/api/v3/sidecar/instances")
	assert.Equal(t, len(createReqs), 1, "expected 1 create-sidecar request; got: %v", reqs)

	var body map[string]any
	assert.NilError(t, json.Unmarshal(createReqs[0].Body, &body))
	envelope, ok := body["data"].(map[string]any)
	assert.Assert(t, ok, "expected data envelope in response body")
	attrs, ok := envelope["attributes"].(map[string]any)
	assert.Assert(t, ok, "expected attributes in data envelope")
	refs, ok := envelope["references"].(map[string]any)
	assert.Assert(t, ok, "expected references in data envelope")
	assert.Equal(t, attrs["image"], "my-snapshot-abc123", "expected sidecar image from config")
	org, ok := refs["org"].(map[string]any)
	assert.Assert(t, ok, "expected org in references")
	assert.Equal(t, org["id"], "org-aaa", "expected org from CIRCLECI_ORG_ID")
}

func TestValidateHookAutoCreatesSidecarFromSidecarImage(t *testing.T) {
	// Stop hook + validation.sidecarImage (no remote: true) should still resolve
	// a sidecar from the configured snapshot and attempt remote validation.
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = "127.0.0.1"
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	cfg := map[string]interface{}{
		"commands": []map[string]interface{}{
			{"name": "install", "run": "echo install"},
			{"name": "test", "run": "echo test", "role": "gate"},
		},
		"validation": map[string]interface{}{
			"sidecarImage": "my-snapshot-abc123",
		},
	}
	data, err := json.Marshal(cfg)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), data, 0o644))

	sshDir := filepath.Join(t.TempDir(), ".ssh")
	assert.NilError(t, os.MkdirAll(sshDir, 0o700))
	identityFile := filepath.Join(sshDir, "chunk_ai")
	assert.NilError(t, generateTestSSHKey(t, identityFile))

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.Extra["CIRCLECI_ORG_ID"] = "org-aaa"

	result := binary.RunCLIWithStdin(t, []string{
		"validate",
		"--identity-file", identityFile,
	}, env, workDir, hookStdin(t, "test-session-sidecar-image", false))

	assert.Assert(t, result.ExitCode != 0, "expected failure because no SSH server is running")

	reqs := cci.Recorder.AllRequests()

	// A sidecar must have been created with the configured image.
	createReqs := filterByPath(reqs, "/api/v3/sidecar/instances")
	assert.Equal(t, len(createReqs), 1, "expected 1 create-sidecar request; got: %v", reqs)

	var body map[string]any
	assert.NilError(t, json.Unmarshal(createReqs[0].Body, &body))
	envelope, ok := body["data"].(map[string]any)
	assert.Assert(t, ok, "expected data envelope in response body")
	attrs, ok := envelope["attributes"].(map[string]any)
	assert.Assert(t, ok, "expected attributes in data envelope")
	refs, ok := envelope["references"].(map[string]any)
	assert.Assert(t, ok, "expected references in data envelope")
	assert.Equal(t, attrs["image"], "my-snapshot-abc123", "expected sidecar image from config")
	org, ok := refs["org"].(map[string]any)
	assert.Assert(t, ok, "expected org in references")
	assert.Equal(t, org["id"], "org-aaa", "expected org from CIRCLECI_ORG_ID")

	// AddSSHKey must be called on the newly created sidecar — proves it was used.
	addKeyReqs := filterByPath(reqs, "/api/v3/sidecar/instances/sidecar-new-123/ssh/add-key")
	assert.Equal(t, len(addKeyReqs), 1, "expected 1 add-key request for newly created sidecar; got: %v", reqs)
}

// TestValidateHookMode_SuccessLine verifies that the "chunk validate passed"
// success line is written to stderr after a clean hook run.
func TestValidateHookMode_SuccessLine(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	// writeProjectConfig leaves an untracked file → dirty tree → hook runs.
	writeProjectConfig(t, workDir, "", "true")

	env := testenv.NewTestEnv(t)
	result := binary.RunCLIWithStdin(t, []string{"validate"}, env, workDir,
		hookStdin(t, "test-session-success-line", false))

	assert.Equal(t, result.ExitCode, 0, "expected exit 0 for passing hook; stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "chunk validate passed"),
		"expected 'chunk validate passed' in stdout; got stdout: %s stderr: %s", result.Stdout, result.Stderr)
}

// TestValidateHookMode_SetupErrorFlushedToStderr verifies that when setup fails
// in hook mode (e.g. SSH unreachable), the error output is flushed to stderr
// rather than silently discarded by hookBuf.
func TestValidateHookMode_SetupErrorFlushedToStderr(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = "127.0.0.1" // no real SSH server — sync will fail
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	cfg := `{"commands":[{"name":"test","run":"true","remote":true}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(cfg), 0o644))
	// Dirty tree ensures the hook actually runs.
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "dirty.txt"), []byte("x"), 0o644))

	sshDir := filepath.Join(t.TempDir(), ".ssh")
	assert.NilError(t, os.MkdirAll(sshDir, 0o700))
	identityFile := filepath.Join(sshDir, "chunk_ai")
	assert.NilError(t, generateTestSSHKey(t, identityFile))

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.Extra["CIRCLECI_ORG_ID"] = "org-aaa"

	result := binary.RunCLIWithStdin(t, []string{
		"validate", "--identity-file", identityFile,
	}, env, workDir, hookStdin(t, "test-session-setup-err", false))

	assert.Assert(t, result.ExitCode != 0, "expected failure; stderr: %s", result.Stderr)
	// Sync status messages must reach stderr — proves setup output is not silently dropped.
	assert.Assert(t, strings.Contains(result.Stderr, "Syncing workspace"),
		"expected sync attempt in stderr; got: %s", result.Stderr)
}
