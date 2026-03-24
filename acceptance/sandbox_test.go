package acceptance

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/acceptance/testutil"
	"github.com/CircleCI-Public/chunk-cli/acceptance/testutil/fakes"
)

func TestSandboxesListHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sandboxes = []fakes.Sandbox{
		{ID: "sb-111", Name: "dev-sandbox", OrganizationID: "org-aaa"},
		{ID: "sb-222", Name: "staging-sandbox", OrganizationID: "org-aaa"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"sandboxes", "list", "--org-id", "org-aaa",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "dev-sandbox"),
		"expected sandbox name in output, got: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "sb-111"),
		"expected sandbox id in output, got: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "staging-sandbox"),
		"expected second sandbox in output, got: %s", result.Stdout)

	// Verify org_id query param was sent
	reqs := cci.Recorder.AllRequests()
	listReqs := filterByPath(reqs, "/api/v2/sandboxes")
	assert.Assert(t, len(listReqs) >= 1, "expected at least 1 list request")
	assert.Equal(t, listReqs[0].URL.Query().Get("org_id"), "org-aaa")
}

func TestSandboxesListEmpty(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"sandboxes", "list", "--org-id", "org-empty",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "No sandboxes") || strings.Contains(combined, "no sandbox"),
		"expected empty message, got: %s", combined)
}

func TestSandboxesListFiltersByOrg(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sandboxes = []fakes.Sandbox{
		{ID: "sb-111", Name: "org-a-box", OrganizationID: "org-a"},
		{ID: "sb-222", Name: "org-b-box", OrganizationID: "org-b"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"sandboxes", "list", "--org-id", "org-a",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "org-a-box"),
		"expected org-a sandbox, got: %s", result.Stdout)
	assert.Assert(t, !strings.Contains(result.Stdout, "org-b-box"),
		"should not contain org-b sandbox, got: %s", result.Stdout)
}

func TestSandboxesMissingToken(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"list", []string{"sandboxes", "list", "--org-id", "org-aaa"}},
		{"create", []string{"sandboxes", "create", "--org-id", "org-aaa", "--name", "my-sandbox"}},
		{"exec", []string{"sandboxes", "exec", "--org-id", "org-aaa", "--sandbox-id", "sb-111", "--command", "ls"}},
		{"ssh", []string{"sandboxes", "ssh", "--org-id", "org-aaa", "--sandbox-id", "sb-111"}},
		{"sync", []string{"sandboxes", "sync", "--org-id", "org-aaa", "--sandbox-id", "sb-111"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := testutil.NewTestEnv(t)
			env.CircleToken = ""

			result := testutil.RunCLI(t, tt.args, env, env.HomeDir)
			assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
		})
	}
}

func TestSandboxesCreateHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"sandboxes", "create",
		"--org-id", "org-aaa",
		"--name", "my-new-sandbox",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "sandbox-new-123"),
		"expected sandbox ID in output, got: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "my-new-sandbox"),
		"expected sandbox name in output, got: %s", result.Stdout)

	// Verify request body
	reqs := cci.Recorder.AllRequests()
	createReqs := filterByMethod(reqs, "POST", "/api/v2/sandboxes")
	assert.Equal(t, len(createReqs), 1, "expected 1 create request")

	var body map[string]interface{}
	err := json.Unmarshal(createReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["organization_id"], "org-aaa")
	assert.Equal(t, body["name"], "my-new-sandbox")
}

func TestSandboxesCreateWithImage(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"sandboxes", "create",
		"--org-id", "org-aaa",
		"--name", "custom-sandbox",
		"--image", "ubuntu:22.04",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	createReqs := filterByMethod(reqs, "POST", "/api/v2/sandboxes")
	assert.Equal(t, len(createReqs), 1)

	var body map[string]interface{}
	err := json.Unmarshal(createReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["image"], "ubuntu:22.04")
}

func TestSandboxesExecHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{
		CommandID: "cmd-789",
		PID:       99,
		Stdout:    "hello world\n",
		Stderr:    "",
		ExitCode:  0,
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"sandboxes", "exec",
		"--org-id", "org-aaa",
		"--sandbox-id", "sb-111",
		"--command", "echo",
		"--args", "hello", "world",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "hello world"),
		"expected command output, got: %s", result.Stdout)

	// Verify access token request was made first
	reqs := cci.Recorder.AllRequests()
	tokenReqs := filterByPath(reqs, "/api/v2/sandboxes/sb-111/access_token")
	assert.Equal(t, len(tokenReqs), 1, "expected 1 access token request")

	// Verify exec request
	execReqs := filterByPath(reqs, "/api/v2/sandboxes/exec")
	assert.Equal(t, len(execReqs), 1, "expected 1 exec request")

	var body map[string]interface{}
	err := json.Unmarshal(execReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["command"], "echo")

	// Verify bearer auth on exec
	assert.Assert(t, strings.HasPrefix(execReqs[0].Header.Get("Authorization"), "Bearer "),
		"expected Bearer auth on exec request")
}

func TestSandboxesAddSshKeyFromString(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = "my-sandbox.dev.example.com"
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"sandboxes", "add-ssh-key",
		"--org-id", "org-aaa",
		"--sandbox-id", "sb-111",
		"--public-key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForTestingPurposesOnly123 test@test",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "my-sandbox.dev.example.com"),
		"expected sandbox domain in output, got: %s", result.Stdout)

	// Verify access token and add-key requests
	reqs := cci.Recorder.AllRequests()
	tokenReqs := filterByPath(reqs, "/api/v2/sandboxes/sb-111/access_token")
	assert.Equal(t, len(tokenReqs), 1, "expected 1 access token request")

	addKeyReqs := filterByPath(reqs, "/api/v2/sandboxes/ssh/add-key")
	assert.Equal(t, len(addKeyReqs), 1, "expected 1 add-key request")

	var body map[string]interface{}
	err := json.Unmarshal(addKeyReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Assert(t, strings.HasPrefix(body["public_key"].(string), "ssh-ed25519"),
		"expected public key in body, got: %v", body["public_key"])
}

