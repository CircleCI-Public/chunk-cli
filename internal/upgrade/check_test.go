package upgrade

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckForUpdate(t *testing.T) {
	t.Run("returns empty for dev version", func(t *testing.T) {
		dir := t.TempDir()
		got := CheckForUpdate(dir, "https://example.com", "dev")
		if got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})

	t.Run("returns latest when newer", func(t *testing.T) {
		srv := releaseServer(t, "v2.0.0")
		dir := t.TempDir()
		got := CheckForUpdate(dir, srv.URL, "v1.0.0")
		if got != "v2.0.0" {
			t.Fatalf("expected v2.0.0, got %q", got)
		}
	})

	t.Run("returns empty when up to date", func(t *testing.T) {
		srv := releaseServer(t, "v1.0.0")
		dir := t.TempDir()
		got := CheckForUpdate(dir, srv.URL, "v1.0.0")
		if got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})

	t.Run("uses cache when fresh", func(t *testing.T) {
		// Write a cache entry claiming v9.0.0 is latest; server would say v2.0.0
		dir := t.TempDir()
		cache := updateCache{
			CheckedAt:     time.Now(),
			LatestVersion: "v9.0.0",
		}
		data, _ := json.Marshal(cache)
		_ = os.WriteFile(filepath.Join(dir, cacheFileName), data, 0o600)

		hitCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hitCount++
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		got := CheckForUpdate(dir, srv.URL, "v1.0.0")
		if got != "v9.0.0" {
			t.Fatalf("expected v9.0.0 from cache, got %q", got)
		}
		if hitCount != 0 {
			t.Fatalf("expected no network calls, got %d", hitCount)
		}
	})

	t.Run("re-fetches when cache is stale", func(t *testing.T) {
		dir := t.TempDir()
		cache := updateCache{
			CheckedAt:     time.Now().Add(-25 * time.Hour),
			LatestVersion: "v1.5.0",
		}
		data, _ := json.Marshal(cache)
		_ = os.WriteFile(filepath.Join(dir, cacheFileName), data, 0o600)

		srv := releaseServer(t, "v2.0.0")
		got := CheckForUpdate(dir, srv.URL, "v1.0.0")
		if got != "v2.0.0" {
			t.Fatalf("expected v2.0.0 from fresh fetch, got %q", got)
		}
	})

	t.Run("returns empty on network error", func(t *testing.T) {
		dir := t.TempDir()
		got := CheckForUpdate(dir, "http://127.0.0.1:0", "v1.0.0")
		if got != "" {
			t.Fatalf("expected empty on error, got %q", got)
		}
	})

	t.Run("writes cache after fetch", func(t *testing.T) {
		dir := t.TempDir()
		srv := releaseServer(t, "v2.0.0")
		CheckForUpdate(dir, srv.URL, "v1.0.0")

		data, err := os.ReadFile(filepath.Join(dir, cacheFileName))
		if err != nil {
			t.Fatalf("cache file not written: %v", err)
		}
		var cached updateCache
		if err := json.Unmarshal(data, &cached); err != nil {
			t.Fatalf("cache file malformed: %v", err)
		}
		if cached.LatestVersion != "v2.0.0" {
			t.Fatalf("expected v2.0.0 in cache, got %q", cached.LatestVersion)
		}
		if time.Since(cached.CheckedAt) > 5*time.Second {
			t.Fatal("cache checked_at is too old")
		}
	})
}

func TestNewerTag(t *testing.T) {
	tests := []struct {
		latest  string
		current string
		want    string
	}{
		{"v2.0.0", "v1.0.0", "v2.0.0"},
		{"v1.0.0", "v1.0.0", ""},
		{"v1.0.0", "v2.0.0", ""},
		{"v1.10.0", "v1.9.0", "v1.10.0"},
		{"invalid", "v1.0.0", ""},
		{"v1.0.0", "invalid", ""},
	}
	for _, tt := range tests {
		got := newerTag(tt.latest, tt.current)
		if got != tt.want {
			t.Errorf("newerTag(%q, %q) = %q, want %q", tt.latest, tt.current, got, tt.want)
		}
	}
}

// releaseServer returns a test server that responds to the GitHub releases
// latest endpoint with a release tagged at tagName.
func releaseServer(t *testing.T, tagName string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := ghRelease{TagName: tagName}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rel)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The cache window is claimed before the HTTP request, so a caller that exits
// without waiting for the fetch still suppresses checks for 24 h rather than
// sending every later invocation back to GitHub.
func TestCheckForUpdate_claimsCacheWindowBeforeFetch(t *testing.T) {
	dir := t.TempDir()

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	if got := CheckForUpdate(dir, srv.URL, "v1.0.0"); got != "" {
		t.Fatalf("expected empty on a failed fetch, got %q", got)
	}
	if hits != 1 {
		t.Fatalf("expected 1 network call, got %d", hits)
	}

	cached, ok := readCache(filepath.Join(dir, cacheFileName))
	if !ok {
		t.Fatal("expected the failed fetch to still claim the cache window")
	}
	if cached.LatestVersion != "" {
		t.Fatalf("expected no version recorded, got %q", cached.LatestVersion)
	}
	if time.Since(cached.CheckedAt) > time.Minute {
		t.Fatal("expected checked_at to be set to now")
	}

	// The claimed window holds off the next check, and an empty cached
	// version yields no notice rather than a bogus one.
	if got := CheckForUpdate(dir, srv.URL, "v1.0.0"); got != "" {
		t.Fatalf("expected empty from the claimed window, got %q", got)
	}
	if hits != 1 {
		t.Fatalf("expected no further network calls, got %d", hits)
	}
}
