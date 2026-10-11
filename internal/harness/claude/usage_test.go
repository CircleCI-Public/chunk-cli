package claude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

const probeToken = "sk-ant-oat01-probe-token"

var probeNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func realFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "real", name))
	assert.NilError(t, err)
	return b
}

func claudeCode(expiresAt time.Time) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) {
		return json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": probeToken, "expiresAt": expiresAt.UnixMilli()}})
	}
}

// usageServer answers /api/oauth/usage with status and body, counts requests
// and fails the test if the token is anywhere but the Authorization header.
func usageServer(t *testing.T, status int, body []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, r.URL.Path, "/api/oauth/usage")
		assert.Equal(t, r.Header.Get("Authorization"), "Bearer "+probeToken)
		assert.Equal(t, r.Header.Get("anthropic-beta"), "oauth-2025-04-20")
		assert.Equal(t, r.URL.RawQuery, "")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func probeFor(t *testing.T, srv *httptest.Server) UsageProbe {
	return UsageProbe{
		BaseURL: srv.URL, CachePath: filepath.Join(t.TempDir(), "claude.json"), Client: srv.Client(),
		Credentials: claudeCode(probeNow.Add(time.Hour)), Now: func() time.Time { return probeNow },
	}
}

func windowNames(u Usage) []string {
	var names []string
	for _, w := range u.Windows {
		names = append(names, w.Window)
	}
	return names
}

func TestUsageMapsTheLimitsArray(t *testing.T) {
	srv, _ := usageServer(t, 200, realFile(t, "claude-oauth-usage.json"))
	u := probeFor(t, srv).Read(t.Context())
	assert.DeepEqual(t, windowNames(u), []string{"5h", "7d", "7d:Fable"})
	assert.Equal(t, *u.Windows[0].UsedPercent, 5.0)
	assert.Equal(t, *u.Windows[1].UsedPercent, 25.0)
	assert.Equal(t, u.Windows[1].ResetsAt.Format(time.RFC3339), "2026-10-12T17:59:59Z")
	assert.Equal(t, u.Windows[0].Source, SourceOAuthUsage)
	assert.Equal(t, u.Note, "")
}

func TestUsageFallsBackToFiveHourAndSevenDay(t *testing.T) {
	body := `{"five_hour":{"utilization":7.5,"resets_at":"2026-10-12T00:00:00+00:00"},"seven_day":{"utilization":30,"resets_at":"2026-10-14T00:00:00+00:00"},"seven_day_opus":{"utilization":99}}`
	srv, _ := usageServer(t, 200, []byte(body))
	u := probeFor(t, srv).Read(t.Context())
	assert.DeepEqual(t, windowNames(u), []string{"5h", "7d"})
	assert.Equal(t, *u.Windows[0].UsedPercent, 7.5)
}

func TestUsageFallsBackToTheCacheOnFailure(t *testing.T) {
	good, _ := usageServer(t, 200, realFile(t, "claude-oauth-usage.json"))
	p := probeFor(t, good)
	assert.Equal(t, len(p.Read(t.Context()).Windows), 3)

	for name, status := range map[string]int{"401": 401, "403": 403, "404": 404} {
		t.Run(name, func(t *testing.T) {
			bad, _ := usageServer(t, status, realFile(t, "claude-oauth-usage-401.json"))
			q := p
			q.BaseURL, q.Client = bad.URL, bad.Client()
			q.Now = func() time.Time { return probeNow.Add(10 * time.Minute) }
			u := q.Read(t.Context())
			assert.Equal(t, len(u.Windows), 3, "last reading is kept")
			assert.Assert(t, strings.Contains(u.Note, name), u.Note)
		})
	}
}

func TestUsageFallsBackWithoutAUsableLogin(t *testing.T) {
	srv, calls := usageServer(t, 200, realFile(t, "claude-oauth-usage.json"))
	missing := probeFor(t, srv)
	missing.Credentials = func(context.Context) ([]byte, error) { return nil, errors.New("keychain item not found") }
	u := missing.Read(t.Context())
	assert.Equal(t, len(u.Windows), 0)
	assert.Assert(t, strings.Contains(u.Note, "no Claude Code login"), u.Note)

	expired := probeFor(t, srv)
	expired.Credentials = claudeCode(probeNow.Add(-time.Minute))
	u = expired.Read(t.Context())
	assert.Assert(t, strings.Contains(u.Note, "open Claude Code once"), u.Note)
	assert.Equal(t, calls.Load(), int32(0), "no request without a good token")
}

func TestUsageBacksOffAfter429AndCachesForAMinute(t *testing.T) {
	limited, calls := usageServer(t, 429, []byte(`{}`))
	p := probeFor(t, limited)
	p.Read(t.Context())
	assert.Equal(t, calls.Load(), int32(1))

	p.Now = func() time.Time { return probeNow.Add(4 * time.Minute) }
	u := p.Read(t.Context())
	assert.Equal(t, calls.Load(), int32(1), "backing off")
	assert.Assert(t, strings.Contains(u.Note, "slow down"), u.Note)

	p.Now = func() time.Time { return probeNow.Add(6 * time.Minute) }
	p.Read(t.Context())
	assert.Equal(t, calls.Load(), int32(2), "backoff over")

	good, goodCalls := usageServer(t, 200, realFile(t, "claude-oauth-usage.json"))
	q := probeFor(t, good)
	q.Read(t.Context())
	q.Now = func() time.Time { return probeNow.Add(30 * time.Second) }
	assert.Equal(t, len(q.Read(t.Context()).Windows), 3)
	assert.Equal(t, goodCalls.Load(), int32(1), "second call within 60s uses the cache")
	q.Now = func() time.Time { return probeNow.Add(90 * time.Second) }
	q.Read(t.Context())
	assert.Equal(t, goodCalls.Load(), int32(2))
}

func TestUsageNeverLeaksTheToken(t *testing.T) {
	srv, _ := usageServer(t, 200, realFile(t, "claude-oauth-usage.json"))
	p := probeFor(t, srv)
	u := p.Read(t.Context())
	out, err := json.Marshal(u)
	assert.NilError(t, err)
	cached, err := os.ReadFile(p.CachePath)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(out)+string(cached), probeToken))

	bad := probeFor(t, srv)
	bad.BaseURL = "http://127.0.0.1:1"
	bad.Client = http.DefaultClient
	u = bad.Read(t.Context())
	assert.Assert(t, !strings.Contains(u.Note, probeToken), u.Note)
}
