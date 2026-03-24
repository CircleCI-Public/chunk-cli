package github_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fixtures"
)

// newTestClient creates a Client wired to the given fake server.
func newTestClient(t *testing.T, srv *httptest.Server) *github.Client {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("GITHUB_API_URL", srv.URL)
	c, err := github.New()
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}
	return c
}

// --- New ---

func TestNew_MissingToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	_, err := github.New()
	if err == nil {
		t.Fatal("expected error when GITHUB_TOKEN is empty")
	}
}

func TestNew_DefaultURL(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GITHUB_API_URL", "")
	c, err := github.New()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

// --- ValidateOrg ---

func TestValidateOrg(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newTestClient(t, srv)

	t.Run("success", func(t *testing.T) {
		gh.SetOrgValidation(fixtures.OrgValidationResponse("my-org"))
		if err := c.ValidateOrg(context.Background(), "my-org"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		gh.SetOrgValidation(`{
			"data": {"organization": null},
			"errors": [{"type": "NOT_FOUND", "message": "Could not resolve to an Organization with the login of 'no-org'."}]
		}`)
		err := c.ValidateOrg(context.Background(), "no-org")
		if err == nil {
			t.Fatal("expected error for missing org")
		}
	})
}

// --- CheckRateLimit ---

func TestCheckRateLimit(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newTestClient(t, srv)

	if err := c.CheckRateLimit(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- FetchOrgRepos ---

func TestFetchOrgRepos(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newTestClient(t, srv)

	t.Run("filter bypass", func(t *testing.T) {
		repos, err := c.FetchOrgRepos(context.Background(), "org", []string{"a", "b"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(repos) != 2 || repos[0] != "a" || repos[1] != "b" {
			t.Fatalf("expected [a b], got %v", repos)
		}
	})

	t.Run("fetches from API", func(t *testing.T) {
		gh.SetOrgRepos(fixtures.OrgReposResponse("repo-x", "repo-y", "repo-z"))
		repos, err := c.FetchOrgRepos(context.Background(), "org", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(repos) != 3 {
			t.Fatalf("expected 3 repos, got %d", len(repos))
		}
		if repos[0] != "repo-x" || repos[1] != "repo-y" || repos[2] != "repo-z" {
			t.Fatalf("unexpected repos: %v", repos)
		}
	})

	t.Run("resolution error", func(t *testing.T) {
		gh.SetOrgRepos(`{
			"data": null,
			"errors": [{"type": "NOT_FOUND", "message": "Could not resolve to an Organization with the login of 'bad-org'."}]
		}`)
		_, err := c.FetchOrgRepos(context.Background(), "bad-org", nil)
		if err == nil {
			t.Fatal("expected error for unresolvable org")
		}
	})

	t.Run("nil data", func(t *testing.T) {
		gh.SetOrgRepos(`{"data": null}`)
		_, err := c.FetchOrgRepos(context.Background(), "org", nil)
		if err == nil {
			t.Fatal("expected error for nil data")
		}
	})
}

// --- FetchReviewActivity ---

func TestFetchReviewActivity(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newTestClient(t, srv)

	t.Run("basic", func(t *testing.T) {
		gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())
		result, err := c.FetchReviewActivity(context.Background(), "test-org", "test-repo", time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result == nil {
			t.Fatal("expected non-nil result")
		}

		// Should have 2 reviewers: alice and bob
		if len(result.Activity) != 2 {
			t.Fatalf("expected 2 reviewers, got %d: %v", len(result.Activity), keys(result.Activity))
		}

		alice := result.Activity["reviewer-alice"]
		if alice == nil {
			t.Fatal("expected alice in activity")
		}
		if alice.Approvals != 1 {
			t.Errorf("alice approvals: got %d, want 1", alice.Approvals)
		}
		if alice.ReviewComments != 2 {
			t.Errorf("alice review comments: got %d, want 2", alice.ReviewComments)
		}

		bob := result.Activity["reviewer-bob"]
		if bob == nil {
			t.Fatal("expected bob in activity")
		}
		if bob.ChangesRequested != 1 {
			t.Errorf("bob changes_requested: got %d, want 1", bob.ChangesRequested)
		}
		if bob.ReviewComments != 1 {
			t.Errorf("bob review comments: got %d, want 1", bob.ReviewComments)
		}

		// Should have 3 comment details (alice x2, bob x1)
		if len(result.Details) != 3 {
			t.Fatalf("expected 3 details, got %d", len(result.Details))
		}
	})

	t.Run("bot filtering", func(t *testing.T) {
		gh.SetReviewActivity("bot-repo", fixtures.ReviewActivityWithBotResponse())
		result, err := c.FetchReviewActivity(context.Background(), "org", "bot-repo", time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// dependabot[bot] should be filtered out of both activity and details
		if _, ok := result.Activity["dependabot[bot]"]; ok {
			t.Error("dependabot[bot] should be filtered from activity")
		}
		for _, d := range result.Details {
			if d.Reviewer == "dependabot[bot]" {
				t.Error("dependabot[bot] should be filtered from details")
			}
		}
	})

	t.Run("since filter", func(t *testing.T) {
		gh.SetReviewActivity("since-repo", fixtures.MultiReviewerResponse())
		// Set since between PR 100 reviews (2026-03-01T01-03) and PR 101 reviews (2026-03-02T01-02).
		// PR 100's updatedAt is 2026-03-01T00:00:00Z which is before since, so the
		// function will return early after processing PR 100 (which has no reviews/comments after since).
		// PR 101's updatedAt is 2026-03-02T00:00:00Z — but the fixture lists PR 100 first.
		// The early return means only PR 100 is seen; its reviews are all before since.
		since := time.Date(2026, 3, 1, 23, 0, 0, 0, time.UTC)
		result, err := c.FetchReviewActivity(context.Background(), "org", "since-repo", since)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// PR 100 (first in fixture) has updatedAt=2026-03-01T00:00:00Z which is before since,
		// so the function returns early with no activity (reviews at T01-T03 are all before since too).
		if len(result.Activity) != 0 {
			t.Errorf("expected 0 reviewers (early return), got %d", len(result.Activity))
		}
	})

	t.Run("since filters reviews within PR", func(t *testing.T) {
		// Custom fixture: PR updatedAt is well in the future, but some reviews/comments
		// are older. This lets us set since between old and new reviews.
		gh.SetReviewActivity("since2-repo", `{
			"data": {
				"repository": {
					"pullRequests": {
						"pageInfo": {"hasNextPage": false, "endCursor": null},
						"nodes": [{
							"number": 1,
							"title": "Test PR",
							"url": "https://github.com/org/since2-repo/pull/1",
							"state": "OPEN",
							"updatedAt": "2026-06-01T00:00:00Z",
							"author": {"login": "author"},
							"reviews": {
								"nodes": [
									{"author": {"login": "alice"}, "state": "APPROVED", "createdAt": "2026-01-01T00:00:00Z"},
									{"author": {"login": "bob"}, "state": "APPROVED", "createdAt": "2026-05-01T00:00:00Z"}
								]
							},
							"reviewThreads": {
								"nodes": [
									{"comments": {"nodes": [{"author": {"login": "alice"}, "body": "old comment", "diffHunk": "@@", "createdAt": "2026-01-01T00:00:00Z"}]}},
									{"comments": {"nodes": [{"author": {"login": "bob"}, "body": "new comment", "diffHunk": "@@", "createdAt": "2026-05-01T00:00:00Z"}]}}
								]
							}
						}]
					}
				},
				"rateLimit": {"remaining": 4999, "resetAt": "2099-01-01T00:00:00Z"}
			}
		}`)

		since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		result, err := c.FetchReviewActivity(context.Background(), "org", "since2-repo", since)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// alice's review and comment are at 2026-01-01 (before since) — excluded.
		// bob's review and comment are at 2026-05-01 (after since) — included.
		if _, ok := result.Activity["alice"]; ok {
			t.Error("alice should be excluded (all activity before since)")
		}
		bob := result.Activity["bob"]
		if bob == nil {
			t.Fatal("expected bob")
		}
		if bob.Approvals != 1 {
			t.Errorf("bob approvals: got %d, want 1", bob.Approvals)
		}
		if bob.ReviewComments != 1 {
			t.Errorf("bob comments: got %d, want 1", bob.ReviewComments)
		}
	})

	t.Run("repo not found", func(t *testing.T) {
		gh.SetRepoError("missing-repo", fixtures.RepoNotFoundError("org", "missing-repo"))
		_, err := c.FetchReviewActivity(context.Background(), "org", "missing-repo", time.Time{})
		if err == nil {
			t.Fatal("expected error for missing repo")
		}
		if !github.IsResolutionError(err) {
			t.Errorf("expected resolution error, got: %v", err)
		}
	})

	t.Run("empty repo", func(t *testing.T) {
		// Default fake returns empty PR list
		result, err := c.FetchReviewActivity(context.Background(), "org", "empty-repo", time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Activity) != 0 {
			t.Errorf("expected no activity, got %d", len(result.Activity))
		}
		if len(result.Details) != 0 {
			t.Errorf("expected no details, got %d", len(result.Details))
		}
	})

	t.Run("detail metadata", func(t *testing.T) {
		gh.SetReviewActivity("meta-repo", fixtures.ReviewActivityResponse())
		result, err := c.FetchReviewActivity(context.Background(), "org", "meta-repo", time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Details) == 0 {
			t.Fatal("expected details")
		}
		d := result.Details[0]
		if d.PR.Repo != "meta-repo" {
			t.Errorf("repo: got %q, want %q", d.PR.Repo, "meta-repo")
		}
		if d.PR.Number != 42 {
			t.Errorf("number: got %d, want 42", d.PR.Number)
		}
		if d.PR.Author != "pr-author" {
			t.Errorf("author: got %q, want %q", d.PR.Author, "pr-author")
		}
		if d.PR.State != "MERGED" {
			t.Errorf("state: got %q, want %q", d.PR.State, "MERGED")
		}
		if d.DiffHunk == "" {
			t.Error("expected non-empty diffHunk")
		}
	})

	t.Run("self review excluded", func(t *testing.T) {
		// The fixtures have pr-author as PR author; any review/comment by pr-author
		// should be excluded. The standard fixture doesn't include pr-author as reviewer,
		// so this is implicitly tested. For explicit coverage, use the multi-reviewer
		// fixture where pr-author is the PR author and all reviewers are different.
		gh.SetReviewActivity("self-repo", fixtures.MultiReviewerResponse())
		result, err := c.FetchReviewActivity(context.Background(), "org", "self-repo", time.Time{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := result.Activity["pr-author"]; ok {
			t.Error("pr-author should be excluded from their own PR reviews")
		}
	})
}

// --- IsResolutionError ---

func TestIsResolutionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", context.Canceled, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := github.IsResolutionError(tt.err); got != tt.want {
				t.Errorf("IsResolutionError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// --- MultiReviewer counts ---

func TestMultiReviewerCounts(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newTestClient(t, srv)

	gh.SetReviewActivity("multi", fixtures.MultiReviewerResponse())
	result, err := c.FetchReviewActivity(context.Background(), "org", "multi", time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 3 human reviewers, no bots
	if len(result.Activity) != 3 {
		t.Fatalf("expected 3 reviewers, got %d: %v", len(result.Activity), keys(result.Activity))
	}

	alice := result.Activity["reviewer-alice"]
	if alice.ReviewComments != 3 {
		t.Errorf("alice comments: got %d, want 3", alice.ReviewComments)
	}
	if alice.Approvals != 2 {
		t.Errorf("alice approvals: got %d, want 2", alice.Approvals)
	}

	bob := result.Activity["reviewer-bob"]
	if bob.ReviewComments != 2 {
		t.Errorf("bob comments: got %d, want 2", bob.ReviewComments)
	}

	charlie := result.Activity["reviewer-charlie"]
	if charlie.ReviewComments != 1 {
		t.Errorf("charlie comments: got %d, want 1", charlie.ReviewComments)
	}
	if charlie.Approvals != 1 {
		t.Errorf("charlie approvals: got %d, want 1", charlie.Approvals)
	}

	// repos active tracking
	if !alice.ReposActiveIn["multi"] {
		t.Error("alice should be active in multi")
	}
}

// --- Request recording ---

func TestAuthHeader(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newTestClient(t, srv)

	_ = c.CheckRateLimit(context.Background())

	reqs := gh.Recorder.AllRequests()
	if len(reqs) == 0 {
		t.Fatal("expected at least one request")
	}
	auth := reqs[0].Header.Get("Authorization")
	if auth != "token test-token" {
		t.Errorf("auth header: got %q, want %q", auth, "token test-token")
	}
}

func keys(m map[string]*github.UserActivity) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
