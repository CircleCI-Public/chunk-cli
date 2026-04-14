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
)

func TestSandboxSnapshotCreateHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "snapshot", "create",
		"--sandbox-id", "sb-111",
		"--name", "my-checkpoint",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "snap-new-123"),
		"expected snapshot ID in stderr, got: %s", result.Stderr)
}

func TestSandboxSnapshotCreateSendsSandboxIDInBody(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "snapshot", "create",
		"--sandbox-id", "sb-111",
		"--name", "my-checkpoint",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	snapReqs := filterByMethod(reqs, "POST", "/api/v2/sandbox/snapshots")
	assert.Equal(t, len(snapReqs), 1, "expected 1 create snapshot request")

	var body map[string]interface{}
	err := json.Unmarshal(snapReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["sandbox_id"], "sb-111")
	assert.Equal(t, body["name"], "my-checkpoint")
}

func TestSandboxSnapshotCreateMissingName(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sandbox", "snapshot", "create",
		"--sandbox-id", "sb-111",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for missing --name")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "name"),
		"expected error about missing --name, got: %s", combined)
}

func TestSandboxSnapshotCreateUsesActiveSandbox(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	workDir := t.TempDir()

	// create sandbox → sets active sandbox to "sandbox-new-123"
	result := binary.RunCLI(t, []string{
		"sandbox", "create",
		"--org-id", "org-aaa",
		"--name", "test-box",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "create stderr: %s", result.Stderr)

	// snapshot create without --sandbox-id should use the active sandbox
	result = binary.RunCLI(t, []string{
		"sandbox", "snapshot", "create",
		"--name", "my-checkpoint",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "snapshot create stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	snapReqs := filterByMethod(reqs, "POST", "/api/v2/sandbox/snapshots")
	assert.Assert(t, len(snapReqs) >= 1, "expected at least 1 create snapshot request")

	var body map[string]interface{}
	err := json.Unmarshal(snapReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["sandbox_id"], "sandbox-new-123",
		"expected active sandbox ID in request body")
}

func TestSandboxSnapshotCreateNoActiveSandbox(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := t.TempDir()

	result := binary.RunCLI(t, []string{
		"sandbox", "snapshot", "create",
		"--name", "my-checkpoint",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit with no sandbox ID")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sandbox-id") || strings.Contains(combined, "active sandbox"),
		"expected helpful error, got: %s", combined)
}

func TestSandboxSnapshotCreateAPIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.CreateSnapshotStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "snapshot", "create",
		"--sandbox-id", "sb-111",
		"--name", "my-checkpoint",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 500 response")
}

func TestSandboxSnapshotGetHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Snapshots = []fakes.Snapshot{
		{ID: "snap-abc", Name: "my-checkpoint"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "snapshot", "get", "snap-abc",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "snap-abc"),
		"expected snapshot ID in output, got: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "my-checkpoint"),
		"expected snapshot name in output, got: %s", result.Stdout)
}

func TestSandboxSnapshotGetNotFound(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sandbox", "snapshot", "get", "snap-does-not-exist",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for not found")
}

func TestSandboxSnapshotMissingToken(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"create", []string{"sandbox", "snapshot", "create", "--sandbox-id", "sb-111", "--name", "snap"}},
		{"get", []string{"sandbox", "snapshot", "get", "snap-abc"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := testenv.NewTestEnv(t)
			env.CircleToken = ""

			result := binary.RunCLI(t, tt.args, env, env.HomeDir)
			assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code without token")
		})
	}
}
