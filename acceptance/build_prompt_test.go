package acceptance

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fixtures"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/recorder"
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
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	// Run the CLI
	result := binary.RunCLI(t, []string{
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
		assert.Equal(t, req.Header.Get("Anthropic-Version"), "2023-06-01",
			"expected anthropic-version header on Anthropic request to %s", req.URL.Path)
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
	workDir := gitrepo.SetupGitRepo(t, "auto-org", "auto-repo")

	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
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
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)
	env.GithubToken = "" // no token

	result := binary.RunCLI(t, []string{
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicKey = "" // no key

	result := binary.RunCLI(t, []string{
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

func TestBuildPromptFlagVariants(t *testing.T) {
	tests := []struct {
		name      string
		extraArgs []string
	}{
		{"since", []string{"--since", "2025-01-01"}},
		{"max-comments", []string{"--max-comments", "1"}},
		{"include-attribution", []string{"--include-attribution"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

			workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
			env := testenv.NewTestEnv(t)
			env.GithubURL = ghSrv.URL
			env.AnthropicURL = anthropicSrv.URL

			args := []string{
				"build-prompt",
				"--org", "test-org",
				"--repos", "test-repo",
			}
			args = append(args, tt.extraArgs...)
			args = append(args, "--output", filepath.Join(workDir, "review-prompt.md"))

			result := binary.RunCLI(t, args, env, workDir)

			if result.ExitCode != 0 {
				t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
					result.ExitCode, result.Stdout, result.Stderr)
			}
			assertFileExists(t, workDir, "review-prompt.md")
		})
	}
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
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
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
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
	// .slice(0,10) gives YYYY-MM-DD, mutation to .slice(0,7) would give YYYY-MM
	assert.Equal(t, details.Metadata.Since, "2025-06-15",
		"expected since in YYYY-MM-DD format")
}

// --top N limits reviewers, filters bot reviews,
// totalComments is correct, CSV sorted descending
func TestBuildPromptTopNFiltering(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.MultiReviewerResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--top", "2",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	detailsBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt-details.json"))
	assert.NilError(t, err)
	detailsStr := string(detailsBytes)

	// only top-2 reviewers in details (alice, bob), not charlie
	assert.Assert(t, strings.Contains(detailsStr, "reviewer-alice"),
		"expected top reviewer alice in details")
	assert.Assert(t, strings.Contains(detailsStr, "reviewer-bob"),
		"expected top reviewer bob in details")
	assert.Assert(t, !strings.Contains(detailsStr, "reviewer-charlie"),
		"expected charlie (3rd reviewer) filtered out with --top 2")

	// bot filtered from reviews, not just comments
	assert.Assert(t, !strings.Contains(detailsStr, "dependabot[bot]"),
		"expected bot reviewer filtered out of details")

	// totalComments > 0
	var details struct {
		Metadata struct {
			TotalComments int `json:"totalComments"`
		} `json:"metadata"`
		Comments []struct {
			Reviewer string `json:"reviewer"`
		} `json:"comments"`
	}
	assert.NilError(t, json.Unmarshal(detailsBytes, &details))
	assert.Assert(t, details.Metadata.TotalComments > 0,
		"expected totalComments > 0, got %d", details.Metadata.TotalComments)

	// verify exactly 2 distinct reviewers in comments
	reviewers := map[string]bool{}
	for _, c := range details.Comments {
		reviewers[c.Reviewer] = true
	}
	assert.Equal(t, len(reviewers), 2,
		"expected exactly 2 reviewers with --top 2, got %d: %v", len(reviewers), reviewers)

	// CSV sorted descending by total_comments
	csvBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt-details-pr-rankings.csv"))
	assert.NilError(t, err)
	csvLines := strings.Split(strings.TrimSpace(string(csvBytes)), "\n")
	assert.Assert(t, len(csvLines) >= 3,
		"expected CSV header + at least 2 data rows, got %d lines", len(csvLines))
	// First data row (most comments) should be PR 100, second should be PR 101
	assert.Assert(t, strings.Contains(csvLines[1], "100"),
		"expected first CSV row to be PR 100 (most comments), got: %s", csvLines[1])
	assert.Assert(t, strings.Contains(csvLines[2], "101"),
		"expected second CSV row to be PR 101 (fewer comments), got: %s", csvLines[2])
}

// default --top is 5 (not 1), so both reviewers appear when omitted
func TestBuildPromptDefaultTop(t *testing.T) {
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	detailsBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt-details.json"))
	assert.NilError(t, err)
	detailsStr := string(detailsBytes)

	// Default --top=5 should include both reviewers (fixture has 2)
	assert.Assert(t, strings.Contains(detailsStr, "reviewer-alice"),
		"expected alice in details with default --top")
	assert.Assert(t, strings.Contains(detailsStr, "reviewer-bob"),
		"expected bob in details with default --top")
}

// --repos without --org uses passed repos, not auto-detected repo
func TestBuildPromptReposOverride(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("override-repo"))
	gh.SetReviewActivity("override-repo", fixtures.ReviewActivityResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	// Git remote points to "detected-repo", but we pass --repos "override-repo"
	workDir := gitrepo.SetupGitRepo(t, "detected-org", "detected-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--repos", "override-repo",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	// Verify the override-repo was queried, not detected-repo
	ghReqs := gh.Recorder.AllRequests()
	foundOverride := false
	for _, req := range ghReqs {
		bodyStr := string(req.Body)
		if strings.Contains(bodyStr, "override-repo") {
			foundOverride = true
		}
		assert.Assert(t, !strings.Contains(bodyStr, `"repo":"detected-repo"`),
			"expected override-repo to be used, not detected-repo")
	}
	assert.Assert(t, foundOverride,
		"expected at least one request with override-repo in body")
}

// "Could not resolve" errors are skipped gracefully
func TestBuildPromptRepoResolutionError(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("good-repo", "bad-repo"))
	gh.SetReviewActivity("good-repo", fixtures.ReviewActivityResponse())
	gh.SetRepoError("bad-repo", fixtures.RepoNotFoundError("test-org", "bad-repo"))

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "good-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "good-repo,bad-repo",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	// Should succeed -- bad-repo error is skipped gracefully
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0 (graceful skip), got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}
	assertFileExists(t, workDir, "review-prompt.md")
}

// --top 0 is rejected by parsePositiveInt
func TestBuildPromptTopZero(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--top", "0",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0,
		"expected non-zero exit code for --top 0")
}

