package buildprompt

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	ghpkg "github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fixtures"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// --- Run (orchestrator) integration tests ---

func TestRunHappyPath(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())

	fakeAnthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(fakeAnthropic)
	defer anthropicSrv.Close()

	t.Setenv("GITHUB_TOKEN", "fake-token")
	t.Setenv("GITHUB_API_URL", ghSrv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-fake")
	t.Setenv("ANTHROPIC_BASE_URL", anthropicSrv.URL)

	ghClient, err := ghpkg.New()
	assert.NilError(t, err)
	anthropicClient, err := anthropic.New()
	assert.NilError(t, err)

	outDir := t.TempDir()
	outputPath := filepath.Join(outDir, "prompt.md")

	var stderr bytes.Buffer
	streams := iostream.Streams{Out: &bytes.Buffer{}, Err: &stderr}

	err = Run(context.Background(), Options{
		Org:          "test-org",
		Repos:        []string{"test-repo"},
		Top:          5,
		Since:        time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		OutputPath:   outputPath,
		AnalyzeModel: "claude-sonnet-4-6",
		PromptModel:  "claude-sonnet-4-6",
	}, ghClient, anthropicClient, streams)
	assert.NilError(t, err)

	// All output files created
	paths := DeriveOutputPaths(outputPath)
	for _, p := range []string{paths.PromptPath, paths.DetailsPath, paths.AnalysisPath, paths.CSVPath} {
		info, statErr := os.Stat(p)
		assert.NilError(t, statErr, "expected file %s", p)
		assert.Assert(t, info.Size() > 0)
	}

	// Details JSON has correct metadata
	detailsBytes, err := os.ReadFile(paths.DetailsPath)
	assert.NilError(t, err)
	var details DetailsJSON
	assert.NilError(t, json.Unmarshal(detailsBytes, &details))
	assert.Equal(t, details.Metadata.Organization, "test-org")
	assert.Assert(t, details.Metadata.TotalComments > 0)

	// Prompt file contains canned response and footer
	promptBytes, err := os.ReadFile(paths.PromptPath)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(promptBytes), "Code Review Prompt"))
	assert.Assert(t, strings.Contains(string(promptBytes), "*Generated:"))

	// Analysis file contains canned analysis wrapped in report
	analysisBytes, err := os.ReadFile(paths.AnalysisPath)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(analysisBytes), "Review Pattern Analysis"))

	// Stderr has step progress
	assert.Assert(t, strings.Contains(stderr.String(), "Step 1/3"))
	assert.Assert(t, strings.Contains(stderr.String(), "Step 2/3"))
	assert.Assert(t, strings.Contains(stderr.String(), "Step 3/3"))
}

func TestRunNoReposFound(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	// Default: empty repos response (no SetOrgRepos called)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()

	t.Setenv("GITHUB_TOKEN", "fake-token")
	t.Setenv("GITHUB_API_URL", ghSrv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-fake")

	ghClient, err := ghpkg.New()
	assert.NilError(t, err)
	anthropicClient, err := anthropic.New()
	assert.NilError(t, err)

	var stderr bytes.Buffer
	streams := iostream.Streams{Out: &bytes.Buffer{}, Err: &stderr}

	// Pass empty repos so FetchOrgRepos queries the API (which returns empty)
	err = Run(context.Background(), Options{
		Org:        "test-org",
		Repos:      nil,
		Top:        5,
		OutputPath: filepath.Join(t.TempDir(), "prompt.md"),
	}, ghClient, anthropicClient, streams)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(stderr.String(), "No repositories found"))
}

func TestRunSkipsRepoResolutionErrors(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("good-repo", "bad-repo"))
	gh.SetReviewActivity("good-repo", fixtures.ReviewActivityResponse())
	gh.SetRepoError("bad-repo", fixtures.RepoNotFoundError("test-org", "bad-repo"))

	fakeAnthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(fakeAnthropic)
	defer anthropicSrv.Close()

	t.Setenv("GITHUB_TOKEN", "fake-token")
	t.Setenv("GITHUB_API_URL", ghSrv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-fake")
	t.Setenv("ANTHROPIC_BASE_URL", anthropicSrv.URL)

	ghClient, err := ghpkg.New()
	assert.NilError(t, err)
	anthropicClient, err := anthropic.New()
	assert.NilError(t, err)

	outDir := t.TempDir()
	outputPath := filepath.Join(outDir, "prompt.md")

	var stderr bytes.Buffer
	streams := iostream.Streams{Out: &bytes.Buffer{}, Err: &stderr}

	err = Run(context.Background(), Options{
		Org:          "test-org",
		Repos:        []string{"good-repo", "bad-repo"},
		Top:          5,
		OutputPath:   outputPath,
		AnalyzeModel: "claude-sonnet-4-6",
		PromptModel:  "claude-sonnet-4-6",
	}, ghClient, anthropicClient, streams)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(stderr.String(), "Skipping bad-repo"))
}

