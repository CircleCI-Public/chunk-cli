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
	"github.com/CircleCI-Public/chunk-cli/internal/testing/recorder"
)

func TestSidecarsListHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sb-111", Name: "dev-sidecar", OrgID: "org-aaa"},
		{ID: "sb-222", Name: "staging-sidecar", OrgID: "org-aaa"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "list", "--org-id", "org-aaa",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "dev-sidecar"),
		"expected sidecar name in output, got: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "sb-111"),
		"expected sidecar id in output, got: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "staging-sidecar"),
		"expected second sidecar in output, got: %s", result.Stdout)

	// Verify org_id query param was sent
	reqs := cci.Recorder.AllRequests()
	listReqs := filterByPath(reqs, "/api/v2/sidecar/instances")
	assert.Assert(t, len(listReqs) >= 1, "expected at least 1 list request")
	assert.Equal(t, listReqs[0].URL.Query().Get("org_id"), "org-aaa")
}

func TestSidecarsListEmpty(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "list", "--org-id", "org-empty",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "No sidecars") || strings.Contains(combined, "no sidecar"),
		"expected empty message, got: %s", combined)
}

func TestSidecarsListFiltersByOrg(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sb-111", Name: "org-a-box", OrgID: "org-a"},
		{ID: "sb-222", Name: "org-b-box", OrgID: "org-b"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "list", "--org-id", "org-a",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "org-a-box"),
		"expected org-a sidecar, got: %s", result.Stdout)
	assert.Assert(t, !strings.Contains(result.Stdout, "org-b-box"),
		"should not contain org-b sidecar, got: %s", result.Stdout)
}

func TestSidecarsMissingToken(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"list", []string{"sidecar", "list", "--org-id", "org-aaa"}},
		{"create", []string{"sidecar", "create", "--org-id", "org-aaa", "--name", "my-sidecar"}},
		{"exec", []string{"sidecar", "exec", "--org-id", "org-aaa", "--sidecar-id", "sb-111", "--command", "ls"}},
		{"ssh", []string{"sidecar", "ssh", "--org-id", "org-aaa", "--sidecar-id", "sb-111"}},
		{"sync", []string{"sidecar", "sync", "--org-id", "org-aaa", "--sidecar-id", "sb-111"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := testenv.NewTestEnv(t)
			env.CircleToken = ""

			result := binary.RunCLI(t, tt.args, env, env.HomeDir)
			assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
		})
	}
}

func TestSidecarsCreateHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--org-id", "org-aaa",
		"--name", "my-new-sidecar",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "sidecar-new-123"),
		"expected sidecar ID in stderr, got: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "my-new-sidecar"),
		"expected sidecar name in stderr, got: %s", result.Stderr)

	// Verify request body
	reqs := cci.Recorder.AllRequests()
	createReqs := filterByMethod(reqs, "POST", "/api/v2/sidecar/instances")
	assert.Equal(t, len(createReqs), 1, "expected 1 create request")

	var body map[string]interface{}
	err := json.Unmarshal(createReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["org_id"], "org-aaa")
	assert.Equal(t, body["name"], "my-new-sidecar")
}

func TestSidecarsCreateWithImage(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--org-id", "org-aaa",
		"--name", "custom-sidecar",
		"--image", "ubuntu:22.04",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	createReqs := filterByMethod(reqs, "POST", "/api/v2/sidecar/instances")
	assert.Equal(t, len(createReqs), 1)

	var body map[string]interface{}
	err := json.Unmarshal(createReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["image"], "ubuntu:22.04")
}

func TestSidecarsExecHappyPath(t *testing.T) {
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

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "exec",
		"--sidecar-id", "sb-111",
		"--command", "echo",
		"--args", "hello", "world",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "hello world"),
		"expected command output, got: %s", result.Stdout)

	// Verify exec request with sidecar ID in path
	reqs := cci.Recorder.AllRequests()
	execReqs := filterByPath(reqs, "/api/v2/sidecar/instances/sb-111/exec")
	assert.Equal(t, len(execReqs), 1, "expected 1 exec request")

	var body map[string]interface{}
	err := json.Unmarshal(execReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["command"], "echo")

	// Verify Circle-Token auth on exec (no more Bearer)
	assert.Assert(t, execReqs[0].Header.Get("Circle-Token") != "",
		"expected Circle-Token auth on exec request")
}

func TestSidecarsAddSSHKeyFromString(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = "my-sidecar.dev.example.com"
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "add-ssh-key",
		"--sidecar-id", "sb-111",
		"--public-key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForTestingPurposesOnly123 test@test",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "my-sidecar.dev.example.com"),
		"expected sidecar domain in stderr, got: %s", result.Stderr)

	// Verify add-key request with sidecar ID in path
	reqs := cci.Recorder.AllRequests()
	addKeyReqs := filterByPath(reqs, "/api/v2/sidecar/instances/sb-111/ssh/add-key")
	assert.Equal(t, len(addKeyReqs), 1, "expected 1 add-key request")

	var body map[string]interface{}
	err := json.Unmarshal(addKeyReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Assert(t, strings.HasPrefix(body["public_key"].(string), "ssh-ed25519"),
		"expected public key in body, got: %v", body["public_key"])
}

