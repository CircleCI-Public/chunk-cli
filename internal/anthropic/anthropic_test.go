package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// newTestClient creates a Client pointing at the given base URL with a test API key.
func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := New(Config{APIKey: "test-key", BaseURL: baseURL})
	assert.NilError(t, err)
	return c
}

func TestNew(t *testing.T) {
	tests := []struct {
		name        string
		apiKey      string
		baseURL     string
		wantErr     bool
		errContains string
	}{
		{
			name:        "missing API key",
			apiKey:      "",
			wantErr:     true,
			errContains: "api key not found",
		},
		{
			name:   "default base URL",
			apiKey: "test-key",
		},
		{
			name:    "custom base URL",
			apiKey:  "test-key",
			baseURL: "http://custom:1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL := tt.baseURL
			if baseURL == "" {
				baseURL = "https://api.anthropic.com"
			}
			c, err := New(Config{APIKey: tt.apiKey, BaseURL: baseURL})
			if tt.wantErr {
				assert.Assert(t, err != nil)
				if tt.errContains != "" {
					assert.Assert(t, strings.Contains(err.Error(), tt.errContains))
				}
				return
			}
			assert.NilError(t, err)
			assert.Assert(t, c != nil)
		})
	}
}

func TestSendMessage(t *testing.T) {
	fake := fakes.NewFakeAnthropic("hello from claude")
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	got, err := c.Ask(context.Background(), "test-model", 100, "say hello")
	assert.NilError(t, err)
	assert.Equal(t, got, "hello from claude")

	// Verify request was recorded with correct path and auth header.
	reqs := fake.Recorder.AllRequests()
	assert.Equal(t, len(reqs), 1)
	assert.Equal(t, reqs[0].URL.Path, "/v1/messages")
	assert.Equal(t, reqs[0].Header.Get("X-Api-Key"), "test-key")

	// Verify request body.
	var body messagesRequest
	assert.NilError(t, json.Unmarshal(reqs[0].Body, &body))
	assert.Equal(t, body.Model, "test-model")
	assert.Equal(t, body.MaxTokens, 100)
	assert.Equal(t, len(body.Messages), 1)
	assert.Equal(t, body.Messages[0].Role, "user")
	assert.Equal(t, body.Messages[0].Content, "say hello")
}

func TestSendMessageQueuedResponses(t *testing.T) {
	fake := fakes.NewFakeAnthropic("first", "second", "third")
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx := context.Background()

	r1, err := c.Ask(ctx, "m", 10, "p1")
	assert.NilError(t, err)
	assert.Equal(t, r1, "first")

	r2, err := c.Ask(ctx, "m", 10, "p2")
	assert.NilError(t, err)
	assert.Equal(t, r2, "second")

	r3, err := c.Ask(ctx, "m", 10, "p3")
	assert.NilError(t, err)
	assert.Equal(t, r3, "third")

	// Beyond queued responses falls back to "default response".
	r4, err := c.Ask(ctx, "m", 10, "p4")
	assert.NilError(t, err)
	assert.Equal(t, r4, "default response")
}

func TestSendMessageAuthError(t *testing.T) {
	// The fake returns 401 when x-api-key header is missing.
	// We create a client with an empty key by building it manually.
	fake := fakes.NewFakeAnthropic("should not reach")
	srv := httptest.NewServer(fake)
	defer srv.Close()

	// New() rejects empty key, so we can't test 401 through New().
	// Instead, directly test that the error path works when the server returns non-2xx.
	// We'll use a custom handler that always returns 401.
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": "unauthorized"}`))
	}))
	defer errSrv.Close()

	c, err := New(Config{APIKey: "bad-key", BaseURL: errSrv.URL})
	assert.NilError(t, err)

	_, err = c.Ask(context.Background(), "m", 10, "p")
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "anthropic messages"))
}

func TestSendMessageServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "internal"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Ask(context.Background(), "m", 10, "p")
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "anthropic messages"))
}

// Edge case: response with no text content blocks.
// Hard to trigger with the fake since it always returns a text block.
// Tested via a custom handler that returns an empty content array.
func TestSendMessageNoTextContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content": []}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Ask(context.Background(), "m", 10, "p")
	assert.Assert(t, err != nil)
	assert.Assert(t, strings.Contains(err.Error(), "no text content"))
}

func TestSendMessageNonTextBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content": [{"type": "image", "text": ""}, {"type": "text", "text": "found it"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	got, err := c.Ask(context.Background(), "m", 10, "p")
	assert.NilError(t, err)
	assert.Equal(t, got, "found it")
}

func TestErrTokenLimit(t *testing.T) {
	t.Run("wrapped for prompt too long", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 200000 tokens > 100000 maximum"}}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv.URL)
		_, err := c.Ask(context.Background(), "m", 10, "p")
		assert.Assert(t, err != nil)
		assert.Assert(t, errors.Is(err, ErrTokenLimit))
	})

	t.Run("not wrapped for other errors", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal server error"}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv.URL)
		_, err := c.Ask(context.Background(), "m", 10, "p")
		assert.Assert(t, err != nil)
		assert.Assert(t, !errors.Is(err, ErrTokenLimit))
	})

	t.Run("not present for nil error", func(t *testing.T) {
		assert.Assert(t, !errors.Is(nil, ErrTokenLimit))
	})
}

func TestCancelledContext(t *testing.T) {
	fake := fakes.NewFakeAnthropic("response")
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := c.Ask(ctx, "m", 10, "p")
	assert.Assert(t, err != nil)
}
