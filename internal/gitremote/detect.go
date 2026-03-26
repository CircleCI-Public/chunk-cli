package gitremote

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
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

// DetectOrgAndRepo runs git remote get-url origin in workDir and parses the result.
func DetectOrgAndRepo(workDir string) (org, repo string, err error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	return ParseRemoteURL(string(out))
}