func TestRunWithMaxComments(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.MultiReviewerResponse())

	fakeAnthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(fakeAnthropic)
	defer anthropicSrv.Close()

	t.Setenv("GITHUB_TOKEN", "fake-token")
	t.Setenv("GITHUB_API_URL", ghSrv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-fake")
	t.Setenv("ANTHROPIC_BASE_URL", anthropicSrv.URL)

	ghClient, err := ghpkg.New()
	assert.NilError(t, err)
	anthropicClient, err := anthropic.New()
	assert.NilError(t, err)

	outDir := t.TempDir()
	outputPath := filepath.Join(outDir, "prompt.md")

	streams := iostream.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}

	err = Run(context.Background(), Options{
		Org:          "test-org",
		Repos:        []string{"test-repo"},
		Top:          5,
		MaxComments:  1,
		OutputPath:   outputPath,
		AnalyzeModel: "claude-sonnet-4-6",
		PromptModel:  "claude-sonnet-4-6",
	}, ghClient, anthropicClient, streams)
	assert.NilError(t, err)

	// Verify the anthropic request was made (analysis prompt built with limited comments)
	reqs := fakeAnthropic.Recorder.AllRequests()
	assert.Assert(t, len(reqs) >= 2, "expected at least 2 anthropic requests")
}

func TestRunRetryOnTokenLimit(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetOrgRepos(fixtures.OrgReposResponse("test-repo"))
	gh.SetReviewActivity("test-repo", fixtures.MultiReviewerResponse())

	fakeAnthropic := fakes.NewFakeAnthropic(
		fixtures.AnalysisResponse,
		fixtures.PromptResponse,
	)
	// First 2 analysis attempts return token limit errors, third succeeds.
	fakeAnthropic.SetTokenLimitErrors(2)

	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()
	anthropicSrv := httptest.NewServer(fakeAnthropic)
	defer anthropicSrv.Close()

	t.Setenv("GITHUB_TOKEN", "fake-token")
	t.Setenv("GITHUB_API_URL", ghSrv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-fake")
	t.Setenv("ANTHROPIC_BASE_URL", anthropicSrv.URL)

	ghClient, err := ghpkg.New()
	assert.NilError(t, err)
	anthropicClient, err := anthropic.New()
	assert.NilError(t, err)

	outDir := t.TempDir()
	outputPath := filepath.Join(outDir, "prompt.md")

	var stderr bytes.Buffer
	streams := iostream.Streams{Out: &bytes.Buffer{}, Err: &stderr}

	err = Run(context.Background(), Options{
		Org:          "test-org",
		Repos:        []string{"test-repo"},
		Top:          5,
		Since:        time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		OutputPath:   outputPath,
		AnalyzeModel: "claude-sonnet-4-6",
		PromptModel:  "claude-sonnet-4-6",
	}, ghClient, anthropicClient, streams)
	assert.NilError(t, err)

	// Verify retry messages appeared in stderr
	assert.Assert(t, strings.Contains(stderr.String(), "Token limit exceeded"))

	// Verify output files were created
	paths := DeriveOutputPaths(outputPath)
	for _, p := range []string{paths.PromptPath, paths.DetailsPath, paths.AnalysisPath} {
		info, statErr := os.Stat(p)
		assert.NilError(t, statErr, "expected file %s", p)
		assert.Assert(t, info.Size() > 0)
	}
}

func TestRunMissingGithubToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// With the decoupled auth, client construction fails before Run is called.
	_, err := ghpkg.New()
	assert.Assert(t, err != nil, "expected error when GITHUB_TOKEN is missing")
}

func TestRunMissingAnthropicKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// With the decoupled auth, client construction fails before Run is called.
	_, err := anthropic.New()
	assert.Assert(t, err != nil, "expected error when Anthropic key is missing")
}

// --- ResolveOrgAndRepos ---

