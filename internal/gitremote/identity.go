package gitremote

import (
	"fmt"
	"net/url"
	"strings"
)

// Identity is the repository origin names, as host/path: host lowercased,
// without userinfo or port; path without a trailing .git or /. The path keeps
// its case, since some hosts (GitLab) treat paths as case-sensitive.
//
// Every clone of one repository gets the same answer however it was cloned:
// git@github.com:org/repo.git, https://github.com/org/repo and
// ssh://git@github.com:22/org/repo.git are all github.com/org/repo.
//
// A remote with no host, a local path or a file:// URL, is an error: it names
// a directory on one machine, which is no identity another runner could share.
// Userinfo is dropped, so a token in an https remote never reaches the result.
func Identity(remote string) (string, error) {
	trimmed := strings.TrimSpace(remote)
	host, path, err := splitRemote(trimmed)
	if err != nil {
		return "", fmt.Errorf("no repository identity in remote URL %q: %w", remote, err)
	}
	host = strings.ToLower(host)
	if host == "" {
		return "", fmt.Errorf("no repository identity in remote URL %q: no host", remote)
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "", fmt.Errorf("no repository identity in remote URL %q: no path", remote)
	}
	return host + "/" + path, nil
}

// splitRemote splits a remote into its host and path. A URL has a scheme; a scp
// style remote ([user@]host:path) has a colon before any slash; anything else
// is a local path, which has no host.
func splitRemote(remote string) (host, path string, err error) {
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return "", "", err
		}
		return u.Hostname(), u.Path, nil
	}
	before, after, ok := strings.Cut(remote, ":")
	if !ok || strings.Contains(before, "/") {
		return "", "", nil
	}
	if i := strings.LastIndex(before, "@"); i >= 0 {
		before = before[i+1:]
	}
	return before, after, nil
}
