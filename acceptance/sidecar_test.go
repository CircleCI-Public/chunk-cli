package acceptance

import (
	"encoding/json"
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
	listReqs := filterByPath(reqs, "/api/v3/sidecar/instances")
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
	assert.Assert(t, strings.Contains(result.Stderr, "sidecar-new-1"),
		"expected sidecar ID in stderr, got: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "my-new-sidecar"),
		"expected sidecar name in stderr, got: %s", result.Stderr)

	// Verify request body
	reqs := cci.Recorder.AllRequests()
	createReqs := filterByMethod(reqs, "POST", "/api/v3/sidecar/instances")
	assert.Equal(t, len(createReqs), 1, "expected 1 create request")

	var body map[string]any
	err := json.Unmarshal(createReqs[0].Body, &body)
	assert.NilError(t, err)

	data := body["data"].(map[string]any)
	attrs := data["attributes"].(map[string]any)
	refs := data["references"].(map[string]any)
	orgRef := refs["org"].(map[string]any)

	assert.Equal(t, orgRef["id"], "org-aaa")
	assert.Equal(t, attrs["name"], "my-new-sidecar")
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
	createReqs := filterByMethod(reqs, "POST", "/api/v3/sidecar/instances")
	assert.Equal(t, len(createReqs), 1)

	var body map[string]any
	err := json.Unmarshal(createReqs[0].Body, &body)
	assert.NilError(t, err)

	data := body["data"].(map[string]any)
	attrs := data["attributes"].(map[string]any)
	assert.Equal(t, attrs["image"], "ubuntu:22.04")
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
	execReqs := filterByPath(reqs, "/api/v3/sidecar/instances/sb-111/exec")
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
	addKeyReqs := filterByPath(reqs, "/api/v3/sidecar/instances/sb-111/ssh/add-key")
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
	addKeyReqs := filterByPath(reqs, "/api/v3/sidecar/instances/sb-111/ssh/add-key")
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

// TestSidecarsSyncCheckoutFlagRemoved verifies that --checkout is no longer a
// recognised flag now that sync uses rsync unconditionally.
func TestSidecarsSyncCheckoutFlagRemoved(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "sync",
		"--sidecar-id", "sb-111",
		"--checkout",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "unknown flag"),
		"--checkout must be an unknown flag; got: %s", combined)
}