func TestResolveOrgAndRepos(t *testing.T) {
	t.Run("org with repos", func(t *testing.T) {
		org, repos, err := ResolveOrgAndRepos("my-org", "repo-a,repo-b", "")
		assert.NilError(t, err)
		assert.Equal(t, org, "my-org")
		assert.DeepEqual(t, repos, []string{"repo-a", "repo-b"})
	})

	t.Run("org without repos errors", func(t *testing.T) {
		_, _, err := ResolveOrgAndRepos("my-org", "", "")
		assert.Assert(t, err != nil)
		assert.Assert(t, strings.Contains(err.Error(), "--repos"))
	})

	t.Run("auto-detect from git remote", func(t *testing.T) {
		workDir := gitrepo.SetupGitRepo(t, "detected-org", "detected-repo")

		org, repos, resolveErr := ResolveOrgAndRepos("", "", workDir)
		assert.NilError(t, resolveErr)
		assert.Equal(t, org, "detected-org")
		assert.DeepEqual(t, repos, []string{"detected-repo"})
	})

	t.Run("auto-detect org with explicit repos", func(t *testing.T) {
		workDir := gitrepo.SetupGitRepo(t, "detected-org", "detected-repo")

		org, repos, resolveErr := ResolveOrgAndRepos("", "override-repo", workDir)
		assert.NilError(t, resolveErr)
		assert.Equal(t, org, "detected-org")
		assert.DeepEqual(t, repos, []string{"override-repo"})
	})
}

// --- DeriveOutputPaths ---

func TestDeriveOutputPaths(t *testing.T) {
	paths := DeriveOutputPaths("/tmp/review-prompt.md")
	assert.Equal(t, paths.PromptPath, "/tmp/review-prompt.md")
	assert.Equal(t, paths.DetailsPath, "/tmp/review-prompt-details.json")
	assert.Equal(t, paths.AnalysisPath, "/tmp/review-prompt-analysis.md")
	assert.Equal(t, paths.CSVPath, "/tmp/review-prompt-details-pr-rankings.csv")
}

func TestDeriveOutputPathsNoExtension(t *testing.T) {
	paths := DeriveOutputPaths("/tmp/review-prompt")
	assert.Equal(t, paths.PromptPath, "/tmp/review-prompt")
	assert.Equal(t, paths.DetailsPath, "/tmp/review-prompt-details.json")
}

// --- AggregateActivity ---

func TestAggregateActivity(t *testing.T) {
	t.Run("merges across repos", func(t *testing.T) {
		repo1 := map[string]*ghpkg.UserActivity{
			"alice": {Login: "alice", TotalActivity: 3, ReviewsGiven: 2, Approvals: 1, ReposActiveIn: map[string]bool{"repo1": true}},
			"bob":   {Login: "bob", TotalActivity: 1, ReviewsGiven: 1, ReposActiveIn: map[string]bool{"repo1": true}},
		}
		repo2 := map[string]*ghpkg.UserActivity{
			"alice": {Login: "alice", TotalActivity: 2, ReviewsGiven: 1, ChangesRequested: 1, ReposActiveIn: map[string]bool{"repo2": true}},
		}

		result := AggregateActivity([]map[string]*ghpkg.UserActivity{repo1, repo2})

		// Sorted by TotalActivity descending
		assert.Equal(t, len(result), 2)
		assert.Equal(t, result[0].Login, "alice")
		assert.Equal(t, result[0].TotalActivity, 5)
		assert.Equal(t, result[0].ReviewsGiven, 3)
		assert.Equal(t, result[0].Approvals, 1)
		assert.Equal(t, result[0].ChangesRequested, 1)
		assert.Assert(t, result[0].ReposActiveIn["repo1"])
		assert.Assert(t, result[0].ReposActiveIn["repo2"])
		assert.Equal(t, result[1].Login, "bob")
	})

	t.Run("empty input", func(t *testing.T) {
		result := AggregateActivity(nil)
		assert.Equal(t, len(result), 0)
	})
}

// --- TopN ---

