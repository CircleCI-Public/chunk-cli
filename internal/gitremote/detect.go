package gitremote

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

var ghRemoteRe = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?$`)

// ParseRemoteURL extracts org and repo from a GitHub remote URL.
func ParseRemoteURL(url string) (org, repo string, err error) {
	m := ghRemoteRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", "", fmt.Errorf("not a GitHub remote URL: %s", url)
	}
	return m[1], m[2], nil
}

// URL returns the configured URL for remote in the repository at workDir.
func URL(ctx context.Context, workDir, remote string) (string, error) {
	out, err := (gitexec.Runner{Dir: workDir}).Output(ctx, "remote", "get-url", remote)
	if err != nil {
		return "", fmt.Errorf("get remote %q URL: %w", remote, err)
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return "", fmt.Errorf("get remote %q URL: empty output", remote)
	}
	return url, nil
}

// DetectOrgAndRepo runs git remote get-url origin in workDir and parses the result.
func DetectOrgAndRepo(workDir string) (org, repo string, err error) {
	return DetectOrgAndRepoCtx(context.Background(), workDir)
}

// DetectOrgAndRepoCtx detects the GitHub org and repo in workDir, honouring ctx
// for cancellation/timeout.
func DetectOrgAndRepoCtx(ctx context.Context, workDir string) (org, repo string, err error) {
	url, err := URL(ctx, workDir, "origin")
	if err != nil {
		return "", "", err
	}
	return ParseRemoteURL(url)
}

// RepoName returns the name of the repository at workDir, read from its origin
// remote. Unlike DetectOrgAndRepo it accepts any host, for callers that need a
// name for the repository rather than its GitHub coordinates.
func RepoName(ctx context.Context, workDir string) (string, error) {
	url, err := URL(ctx, workDir, "origin")
	if err != nil {
		return "", err
	}
	return ParseRepoName(url)
}

// repoNameRe is what a name may contain to be used unquoted as a directory on
// a sidecar and as an rsync destination, which the remote shell re-parses.
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ParseRepoName extracts a repo name from any git remote URL by taking its last
// path segment and stripping a trailing .git. It handles scp style remotes
// (git@host:org/repo.git) as well as URLs and local paths.
func ParseRepoName(url string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(url), "/")
	// A scheme's host carries no repo name, and neither does a scp style
	// remote's. What is left of either is the path.
	if _, rest, ok := strings.Cut(trimmed, "://"); ok {
		_, rest, ok = strings.Cut(rest, "/")
		if !ok {
			return "", fmt.Errorf("no repo name in remote URL: %q", url)
		}
		trimmed = rest
	} else if _, rest, ok := strings.Cut(trimmed, ":"); ok {
		trimmed = rest
	}
	repo := strings.TrimSuffix(path.Base(trimmed), ".git")
	// A name reaches rm -rf and rsync --delete on the sidecar, so ".." or a
	// shell metacharacter in it would act outside the workspace.
	if repo == "." || repo == ".." || !repoNameRe.MatchString(repo) {
		return "", fmt.Errorf("no repo name in remote URL: %q", url)
	}
	return repo, nil
}