// TestSidecarsSshNoKey verifies that `sidecar ssh` generates the default
// keypair at ~/.ssh/chunk_ai instead of asking the user to run ssh-keygen.
// Reaching a sidecar has to work without `sidecar setup` having run first.
//
// The first run uses a server accepting any key, since the test cannot know
// the generated key in advance. It then checks that the key registered with
// the API is the one left on disk, and that a second run authenticates
// against a server authorising only that key — a generate-then-register
// mismatch would pass the any-key handshake but fail here.
func TestSidecarsSshNoKey(t *testing.T) {
	sshSrv := fakes.NewSSHServerAcceptingAnyKey(t)
	sshSrv.SetResult("hello\n", 0)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	// No key at env.HomeDir/.ssh/chunk_ai — the default path.
	keyPath := filepath.Join(env.HomeDir, ".ssh", "chunk_ai")
	_, err := os.Stat(keyPath)
	assert.Assert(t, os.IsNotExist(err), "expected no key before the run")

	result := binary.RunCLI(t, []string{"sidecar", "ssh", "--sidecar-id", "sb-111", "echo", "hello"}, env, env.HomeDir)

	combined := result.Stdout + result.Stderr
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	assert.Assert(t, !strings.Contains(combined, "SSH key not found"),
		"key should have been generated, not reported missing: %s", combined)

	_, err = os.Stat(keyPath)
	assert.NilError(t, err, "private key should have been generated")
	pubKeyData, err := os.ReadFile(keyPath + ".pub")
	assert.NilError(t, err, "public key should have been generated")

	// The key registered with the API must be the one left on disk.
	addKeyReqs := filterByPath(cci.Recorder.AllRequests(), "/api/v3/sidecar/instances/sb-111/ssh/add-key")
	assert.Equal(t, len(addKeyReqs), 1)
	var addKeyBody struct {
		PublicKey string `json:"public_key"`
	}
	assert.NilError(t, json.Unmarshal(addKeyReqs[0].Body, &addKeyBody))
	assert.Equal(t, addKeyBody.PublicKey, strings.TrimSpace(string(pubKeyData)),
		"registered key should match the generated key on disk")

	// A second run has to authenticate against a server that authorises only
	// the key on disk, proving the private key and registered key are a pair.
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(pubKeyData)
	assert.NilError(t, err)
	sshSrv.RestrictToKey(pubKey)

	result = binary.RunCLI(t, []string{"sidecar", "ssh", "--sidecar-id", "sb-111", "echo", "hello"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "second run should authenticate with the generated key: stdout: %s\nstderr: %s",
		result.Stdout, result.Stderr)
}

// TestSidecarsSshPassesThroughExitCode verifies that a remote command exiting
// non-zero makes `sidecar ssh` exit with that same code, as ssh does, without
// chunk printing an error of its own over the command's output.
func TestSidecarsSshPassesThroughExitCode(t *testing.T) {
	sshSrv := fakes.NewSSHServerAcceptingAnyKey(t)
	sshSrv.SetResult("partial output\n", 3)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{"sidecar", "ssh", "--sidecar-id", "sb-111", "--", "false"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 3, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	assert.Equal(t, result.Stdout, "partial output\n")
	assert.Assert(t, !strings.Contains(result.Stderr, "error"),
		"remote exit should not be reported as a chunk error: %s", result.Stderr)
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
	execReqs := filterByPath(reqs, "/api/v3/sidecar/instances/sb-111/exec")
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
	assert.Assert(t, strings.Contains(result.Stderr, "Set sidecar-new-1 as active sidecar"),
		"expected active sidecar message, got: %s", result.Stderr)

	// current should show the sidecar set by create
	result = binary.RunCLI(t, []string{"sidecar", "current"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sidecar-new-1"),
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
	execReqs := filterByPath(reqs, "/api/v3/sidecar/instances/sb-explicit/exec")
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

// TestSidecarDeleteRequiresForceWithoutTTY verifies that sidecar delete requires
// --force (or a TTY) to proceed; without either it exits non-zero.
func TestSidecarDeleteRequiresForceWithoutTTY(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sb-del-1", Name: "to-delete", OrgID: "org-aaa"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{"sidecar", "delete", "--sidecar-id", "sb-del-1"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit without --force, got 0")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "force"),
		"expected --force suggestion in output, got: %s", combined)

	// Sidecar should not have been deleted.
	assert.Equal(t, len(cci.Sidecars), 1, "expected sidecar to remain after cancelled delete")
}

// TestSidecarDeleteForceSkipsConfirmation verifies that --force deletes without prompting.
func TestSidecarDeleteForceSkipsConfirmation(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sb-del-2", Name: "to-delete", OrgID: "org-aaa"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{"sidecar", "delete", "--sidecar-id", "sb-del-2", "--force"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Deleted"),
		"expected success message, got: %s", combined)

	assert.Equal(t, len(cci.Sidecars), 0, "expected sidecar to be deleted")
}

// TestPruneRequiresForceWithoutTTY verifies that chunk prune requires --force
// (or a TTY) to proceed; without either it exits non-zero.
func TestPruneRequiresForceWithoutTTY(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sb-prune-1", Name: "old-sidecar", OrgID: "org-bbb"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{"prune", "--org-id", "org-bbb"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit without --force, got 0")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "force"),
		"expected --force suggestion in output, got: %s", combined)

	// Nothing should have been pruned.
	assert.Equal(t, len(cci.Sidecars), 1, "expected sidecar to remain after cancelled prune")
}

// TestPruneForceSkipsConfirmation verifies that --force prunes without prompting.
func TestPruneForceSkipsConfirmation(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sb-prune-2", Name: "old-sidecar", OrgID: "org-ccc"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{"prune", "--org-id", "org-ccc", "--force"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Deleted"),
		"expected success message, got: %s", combined)

	assert.Equal(t, len(cci.Sidecars), 0, "expected sidecars to be pruned")
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