func TestTopN(t *testing.T) {
	activities := []*ghpkg.UserActivity{
		{Login: "alice", TotalActivity: 10},
		{Login: "bob", TotalActivity: 5},
		{Login: "charlie", TotalActivity: 1},
	}

	t.Run("truncates", func(t *testing.T) {
		top, err := TopN(activities, 2)
		assert.NilError(t, err)
		assert.Equal(t, len(top), 2)
		assert.Equal(t, top[0].Login, "alice")
		assert.Equal(t, top[1].Login, "bob")
	})

	t.Run("n greater than length", func(t *testing.T) {
		top, err := TopN(activities, 10)
		assert.NilError(t, err)
		assert.Equal(t, len(top), 3)
	})

	t.Run("n equals length", func(t *testing.T) {
		top, err := TopN(activities, 3)
		assert.NilError(t, err)
		assert.Equal(t, len(top), 3)
	})

	t.Run("zero errors", func(t *testing.T) {
		_, err := TopN(activities, 0)
		assert.Assert(t, err != nil)
		assert.Assert(t, strings.Contains(err.Error(), "positive"))
	})

	t.Run("negative errors", func(t *testing.T) {
		_, err := TopN(activities, -1)
		assert.Assert(t, err != nil)
	})
}

// --- AggregateDetails ---

func TestAggregateDetails(t *testing.T) {
	batch1 := []ghpkg.ReviewCommentDetail{
		{Reviewer: "alice", Body: "comment1"},
	}
	batch2 := []ghpkg.ReviewCommentDetail{
		{Reviewer: "bob", Body: "comment2"},
		{Reviewer: "alice", Body: "comment3"},
	}

	result := AggregateDetails([][]ghpkg.ReviewCommentDetail{batch1, batch2})
	assert.Equal(t, len(result), 3)
}

func TestAggregateDetailsEmpty(t *testing.T) {
	result := AggregateDetails(nil)
	assert.Equal(t, len(result), 0)
}

// --- FilterDetailsByReviewers ---

func TestFilterDetailsByReviewers(t *testing.T) {
	details := []ghpkg.ReviewCommentDetail{
		{Reviewer: "alice", Body: "a1"},
		{Reviewer: "bob", Body: "b1"},
		{Reviewer: "charlie", Body: "c1"},
		{Reviewer: "alice", Body: "a2"},
	}
	reviewers := []*ghpkg.UserActivity{
		{Login: "alice"},
		{Login: "bob"},
	}

	filtered := FilterDetailsByReviewers(details, reviewers)
	assert.Equal(t, len(filtered), 3)
	for _, d := range filtered {
		assert.Assert(t, d.Reviewer == "alice" || d.Reviewer == "bob")
	}
}

func TestFilterDetailsByReviewersEmpty(t *testing.T) {
	filtered := FilterDetailsByReviewers(nil, nil)
	assert.Equal(t, len(filtered), 0)
}

// --- WriteDetailsJSON ---

func TestWriteDetailsJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "details.json")

	details := []ghpkg.ReviewCommentDetail{
		{Reviewer: "alice", Body: "good stuff", PR: ghpkg.ReviewCommentDetailPR{Repo: "repo1", Number: 1}},
	}
	since := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)

	err := WriteDetailsJSON(details, path, "my-org", since, 3)
	assert.NilError(t, err)

	data, err := os.ReadFile(path)
	assert.NilError(t, err)

	var parsed DetailsJSON
	assert.NilError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, parsed.Metadata.Organization, "my-org")
	assert.Equal(t, parsed.Metadata.Since, "2025-06-15")
	assert.Equal(t, parsed.Metadata.TotalRepos, 3)
	assert.Equal(t, parsed.Metadata.TotalComments, 1)
	assert.Equal(t, len(parsed.Comments), 1)
	assert.Equal(t, parsed.Comments[0].Reviewer, "alice")
}

// --- AggregatePRRankings ---

func TestAggregatePRRankings(t *testing.T) {
	details := []ghpkg.ReviewCommentDetail{
		{Reviewer: "alice", PR: ghpkg.ReviewCommentDetailPR{Repo: "r1", Number: 10, Title: "PR10", Author: "dan", URL: "http://10", State: "MERGED"}},
		{Reviewer: "bob", PR: ghpkg.ReviewCommentDetailPR{Repo: "r1", Number: 10, Title: "PR10", Author: "dan", URL: "http://10", State: "MERGED"}},
		{Reviewer: "alice", PR: ghpkg.ReviewCommentDetailPR{Repo: "r1", Number: 10, Title: "PR10", Author: "dan", URL: "http://10", State: "MERGED"}},
		{Reviewer: "alice", PR: ghpkg.ReviewCommentDetailPR{Repo: "r1", Number: 20, Title: "PR20", Author: "dan", URL: "http://20", State: "OPEN"}},
	}

	rankings := AggregatePRRankings(details)
	assert.Equal(t, len(rankings), 2)

	// Sorted descending by comments
	assert.Equal(t, rankings[0].PRNumber, 10)
	assert.Equal(t, rankings[0].TotalComments, 3)
	assert.Equal(t, rankings[0].ReviewerCount, 2) // alice + bob
	assert.Equal(t, rankings[0].Rank, 1)
	assert.Equal(t, rankings[0].State, "MERGED")

	assert.Equal(t, rankings[1].PRNumber, 20)
	assert.Equal(t, rankings[1].TotalComments, 1)
	assert.Equal(t, rankings[1].Rank, 2)
}

