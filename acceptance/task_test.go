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

const runConfigJSON = `{
  "org_id": "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
  "project_id": "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
  "org_type": "github",
  "definitions": {
    "dev": {
      "definition_id": "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
      "chunk_environment_id": "b3c27e5f-1234-5678-9abc-def012345678",
      "default_branch": "develop"
    },
    "prod": {
      "definition_id": "f3127f5f-0283-48c4-b5fb-b4ff2b693ccb",
      "chunk_environment_id": null,
      "default_branch": "main"
    }
  }
}`

func writeRunConfig(t *testing.T, workDir string) {
	t.Helper()
	chunkDir := filepath.Join(workDir, ".chunk")
	err := os.MkdirAll(chunkDir, 0o755)
	assert.NilError(t, err)
	err = os.WriteFile(filepath.Join(chunkDir, "run.json"), []byte(runConfigJSON), 0o644)
	assert.NilError(t, err)
}

func TestTaskRunHappyPath(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeRunConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"task", "run",
		"--definition", "dev",
		"--prompt", "Fix the flaky test",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "run-abc-123"),
		"expected run ID in output, got: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "pipeline-def-456"),
		"expected pipeline ID in output, got: %s", result.Stdout)

	// Verify the request hit the correct endpoint
	reqs := cci.Recorder.AllRequests()
	runReqs := filterByPathPrefix(reqs, "/api/v2/agents/org/")
	assert.Equal(t, len(runReqs), 1, "expected 1 trigger run request")

	// Verify request body
	var body map[string]interface{}
	err := json.Unmarshal(runReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["agent_type"], "prompt")
	assert.Equal(t, body["checkout_branch"], "develop")
	assert.Equal(t, body["definition_id"], "e2016e4e-0172-47b3-a4ea-a3ee1a592dba")
	assert.Equal(t, body["trigger_source"], "chunk-cli")

	params := body["parameters"].(map[string]interface{})
	assert.Equal(t, params["custom-prompt"], "Fix the flaky test")
	assert.Equal(t, params["run-pipeline-as-a-tool"], true)
	assert.Equal(t, params["create-new-branch"], false)

	// Verify auth header
	assert.Assert(t, runReqs[0].Header.Get("Circle-Token") != "",
		"expected Circle-Token header")
}

func TestTaskRunBranchOverride(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeRunConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"task", "run",
		"--definition", "dev",
		"--prompt", "Fix tests",
		"--branch", "feature/my-branch",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	runReqs := filterByPathPrefix(reqs, "/api/v2/agents/org/")
	assert.Equal(t, len(runReqs), 1)

	var body map[string]interface{}
	err := json.Unmarshal(runReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["checkout_branch"], "feature/my-branch")
}

func TestTaskRunNewBranch(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeRunConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"task", "run",
		"--definition", "dev",
		"--prompt", "Add types",
		"--new-branch",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	runReqs := filterByPathPrefix(reqs, "/api/v2/agents/org/")
	assert.Equal(t, len(runReqs), 1)

	var body map[string]interface{}
	err := json.Unmarshal(runReqs[0].Body, &body)
	assert.NilError(t, err)

	params := body["parameters"].(map[string]interface{})
	assert.Equal(t, params["create-new-branch"], true)
}

func TestTaskRunPipelineAsToolDefault(t *testing.T) {
	// --pipeline-as-tool defaults to true when not specified
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeRunConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"task", "run",
		"--definition", "dev",
		"--prompt", "Add types",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	runReqs := filterByPathPrefix(reqs, "/api/v2/agents/org/")
	assert.Equal(t, len(runReqs), 1)

	var body map[string]interface{}
	err := json.Unmarshal(runReqs[0].Body, &body)
	assert.NilError(t, err)

	params := body["parameters"].(map[string]interface{})
	assert.Equal(t, params["run-pipeline-as-a-tool"], true)
}

func TestTaskRunProdDefinition(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeRunConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"task", "run",
		"--definition", "prod",
		"--prompt", "Deploy fix",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	runReqs := filterByPathPrefix(reqs, "/api/v2/agents/org/")
	assert.Equal(t, len(runReqs), 1)

	var body map[string]interface{}
	err := json.Unmarshal(runReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["definition_id"], "f3127f5f-0283-48c4-b5fb-b4ff2b693ccb")
	assert.Equal(t, body["checkout_branch"], "main")
	// chunk_environment_id should be null for prod
	assert.Assert(t, body["chunk_environment_id"] == nil,
		"expected null chunk_environment_id, got: %v", body["chunk_environment_id"])
}

func TestTaskRunRawUUID(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeRunConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	rawUUID := "11111111-2222-3333-4444-555555555555"
	result := testutil.RunCLI(t, []string{
		"task", "run",
		"--definition", rawUUID,
		"--prompt", "Fix it",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	runReqs := filterByPathPrefix(reqs, "/api/v2/agents/org/")

	var body map[string]interface{}
	err := json.Unmarshal(runReqs[0].Body, &body)
	assert.NilError(t, err)
	assert.Equal(t, body["definition_id"], rawUUID)
	assert.Equal(t, body["checkout_branch"], "main") // default when using raw UUID
}

func TestTaskRunMissingToken(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeRunConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.CircleToken = "" // no token

	result := testutil.RunCLI(t, []string{
		"task", "run",
		"--definition", "dev",
		"--prompt", "Fix it",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "CIRCLE_TOKEN") || strings.Contains(combined, "token"),
		"expected token error message, got: %s", combined)
}

func TestTaskRunMissingConfig(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	// No .chunk/run.json

	env := testutil.NewTestEnv(t)

	result := testutil.RunCLI(t, []string{
		"task", "run",
		"--definition", "dev",
		"--prompt", "Fix it",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "run.json") || strings.Contains(combined, "configuration"),
		"expected config error message, got: %s", combined)
}

func TestTaskRunUnknownDefinition(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeRunConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"task", "run",
		"--definition", "staging",
		"--prompt", "Fix it",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "staging") || strings.Contains(combined, "Unknown"),
		"expected unknown definition error, got: %s", combined)
}

func TestTaskRunAPIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.RunStatusCode = 403
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeRunConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"task", "run",
		"--definition", "dev",
		"--prompt", "Fix it",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "forbidden") || strings.Contains(combined, "error") || strings.Contains(combined, "Error"),
		"expected API error message, got: %s", combined)
}

func TestTaskRunURLContainsOrgAndProject(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeRunConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := testutil.RunCLI(t, []string{
		"task", "run",
		"--definition", "dev",
		"--prompt", "Fix it",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	reqs := cci.Recorder.AllRequests()
	runReqs := filterByPathPrefix(reqs, "/api/v2/agents/org/")
	assert.Equal(t, len(runReqs), 1)

	// Verify the URL contains the org and project IDs from run.json
	url := runReqs[0].URL.Path
	assert.Assert(t, strings.Contains(url, "a37b44de-e4f8-4d09-956a-9c1148f3adf5"),
		"expected org_id in URL path, got: %s", url)
	assert.Assert(t, strings.Contains(url, "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb"),
		"expected project_id in URL path, got: %s", url)
}
