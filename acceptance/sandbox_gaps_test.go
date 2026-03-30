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

// --- exec error paths ---

func TestSandboxExecMissingCommand(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sandbox", "exec",
		"--sandbox-id", "sb-111",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for missing --command")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "command"),
		"expected error about missing --command flag, got: %s", combined)
}

func TestSandboxExecMissingSandboxID(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sandbox", "exec",
		"--command", "ls",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for missing --sandbox-id")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sandbox-id"),
		"expected error about missing --sandbox-id, got: %s", combined)
}

func TestSandboxExecStderrOutput(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{
		CommandID: "cmd-001",
		PID:       1,
		Stdout:    "",
		Stderr:    "something went wrong\n",
		ExitCode:  0,
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "exec",
		"--sandbox-id", "sb-111",
		"--command", "fail-cmd",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "exit code should be 0")
	assert.Assert(t, strings.Contains(result.Stderr, "something went wrong"),
		"expected stderr output, got: %s", result.Stderr)
}

func TestSandboxExecArgsInRequestBody(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{
		CommandID: "cmd-002",
		PID:       2,
		Stdout:    "ok\n",
		ExitCode:  0,
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "exec",
		"--sandbox-id", "sb-111",
		"--command", "grep",
		"--args", "-r", "--args", "pattern",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	execReqs := filterByPath(reqs, "/api/v2/sandbox/instances/sb-111/exec")
	assert.Equal(t, len(execReqs), 1)

	var body map[string]interface{}
	err := json.Unmarshal(execReqs[0].Body, &body)
	assert.NilError(t, err)

	args, ok := body["args"].([]interface{})
	assert.Assert(t, ok, "expected args array in request body, got: %v", body["args"])
	assert.Equal(t, len(args), 2, "expected 2 args")
	assert.Equal(t, args[0], "-r")
	assert.Equal(t, args[1], "pattern")
}

func TestSandboxExecAPIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "exec",
		"--sandbox-id", "sb-111",
		"--command", "ls",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 500 response")
}

// --- build error paths ---

func TestSandboxBuildMissingDockerfile(t *testing.T) {
	dir := t.TempDir()

	env := testenv.NewTestEnv(t)
	result := binary.RunCLI(t, []string{
		"sandbox", "build",
		"--dir", dir,
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit when Dockerfile.test missing")
}

func TestSandboxBuildInvalidTag(t *testing.T) {
	env := testenv.NewTestEnv(t)
	result := binary.RunCLI(t, []string{
		"sandbox", "build",
		"--tag", "!!!invalid",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for invalid tag")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "invalid docker tag"),
		"expected invalid tag error, got: %s", combined)
}

func TestSandboxBuildNonexistentDir(t *testing.T) {
	env := testenv.NewTestEnv(t)
	result := binary.RunCLI(t, []string{
		"sandbox", "build",
		"--dir", "/tmp/nonexistent-dir-abc123",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for nonexistent dir")
}

// --- env error paths ---

func TestSandboxEnvEmptyDir(t *testing.T) {
	dir := t.TempDir()

	env := testenv.NewTestEnv(t)
	result := binary.RunCLI(t, []string{
		"sandbox", "env",
		"--dir", dir,
	}, env, env.HomeDir)

	// Empty dir should still succeed (unknown stack) and produce JSON + Dockerfile.test
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// Verify JSON output on stdout
	var envOutput map[string]interface{}
	err := json.Unmarshal([]byte(result.Stdout), &envOutput)
	assert.NilError(t, err, "expected valid JSON on stdout, got: %s", result.Stdout)

	// Verify Dockerfile.test was written
	_, err = os.Stat(filepath.Join(dir, "Dockerfile.test"))
	assert.NilError(t, err, "expected Dockerfile.test to be written")
}

func TestSandboxEnvNonexistentDir(t *testing.T) {
	env := testenv.NewTestEnv(t)
	result := binary.RunCLI(t, []string{
		"sandbox", "env",
		"--dir", "/tmp/nonexistent-dir-xyz789",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for nonexistent dir")
}

// --- create error paths ---

func TestSandboxCreateOrgIDFromConfig(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeChunkConfig(t, workDir, "org-from-config")

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	// No --org-id flag; should read from config
	result := binary.RunCLI(t, []string{
		"sandbox", "create",
		"--name", "config-sandbox",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// Verify org_id in request body came from config
	reqs := cci.Recorder.AllRequests()
	createReqs := filterByMethod(reqs, "POST", "/api/v2/sandbox/instances")
	assert.Equal(t, len(createReqs), 1)

	var body map[string]interface{}
	err := json.Unmarshal(createReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["organization_id"], "org-from-config")
}

func TestSandboxCreateNoOrgIDNoConfig(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sandbox", "create",
		"--name", "orphan-sandbox",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit without org-id")
	combined := result.Stdout + result.Stderr
	assert.Assert(t,
		strings.Contains(combined, "--org-id") || strings.Contains(combined, "chunk init"),
		"expected helpful error, got: %s", combined)
}

func TestSandboxCreateAPIError500(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.CreateStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "create",
		"--org-id", "org-aaa",
		"--name", "fail-sandbox",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 500 response")
}

func TestSandboxCreateAPIError403(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.CreateStatusCode = 403
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "create",
		"--org-id", "org-aaa",
		"--name", "forbidden-sandbox",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 403 response")
}

// --- list error paths ---

func TestSandboxListAPIError500(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ListStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "list",
		"--org-id", "org-aaa",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 500 response")
}

func TestSandboxListAPIError404(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ListStatusCode = 404
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "list",
		"--org-id", "org-aaa",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 404 response")
}

// --- add-ssh-key error paths ---

func TestSandboxAddSSHKeyMissingToken(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"sandbox", "add-ssh-key",
		"--sandbox-id", "sb-111",
		"--public-key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKey test@test",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit without token")
}

func TestSandboxAddSSHKeyNonexistentFile(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "add-ssh-key",
		"--sandbox-id", "sb-111",
		"--public-key-file", "/tmp/nonexistent-key-file-abc.pub",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for nonexistent key file")
}

func TestSandboxAddSSHKeyAPIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "add-ssh-key",
		"--sandbox-id", "sb-111",
		"--public-key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKey test@test",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 500 response")
}

// --- ssh / sync error paths ---

func TestSandboxSSHMissingSandboxID(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sandbox", "ssh",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for missing --sandbox-id")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sandbox-id"),
		"expected error about missing --sandbox-id, got: %s", combined)
}

func TestSandboxSyncMissingSandboxID(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sandbox", "sync",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for missing --sandbox-id")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sandbox-id"),
		"expected error about missing --sandbox-id, got: %s", combined)
}
