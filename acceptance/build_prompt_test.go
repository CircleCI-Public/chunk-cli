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
	"github.com/CircleCI-Public/chunk-cli/acceptance/testutil/fixtures"
)

func TestBuildPromptHappyPath(t *testing.T) {
	// Set up fakes
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	// Set up git repo and environment
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")

	env := testutil.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	// Run the CLI
	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--top", "2",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	// Assert exit code
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	// Assert output files exist and have expected content
	assertFileExists(t, workDir, "review-prompt.md")
	assertFileExists(t, workDir, "review-prompt-details.json")
	assertFileExists(t, workDir, "review-prompt-analysis.md")
	assertFileExists(t, workDir, "review-prompt-details-pr-rankings.csv")

	// Validate details JSON structure
	detailsBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt-details.json"))
	assert.NilError(t, err)

	var details struct {
		Metadata struct {
			Organization string `json:"organization"`
		} `json:"metadata"`
		Comments []json.RawMessage `json:"comments"`
	}
	assert.NilError(t, json.Unmarshal(detailsBytes, &details))
	assert.Equal(t, details.Metadata.Organization, "test-org")
	assert.Assert(t, len(details.Comments) > 0, "expected comments in details.json")

	// Validate analysis contains our canned analysis
	analysisBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt-analysis.md"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(analysisBytes), "Code Review Pattern Analysis"))

	// Validate review prompt contains our canned prompt
	promptBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt.md"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(promptBytes), "Code Review Prompt"))

	// Validate CSV has header and data
	csvBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt-details-pr-rankings.csv"))
	assert.NilError(t, err)
	csvLines := strings.Split(strings.TrimSpace(string(csvBytes)), "\n")
	assert.Assert(t, len(csvLines) >= 2, "expected CSV header + at least 1 data row, got %d lines", len(csvLines))

	// Assert on recorded requests
	ghReqs := gh.Recorder.AllRequests()
	assert.Assert(t, len(ghReqs) >= 3,
		"expected at least 3 GitHub requests (org validation + repos + review activity), got %d", len(ghReqs))
	for _, req := range ghReqs {
		assert.Assert(t, req.Header.Get("Authorization") != "",
			"expected authorization header on GitHub request to %s", req.URL.Path)
	}

	anthropicReqs := anthropic.Recorder.AllRequests()
	messageReqs := filterByPath(anthropicReqs, "/v1/messages")
	assert.Equal(t, len(messageReqs), 2, "expected exactly 2 Anthropic /v1/messages requests")
	for _, req := range anthropicReqs {
		assert.Assert(t, req.Header.Get("X-Api-Key") != "",
			"expected x-api-key header on Anthropic request")
	}
}

func TestBuildPromptAutoDetectOrg(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("auto-repo"))
	gh.SetReviewActivity("auto-repo", fixtures.ReviewActivityResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	// Git remote determines org/repo
	workDir := testutil.SetupGitRepo(t, "auto-org", "auto-repo")

	env := testutil.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	// Verify org was auto-detected
	detailsBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt-details.json"))
	assert.NilError(t, err)

	var details struct {
		Metadata struct {
			Organization string `json:"organization"`
		} `json:"metadata"`
	}
	assert.NilError(t, json.Unmarshal(detailsBytes, &details))
	assert.Equal(t, details.Metadata.Organization, "auto-org")
}

func TestBuildPromptMissingGithubToken(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")

	env := testutil.NewTestEnv(t)
	env.GithubToken = "" // no token

	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "GITHUB_TOKEN"),
		"expected error to mention GITHUB_TOKEN, got: %s", combined)
}

func TestBuildPromptMissingAnthropicKey(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")

	env := testutil.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicKey = "" // no key

	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t,
		strings.Contains(combined, "ANTHROPIC_API_KEY") || strings.Contains(combined, "anthropic") || strings.Contains(combined, "API key"),
		"expected error to mention API key, got: %s", combined)
}