func TestSidecarsAddSSHKeyFromFile(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	// Write a fake public key file
	keyFile := filepath.Join(env.HomeDir, "test-key.pub")
	err := os.WriteFile(keyFile, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForTestingPurposesOnly123 test@test\n"), 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{
		"sidecar", "add-ssh-key",
		"--sidecar-id", "sb-111",
		"--public-key-file", keyFile,
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// Verify the key was sent in the request
	reqs := cci.Recorder.AllRequests()
	addKeyReqs := filterByPath(reqs, "/api/v2/sidecar/instances/sb-111/ssh/add-key")
	assert.Equal(t, len(addKeyReqs), 1)

	var body map[string]interface{}
	err = json.Unmarshal(addKeyReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Assert(t, strings.HasPrefix(body["public_key"].(string), "ssh-ed25519"),
		"expected public key from file in body")
}

func TestSidecarsAddSSHKeyMutuallyExclusive(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	keyFile := filepath.Join(env.HomeDir, "test-key.pub")
	err := os.WriteFile(keyFile, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKey test@test\n"), 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{
		"sidecar", "add-ssh-key",
		"--sidecar-id", "sb-111",
		"--public-key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKey test@test",
		"--public-key-file", keyFile,
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code for mutually exclusive flags")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "mutually exclusive") || strings.Contains(combined, "exclusive"),
		"expected mutually exclusive error, got: %s", combined)
}

func TestSidecarsAddSSHKeyNeitherProvided(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "add-ssh-key",
		"--sidecar-id", "sb-111",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code when no key provided")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "public-key") || strings.Contains(combined, "required"),
		"expected missing key error, got: %s", combined)
}

func TestSidecarsAddSSHKeyPrivateKeyRejected(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	// Write a fake private key file (detected by PRIVATE KEY marker)
	keyFile := filepath.Join(env.HomeDir, "private-key.pub")
	err := os.WriteFile(keyFile, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfakedata\n-----END OPENSSH PRIVATE KEY-----\n"), 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{
		"sidecar", "add-ssh-key",
		"--sidecar-id", "sb-111",
		"--public-key-file", keyFile,
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected rejection of private key")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(strings.ToLower(combined), "private"),
		"expected private key error, got: %s", combined)
}

// TestSidecarsSshSyncFlags verifies that SSH/sync flags are accepted and
// code progresses past flag parsing (fails at SSH step, not at parsing).
func TestSidecarsSshSyncFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"ssh identity-file", []string{"sidecar", "ssh", "--sidecar-id", "sb-111", "--identity-file", "/tmp/fake-key"}},
		{"sync identity-file", []string{"sidecar", "sync", "--sidecar-id", "sb-111", "--identity-file", "/tmp/fake-key"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cci := fakes.NewFakeCircleCI()
			srv := httptest.NewServer(cci)
			defer srv.Close()

			env := testenv.NewTestEnv(t)
			env.CircleCIURL = srv.URL

			result := binary.RunCLI(t, tt.args, env, env.HomeDir)

			// Commands should fail at SSH key step, not at flag parsing
			assert.Assert(t, result.ExitCode != 0, "expected non-zero exit (SSH fails)")
			combined := result.Stdout + result.Stderr
			assert.Assert(t, strings.Contains(combined, "SSH key not found"),
				"expected SSH key error (proves flags accepted), got: %s", combined)
		})
	}
}

func TestSidecarsExecWithArgs(t *testing.T) {
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

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "exec",
		"--sidecar-id", "sb-111",
		"--command", "ls",
		"--args", "-la", "/tmp",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// Verify exec request body has the command
	reqs := cci.Recorder.AllRequests()
	execReqs := filterByPath(reqs, "/api/v2/sidecar/instances/sb-111/exec")
	assert.Equal(t, len(execReqs), 1)

	var body map[string]interface{}
	err := json.Unmarshal(execReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["command"], "ls")
}

func TestSidecarsCreateMissingName(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--org-id", "org-aaa",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code for missing --name")
}

func TestSidecarsListFromConfig(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sb-999", Name: "config-sidecar", OrgID: "org-from-config"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.Extra["CIRCLECI_ORG_ID"] = "org-from-config"

	// No --org-id flag; should read from CIRCLECI_ORG_ID
	result := binary.RunCLI(t, []string{"sidecar", "list"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "config-sidecar"),
		"expected sidecar from config org, got: %s", result.Stdout)
}

