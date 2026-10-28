package github_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fixtures"
)

// newTestClient creates a Client wired to the given fake server.
func newTestClient(t *testing.T, srv *httptest.Server) *github.Client {
	t.Helper()
	c, err := github.New(github.Config{Token: "test-token", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}
	return c
}

// --- New ---

func TestNew_MissingToken(t *testing.T) {
	_, err := github.New(github.Config{BaseURL: "http://localhost"})
	if err == nil {
		t.Fatal("expected error when token is empty")
	}
}

func TestNew_DefaultURL(t *testing.T) {
	c, err := github.New(github.Config{Token: "tok", BaseURL: "https://api.github.com"})
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

// --- FetchPRProgress ---

func TestFetchPRProgress(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newTestClient(t, srv)

	// reviewer a asked for changes and then approved, so only b still blocks.
	gh.SetPullRequestProgress(3, `{
		"state": "OPEN", "isDraft": true,
		"reviewRequests": {"totalCount": 1},
		"reviews": {"totalCount": 4, "nodes": [
			{"state": "CHANGES_REQUESTED", "author": {"login": "a"}},
			{"state": "COMMENTED", "author": {"login": "b"}},
			{"state": "APPROVED", "author": {"login": "a"}},
			{"state": "CHANGES_REQUESTED", "author": {"login": "b"}}
		]},
		"reviewThreads": {"nodes": [{"isResolved": true}, {"isResolved": false}, {"isResolved": false}]},
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "FAILURE"}}}]}
	}`)
	got, err := c.FetchPRProgress(context.Background(), "acme", "widgets", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := github.PRProgress{
		State: "OPEN", Draft: true, ReviewRequested: true, Reviewed: true,
		ChangesRequested: true, OpenThreads: 2, CheckState: "FAILURE",
	}
	if got == nil || *got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	gh.SetPullRequestProgress(4, `{
		"state": "OPEN",
		"reviews": {"totalCount": 2, "nodes": [
			{"state": "CHANGES_REQUESTED", "author": {"login": "a"}},
			{"state": "APPROVED", "author": {"login": "a"}}
		]}
	}`)
	got, err = c.FetchPRProgress(context.Background(), "acme", "widgets", 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ChangesRequested {
		t.Fatalf("a reviewer who went on to approve still blocks: %+v", got)
	}
}