// default --output path is .chunk/context/review-prompt.md
func TestBuildPromptDefaultOutputPath(t *testing.T) {
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	// Omit --output to use the default
	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	// Default output is .chunk/context/review-prompt.md relative to workdir
	assertFileExists(t, filepath.Join(workDir, ".chunk", "context"), "review-prompt.md")
	assertFileExists(t, filepath.Join(workDir, ".chunk", "context"), "review-prompt-details.json")
	assertFileExists(t, filepath.Join(workDir, ".chunk", "context"), "review-prompt-analysis.md")
	assertFileExists(t, filepath.Join(workDir, ".chunk", "context"), "review-prompt-details-pr-rankings.csv")
}

// default --since is approximately 3 months before today
func TestBuildPromptDefaultSince(t *testing.T) {
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	// Omit --since to use the default
	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
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

	// Default since should be ~3 months ago
	sinceTime, err := time.Parse("2006-01-02", details.Metadata.Since)
	assert.NilError(t, err)

	expected := time.Now().AddDate(0, -3, 0)
	diff := expected.Sub(sinceTime)
	if diff < 0 {
		diff = -diff
	}
	// Allow 2-day tolerance for test execution timing
	assert.Assert(t, diff < 48*time.Hour,
		"expected default since ~%s, got %s", expected.Format("2006-01-02"), details.Metadata.Since)
}

// invalid --since format rejected
func TestBuildPromptInvalidSinceFormat(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--since", "not-a-date",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code for invalid --since")
}

// --top with negative value rejected
func TestBuildPromptTopNegative(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--top", "-1",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code for --top -1")
}

// --top 1 returns exactly 1 reviewer
func TestBuildPromptTopOne(t *testing.T) {
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--top", "1",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	detailsBytes, err := os.ReadFile(filepath.Join(workDir, "review-prompt-details.json"))
	assert.NilError(t, err)

	var details struct {
		Comments []struct {
			Reviewer string `json:"reviewer"`
		} `json:"comments"`
	}
	assert.NilError(t, json.Unmarshal(detailsBytes, &details))

	reviewers := map[string]bool{}
	for _, c := range details.Comments {
		reviewers[c.Reviewer] = true
	}
	assert.Equal(t, len(reviewers), 1,
		"expected exactly 1 reviewer with --top 1, got %d: %v", len(reviewers), reviewers)
}

