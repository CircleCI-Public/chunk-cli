package httpcl_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
