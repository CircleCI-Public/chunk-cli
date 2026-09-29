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

func writePrompts(t *testing.T, dir string, names ...string) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(dir, 0o755))
	for _, name := range names {
		assert.NilError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte("review "+name), 0o644))
	}
}

// claudeResult is what claude prints with --output-format json for a review
// answering "One bug." with one finding at main.go:12.
const claudeResult = `{"type":"result","subtype":"success","is_error":false,"structured_output":{"summary":"One bug.",` +
	`"findings":[{"file":"main.go","line":12,"severity":"high","confidence":90,"claim":"nil map write","failure_scenario":"empty config panics"}]}}` + "\n"

func TestReviewNoDefaultDir(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"review"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "No .chunk/reviews directory"), "stderr: %s", result.Stderr)
}

func TestReviewTooManyDirs(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"review", "a", "b"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "at most one directory"), "stderr: %s", result.Stderr)
}

func TestReviewNoPrompts(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := filepath.Join(env.HomeDir, "prompts")
	assert.NilError(t, os.MkdirAll(dir, 0o755))

	result := binary.RunCLI(t, []string{"review", dir}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "No prompts found"), "stderr: %s", result.Stderr)
}

// setupReviewProject returns a project with a .chunk/reviews directory and a
// fake CircleCI whose sidecars answer every exec with resp.
func setupReviewProject(t *testing.T, resp *fakes.ExecResponse, prompts ...string) (*testenv.TestEnv, *fakes.FakeCircleCI, string) {
	t.Helper()
	env := testenv.NewTestEnv(t)
	env.Extra["CIRCLECI_ORG_ID"] = "org-aaa"

	sshDir := filepath.Join(env.HomeDir, ".ssh")
	assert.NilError(t, os.MkdirAll(sshDir, 0o700))
	pubKey := fakes.GenerateSSHKeypairAt(t, filepath.Join(sshDir, "chunk_ai"))
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("", 0)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	cci.ExecResponse = resp
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	env.CircleCIURL = srv.URL

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeChunkConfig(t, workDir, nil)
	writePrompts(t, filepath.Join(workDir, ".chunk", "reviews"), prompts...)
	return env, cci, workDir
}

func TestReviewRunsEachPromptOnPool(t *testing.T) {
	env, cci, workDir := setupReviewProject(t,
		&fakes.ExecResponse{CommandID: "cmd-1", Stdout: claudeResult, Stderr: "progress noise\n"},
		"api", "security", "style")

	result := binary.RunCLI(t, []string{"review", "--parallelism", "2"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	for _, name := range []string{"## api", "## security", "## style"} {
		assert.Assert(t, strings.Contains(result.Stdout, name), "stdout: %s", result.Stdout)
	}
	assert.Equal(t, strings.Count(result.Stdout, "One bug."), 3, "stdout: %s", result.Stdout)
	assert.Equal(t, strings.Count(result.Stdout, "- **high** `main.go:12` nil map write (confidence 90%)\n  empty config panics"), 3,
		"stdout: %s", result.Stdout)
	assert.Assert(t, !strings.Contains(result.Stdout, "progress noise"), "stdout: %s", result.Stdout)
	assert.Assert(t, strings.Index(result.Stdout, "## api") < strings.Index(result.Stdout, "## style"))

	execs := filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances/")
	var reviews int
	for _, r := range execs {
		if strings.HasSuffix(r.URL.Path, "/exec") && strings.Contains(string(r.Body), "--output-format") {
			reviews++
		}
	}
	assert.Equal(t, reviews, 3)

	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "review-pool.json"))
	assert.NilError(t, err)
	var state struct {
		SidecarIDs []string `json:"sidecar_ids"`
	}
	assert.NilError(t, json.Unmarshal(data, &state))
	assert.Equal(t, len(state.SidecarIDs), 2)
}

func TestReviewNoCredential(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, nil, "api")
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{"review"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "No Claude credential"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0,
		"no sidecar should boot without a key")
}

// execEnv decodes the env map from the first exec request the CLI sent.
func execEnv(t *testing.T, cci *fakes.FakeCircleCI) map[string]string {
	t.Helper()
	var req struct {
		Env map[string]string `json:"env"`
	}
	for _, r := range cci.Recorder.AllRequests() {
		if strings.Contains(r.URL.Path, "/exec") {
			assert.NilError(t, json.Unmarshal(r.Body, &req))
			return req.Env
		}
	}
	t.Fatal("no exec request recorded")
	return nil
}