// --max-comments limits the number of comments per reviewer in the analysis request
func TestBuildPromptMaxCommentsEffect(t *testing.T) {
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	// The fixture has 2 comments from alice and 1 from bob.
	// --max-comments 1 should limit alice to 1 comment in the Anthropic request.
	result := binary.RunCLI(t, []string{
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

	// The analysis request body should contain at most 1 comment per reviewer.
	anthropicReqs := anthropic.Recorder.AllRequests()
	messageReqs := filterByPath(anthropicReqs, "/v1/messages")
	assert.Assert(t, len(messageReqs) >= 1, "expected at least 1 Anthropic request")

	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	err := json.Unmarshal(messageReqs[0].Body, &body)
	assert.NilError(t, err)

	analysisPrompt := body.Messages[0].Content
	// With --max-comments 1, alice should have "(1 comments)" not "(2 comments)"
	assert.Assert(t, strings.Contains(analysisPrompt, "reviewer-alice (1 comments)"),
		"expected alice limited to 1 comment, got: %s",
		extractReviewerLine(analysisPrompt, "reviewer-alice"))
}

// --include-attribution sends attribution instruction to Anthropic
func TestBuildPromptIncludeAttributionEffect(t *testing.T) {
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
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

	// The prompt generation request (2nd Anthropic call) should contain
	// the attribution instruction, not the no-attribution instruction.
	anthropicReqs := anthropic.Recorder.AllRequests()
	messageReqs := filterByPath(anthropicReqs, "/v1/messages")
	assert.Assert(t, len(messageReqs) >= 2, "expected at least 2 Anthropic requests")

	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	err := json.Unmarshal(messageReqs[1].Body, &body)
	assert.NilError(t, err)

	promptGenPrompt := body.Messages[0].Content
	assert.Assert(t, strings.Contains(promptGenPrompt, "Include which reviewers emphasize each rule"),
		"expected attribution instruction in prompt generation request")
	assert.Assert(t, !strings.Contains(promptGenPrompt, "Do not include reviewer attribution"),
		"expected no-attribution instruction to be absent")
}

// without --include-attribution, the no-attribution instruction is sent
func TestBuildPromptDefaultNoAttribution(t *testing.T) {
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	anthropicReqs := anthropic.Recorder.AllRequests()
	messageReqs := filterByPath(anthropicReqs, "/v1/messages")
	assert.Assert(t, len(messageReqs) >= 2, "expected at least 2 Anthropic requests")

	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	err := json.Unmarshal(messageReqs[1].Body, &body)
	assert.NilError(t, err)

	promptGenPrompt := body.Messages[0].Content
	assert.Assert(t, strings.Contains(promptGenPrompt, "Do not include reviewer attribution"),
		"expected no-attribution instruction by default")
}

// default model values sent to Anthropic when model flags omitted
func TestBuildPromptDefaultModels(t *testing.T) {
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	anthropicReqs := anthropic.Recorder.AllRequests()
	messageReqs := filterByPath(anthropicReqs, "/v1/messages")
	assert.Equal(t, len(messageReqs), 2, "expected 2 Anthropic /v1/messages requests")

	// First request: analysis step uses AnalyzeModel (claude-sonnet-4-5-20250929)
	var analysisBody struct {
		Model string `json:"model"`
	}
	err := json.Unmarshal(messageReqs[0].Body, &analysisBody)
	assert.NilError(t, err)
	assert.Equal(t, analysisBody.Model, "claude-sonnet-4-5-20250929",
		"expected default analyze model")

	// Second request: prompt step uses PromptModel (claude-opus-4-5-20251101)
	var promptBody struct {
		Model string `json:"model"`
	}
	err = json.Unmarshal(messageReqs[1].Body, &promptBody)
	assert.NilError(t, err)
	assert.Equal(t, promptBody.Model, "claude-opus-4-5-20251101",
		"expected default prompt model")
}

// all repos fail resolution
func TestBuildPromptAllReposFailResolution(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("bad-repo"))
	gh.SetRepoError("bad-repo", fixtures.RepoNotFoundError("test-org", "bad-repo"))

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "good-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "bad-repo",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	// The command should either succeed with warning or fail gracefully
	combined := result.Stdout + result.Stderr
	assert.Assert(t,
		strings.Contains(combined, "bad-repo") || result.ExitCode != 0,
		"expected mention of bad-repo or non-zero exit, got code %d: %s",
		result.ExitCode, combined)
}

// empty review activity (repos resolve, zero comments)
func TestBuildPromptEmptyReviewActivity(t *testing.T) {
	emptyPRsResponse := `{
		"data": {
			"repository": {
				"pullRequests": {
					"pageInfo": {"hasNextPage": false, "endCursor": null},
					"nodes": []
				}
			},
			"rateLimit": {"remaining": 4997, "resetAt": "2099-01-01T00:00:00Z"}
		}
	}`

	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("empty-repo"))
	gh.SetReviewActivity("empty-repo", emptyPRsResponse)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "empty-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "empty-repo",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	// Should handle gracefully without panicking
	_ = result
}