func TestAggregatePRRankingsEmpty(t *testing.T) {
	rankings := AggregatePRRankings(nil)
	assert.Equal(t, len(rankings), 0)
}

// --- WritePRRankingsCSV ---

func TestWritePRRankingsCSV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "rankings.csv")

	rankings := []PRRankingRow{
		{Rank: 1, Repo: "r1", PRNumber: 10, PRTitle: "Big PR", PRAuthor: "dan", PRURL: "http://10", TotalComments: 5, ReviewerCount: 2, State: "MERGED"},
		{Rank: 2, Repo: "r1", PRNumber: 20, PRTitle: "Small PR", PRAuthor: "dan", PRURL: "http://20", TotalComments: 1, ReviewerCount: 1, State: "OPEN"},
	}

	err := WritePRRankingsCSV(rankings, path)
	assert.NilError(t, err)

	f, err := os.Open(path)
	assert.NilError(t, err)
	defer func() { _ = f.Close() }()

	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	assert.NilError(t, err)

	// Header + 2 rows
	assert.Equal(t, len(records), 3)
	assert.Equal(t, records[0][0], "rank")
	assert.Equal(t, records[1][0], "1")
	assert.Equal(t, records[1][1], "r1")
	assert.Equal(t, records[1][2], "10")
	assert.Equal(t, records[2][0], "2")
}

func TestWritePRRankingsCSVEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rankings.csv")

	err := WritePRRankingsCSV(nil, path)
	assert.NilError(t, err)

	f, err := os.Open(path)
	assert.NilError(t, err)
	defer func() { _ = f.Close() }()

	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	assert.NilError(t, err)
	assert.Equal(t, len(records), 1) // header only
}

// --- GroupByReviewer ---

func TestGroupByReviewer(t *testing.T) {
	comments := []ghpkg.ReviewCommentDetail{
		{Reviewer: "alice", Body: "a1", PR: ghpkg.ReviewCommentDetailPR{Repo: "r1", Number: 1}},
		{Reviewer: "bob", Body: "b1", PR: ghpkg.ReviewCommentDetailPR{Repo: "r1", Number: 1}},
		{Reviewer: "alice", Body: "a2", PR: ghpkg.ReviewCommentDetailPR{Repo: "r2", Number: 2}},
	}

	groups := GroupByReviewer(comments)
	assert.Equal(t, len(groups), 2)

	byName := map[string]ReviewerGroup{}
	for _, g := range groups {
		byName[g.Reviewer] = g
	}

	assert.Equal(t, byName["alice"].TotalComments, 2)
	assert.Equal(t, len(byName["alice"].Comments), 2)
	assert.Equal(t, byName["bob"].TotalComments, 1)

	// Verify PR context is populated
	for _, c := range byName["alice"].Comments {
		assert.Assert(t, c.PR != nil)
		assert.Assert(t, c.PR.Number > 0)
	}
}

// --- LimitCommentsPerReviewer ---