func TestReviewUsesOAuthTokenWhenNoAPIKey(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: claudeResult}, "api")
	env.AnthropicKey = ""
	env.Extra["CLAUDE_CODE_OAUTH_TOKEN"] = "sk-ant-oat01-tok"

	result := binary.RunCLI(t, []string{"review"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	got := execEnv(t, cci)
	assert.Equal(t, got["CLAUDE_CODE_OAUTH_TOKEN"], "sk-ant-oat01-tok")
	_, hasKey := got["ANTHROPIC_API_KEY"]
	assert.Assert(t, !hasKey, "env: %v", got)
}

// An API key wins, so nothing changes for anyone who already has one.
func TestReviewAPIKeyBeatsOAuthToken(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: claudeResult}, "api")
	env.Extra["CLAUDE_CODE_OAUTH_TOKEN"] = "sk-ant-oat01-tok"

	result := binary.RunCLI(t, []string{"review"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	got := execEnv(t, cci)
	assert.Equal(t, got["ANTHROPIC_API_KEY"], env.AnthropicKey)
	_, hasToken := got["CLAUDE_CODE_OAUTH_TOKEN"]
	assert.Assert(t, !hasToken, "env: %v", got)
}

// One rejected credential fails every review the same way, so the pass stops
// rather than reporting the same 401 once per prompt.
func TestReviewCredentialRejected(t *testing.T) {
	env, _, workDir := setupReviewProject(t, &fakes.ExecResponse{
		CommandID: "cmd-1",
		Stdout:    "Failed to authenticate. API Error: 401 API key is invalid.",
		ExitCode:  1,
	}, "api", "security")

	result := binary.RunCLI(t, []string{"review"}, env, workDir)

	assert.Equal(t, result.ExitCode, 3, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "ANTHROPIC_API_KEY"), "stderr: %s", result.Stderr)
	assert.Assert(t, !strings.Contains(result.Stderr, "2 of 2 review(s) failed"), "stderr: %s", result.Stderr)
}

func TestReviewClaudeMissing(t *testing.T) {
	// 97 is the review script's own exit code for claude not being on PATH.
	env, _, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", ExitCode: 97}, "api", "security")

	result := binary.RunCLI(t, []string{"review"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Claude Code is not installed"), "stderr: %s", result.Stderr)
}

// A 127 comes from claude shelling out to something missing, not from claude
// being absent, so it fails that review without stopping the pass.
func TestReviewShellNotFoundIsNotClaudeMissing(t *testing.T) {
	env, _, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", ExitCode: 127}, "api", "security")

	result := binary.RunCLI(t, []string{"review"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, !strings.Contains(result.Stderr, "Claude Code is not installed"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "claude exited 127"), "stderr: %s", result.Stderr)
}

func TestReviewFailedReviewExitsNonZero(t *testing.T) {
	env, _, workDir := setupReviewProject(t,
		&fakes.ExecResponse{CommandID: "cmd-1", Stdout: "partial", Stderr: "overloaded\n", ExitCode: 1}, "api")

	result := binary.RunCLI(t, []string{"review"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stdout, "claude exited 1: overloaded"), "stdout: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stderr, "1 of 1 review(s) failed"), "stderr: %s", result.Stderr)
}

type reviewReport struct {
	Reviews []struct {
		Prompt    string `json:"prompt"`
		SidecarID string `json:"sidecar_id"`
		Summary   string `json:"summary"`
		Findings  []struct {
			File            string `json:"file"`
			Line            int    `json:"line"`
			Severity        string `json:"severity"`
			Confidence      int    `json:"confidence"`
			Claim           string `json:"claim"`
			FailureScenario string `json:"failure_scenario"`
		} `json:"findings"`
		Output          string  `json:"output"`
		Error           string  `json:"error"`
		DurationSeconds float64 `json:"duration_seconds"`
	} `json:"reviews"`
	Failed int `json:"failed"`
}

func TestReviewJSON(t *testing.T) {
	env, _, workDir := setupReviewProject(t,
		&fakes.ExecResponse{CommandID: "cmd-1", Stdout: claudeResult, Stderr: "progress noise\n"},
		"api", "security")

	result := binary.RunCLI(t, []string{"review", "--json"}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	var report reviewReport
	assert.NilError(t, json.Unmarshal([]byte(result.Stdout), &report), "stdout: %s", result.Stdout)
	assert.Equal(t, report.Failed, 0)
	assert.Equal(t, len(report.Reviews), 2)
	for i, name := range []string{"api", "security"} {
		r := report.Reviews[i]
		assert.Equal(t, r.Prompt, name)
		assert.Equal(t, r.Summary, "One bug.")
		assert.Equal(t, len(r.Findings), 1)
		f := r.Findings[0]
		assert.Equal(t, f.File, "main.go")
		assert.Equal(t, f.Line, 12)
		assert.Equal(t, f.Severity, "high")
		assert.Equal(t, f.Confidence, 90)
		assert.Equal(t, f.Claim, "nil map write")
		assert.Equal(t, f.FailureScenario, "empty config panics")
		assert.Equal(t, r.Output, "")
		assert.Equal(t, r.Error, "")
		assert.Assert(t, r.SidecarID != "")
	}
	assert.Assert(t, strings.Contains(result.Stderr, "Running 2 review(s)"), "progress belongs on stderr: %s", result.Stderr)
}

func TestReviewJSONFailedReview(t *testing.T) {
	env, _, workDir := setupReviewProject(t,
		&fakes.ExecResponse{CommandID: "cmd-1", Stdout: "partial", Stderr: "overloaded\n", ExitCode: 1}, "api")

	result := binary.RunCLI(t, []string{"review", "--json"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	var report reviewReport
	assert.NilError(t, json.Unmarshal([]byte(result.Stdout), &report), "stdout: %s", result.Stdout)
	assert.Equal(t, report.Failed, 1)
	assert.Equal(t, len(report.Reviews), 1)
	assert.Equal(t, report.Reviews[0].Output, "partial")
	assert.Equal(t, report.Reviews[0].Error, "claude exited 1: overloaded")
	assert.Assert(t, report.Reviews[0].Findings == nil, "a failed review has no findings")
	assert.Assert(t, strings.Contains(result.Stderr, "1 of 1 review(s) failed"), "stderr: %s", result.Stderr)
}