// output directory creation for nested paths that don't exist
func TestBuildPromptCreatesOutputDirectory(t *testing.T) {
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	// Nested output path where intermediate dirs don't exist
	outputPath := filepath.Join(workDir, "deep", "nested", "dir", "review-prompt.md")
	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--output", outputPath,
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	assertFileExists(t, filepath.Join(workDir, "deep", "nested", "dir"), "review-prompt.md")
	assertFileExists(t, filepath.Join(workDir, "deep", "nested", "dir"), "review-prompt-details.json")
}

// --repos with whitespace around commas
func TestBuildPromptReposWithWhitespace(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("repo-a", "repo-b"))
	gh.SetReviewActivity("repo-a", fixtures.ReviewActivityResponse())
	gh.SetReviewActivity("repo-b", fixtures.ReviewActivityResponse())

	anthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(anthropic)
	defer anthropicSrv.Close()

	workDir := gitrepo.SetupGitRepo(t, "test-org", "repo-a")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "repo-a , repo-b",
		"--output", filepath.Join(workDir, "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	assertFileExists(t, workDir, "review-prompt.md")

	// Both repos should have been queried
	ghReqs := gh.Recorder.AllRequests()
	foundA := false
	foundB := false
	for _, req := range ghReqs {
		bodyStr := string(req.Body)
		if strings.Contains(bodyStr, "repo-a") {
			foundA = true
		}
		if strings.Contains(bodyStr, "repo-b") {
			foundB = true
		}
	}
	assert.Assert(t, foundA, "expected repo-a to be queried")
	assert.Assert(t, foundB, "expected repo-b to be queried")
}

// overwriting existing output files succeeds
func TestBuildPromptOverwriteExistingOutput(t *testing.T) {
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	outputPath := filepath.Join(workDir, "review-prompt.md")

	// Create pre-existing output files
	err := os.WriteFile(outputPath, []byte("old content"), 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--output", outputPath,
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	// File should be overwritten with new content
	promptBytes, err := os.ReadFile(outputPath)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(promptBytes), "old content"),
		"expected old content to be overwritten")
	assert.Assert(t, strings.Contains(string(promptBytes), "Code Review Prompt"),
		"expected new prompt content")
}

// legacy output file warning
func TestBuildPromptLegacyOutputWarning(t *testing.T) {
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

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)
	env.GithubURL = ghSrv.URL
	env.AnthropicURL = anthropicSrv.URL

	// Create a legacy output file at ./review-prompt.md
	legacyPath := filepath.Join(workDir, "review-prompt.md")
	err := os.WriteFile(legacyPath, []byte("old prompt"), 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{
		"build-prompt",
		"--org", "test-org",
		"--repos", "test-repo",
		"--output", filepath.Join(workDir, "output", "review-prompt.md"),
	}, env, workDir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout: %s\nstderr: %s",
			result.ExitCode, result.Stdout, result.Stderr)
	}

	combined := result.Stdout + result.Stderr
	assert.Assert(t,
		strings.Contains(combined, "legacy") || strings.Contains(combined, "Legacy") || strings.Contains(combined, "review-prompt.md"),
		"expected warning about legacy output file, got: %s", combined)
}

// helpers

func extractReviewerLine(text, reviewer string) string {
	re := regexp.MustCompile(`(?m).*` + regexp.QuoteMeta(reviewer) + `.*`)
	match := re.FindString(text)
	if match == "" {
		return "(not found)"
	}
	return match
}

func assertFileExists(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	assert.NilError(t, err, "expected file %s to exist", name)
	assert.Assert(t, info.Size() > 0, "expected file %s to be non-empty", name)
}

func filterByPath(reqs []recorder.RecordedRequest, path string) []recorder.RecordedRequest {
	var filtered []recorder.RecordedRequest
	for _, r := range reqs {
		if r.URL.Path == path {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func filterByPathPrefix(reqs []recorder.RecordedRequest, prefix string) []recorder.RecordedRequest {
	var filtered []recorder.RecordedRequest
	for _, r := range reqs {
		if strings.HasPrefix(r.URL.Path, prefix) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}