func TestSidecarsListNoOrgIDNoConfig(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{"sidecar", "list"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "--org-id"),
		"expected helpful error message, got: %s", combined)
}

// TestSidecarsSSHEnvFlag verifies the -e/--env flag rejects invalid input.
// Direct env resolution logic is tested in internal/sidecar/env_test.go (TestResolveEnv).
//
// TODO: Once the fake SSH server from #188 lands, add tests that spin up the
// fake server and assert on the actual env vars sent over the wire (valid pairs,
// .env.local loading, multiple -e flags, --no-env-file).
func TestSidecarsSSHEnvFlag(t *testing.T) {
	t.Run("invalid entry without equals returns error", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(cci)
		defer srv.Close()

		env := testenv.NewTestEnv(t)
		env.CircleCIURL = srv.URL

		result := binary.RunCLI(t, []string{
			"sidecar", "ssh",
			"--sidecar-id", "sb-111",
			"--env", "NOEQUALS",
		}, env, env.HomeDir)

		assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
		combined := result.Stdout + result.Stderr
		assert.Assert(t, strings.Contains(combined, "NOEQUALS"),
			"expected invalid entry in error, got: %s", combined)
	})
}

func TestSidecarsCreateSetsActiveSidecar(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	workDir := t.TempDir()

	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--org-id", "org-aaa",
		"--name", "my-new-sidecar",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "Set sidecar-new-123 as active sidecar"),
		"expected active sidecar message, got: %s", result.Stderr)

	// current should show the sidecar set by create
	result = binary.RunCLI(t, []string{"sidecar", "current"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sidecar-new-123"),
		"expected sidecar ID from current, got: %s", combined)
}

func TestSidecarsExecUsesActiveSidecar(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{
		CommandID: "cmd-1",
		PID:       1,
		Stdout:    "active sidecar output\n",
		ExitCode:  0,
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	workDir := t.TempDir()

	// create sets the active sidecar
	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--org-id", "org-aaa",
		"--name", "test-box",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "create stderr: %s", result.Stderr)

	// exec without --sidecar-id should succeed using active sidecar
	result = binary.RunCLI(t, []string{
		"sidecar", "exec",
		"--command", "echo",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "exec stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "active sidecar output"),
		"expected output, got: %s", result.Stdout)
}

func TestSidecarsForgetClearsActiveSidecar(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	workDir := t.TempDir()

	// create sets active sidecar
	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--org-id", "org-aaa",
		"--name", "temp-box",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "create stderr: %s", result.Stderr)

	// forget clears it
	result = binary.RunCLI(t, []string{"sidecar", "forget"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "forget stderr: %s", result.Stderr)

	// exec without --sidecar-id should now fail
	result = binary.RunCLI(t, []string{
		"sidecar", "exec",
		"--command", "echo",
	}, env, workDir)
	assert.Assert(t, result.ExitCode != 0, "expected failure after forget")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sidecar-id") || strings.Contains(combined, "active sidecar"),
		"expected missing sidecar error, got: %s", combined)
}

func TestSidecarsExplicitIDOverridesActive(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{
		CommandID: "cmd-2",
		PID:       2,
		Stdout:    "explicit output\n",
		ExitCode:  0,
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	workDir := t.TempDir()

	// Set active sidecar to something
	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--org-id", "org-aaa",
		"--name", "default-box",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "create stderr: %s", result.Stderr)

	// exec with explicit --sidecar-id should use it (not the active one)
	result = binary.RunCLI(t, []string{
		"sidecar", "exec",
		"--sidecar-id", "sb-explicit",
		"--command", "echo",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "exec stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	execReqs := filterByPath(reqs, "/api/v2/sidecar/instances/sb-explicit/exec")
	assert.Assert(t, len(execReqs) >= 1, "expected exec request to use explicit sidecar ID, got requests: %v", reqs)
}

func TestSidecarsCurrentNoActive(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := t.TempDir()

	result := binary.RunCLI(t, []string{"sidecar", "current"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "No active sidecar"),
		"expected no active message, got: %s", combined)
}

func TestSidecarsUseCommand(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{
		CommandID: "cmd-3",
		PID:       3,
		Stdout:    "use output\n",
		ExitCode:  0,
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	workDir := t.TempDir()

	result := binary.RunCLI(t, []string{"sidecar", "use", "sb-manual"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	result = binary.RunCLI(t, []string{"sidecar", "current"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sb-manual"),
		"expected sb-manual in current output, got: %s", combined)
}

// filterByMethod returns requests matching both method and path prefix.
func filterByMethod(reqs []recorder.RecordedRequest, method, pathPrefix string) []recorder.RecordedRequest {
	var out []recorder.RecordedRequest
	for _, r := range reqs {
		if r.Method == method && strings.HasPrefix(r.URL.Path, pathPrefix) {
			out = append(out, r)
		}
	}
	return out
}
