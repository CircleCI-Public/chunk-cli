package task

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

const testRunConfig = `{
  "org_id": "org-111",
  "project_id": "proj-222",
  "org_type": "github",
  "definitions": {
    "dev": {
      "definition_id": "def-aaa",
      "chunk_environment_id": "env-bbb",
      "default_branch": "develop"
    },
    "prod": {
      "definition_id": "def-ccc",
      "chunk_environment_id": null,
      "default_branch": "main"
    }
  }
}`

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	chunkDir := filepath.Join(dir, ".chunk")
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chunkDir, "run.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func strPtr(s string) *string { return &s }

// --- MapVcsTypeToOrgType ---

func TestMapVcsTypeToOrgType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"github", "github"},
		{"GitHub", "github"},
		{"gh", "github"},
		{"GH", "github"},
		{"bitbucket", "circleci"},
		{"Bitbucket", "circleci"},
		{"circleci", "circleci"},
		{"", "circleci"},
		{"something-else", "circleci"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := MapVcsTypeToOrgType(tt.input)
			if got != tt.want {
				t.Fatalf("MapVcsTypeToOrgType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- IsValidUUID ---

func TestIsValidUUID(t *testing.T) {
	if !IsValidUUID("550e8400-e29b-41d4-a716-446655440000") {
		t.Fatal("expected valid UUID")
	}
	if IsValidUUID("not-a-uuid") {
		t.Fatal("expected invalid UUID")
	}
	if IsValidUUID("") {
		t.Fatal("expected empty string to be invalid")
	}
}

// --- ConfigExists ---

func TestConfigExists(t *testing.T) {
	dir := t.TempDir()
	if ConfigExists(dir) {
		t.Fatal("expected false for missing config")
	}
	writeConfig(t, dir, testRunConfig)
	if !ConfigExists(dir) {
		t.Fatal("expected true for existing config")
	}
}

// --- LoadRunConfig ---

func TestLoadRunConfig(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, testRunConfig)

	cfg, err := LoadRunConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OrgID != "org-111" {
		t.Fatalf("OrgID = %q, want %q", cfg.OrgID, "org-111")
	}
	if cfg.ProjectID != "proj-222" {
		t.Fatalf("ProjectID = %q, want %q", cfg.ProjectID, "proj-222")
	}
	if len(cfg.Definitions) != 2 {
		t.Fatalf("got %d definitions, want 2", len(cfg.Definitions))
	}

	dev := cfg.Definitions["dev"]
	if dev.DefinitionID != "def-aaa" {
		t.Fatalf("dev.DefinitionID = %q, want %q", dev.DefinitionID, "def-aaa")
	}
	if dev.ChunkEnvironmentID == nil || *dev.ChunkEnvironmentID != "env-bbb" {
		t.Fatalf("dev.ChunkEnvironmentID = %v, want %q", dev.ChunkEnvironmentID, "env-bbb")
	}
	if dev.DefaultBranch != "develop" {
		t.Fatalf("dev.DefaultBranch = %q, want %q", dev.DefaultBranch, "develop")
	}

	prod := cfg.Definitions["prod"]
	if prod.ChunkEnvironmentID != nil {
		t.Fatalf("prod.ChunkEnvironmentID = %v, want nil", prod.ChunkEnvironmentID)
	}
}

func TestLoadRunConfigMissing(t *testing.T) {
	_, err := LoadRunConfig(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestLoadRunConfigBadJSON(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{invalid`)
	_, err := LoadRunConfig(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// --- GetDefinitionByNameOrID ---

func TestGetDefinitionByNameOrID(t *testing.T) {
	cfg := &RunConfig{
		Definitions: map[string]RunDefinition{
			"dev": {
				DefinitionID:       "def-aaa",
				ChunkEnvironmentID: strPtr("env-bbb"),
				DefaultBranch:      "develop",
			},
			"prod": {
				DefinitionID:  "def-ccc",
				DefaultBranch: "main",
			},
		},
	}

	tests := []struct {
		name      string
		input     string
		wantDef   string
		wantEnv   *string
		wantBranch string
		wantErr   bool
	}{
		{
			name:       "by name with env",
			input:      "dev",
			wantDef:    "def-aaa",
			wantEnv:    strPtr("env-bbb"),
			wantBranch: "develop",
		},
		{
			name:       "by name nil env",
			input:      "prod",
			wantDef:    "def-ccc",
			wantEnv:    nil,
			wantBranch: "main",
		},
		{
			name:       "raw UUID",
			input:      "11111111-2222-3333-4444-555555555555",
			wantDef:    "11111111-2222-3333-4444-555555555555",
			wantEnv:    nil,
			wantBranch: "main",
		},
		{
			name:    "unknown name",
			input:   "staging",
			wantErr: true,
		},
		{
			name:    "invalid format",
			input:   "not-a-uuid-and-not-a-name",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defID, envID, branch, err := GetDefinitionByNameOrID(cfg, tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if defID != tt.wantDef {
				t.Fatalf("defID = %q, want %q", defID, tt.wantDef)
			}
			if branch != tt.wantBranch {
				t.Fatalf("branch = %q, want %q", branch, tt.wantBranch)
			}
			if tt.wantEnv == nil {
				if envID != nil {
					t.Fatalf("envID = %v, want nil", envID)
				}
			} else {
				if envID == nil || *envID != *tt.wantEnv {
					t.Fatalf("envID = %v, want %q", envID, *tt.wantEnv)
				}
			}
		})
	}
}

// --- TriggerRun ---

func newFakeAndClient(t *testing.T) (*fakes.FakeCircleCI, *circleci.Client) {
	t.Helper()
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	t.Setenv("CIRCLE_TOKEN", "test-token")
	t.Setenv("CIRCLECI_BASE_URL", srv.URL)

	client, err := circleci.NewClient()
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	return cci, client
}

func testCfg() *RunConfig {
	return &RunConfig{
		OrgID:     "org-111",
		ProjectID: "proj-222",
		Definitions: map[string]RunDefinition{
			"dev": {
				DefinitionID:       "def-aaa",
				ChunkEnvironmentID: strPtr("env-bbb"),
				DefaultBranch:      "develop",
			},
		},
	}
}

func TestTriggerRunHappyPath(t *testing.T) {
	cci, client := newFakeAndClient(t)
	cfg := testCfg()

	resp, err := TriggerRun(context.Background(), client, cfg, RunParams{
		Definition:     "dev",
		Prompt:         "Fix tests",
		PipelineAsTool: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.RunID == "" {
		t.Fatal("expected non-empty RunID")
	}
	if resp.PipelineID == "" {
		t.Fatal("expected non-empty PipelineID")
	}

	// Verify request body
	reqs := cci.Recorder.AllRequests()
	var body map[string]interface{}
	for _, r := range reqs {
		if r.URL.Path == "/api/v2/agents/org/org-111/project/proj-222/runs" {
			if err := json.Unmarshal(r.Body, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			break
		}
	}
	if body == nil {
		t.Fatal("no request to trigger run endpoint")
	}
	if body["agent_type"] != "prompt" {
		t.Fatalf("agent_type = %v, want prompt", body["agent_type"])
	}
	if body["definition_id"] != "def-aaa" {
		t.Fatalf("definition_id = %v, want def-aaa", body["definition_id"])
	}
	if body["checkout_branch"] != "develop" {
		t.Fatalf("checkout_branch = %v, want develop", body["checkout_branch"])
	}
	if body["trigger_source"] != "chunk-cli" {
		t.Fatalf("trigger_source = %v, want chunk-cli", body["trigger_source"])
	}

	params := body["parameters"].(map[string]interface{})
	if params["agent-type"] != "prompt" {
		t.Fatalf("agent-type = %v, want prompt", params["agent-type"])
	}
	if params["custom-prompt"] != "Fix tests" {
		t.Fatalf("custom-prompt = %v, want 'Fix tests'", params["custom-prompt"])
	}
	if params["run-pipeline-as-a-tool"] != true {
		t.Fatalf("run-pipeline-as-a-tool = %v, want true", params["run-pipeline-as-a-tool"])
	}
	if params["create-new-branch"] != false {
		t.Fatalf("create-new-branch = %v, want false", params["create-new-branch"])
	}
}

func TestTriggerRunBranchOverride(t *testing.T) {
	cci, client := newFakeAndClient(t)
	cfg := testCfg()

	_, err := TriggerRun(context.Background(), client, cfg, RunParams{
		Definition: "dev",
		Prompt:     "Fix tests",
		Branch:     "feature/custom",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range cci.Recorder.AllRequests() {
		if r.URL.Path == "/api/v2/agents/org/org-111/project/proj-222/runs" {
			var body map[string]interface{}
			if err := json.Unmarshal(r.Body, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if body["checkout_branch"] != "feature/custom" {
				t.Fatalf("checkout_branch = %v, want feature/custom", body["checkout_branch"])
			}
			return
		}
	}
	t.Fatal("no request to trigger run endpoint")
}

func TestTriggerRunNewBranch(t *testing.T) {
	cci, client := newFakeAndClient(t)
	cfg := testCfg()

	_, err := TriggerRun(context.Background(), client, cfg, RunParams{
		Definition: "dev",
		Prompt:     "Add feature",
		NewBranch:  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range cci.Recorder.AllRequests() {
		if r.URL.Path == "/api/v2/agents/org/org-111/project/proj-222/runs" {
			var body map[string]interface{}
			if err := json.Unmarshal(r.Body, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			params := body["parameters"].(map[string]interface{})
			if params["create-new-branch"] != true {
				t.Fatalf("create-new-branch = %v, want true", params["create-new-branch"])
			}
			return
		}
	}
	t.Fatal("no request to trigger run endpoint")
}

func TestTriggerRunUnknownDefinition(t *testing.T) {
	_, client := newFakeAndClient(t)
	cfg := testCfg()

	_, err := TriggerRun(context.Background(), client, cfg, RunParams{
		Definition: "staging",
		Prompt:     "Deploy",
	})
	if err == nil {
		t.Fatal("expected error for unknown definition")
	}
}

func TestTriggerRunAPIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.RunStatusCode = 500
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	t.Setenv("CIRCLE_TOKEN", "test-token")
	t.Setenv("CIRCLECI_BASE_URL", srv.URL)

	client, err := circleci.NewClient()
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	cfg := testCfg()
	_, err = TriggerRun(context.Background(), client, cfg, RunParams{
		Definition: "dev",
		Prompt:     "Fix it",
	})
	if err == nil {
		t.Fatal("expected error from API")
	}
}

func TestTriggerRunEnvIDIncluded(t *testing.T) {
	cci, client := newFakeAndClient(t)
	cfg := testCfg()

	_, err := TriggerRun(context.Background(), client, cfg, RunParams{
		Definition: "dev",
		Prompt:     "Check env",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range cci.Recorder.AllRequests() {
		if r.URL.Path == "/api/v2/agents/org/org-111/project/proj-222/runs" {
			var body map[string]interface{}
			if err := json.Unmarshal(r.Body, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if body["chunk_environment_id"] != "env-bbb" {
				t.Fatalf("chunk_environment_id = %v, want env-bbb", body["chunk_environment_id"])
			}
			return
		}
	}
	t.Fatal("no request to trigger run endpoint")
}
