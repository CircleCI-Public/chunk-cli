package gitremote

import (
	"context"
	"fmt"
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
