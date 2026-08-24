package upgrade

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Masterminds/semver/v3"
)

const (
	checkCacheTTL = 24 * time.Hour
	checkTimeout  = 5 * time.Second
	cacheFileName = "update-check.json"
)

type updateCache struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
}

// CheckForUpdate returns the latest release tag if it is strictly newer than
// current, or "" if up to date, the check fails, or current is "dev".
// Results are cached in cacheDir for 24 h to stay within GitHub's unauthenticated
// rate limit (60 req/hr) even for users running many commands.
func CheckForUpdate(cacheDir, apiBase, current string) string {
	if current == "dev" || current == "" {
		return ""
	}

	cacheFile := filepath.Join(cacheDir, cacheFileName)

	if cached, ok := readCache(cacheFile); ok && time.Since(cached.CheckedAt) < checkCacheTTL {
		return newerTag(cached.LatestVersion, current)
	}

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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