func TestLimitCommentsPerReviewer(t *testing.T) {
	t.Run("limits to N most recent", func(t *testing.T) {
		groups := []ReviewerGroup{
			{
				Reviewer: "alice",
				Comments: []ReviewCommentWithContext{
					{Body: "old", CreatedAt: "2025-01-01T00:00:00Z"},
					{Body: "newer", CreatedAt: "2025-06-01T00:00:00Z"},
					{Body: "newest", CreatedAt: "2025-12-01T00:00:00Z"},
				},
				TotalComments: 3,
			},
		}

		result := LimitCommentsPerReviewer(groups, 2)
		assert.Equal(t, len(result), 1)
		assert.Equal(t, len(result[0].Comments), 2)
		// Most recent first
		assert.Equal(t, result[0].Comments[0].Body, "newest")
		assert.Equal(t, result[0].Comments[1].Body, "newer")
	})

	t.Run("no-op when under limit", func(t *testing.T) {
		groups := []ReviewerGroup{
			{Reviewer: "bob", Comments: []ReviewCommentWithContext{{Body: "only"}}, TotalComments: 1},
		}
		result := LimitCommentsPerReviewer(groups, 5)
		assert.Equal(t, len(result[0].Comments), 1)
	})

	t.Run("zero limit returns all", func(t *testing.T) {
		groups := []ReviewerGroup{
			{Reviewer: "bob", Comments: []ReviewCommentWithContext{{Body: "a"}, {Body: "b"}}, TotalComments: 2},
		}
		result := LimitCommentsPerReviewer(groups, 0)
		assert.Equal(t, len(result[0].Comments), 2)
	})
}

// --- BuildAnalysisPrompt ---

func TestBuildAnalysisPrompt(t *testing.T) {
	groups := []ReviewerGroup{
		{
			Reviewer: "alice",
			Comments: []ReviewCommentWithContext{
				{
					Body:     "Use early return",
					DiffHunk: "@@ some diff",
					Repo:     "repo1",
					PR:       &PRContext{Number: 42, Title: "Fix stuff", Author: "dan", URL: "http://42", State: "MERGED"},
				},
			},
			TotalComments: 1,
		},
		{
			Reviewer:      "bob",
			Comments:      []ReviewCommentWithContext{{Body: "Handle nil", Repo: "repo1"}},
			TotalComments: 1,
		},
	}

	prompt := BuildAnalysisPrompt(groups)

	assert.Assert(t, strings.Contains(prompt, "2 review comments from 2 reviewer(s)"))
	assert.Assert(t, strings.Contains(prompt, "## alice (1 comments)"))
	assert.Assert(t, strings.Contains(prompt, "## bob (1 comments)"))
	assert.Assert(t, strings.Contains(prompt, "> Use early return"))
	assert.Assert(t, strings.Contains(prompt, "**PR**: [#42]"))
	assert.Assert(t, strings.Contains(prompt, "**Code context:**"))
	assert.Assert(t, strings.Contains(prompt, "> Handle nil"))

	// Verify enriched instructions from TS parity
	assert.Assert(t, strings.Contains(prompt, "Per-Reviewer Analysis"))
	assert.Assert(t, strings.Contains(prompt, "For each reviewer (alice, bob)"))
	assert.Assert(t, strings.Contains(prompt, "3-7 patterns"))
	assert.Assert(t, strings.Contains(prompt, "Cross-Cutting Themes"))
	assert.Assert(t, strings.Contains(prompt, "Recommendations"))
	assert.Assert(t, strings.Contains(prompt, "Notable Repos"))
}

// --- FormatMarkdownReport ---

func TestFormatMarkdownReport(t *testing.T) {
	report := FormatMarkdownReport("analysis content here", "/tmp/details.json", 42, []string{"alice", "bob"})

	assert.Assert(t, strings.Contains(report, "# Code Review Pattern Analysis"))
	assert.Assert(t, strings.Contains(report, "**Total Comments:** 42"))
	assert.Assert(t, strings.Contains(report, "**Reviewers:** alice, bob"))
	assert.Assert(t, strings.Contains(report, "analysis content here"))
	assert.Assert(t, strings.Contains(report, "/tmp/details.json"))
}

// --- splitRepos (via ResolveOrgAndRepos) ---

func TestSplitReposEdgeCases(t *testing.T) {
	// Test via ResolveOrgAndRepos since splitRepos is unexported
	t.Run("trims whitespace", func(t *testing.T) {
		org, repos, err := ResolveOrgAndRepos("org", " repo-a , repo-b ", "")
		assert.NilError(t, err)
		assert.Equal(t, org, "org")
		assert.DeepEqual(t, repos, []string{"repo-a", "repo-b"})
	})

	t.Run("ignores empty segments", func(t *testing.T) {
		org, repos, err := ResolveOrgAndRepos("org", "repo-a,,repo-b,", "")
		assert.NilError(t, err)
		assert.Equal(t, org, "org")
		assert.DeepEqual(t, repos, []string{"repo-a", "repo-b"})
	})
}