func TestBuildPromptWithSinceFlag(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--since", "2025-01-01",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}
	assertFileExists(t, workDir, "review-prompt.md")
}

func TestBuildPromptWithMaxComments(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--max-comments", "1",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}
	assertFileExists(t, workDir, "review-prompt.md")
}

func TestBuildPromptWithIncludeAttribution(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--include-attribution",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}
	assertFileExists(t, workDir, "review-prompt.md")
}

func TestBuildPromptWithModelOverrides(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--analyze-model", "claude-haiku-4-5-20251001",
		"--prompt-model", "claude-haiku-4-5-20251001",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}
	assertFileExists(t, workDir, "review-prompt.md")

	// Verify the Anthropic requests used the specified model
	anthropicReqs := anthropic.Recorder.AllRequests()
	messageReqs := filterByPath(anthropicReqs, "/v1/messages")
	assert.Equal(t, len(messageReqs), 2, "expected 2 Anthropic /v1/messages requests")

	for i, req := range messageReqs {
		var body struct {
			Model string `json:"model"`
		}
		err := json.Unmarshal(req.Body, &body)
		assert.NilError(t, err, "failed to parse Anthropic request body %d", i)
		assert.Equal(t, body.Model, "claude-haiku-4-5-20251001",
			"expected model override in request %d", i)
	}
}

func TestBuildPromptOrgWithoutRepos(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)

	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code when --org without --repos")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "--repos"),
		"expected error to mention --repos, got: %s", combined)
}

func TestBuildPromptBotFiltering(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityWithBotResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--top", "5",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	// Verify bot comments are excluded from details JSON
	detailsBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt-details.json"))
	assert.NilError(t, err)

	detailsStr := string(detailsBytes)
	assert.Assert(t, !strings.Contains(detailsStr, "dependabot[bot]"),
		"expected bot reviewer to be filtered out of details JSON")
	assert.Assert(t, !strings.Contains(detailsStr, "This dependency update is safe to merge"),
		"expected bot comment body to be filtered out of details JSON")

	// Verify human reviewers ARE present
	assert.Assert(t, strings.Contains(detailsStr, "reviewer-alice"),
		"expected human reviewer alice in details JSON")
	assert.Assert(t, strings.Contains(detailsStr, "reviewer-bob"),
		"expected human reviewer bob in details JSON")
}

func TestBuildPromptFooter(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	promptBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt.md"))
	assert.NilError(t, err)
	promptStr := string(promptBytes)

	assert.Assert(t, strings.Contains(promptStr, "*Generated:"),
		"expected footer with Generated timestamp, got: %s", promptStr)
	assert.Assert(t, strings.Contains(promptStr, "*Model:"),
		"expected footer with Model, got: %s", promptStr)
	assert.Assert(t, strings.Contains(promptStr, "*Source:"),
		"expected footer with Source path, got: %s", promptStr)
}

func TestBuildPromptSinceDateFormat(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := testutil.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--since", "2025-06-15",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	detailsBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt-details.json"))
	assert.NilError(t, err)

	var details struct {
		Metadata struct {
			Since string `json:"since"`
		} `json:"metadata"`
	}
	assert.NilError(t, json.Unmarshal(detailsBytes, &details))
	// MUT-014: .slice(0,10) gives YYYY-MM-DD, mutation to .slice(0,7) would give YYYY-MM
	assert.Equal(t, details.Metadata.Since, "2025-06-15",
		"expected since in YYYY-MM-DD format")
}

// helpers

func assertFileExists(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	assert.NilError(t, err, "expected file %s to exist", name)
	assert.Assert(t, info.Size() > 0, "expected file %s to be non-empty", name)
}

func filterByPath(reqs []testutil.RecordedRequest, path string) []testutil.RecordedRequest {
	var filtered []testutil.RecordedRequest
	for _, r := range reqs {
		if r.URL.Path == path {
			filtered = append(filtered, r)
		}
	}
	return filtered
}
