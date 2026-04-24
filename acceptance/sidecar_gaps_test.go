package acceptance

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// --- exec error paths ---

func TestSidecarExecMissingCommand(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sidecar", "exec",
		"--sidecar-id", "sb-111",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for missing --command")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "command"),
		"expected error about missing --command flag, got: %s", combined)
}

func TestSidecarExecMissingSidecarID(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sidecar", "exec",
		"--command", "ls",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for missing --sidecar-id")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sidecar-id"),
		"expected error about missing --sidecar-id, got: %s", combined)
}

func TestSidecarExecStderrOutput(t *testing.T) {
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
		"sidecar", "exec",
		"--sidecar-id", "sb-111",
		"--command", "fail-cmd",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "exit code should be 0")
	assert.Assert(t, strings.Contains(result.Stderr, "something went wrong"),
		"expected stderr output, got: %s", result.Stderr)
}

func TestSidecarExecArgsInRequestBody(t *testing.T) {
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
		"sidecar", "exec",
		"--sidecar-id", "sb-111",
		"--command", "grep",
		"--args", "-r", "--args", "pattern",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	execReqs := filterByPath(reqs, "/api/v2/sidecar/instances/sb-111/exec")
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

func TestSidecarExecAPIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "exec",
		"--sidecar-id", "sb-111",
		"--command", "ls",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 500 response")
}

// --- build error paths ---

func TestSidecarBuildMissingDockerfile(t *testing.T) {
	dir := t.TempDir()

	env := testenv.NewTestEnv(t)
	result := binary.RunCLI(t, []string{
		"sidecar", "build",
		"--dir", dir,
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit when Dockerfile.test missing")
}

func TestSidecarBuildInvalidTag(t *testing.T) {
	env := testenv.NewTestEnv(t)
	result := binary.RunCLI(t, []string{
		"sidecar", "build",
		"--tag", "!!!invalid",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for invalid tag")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Invalid image tag"),
		"expected invalid tag error, got: %s", combined)
}

func TestSidecarBuildNonexistentDir(t *testing.T) {
	env := testenv.NewTestEnv(t)
	result := binary.RunCLI(t, []string{
		"sidecar", "build",
		"--dir", "/tmp/nonexistent-dir-abc123",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for nonexistent dir")
}

// --- env error paths ---

func TestSidecarEnvEmptyDir(t *testing.T) {
	dir := t.TempDir()

	env := testenv.NewTestEnv(t)
	result := binary.RunCLI(t, []string{
		"sidecar", "env",
		"--dir", dir,
	}, env, env.HomeDir)

	// Empty dir should still succeed (unknown stack) and produce JSON on stdout.
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// Verify JSON output on stdout
	var envOutput map[string]interface{}
	err := json.Unmarshal([]byte(result.Stdout), &envOutput)
	assert.NilError(t, err, "expected valid JSON on stdout, got: %s", result.Stdout)
}

func TestSidecarEnvNonexistentDir(t *testing.T) {
	env := testenv.NewTestEnv(t)
	result := binary.RunCLI(t, []string{
		"sidecar", "env",
		"--dir", "/tmp/nonexistent-dir-xyz789",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for nonexistent dir")
}

// --- create error paths ---

func TestSidecarCreateOrgIDFromConfig(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.Extra["CIRCLECI_ORG_ID"] = "org-from-config"

	// No --org-id flag; should read from CIRCLECI_ORG_ID
	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--name", "config-sidecar",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// Verify org_id in request body came from config
	reqs := cci.Recorder.AllRequests()
	createReqs := filterByMethod(reqs, "POST", "/api/v2/sidecar/instances")
	assert.Equal(t, len(createReqs), 1)

	var body map[string]interface{}
	err := json.Unmarshal(createReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["org_id"], "org-from-config")
}

func TestSidecarCreateNoOrgIDNoConfig(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--name", "orphan-sidecar",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit without org-id")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "--org-id"),
		"expected helpful error, got: %s", combined)
}

func TestSidecarCreateAPIError500(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.CreateStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--org-id", "org-aaa",
		"--name", "fail-sidecar",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 500 response")
}

func TestSidecarCreateAPIError403(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.CreateStatusCode = 403
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--org-id", "org-aaa",
		"--name", "forbidden-sidecar",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 403 response")
}

// --- create org picker paths ---

func TestSidecarCreateCollaborationsAPIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.CollaborationsStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--name", "my-sidecar",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "--org-id"),
		"expected org-id error, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "Could not list organizations"),
		"expected collaborations error detail, got: %s", combined)
}

func TestSidecarCreateNoCollaborationsAvailable(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--name", "my-sidecar",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "--org-id"),
		"expected org-id error, got: %s", combined)

	reqs := cci.Recorder.AllRequests()
	collabReqs := filterByMethod(reqs, "GET", "/api/v2/me/collaborations")
	assert.Equal(t, len(collabReqs), 1, "expected collaborations endpoint to be called")
}

func TestSidecarCreateOrgPickerCalledWhenCollaborationsExist(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Collaborations = []fakes.Collaboration{
		{ID: "org-111", Name: "my-org", VCSType: "github"},
		{ID: "org-222", Name: "other-org", VCSType: "bitbucket"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--name", "my-sidecar",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0)

	reqs := cci.Recorder.AllRequests()
	collabReqs := filterByMethod(reqs, "GET", "/api/v2/me/collaborations")
	assert.Equal(t, len(collabReqs), 1, "expected collaborations endpoint to be called")
}

// --- list error paths ---

func TestSidecarListAPIError500(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ListStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "list",
		"--org-id", "org-aaa",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 500 response")
}

func TestSidecarListAPIError404(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ListStatusCode = 404
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "list",
		"--org-id", "org-aaa",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 404 response")
}

// --- add-ssh-key error paths ---

func TestSidecarAddSSHKeyMissingToken(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.CircleToken = ""

	result := binary.RunCLI(t, []string{
		"sidecar", "add-ssh-key",
		"--sidecar-id", "sb-111",
		"--public-key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKey test@test",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit without token")
}

func TestSidecarAddSSHKeyNonexistentFile(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "add-ssh-key",
		"--sidecar-id", "sb-111",
		"--public-key-file", "/tmp/nonexistent-key-file-abc.pub",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for nonexistent key file")
}

func TestSidecarAddSSHKeyAPIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "add-ssh-key",
		"--sidecar-id", "sb-111",
		"--public-key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKey test@test",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 500 response")
}

// --- ssh / sync error paths ---

func TestSidecarSSHMissingSidecarID(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sidecar", "ssh",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for missing --sidecar-id")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sidecar-id"),
		"expected error about missing --sidecar-id, got: %s", combined)
}

func TestSidecarSyncMissingSidecarID(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sidecar", "sync",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for missing --sidecar-id")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sidecar-id"),
		"expected error about missing --sidecar-id, got: %s", combined)
}