func TestSandboxesAddSshKeyFromFile(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	// Write a fake public key file
	keyFile := filepath.Join(env.HomeDir, "test-key.pub")
	err := os.WriteFile(keyFile, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForTestingPurposesOnly123 test@test\n"), 0o644)
	assert.NilError(t, err)

	result := testutil.RunCLI(t, []string{
		"sandboxes", "add-ssh-key",
		"--org-id", "org-aaa",
		"--sandbox-id", "sb-111",
		"--public-key-file", keyFile,
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// Verify the key was sent in the request
	reqs := cci.Recorder.AllRequests()
	addKeyReqs := filterByPath(reqs, "/api/v2/sandboxes/ssh/add-key")
	assert.Equal(t, len(addKeyReqs), 1)

	var body map[string]interface{}
	err = json.Unmarshal(addKeyReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Assert(t, strings.HasPrefix(body["public_key"].(string), "ssh-ed25519"),
		"expected public key from file in body")
}

func TestSandboxesAddSshKeyMutuallyExclusive(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	keyFile := filepath.Join(env.HomeDir, "test-key.pub")
	err := os.WriteFile(keyFile, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKey test@test\n"), 0o644)
	assert.NilError(t, err)

	result := testutil.RunCLI(t, []string{
		"sandboxes", "add-ssh-key",
		"--org-id", "org-aaa",
		"--sandbox-id", "sb-111",
		"--public-key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKey test@test",
		"--public-key-file", keyFile,
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code for mutually exclusive flags")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "mutually exclusive") || strings.Contains(combined, "exclusive"),
		"expected mutually exclusive error, got: %s", combined)
}

func TestSandboxesAddSshKeyNeitherProvided(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"sandboxes", "add-ssh-key",
		"--org-id", "org-aaa",
		"--sandbox-id", "sb-111",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code when no key provided")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "public-key") || strings.Contains(combined, "required"),
		"expected missing key error, got: %s", combined)
}

func TestSandboxesAddSshKeyPrivateKeyRejected(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	// Write a fake private key file (detected by PRIVATE KEY marker)
	keyFile := filepath.Join(env.HomeDir, "private-key.pub")
	err := os.WriteFile(keyFile, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfakedata\n-----END OPENSSH PRIVATE KEY-----\n"), 0o644)
	assert.NilError(t, err)

	result := testutil.RunCLI(t, []string{
		"sandboxes", "add-ssh-key",
		"--org-id", "org-aaa",
		"--sandbox-id", "sb-111",
		"--public-key-file", keyFile,
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected rejection of private key")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(strings.ToLower(combined), "private"),
		"expected private key error, got: %s", combined)
}

func TestSandboxesPrepareNotGitRepo(t *testing.T) {
	env := testutil.NewTestEnv(t)

	result := testutil.RunCLI(t, []string{
		"sandboxes", "prepare",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "git"),
		"expected git repo error, got: %s", combined)
}

func TestSandboxesPrepareDockerSudo(t *testing.T) {
	env := testutil.NewTestEnv(t)

	// --docker-sudo should be accepted as a flag; command fails for other reasons (not a git repo)
	result := testutil.RunCLI(t, []string{
		"sandboxes", "prepare", "--docker-sudo",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	// Should fail because not a git repo, NOT because of unknown flag
	assert.Assert(t, strings.Contains(combined, "git"),
		"expected git repo error (not flag parse error), got: %s", combined)
}

// TestSandboxesSshSyncFlags verifies that SSH/sync flags are accepted and
// code progresses past flag parsing (fails at SSH step, not at parsing).
func TestSandboxesSshSyncFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"ssh identity-file", []string{"sandboxes", "ssh", "--org-id", "org-aaa", "--sandbox-id", "sb-111", "--identity-file", "/tmp/fake-key"}},
		{"sync dest", []string{"sandboxes", "sync", "--org-id", "org-aaa", "--sandbox-id", "sb-111", "--dest", "/custom/path"}},
		{"sync identity-file", []string{"sandboxes", "sync", "--org-id", "org-aaa", "--sandbox-id", "sb-111", "--identity-file", "/tmp/fake-key"}},
		{"sync bootstrap", []string{"sandboxes", "sync", "--org-id", "org-aaa", "--sandbox-id", "sb-111", "--bootstrap"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cci := fakes.NewFakeCircleCI()
			srv := httptest.NewServer(cci)
			defer srv.Close()

			env := testutil.NewTestEnv(t)
			env.CircleCIURL = srv.URL

			result := testutil.RunCLI(t, tt.args, env, env.HomeDir)

			// Verify access token request was made (proves flag was accepted)
			reqs := cci.Recorder.AllRequests()
			tokenReqs := filterByPath(reqs, "/api/v2/sandboxes/sb-111/access_token")
			assert.Equal(t, len(tokenReqs), 1, "expected access token request (flag accepted)")
			assert.Assert(t, result.ExitCode != 0, "expected non-zero exit (SSH fails)")
		})
	}
}

func TestSandboxesPrepareMissingApiKey(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")

	env := testutil.NewTestEnv(t)
	env.AnthropicKey = ""

	result := testutil.RunCLI(t, []string{
		"sandboxes", "prepare",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "ANTHROPIC_API_KEY"),
		"expected API key error, got: %s", combined)
}

func TestSandboxesExecWithArgs(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{
		CommandID: "cmd-789",
		PID:       99,
		Stdout:    "file1.txt\nfile2.txt\n",
		Stderr:    "",
		ExitCode:  0,
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"sandboxes", "exec",
		"--org-id", "org-aaa",
		"--sandbox-id", "sb-111",
		"--command", "ls",
		"--args", "-la", "/tmp",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// Verify exec request body has the command
	reqs := cci.Recorder.AllRequests()
	execReqs := filterByPath(reqs, "/api/v2/sandboxes/exec")
	assert.Equal(t, len(execReqs), 1)

	var body map[string]interface{}
	err := json.Unmarshal(execReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["command"], "ls")
}

func TestSandboxesCreateMissingName(t *testing.T) {
	env := testutil.NewTestEnv(t)

	result := testutil.RunCLI(t, []string{
		"sandboxes", "create",
		"--org-id", "org-aaa",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code for missing --name")
}

// filterByMethod returns requests matching both method and path prefix.
func filterByMethod(reqs []testutil.RecordedRequest, method, pathPrefix string) []testutil.RecordedRequest {
	var out []testutil.RecordedRequest
	for _, r := range reqs {
		if r.Method == method && strings.HasPrefix(r.URL.Path, pathPrefix) {
			out = append(out, r)
		}
	}
	return out
}
