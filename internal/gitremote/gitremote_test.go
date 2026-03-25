package gitremote_test

import (
	"os"
	"testing"

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

	// DetectOrgAndRepo shells out to git, so we need to be in the repo dir.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	org, repo, err := gitremote.DetectOrgAndRepo()
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

func TestDetectOrgAndRepo_NoRemote(t *testing.T) {
	// Use a temp dir with no git repo — should fail.
	dir := t.TempDir()

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	_, _, err = gitremote.DetectOrgAndRepo()
	if err == nil {
		t.Fatal("expected error when not in a git repo")
	}
}
