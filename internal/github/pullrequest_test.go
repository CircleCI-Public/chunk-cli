package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestCreatePullRequestOpensIt(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newTestClient(t, srv)

	pr, err := c.CreatePullRequest(context.Background(), "acme", "widgets", github.NewPullRequest{
		Base: "main", Head: "chunk/factory/add-a-flag/20261002-150405-ab12cd",
		Title: "Add a flag", Body: "Because.", Draft: true,
	})

	assert.NilError(t, err)
	assert.Equal(t, pr.Number, 1)
	assert.Equal(t, pr.URL, "https://github.com/acme/widgets/pull/1")
	assert.DeepEqual(t, gh.PullRequests(), []fakes.OpenedPullRequest{{
		Owner: "acme", Repo: "widgets", Number: 1,
		Title: "Add a flag", Body: "Because.", Head: "chunk/factory/add-a-flag/20261002-150405-ab12cd", Base: "main", Draft: true,
	}})
}

func TestCreatePullRequestSaysWhyGitHubRefused(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{
			name: "a pull request already exists", status: http.StatusUnprocessableEntity,
			body: `{"message":"Validation Failed","errors":[{"message":"A pull request already exists for acme:topic."}]}`,
			want: []string{"422", "Validation Failed", "A pull request already exists"},
		},
		{
			name: "a token that cannot write", status: http.StatusForbidden,
			body: `{"message":"Resource not accessible by personal access token"}`,
			want: []string{"403", "acme/widgets", "Pull requests: write", "Resource not accessible"},
		},
		{
			name: "a repository the token cannot see", status: http.StatusNotFound,
			body: `{"message":"Not Found"}`,
			want: []string{"404", "no access to acme/widgets"},
		},
		{
			name: "a token not authorized for single sign-on", status: http.StatusForbidden,
			body: `{"message":"Resource protected by organization SAML enforcement. You must grant your Personal Access token access to this organization."}`,
			want: []string{"403", "single sign-on", "Configure SSO", "SAML enforcement"},
		},
		{
			name: "a token GitHub rejects", status: http.StatusUnauthorized,
			body: `{"message":"Bad credentials"}`,
			want: []string{"401", "token was not accepted"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := fakes.NewFakeGitHub()
			gh.RefusePullRequests(tt.status, tt.body)
			srv := httptest.NewServer(gh)
			defer srv.Close()
			c := newTestClient(t, srv)

			_, err := c.CreatePullRequest(context.Background(), "acme", "widgets",
				github.NewPullRequest{Base: "main", Head: "topic", Title: "t"})

			assert.Assert(t, err != nil)
			for _, w := range tt.want {
				assert.Assert(t, strings.Contains(err.Error(), w), "%q is not in %q", w, err)
			}
			assert.Equal(t, len(gh.PullRequests()), 0)
		})
	}
}

func TestDefaultBranch(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.SetDefaultBranch("trunk")
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newTestClient(t, srv)

	got, err := c.DefaultBranch(context.Background(), "acme", "widgets")

	assert.NilError(t, err)
	assert.Equal(t, got, "trunk")
}

func TestOpenPullRequestForBranchFindsTheOneAlreadyOpen(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	gh.AddPullRequest("acme", "widgets", "topic")
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newTestClient(t, srv)

	pr, err := c.OpenPullRequestForBranch(context.Background(), "acme", "widgets", "topic")
	assert.NilError(t, err)
	assert.Assert(t, pr != nil)
	assert.Equal(t, pr.Number, 1)
	assert.Equal(t, pr.URL, "https://github.com/acme/widgets/pull/1")

	// A branch with none is no error: there is simply nothing open for it.
	pr, err = c.OpenPullRequestForBranch(context.Background(), "acme", "widgets", "other")
	assert.NilError(t, err)
	assert.Assert(t, pr == nil)
}
