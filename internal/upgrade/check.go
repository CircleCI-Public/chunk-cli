package upgrade

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/version"
)

const (
	checkCacheTTL  = 24 * time.Hour
	checkRetryTTL  = 5 * time.Minute
	checkTimeout   = 5 * time.Second
	cacheFileName  = "update-check.json"
	defaultAPIBase = "https://api.github.com"
)

type updateCache struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
}

// Check reports the latest release tag if it is newer than the running binary,
// or "" if there is nothing to report. It is the entry point for the update
// notice on every surface that shows one, so the CI guard lives here rather
// than at each call site — CI has nobody to read a notice. Test runs are
// covered by CheckForUpdate's "dev" guard: version.Value is only set from
// main.go, so it is always "dev" under test and nothing is fetched or written.
//
// Callers that need to inject a cache dir or API base — the tests below —
// should use CheckForUpdate instead.
func Check() string {
	if os.Getenv(config.EnvCI) != "" {
		return ""
	}
	stateDir, err := config.AppState()
	if err != nil {
		return ""
	}
	apiBase := os.Getenv(config.EnvGitHubAPIURL)
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	return CheckForUpdate(stateDir, apiBase, version.Value)
}

// CheckForUpdate returns the latest release tag if it is strictly newer than
// current, or "" if up to date, the check fails, or current is "dev".
// Results are cached in cacheDir for 24 h to stay within GitHub's unauthenticated
// rate limit (60 req/hr) even for users running many commands. A cache entry
// with no version records that the window was used without a usable answer.
func CheckForUpdate(cacheDir, apiBase, current string) string {
	if current == "dev" || current == "" {
		return ""
	}

	cacheFile := filepath.Join(cacheDir, cacheFileName)

	if cached, ok := readCache(cacheFile); ok && time.Since(cached.CheckedAt) < checkCacheTTL {
		return newerTag(cached.LatestVersion, current)
	}

	// Claim a short window before the request. Short-lived commands kick this
	// off in a goroutine and may exit before the GitHub round trip finishes;
	// using a retry window rather than the full 24 h means an aborted fetch
	// is retried in a few minutes rather than suppressed until tomorrow.
	_ = writeCache(cacheFile, updateCache{CheckedAt: time.Now().Add(-checkCacheTTL + checkRetryTTL)})

	client := &http.Client{Timeout: checkTimeout}
	rel, err := fetchLatestRelease(client, apiBase)
	if err != nil {
		return ""
	}

	_ = writeCache(cacheFile, updateCache{
		CheckedAt:     time.Now(),
		LatestVersion: rel.TagName,
	})

	return newerTag(rel.TagName, current)
}

func newerTag(latest, current string) string {
	l, err := semver.NewVersion(latest)
	if err != nil {
		return ""
	}
	c, err := semver.NewVersion(current)
	if err != nil {
		return ""
	}
	if l.GreaterThan(c) {
		return latest
	}
	return ""
}

func readCache(path string) (updateCache, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return updateCache{}, false
	}
	var c updateCache
	if err := json.Unmarshal(data, &c); err != nil {
		return updateCache{}, false
	}
	return c, true
}

func writeCache(path string, c updateCache) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".update-check-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("atomic cache write: %w", err)
	}
	return nil
}
