package httpcl_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

func TestCallJSONRoundTrip(t *testing.T) {
	type reqBody struct {
		Name string `json:"name"`
	}
	type respBody struct {
		Greeting string `json:"greeting"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Errorf("expected JSON content-type, got %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected bearer auth, got %q", r.Header.Get("Authorization"))
		}

		var body reqBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(respBody{Greeting: "hello " + body.Name})
	}))
	defer srv.Close()

	c := hc.New(hc.Config{
		BaseURL:   srv.URL,
		AuthToken: "test-token",
	})

	var resp respBody
	status, err := c.Call(context.Background(), hc.NewRequest("POST", "/test",
		hc.Body(reqBody{Name: "world"}),
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	if resp.Greeting != "hello world" {
		t.Fatalf("expected 'hello world', got %q", resp.Greeting)
	}
}

func TestCallHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := hc.New(hc.Config{BaseURL: srv.URL})

	status, err := c.Call(context.Background(), hc.NewRequest("GET", "/missing"))
	if status != 404 {
		t.Fatalf("expected 404, got %d", status)
	}
	if !hc.HasStatusCode(err, http.StatusNotFound) {
		t.Fatalf("expected HTTPError with 404, got %v", err)
	}
}

func TestDisableRetries(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := hc.New(hc.Config{
		BaseURL:        srv.URL,
		DisableRetries: true,
	})

	_, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
	if n := attempts.Load(); n != 1 {
		t.Fatalf("expected exactly 1 attempt with retries disabled, got %d", n)
	}
}

func TestCallCustomAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "my-key" {
			t.Errorf("expected x-api-key header, got %q", r.Header.Get("x-api-key"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := hc.New(hc.Config{
		BaseURL:    srv.URL,
		AuthToken:  "my-key",
		AuthHeader: "x-api-key",
	})

	status, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
}

func TestHeaderOverridesDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Client defaults to "application/json"; a per-request Header must
		// replace it, not append, or Header.Get on the server still sees JSON.
		if got := r.Header.Values("Accept"); len(got) != 1 || got[0] != "text/event-stream" {
			t.Errorf("expected exactly one Accept of text/event-stream, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := hc.New(hc.Config{BaseURL: srv.URL})

	status, err := c.Call(context.Background(), hc.NewRequest("GET", "/",
		hc.Header("Accept", "text/event-stream"),
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
}

func TestRouteParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/sidecar/instances/sb-42/exec" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := hc.New(hc.Config{BaseURL: srv.URL, DisableRetries: true})

	status, err := c.Call(context.Background(), hc.NewRequest("GET",
		"/api/v2/sidecar/instances/%s/exec",
		hc.RouteParams("sb-42"),
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
}

func TestRetryOn429_RetriesWithinBudget(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := hc.New(hc.Config{
		BaseURL:          srv.URL,
		RetryOn429Budget: 10 * time.Second,
	})

	status, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	if n := attempts.Load(); n != 2 {
		t.Fatalf("expected 2 attempts, got %d", n)
	}
}

func TestRetryOn429_BailsWhenRetryAfterExceedsBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := hc.New(hc.Config{
		BaseURL:          srv.URL,
		RetryOn429Budget: 30 * time.Second,
	})

	_, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !hc.IsRateLimitError(err) {
		t.Fatalf("expected RateLimitError, got: %v", err)
	}
}

func TestRetryOn429_MessageContainsBackoffHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "45")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := hc.New(hc.Config{
		BaseURL:          srv.URL,
		RetryOn429Budget: 30 * time.Second,
	})

	_, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "rate limited") {
		t.Errorf("error message should mention rate limiting: %q", msg)
	}
	if !strings.Contains(msg, "try again later") {
		t.Errorf("error message should hint to retry later: %q", msg)
	}
}

func TestRetryOn429_DisabledByDefault(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	// No RetryOn429Budget — falls through to DefaultRetryPolicy which
	// retries 429 up to RetryMax times with normal backoff (not a budget error).
	c := hc.New(hc.Config{
		BaseURL:        srv.URL,
		DisableRetries: true,
	})

	_, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if hc.IsRateLimitError(err) {
		t.Fatal("expected plain HTTPError (no budget configured), got RateLimitError")
	}
	if n := attempts.Load(); n != 1 {
		t.Fatalf("expected 1 attempt with retries disabled, got %d", n)
	}
}

func TestRetryOn429_5xxStillCapsAtThreeWithBudgetSet(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := hc.New(hc.Config{
		BaseURL:          srv.URL,
		RetryOn429Budget: 30 * time.Second,
	})

	_, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if hc.IsRateLimitError(err) {
		t.Fatalf("expected plain HTTPError for 500, got RateLimitError")
	}
	if n := attempts.Load(); n != 4 {
		t.Fatalf("expected 4 attempts (1 + 3 retries), got %d", n)
	}
}

func TestDeprecationWarning_SunsetHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		w.Header().Set("Sunset", "Sat, 01 Jan 2028 00:00:00 GMT")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var msgs []string
	c := hc.New(hc.Config{BaseURL: srv.URL, OnWarn: func(msg string) { msgs = append(msgs, msg) }})

	_, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected deprecation warning, got none")
	}
	if !strings.Contains(msgs[0], "deprecated") {
		t.Errorf("expected deprecation warning, got %q", msgs[0])
	}
	if !strings.Contains(msgs[0], "days") {
		t.Errorf("expected days-remaining in warning, got %q", msgs[0])
	}
}

func TestDeprecationWarning_DeprecationOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var msgs []string
	c := hc.New(hc.Config{BaseURL: srv.URL, OnWarn: func(msg string) { msgs = append(msgs, msg) }})

	_, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) == 0 || !strings.Contains(msgs[0], "deprecated") {
		t.Errorf("expected deprecation warning, got %v", msgs)
	}
}

func TestDeprecationWarning_NoCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		w.Header().Set("Sunset", "Sat, 01 Jan 2027 00:00:00 GMT")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// no OnWarn — must not panic
	c := hc.New(hc.Config{BaseURL: srv.URL})
	_, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeprecationWarning_NoHeadersNoCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	called := false
	c := hc.New(hc.Config{BaseURL: srv.URL, OnWarn: func(msg string) { called = true }})

	_, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Error("OnWarn should not be called without deprecation headers")
	}
}

func TestRouteParamsMultiple(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/agents/org/org-1/project/proj-2/runs" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := hc.New(hc.Config{BaseURL: srv.URL, DisableRetries: true})

	status, err := c.Call(context.Background(), hc.NewRequest("POST",
		"/api/v2/agents/org/%s/project/%s/runs",
		hc.RouteParams("org-1", "proj-2"),
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
}

// TestRetries504ThenSucceeds pins that a Gateway Timeout is retried and recovered
// from without the caller seeing it.
//
// The sidecar API answers 504 when a sidecar agent does not reply inside the API's
// own per-call budget. That is a transient stall — measured in production, one
// sidecar produced four in a single minute while serving eighteen commands either
// side of them — so a client that surfaced it as a failure would turn a momentary
// hiccup into a failed command. Previously the API reported these as 500, which
// this policy also retries; the point of the test is that the more accurate status
// did not quietly opt out of retrying.
func TestRetries504ThenSucceeds(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := hc.New(hc.Config{BaseURL: srv.URL, RetryOn429Budget: 30 * time.Second})

	var body struct {
		OK bool `json:"ok"`
	}
	_, err := c.Call(context.Background(), hc.NewRequest("GET", "/", hc.JSONDecoder(&body)))
	if err != nil {
		t.Fatalf("a retried 504 must not reach the caller: %v", err)
	}
	if !body.OK {
		t.Fatal("expected the second attempt's body to be decoded")
	}
	if n := attempts.Load(); n != 2 {
		t.Fatalf("expected 2 attempts (1 + 1 retry), got %d", n)
	}
}

// TestRetries504Exhausted pins the attempt count when every attempt times out, so
// a 504 costs the same bounded number of tries as any other 5xx rather than
// hanging or retrying forever.
func TestRetries504Exhausted(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer srv.Close()

	c := hc.New(hc.Config{BaseURL: srv.URL, RetryOn429Budget: 30 * time.Second})

	_, err := c.Call(context.Background(), hc.NewRequest("GET", "/"))
	if err == nil {
		t.Fatal("expected an error once the retries are spent")
	}
	if n := attempts.Load(); n != 4 {
		t.Fatalf("expected 4 attempts (1 + 3 retries), got %d", n)
	}
}

// A 401 is the only signal that a long-lived client's token has gone stale.
// Reloading and retrying is what lets a process that outlives a login — the
// watch daemon holds one client for its whole life — pick the new token up.
func TestReloadToken_RetriesOnceWithTheNewToken(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("Circle-Token")
		seen = append(seen, tok)
		if tok != "fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var reloads atomic.Int32
	cl := hc.New(hc.Config{
		BaseURL:    srv.URL,
		AuthToken:  "stale",
		AuthHeader: "Circle-Token",
		ReloadToken: func() (string, error) {
			reloads.Add(1)
			return "fresh", nil
		},
	})

	status, err := cl.Call(context.Background(), hc.NewRequest(http.MethodGet, "/x"))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if got := []string{"stale", "fresh"}; len(seen) != 2 || seen[0] != got[0] || seen[1] != got[1] {
		t.Errorf("tokens sent = %v, want %v", seen, got)
	}
	if n := reloads.Load(); n != 1 {
		t.Errorf("reloads = %d, want 1", n)
	}
}

// Retrying with the same token would only buy a second 401, and looping on
// reload would turn a revoked token into a keychain read per request.
func TestReloadToken_NoRetryWhenTokenIsUnchanged(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cl := hc.New(hc.Config{
		BaseURL:     srv.URL,
		AuthToken:   "stale",
		AuthHeader:  "Circle-Token",
		ReloadToken: func() (string, error) { return "stale", nil },
	})

	status, err := cl.Call(context.Background(), hc.NewRequest(http.MethodGet, "/x"))
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
	if err == nil {
		t.Error("expected the 401 to surface as an error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("server calls = %d, want 1 (no retry)", n)
	}
}

// A failed reload must not mask the 401, which is the more useful of the two.
func TestReloadToken_ReloadFailureSurfacesTheOriginal401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cl := hc.New(hc.Config{
		BaseURL:     srv.URL,
		AuthToken:   "stale",
		AuthHeader:  "Circle-Token",
		ReloadToken: func() (string, error) { return "", errors.New("keychain unavailable") },
	})

	status, err := cl.Call(context.Background(), hc.NewRequest(http.MethodGet, "/x"))
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
	if err == nil || !hc.HasStatusCode(err, http.StatusUnauthorized) {
		t.Errorf("err = %v, want the original 401", err)
	}
}

// Without a ReloadToken the client behaves exactly as before.
func TestReloadToken_AbsentMeansNoRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cl := hc.New(hc.Config{BaseURL: srv.URL, AuthToken: "stale", AuthHeader: "Circle-Token"})
	_, _ = cl.Call(context.Background(), hc.NewRequest(http.MethodGet, "/x"))
	if n := calls.Load(); n != 1 {
		t.Errorf("server calls = %d, want 1", n)
	}
}

// The first attempt drains the request body, so the retry has to send its own
// copy — otherwise a reloaded token would resend an empty POST.
func TestReloadToken_RetryResendsTheBody(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, strings.TrimSpace(string(b)))
		if r.Header.Get("Circle-Token") != "fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cl := hc.New(hc.Config{
		BaseURL:     srv.URL,
		AuthToken:   "stale",
		AuthHeader:  "Circle-Token",
		ReloadToken: func() (string, error) { return "fresh", nil },
	})

	_, err := cl.Call(context.Background(),
		hc.NewRequest(http.MethodPost, "/x", hc.Body(map[string]string{"name": "chunk"})))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("got %d requests, want 2", len(bodies))
	}
	if bodies[0] != bodies[1] {
		t.Errorf("retry body = %q, want the original %q", bodies[1], bodies[0])
	}
}

// Concurrent 401s must not each trigger a read: on a daemon streaming several
// commands this is a keychain round trip per goroutine.
func TestReloadToken_Concurrent401sReloadOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Circle-Token") != "fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var reloads atomic.Int32
	cl := hc.New(hc.Config{
		BaseURL:    srv.URL,
		AuthToken:  "stale",
		AuthHeader: "Circle-Token",
		ReloadToken: func() (string, error) {
			reloads.Add(1)
			return "fresh", nil
		},
	})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cl.Call(context.Background(), hc.NewRequest(http.MethodGet, "/x"))
		}()
	}
	wg.Wait()

	if n := reloads.Load(); n != 1 {
		t.Errorf("reloads = %d, want 1", n)
	}
}
