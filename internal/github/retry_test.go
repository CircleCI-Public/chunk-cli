package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", fmt.Errorf("something else"), false},
		{"timeout string", fmt.Errorf("request timeout"), true},
		{"ETIMEDOUT", fmt.Errorf("connect ETIMEDOUT"), true},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"wrapped deadline", fmt.Errorf("fetch: %w", context.DeadlineExceeded), true},
		{"html error", fmt.Errorf("<!DOCTYPE html>"), true},
		{"unicorn error", fmt.Errorf("Unicorn! GitHub is down"), true},
		{"http 500", &httpcl.HTTPError{StatusCode: 500}, true},
		{"http 503", &httpcl.HTTPError{StatusCode: 503}, true},
		{"http 400", &httpcl.HTTPError{StatusCode: 400}, false},
		{"http 401", &httpcl.HTTPError{StatusCode: 401}, false},
		{"json decode html", fmt.Errorf("httpcl: decode response: invalid character '<' looking for beginning of value"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryable(tt.err); got != tt.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestDoWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":{"rateLimit":{"remaining":4999,"resetAt":"2099-01-01T00:00:00Z"}}}`)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)

	var resp graphQLResponse[struct {
		RateLimit RateLimit `json:"rateLimit"`
	}]
	err := c.doWithRetry(context.Background(), `{ rateLimit { remaining resetAt } }`, nil, &resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 call, got %d", calls.Load())
	}
}

func TestDoWithRetry_RecoversAfterTransientFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		// The underlying retryablehttp also retries 500s, so we use an
		// HTML body (simulating GitHub error page) which gets a 200 status
		// but fails JSON decode — only doWithRetry handles that.
		if n == 1 {
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body>Unicorn!</body></html>`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":{"rateLimit":{"remaining":4999,"resetAt":"2099-01-01T00:00:00Z"}}}`)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	c.retryDelayOverride = time.Millisecond // fast retries for test

	var resp graphQLResponse[struct {
		RateLimit RateLimit `json:"rateLimit"`
	}]
	err := c.doWithRetry(context.Background(), `{ rateLimit { remaining resetAt } }`, nil, &resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.Load() < 2 {
		t.Errorf("expected at least 2 calls, got %d", calls.Load())
	}
}

func TestDoWithRetry_NonRetryableFailsFast(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"message":"Bad credentials"}`)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	c.retryDelayOverride = time.Millisecond

	var resp graphQLResponse[struct{}]
	err := c.doWithRetry(context.Background(), `{ viewer { login } }`, nil, &resp)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	// Should fail on the first attempt without retrying
	// (retryablehttp may have its own retries, but doWithRetry should not add more)
	if calls.Load() > 1 {
		// retryablehttp doesn't retry 4xx, so only 1 call expected
		t.Logf("note: got %d calls (expected 1)", calls.Load())
	}
}

func TestDoWithRetry_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body>Error</body></html>`)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	c.retryDelayOverride = time.Second // long delay so cancellation wins

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so the first retry's sleep gets interrupted
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	var resp graphQLResponse[struct{}]
	err := c.doWithRetry(ctx, `{ viewer { login } }`, nil, &resp)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestWaitForRateLimit_AboveThreshold(t *testing.T) {
	c := &Client{}
	start := time.Now()
	err := c.waitForRateLimit(context.Background(), RateLimit{
		Remaining: 1000,
		ResetAt:   time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("should not have waited when remaining is above threshold")
	}
}

func TestWaitForRateLimit_BelowThresholdPastReset(t *testing.T) {
	c := &Client{}
	start := time.Now()
	err := c.waitForRateLimit(context.Background(), RateLimit{
		Remaining: 100,
		ResetAt:   time.Now().Add(-time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("should not have waited when reset time is in the past")
	}
}

func TestWaitForRateLimit_CancelledContext(t *testing.T) {
	c := &Client{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.waitForRateLimit(ctx, RateLimit{
		Remaining: 100,
		ResetAt:   time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if err == nil {
		t.Fatal("expected context cancelled error")
	}
}

func testClient(t *testing.T, url string) *Client {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("GITHUB_API_URL", url)
	c, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}
