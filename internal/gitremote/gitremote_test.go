package gitremote_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func TestParseRemoteURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantOrg  string
		wantRepo string
		wantErr  bool
	}{
		{
			name:     "ssh",
			url:      "git@github.com:CircleCI-Public/chunk-cli.git",
			wantOrg:  "CircleCI-Public",
			wantRepo: "chunk-cli",
		},
		{
			name:     "https",
			url:      "https://github.com/CircleCI-Public/chunk-cli.git",
			wantOrg:  "CircleCI-Public",
			wantRepo: "chunk-cli",
		},
		{
			name:     "https without .git",
			url:      "https://github.com/CircleCI-Public/chunk-cli",
			wantOrg:  "CircleCI-Public",
			wantRepo: "chunk-cli",
		},
		{
			name:     "ssh without .git",
			url:      "git@github.com:some-org/some-repo",
			wantOrg:  "some-org",
			wantRepo: "some-repo",
		},
		{
			name:     "with trailing whitespace",
			url:      "git@github.com:org/repo.git\n",
			wantOrg:  "org",
			wantRepo: "repo",
		},
		{
			name:    "not github",
			url:     "git@gitlab.com:org/repo.git",
			wantErr: true,
		},
		{
			name:    "empty",
			url:     "",
			wantErr: true,
		},
		{
			name:    "garbage",
			url:     "not-a-url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			org, repo, err := gitremote.ParseRemoteURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got org=%q repo=%q", org, repo)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if org != tt.wantOrg {
				t.Errorf("org = %q, want %q", org, tt.wantOrg)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}

func TestDetectOrgAndRepo(t *testing.T) {
	dir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	org, repo, err := gitremote.DetectOrgAndRepo(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if org != "test-org" {
		t.Errorf("org = %q, want %q", org, "test-org")
	}
	if repo != "test-repo" {
		t.Errorf("repo = %q, want %q", repo, "test-repo")
	}
}

func TestURL(t *testing.T) {
	dir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	url, err := gitremote.URL(context.Background(), dir, "origin")
	assert.NilError(t, err)
	assert.Assert(t, url != "", "expected non-empty URL")
	// Parse so the test passes regardless of whether git rewrites SSH to HTTPS.
	org, repo, parseErr := gitremote.ParseRemoteURL(url)
	assert.NilError(t, parseErr)
	assert.Equal(t, org, "test-org")
	assert.Equal(t, repo, "test-repo")
}

func TestURLReportsMissingRemote(t *testing.T) {
	_, err := gitremote.URL(context.Background(), gitrepo.SetupGitRepo(t, "test-org", "test-repo"), "missing")
	assert.Assert(t, err != nil)
}

func TestDetectOrgAndRepo_NoRemote(t *testing.T) {
	// Use a temp dir with no git repo — should fail.
	dir := t.TempDir()

	_, _, err := gitremote.DetectOrgAndRepo(dir)
	if err == nil {
		t.Fatal("expected error when not in a git repo")
	}
}

func TestDetectOrgAndRepoCtxHonoursCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := gitremote.DetectOrgAndRepoCtx(ctx, gitrepo.SetupGitRepo(t, "test-org", "test-repo"))
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
}

func TestParseRepoName(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{name: "github ssh", url: "git@github.com:CircleCI-Public/chunk-cli.git", want: "chunk-cli"},
		{name: "github https", url: "https://github.com/CircleCI-Public/chunk-cli", want: "chunk-cli"},
		{name: "gitlab subgroup", url: "https://gitlab.com/group/sub/repo.git", want: "repo"},
		{name: "enterprise host", url: "git@ghe.example.com:org/repo.git", want: "repo"},
		{name: "scp without path", url: "git@host:repo.git", want: "repo"},
		{name: "local path", url: "/srv/git/repo.git/", want: "repo"},
		{name: "trailing whitespace", url: "git@gitlab.com:org/repo.git\n", want: "repo"},
		{name: "empty", url: "", wantErr: true},
		{name: "whitespace only", url: "   ", wantErr: true},
		{name: "root only", url: "/", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitremote.ParseRepoName(tt.url)
			if tt.wantErr {
				assert.Assert(t, err != nil, "got %q", got)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, got, tt.want)
		})
	}
}

func TestRepoNameAcceptsAnyHost(t *testing.T) {
	dir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	cmd := exec.Command("git", "remote", "set-url", "origin", "https://gitlab.com/test-org/gitlab-repo.git")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))

	repo, err := gitremote.RepoName(context.Background(), dir)
	assert.NilError(t, err)
	assert.Equal(t, repo, "gitlab-repo")
}

func TestRepoNameReportsMissingOrigin(t *testing.T) {
	_, err := gitremote.RepoName(context.Background(), t.TempDir())
	assert.Assert(t, err != nil)
}
