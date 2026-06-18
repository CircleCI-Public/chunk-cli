package acceptance

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
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

// commitAll stages and commits all files in dir.
func commitAll(t *testing.T, dir, message string) {
	t.Helper()
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", message},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = gitrepo.GitEnv(dir)
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, "%v: %s", args, out)
	}
}

// TestValidateHookMode_DirtyTree verifies that piping a hook payload triggers
// hook mode and re-signals the agent (exit 2) when commands fail.
func TestValidateHookMode_DirtyTree(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	// writeProjectConfig creates an untracked file → dirty working tree.
	writeProjectConfig(t, workDir, "", "false")

	env := testenv.NewTestEnv(t)
	result := binary.RunCLIWithStdin(t, []string{"validate"}, env, workDir,
		hookStdin(t, "test-session-dirty", false))

	assert.Equal(t, result.ExitCode, 2,
		"expected exit 2 (hook re-signal) for dirty tree with failing command; stderr: %s", result.Stderr)
}

// TestValidateHookMode_CleanTree verifies that piping a hook payload exits 0
// without running any commands when the working tree is clean.
func TestValidateHookMode_CleanTree(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	// Write config then commit it so the tree is clean.
	writeProjectConfig(t, workDir, "", "false") // deliberately failing command
	commitAll(t, workDir, "add config")

	env := testenv.NewTestEnv(t)
	result := binary.RunCLIWithStdin(t, []string{"validate"}, env, workDir,
		hookStdin(t, "test-session-clean", false))

	assert.Equal(t, result.ExitCode, 0,
		"expected exit 0 (skipped) for clean tree; stderr: %s", result.Stderr)
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
	addKeyReqs := filterByPath(reqs, "/api/v2/sidecar/instances/sidecar-123/ssh/add-key")
	assert.Equal(t, len(addKeyReqs), 1, "expected 1 add-key request; got: %v", reqs)

	// HTTP exec must NOT be called — SSH is used instead.
	execReqs := filterByPath(reqs, "/api/v2/sidecar/instances/sidecar-123/exec")
	assert.Equal(t, len(execReqs), 0, "expected 0 HTTP exec requests (SSH should be used)")
}

func TestValidateSidecarImageNoActiveSidecarRunsLocally(t *testing.T) {
	// Plain chunk validate with sidecarImage configured but no active sidecar must
	// NOT auto-create a sandbox — it runs commands locally instead. Auto-create is
	// reserved for the Stop hook path (TestValidateHookAutoCreatesSidecarFromSidecarImage).
	cci := fakes.NewFakeCircleCI()
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

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.Extra["CIRCLECI_ORG_ID"] = "org-aaa"

	result := binary.RunCLI(t, []string{"validate"}, env, workDir)

	// Command runs locally (echo test-output) — must succeed.
	assert.Equal(t, result.ExitCode, 0, "expected local fallback to succeed, stderr: %s", result.Stderr)

	// No sidecar must have been created.
	reqs := cci.Recorder.AllRequests()
	createReqs := filterByPath(reqs, "/api/v2/sidecar/instances")
	assert.Equal(t, len(createReqs), 0, "expected no create-sidecar request when no active sidecar exists; got: %v", reqs)
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
	createReqs := filterByPath(reqs, "/api/v2/sidecar/instances")
	assert.Equal(t, len(createReqs), 1, "expected 1 create-sidecar request; got: %v", reqs)

	var body map[string]interface{}
	assert.NilError(t, json.Unmarshal(createReqs[0].Body, &body))
	assert.Equal(t, body["image"], "my-snapshot-abc123")
	assert.Equal(t, body["org_id"], "org-aaa")

	addKeyReqs := filterByPath(reqs, "/api/v2/sidecar/instances/sidecar-new-123/ssh/add-key")
	assert.Equal(t, len(addKeyReqs), 1, "expected 1 add-key request for newly created sidecar; got: %v", reqs)
}

// writeRemoteProjectConfig writes a config with a single remote command.
func writeRemoteProjectConfig(t *testing.T, workDir string) {
	t.Helper()
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	cfg := `{"commands":[{"name":"test","run":"true","remote":true}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(cfg), 0o644))
}

// writeSidecarState writes a session-keyed sidecar state file into the test
// environment's XDG data directory for the given project root.
func writeSidecarState(t *testing.T, e *testenv.TestEnv, projectRoot, sessionID, sidecarID string) {
	t.Helper()
	// Resolve symlinks so the hash matches what os.Getwd() returns in the subprocess.
	// On macOS, t.TempDir() returns /var/folders/... but os.Getwd() resolves to /private/var/...
	realRoot, err := filepath.EvalSymlinks(projectRoot)
	assert.NilError(t, err)
	// Compute the data dir directly from e.HomeDir so we don't touch the parent process env.
	// This mirrors config.ProjectDataDir: <XDG_DATA_HOME>/chunk/<sha256(root)>
	sum := sha256.Sum256([]byte(filepath.Clean(realRoot)))
	dir := filepath.Join(e.HomeDir, ".local", "share", "chunk", fmt.Sprintf("%x", sum))
	assert.NilError(t, os.MkdirAll(dir, 0o755))
	// Detect the branch so the file name matches what the subprocess will look for.
	branch := gitCurrentBranch(t, projectRoot)
	filename := sidecar.StateFileName(sessionID, branch)
	data := []byte(`{"sidecar_id":"` + sidecarID + `"}`)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, filename), data, 0o644))
}

// gitCurrentBranch returns the current branch of the git repo at dir, or ""
// on any error.
func gitCurrentBranch(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b == "HEAD" {
		return ""
	}
	return b
}

// TestValidateHookMode_SessionIsolation verifies that two concurrent Claude
// sessions each see their own sidecar state rather than sharing one file.
func TestValidateHookMode_SessionIsolation(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeRemoteProjectConfig(t, workDir)
	// Add an untracked file so the working tree is dirty and validate runs.
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "dirty.txt"), []byte("x"), 0o644))

	envA := testenv.NewTestEnv(t)
	envB := testenv.NewTestEnv(t)

	writeSidecarState(t, envA, workDir, "sess-a", "sidecar-aaa")
	writeSidecarState(t, envB, workDir, "sess-b", "sidecar-bbb")

	resultA := binary.RunCLIWithStdin(t, []string{"validate"}, envA, workDir,
		hookStdin(t, "sess-a", true))
	resultB := binary.RunCLIWithStdin(t, []string{"validate"}, envB, workDir,
		hookStdin(t, "sess-b", true))

	assert.Assert(t, strings.Contains(resultA.Stderr, "sidecar-aaa"),
		"session A should load sidecar-aaa; stderr: %s", resultA.Stderr)
	assert.Assert(t, !strings.Contains(resultA.Stderr, "sidecar-bbb"),
		"session A should not see sidecar-bbb; stderr: %s", resultA.Stderr)

	assert.Assert(t, strings.Contains(resultB.Stderr, "sidecar-bbb"),
		"session B should load sidecar-bbb; stderr: %s", resultB.Stderr)
	assert.Assert(t, !strings.Contains(resultB.Stderr, "sidecar-aaa"),
		"session B should not see sidecar-aaa; stderr: %s", resultB.Stderr)
}
