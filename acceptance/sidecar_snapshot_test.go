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

func TestSidecarSnapshotCreateSendsSidecarIDInBody(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "snapshot", "create",
		"--sidecar-id", "sb-111",
		"--name", "my-checkpoint",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	snapReqs := filterByMethod(reqs, "POST", "/api/v2/sidecar/snapshots")
	assert.Equal(t, len(snapReqs), 1, "expected 1 create snapshot request")

	var body map[string]interface{}
	err := json.Unmarshal(snapReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["sidecar_id"], "sb-111")
	assert.Equal(t, body["name"], "my-checkpoint")
}

func TestSidecarSnapshotCreateMissingName(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"sidecar", "snapshot", "create",
		"--sidecar-id", "sb-111",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for missing --name")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "name"),
		"expected error about missing --name, got: %s", combined)
}

func TestSidecarSnapshotCreateUsesActiveSidecar(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	workDir := t.TempDir()

	// create sidecar → sets active sidecar to "sidecar-new-123"
	result := binary.RunCLI(t, []string{
		"sidecar", "create",
		"--org-id", "org-aaa",
		"--name", "test-box",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "create stderr: %s", result.Stderr)

	// snapshot create without --sidecar-id should use the active sidecar
	result = binary.RunCLI(t, []string{
		"sidecar", "snapshot", "create",
		"--name", "my-checkpoint",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "snapshot create stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	snapReqs := filterByMethod(reqs, "POST", "/api/v2/sidecar/snapshots")
	assert.Assert(t, len(snapReqs) >= 1, "expected at least 1 create snapshot request")

	var body map[string]interface{}
	err := json.Unmarshal(snapReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["sidecar_id"], "sidecar-new-123",
		"expected active sidecar ID in request body")

	// After a successful snapshot, the source sidecar should have been deleted
	// and the local active-sidecar state cleared.
	currentResult := binary.RunCLI(t, []string{"sidecar", "current"}, env, workDir)
	assert.Equal(t, currentResult.ExitCode, 0)
	assert.Assert(t, strings.Contains(currentResult.Stderr, "No active sidecar"),
		"expected active sidecar to be cleared after snapshot, got stderr: %s stdout: %s",
		currentResult.Stderr, currentResult.Stdout)
}

func TestSidecarSnapshotCreateDeletesSourceSidecar(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "snapshot", "create",
		"--sidecar-id", "sb-111",
		"--name", "my-checkpoint",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "Deleted sidecar sb-111"),
		"expected delete confirmation in stderr, got: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	deleteReqs := filterByMethod(reqs, "DELETE", "/api/v2/sidecar/instances/sb-111")
	assert.Equal(t, len(deleteReqs), 1, "expected exactly 1 DELETE request, got %d", len(deleteReqs))
}

func TestSidecarSnapshotCreateWarnsWhenDeleteFails(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.DeleteStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "snapshot", "create",
		"--sidecar-id", "sb-222",
		"--name", "my-checkpoint",
	}, env, env.HomeDir)

	// Snapshot itself succeeded, so exit code stays 0.
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "Warning"),
		"expected warning in stderr, got: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "sb-222"),
		"expected sidecar id in warning, got: %s", result.Stderr)
}

func TestSidecarSnapshotCreateNoActiveSidecar(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := t.TempDir()

	result := binary.RunCLI(t, []string{
		"sidecar", "snapshot", "create",
		"--name", "my-checkpoint",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit with no sidecar ID")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "sidecar-id") || strings.Contains(combined, "active sidecar"),
		"expected helpful error, got: %s", combined)
}

func TestSidecarSnapshotCreateAPIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.CreateSnapshotStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "snapshot", "create",
		"--sidecar-id", "sb-111",
		"--name", "my-checkpoint",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for 500 response")
}

func TestSidecarSnapshotGetHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Snapshots = []fakes.Snapshot{
		{ID: "snap-abc", Name: "my-checkpoint"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "snapshot", "get", "snap-abc",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "snap-abc"),
		"expected snapshot ID in output, got: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "my-checkpoint"),
		"expected snapshot name in output, got: %s", result.Stdout)
}

func TestSidecarSnapshotGetNotFound(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "snapshot", "get", "snap-does-not-exist",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit for not found")
}

func TestSidecarSnapshotMissingToken(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"create", []string{"sidecar", "snapshot", "create", "--sidecar-id", "sb-111", "--name", "snap"}},
		{"get", []string{"sidecar", "snapshot", "get", "snap-abc"}},
		{"list", []string{"sidecar", "snapshot", "list", "--org-id", "org-abc"}},
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

func TestSidecarSnapshotListHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Snapshots = []fakes.Snapshot{
		{ID: "snap-1", OrgID: "org-abc", Name: "baseline"},
		{ID: "snap-2", OrgID: "org-abc", Name: "with-deps"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "snapshot", "list", "--org-id", "org-abc",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "baseline"), "expected 'baseline' in output: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "snap-1"), "expected 'snap-1' in output: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "with-deps"), "expected 'with-deps' in output: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "snap-2"), "expected 'snap-2' in output: %s", result.Stdout)
}

func TestSidecarSnapshotListEmpty(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{
		"sidecar", "snapshot", "list", "--org-id", "org-abc",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "No snapshots found"), "expected 'No snapshots found' in stderr: %s", result.Stderr)
}
