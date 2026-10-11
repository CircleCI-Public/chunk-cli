package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/harness"
)

// Usage probe timings.
const (
	usageFreshFor = 60 * time.Second
	usageBackoff  = 5 * time.Minute
	usageTimeout  = 10 * time.Second

	// SourceOAuthUsage labels figures read from the usage endpoint.
	SourceOAuthUsage = "oauth-usage"

	// usageKeychainItem is where Claude Code keeps its own login on macOS.
	usageKeychainItem = "Claude Code-credentials"
	usageBeta         = "oauth-2025-04-20"
)

// UsageProbe reads Claude's usage windows from the undocumented
// /api/oauth/usage endpoint with Claude Code's own login, which it only ever
// reads: it never writes or refreshes it. Anthropic has said third-party tools
// should not call the endpoint and it can change without notice, so every
// failure falls back to the last reading, and the token goes only into the
// request's Authorization header.
type UsageProbe struct {
	// BaseURL is the API origin; Anthropic's when empty.
	BaseURL string
	// CachePath is the file the last reading and any back-off are kept in.
	CachePath string
	Client    *http.Client
	// Credentials returns Claude Code's stored login as JSON. It reads the
	// macOS keychain item when nil.
	Credentials func(ctx context.Context) ([]byte, error)
	Now         func() time.Time
}

// Usage is a probe's answer.
type Usage struct {
	Windows []harness.Window
	// Note says why the windows are not fresh, or why there are none.
	Note string
}

type usageCache struct {
	FetchedAt    time.Time        `json:"fetched_at"`
	BackoffUntil time.Time        `json:"backoff_until,omitzero"`
	Windows      []harness.Window `json:"windows,omitempty"`
}

func (p UsageProbe) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Read returns fresh windows when it can, and the last reading with a note
// when it cannot.
func (p UsageProbe) Read(ctx context.Context) Usage {
	now := p.now()
	cached := p.loadCache()
	if now.Sub(cached.FetchedAt) < usageFreshFor && len(cached.Windows) > 0 {
		return Usage{Windows: cached.Windows}
	}
	if now.Before(cached.BackoffUntil) {
		return Usage{Windows: cached.Windows, Note: "the usage endpoint asked smelt to slow down; showing the last reading"}
	}
	windows, status, err := p.fetch(ctx, now)
	switch {
	case err == nil:
		p.saveCache(usageCache{FetchedAt: now, Windows: windows})
		return Usage{Windows: windows}
	case status == http.StatusTooManyRequests:
		cached.BackoffUntil = now.Add(usageBackoff)
		p.saveCache(cached)
	}
	return Usage{Windows: cached.Windows, Note: "claude usage unavailable: " + err.Error()}
}

func (p UsageProbe) fetch(ctx context.Context, now time.Time) (windows []harness.Window, status int, err error) {
	token, err := p.token(ctx, now)
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/oauth/usage", nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", usageBeta)
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// The error's text can quote the URL but never a header.
		return nil, 0, errors.New("the request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("the usage endpoint answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, errors.New("read the usage response")
	}
	windows, err = ParseUsage(body, now)
	return windows, resp.StatusCode, err
}

// token is Claude Code's access token, unless it has expired: Claude Code
// refreshes its own login when it runs, and smelt does not.
func (p UsageProbe) token(ctx context.Context, now time.Time) (string, error) {
	read := p.Credentials
	if read == nil {
		read = macKeychainCredentials
	}
	raw, err := read(ctx)
	if err != nil {
		return "", fmt.Errorf("no Claude Code login to read: %w", err)
	}
	var c struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"` // unix milliseconds
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(raw, &c) != nil || c.OAuth.AccessToken == "" {
		return "", errors.New("the Claude Code login has no access token")
	}
	if c.OAuth.ExpiresAt > 0 && !time.UnixMilli(c.OAuth.ExpiresAt).After(now) {
		return "", errors.New("open Claude Code once to refresh its login")
	}
	return c.OAuth.AccessToken, nil
}

func macKeychainCredentials(ctx context.Context) ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("only read from the macOS keychain")
	}
	out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", usageKeychainItem, "-w").Output()
	if err != nil {
		return nil, errors.New("keychain item not found")
	}
	return out, nil
}

type usageLimit struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type usageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

// ParseUsage reads the endpoint's body: the limits array first, then
// five_hour and seven_day. Every other key is ignored.
func ParseUsage(body []byte, now time.Time) ([]harness.Window, error) {
	var u struct {
		Limits    []usageLimit `json:"limits"`
		FiveHour  *usageWindow `json:"five_hour"`
		SevenDay  *usageWindow `json:"seven_day"`
		ErrorType *struct{}    `json:"error"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.ErrorType != nil {
		return nil, errors.New("the usage response has an unexpected shape")
	}
	var out []harness.Window
	for _, l := range u.Limits {
		if name := limitWindow(l); name != "" {
			out = append(out, usageEntry(name, l.Percent, l.ResetsAt, now))
		}
	}
	if len(out) == 0 {
		for name, w := range map[string]*usageWindow{"5h": u.FiveHour, "7d": u.SevenDay} {
			if w != nil {
				out = append(out, usageEntry(name, w.Utilization, w.ResetsAt, now))
			}
		}
		// Map order is random; the shorter window goes first.
		if len(out) == 2 && out[0].Window != "5h" {
			out[0], out[1] = out[1], out[0]
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the usage response has no windows")
	}
	return out, nil
}

func limitWindow(l usageLimit) string {
	switch l.Kind {
	case "session":
		return "5h"
	case "weekly_all":
		return "7d"
	case "weekly_scoped":
		if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "" {
			return "7d:" + l.Scope.Model.DisplayName
		}
	}
	return ""
}

func usageEntry(name string, pct *float64, resets string, now time.Time) harness.Window {
	w := harness.Window{Window: name, UsedPercent: pct, Source: SourceOAuthUsage, SeenAt: &now}
	if t, err := time.Parse(time.RFC3339Nano, resets); err == nil {
		t = t.UTC()
		w.ResetsAt = &t
	}
	return w
}

func (p UsageProbe) loadCache() usageCache {
	var c usageCache
	if raw, err := os.ReadFile(p.CachePath); err == nil {
		_ = json.Unmarshal(raw, &c)
	}
	return c
}

func (p UsageProbe) saveCache(c usageCache) {
	if p.CachePath == "" {
		return
	}
	raw, err := json.Marshal(c)
	if err != nil || os.MkdirAll(filepath.Dir(p.CachePath), 0o700) != nil {
		return
	}
	_ = os.WriteFile(p.CachePath, raw, 0o600)
}
