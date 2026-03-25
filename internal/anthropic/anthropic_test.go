package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fixtures"
)

// newTestClient creates a Client pointing at the given base URL with a test API key.
func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_BASE_URL", baseURL)
	c, err := New()
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
			errContains: "ANTHROPIC_API_KEY",
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
			t.Setenv("ANTHROPIC_API_KEY", tt.apiKey)
			t.Setenv("ANTHROPIC_BASE_URL", tt.baseURL)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())

			c, err := New()
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

	// Set env so New() succeeds, then the fake will check the header.
	t.Setenv("ANTHROPIC_API_KEY", "") // empty key
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	// New() rejects empty key, so we can't test 401 through New().
	// Instead, directly test that the error path works when the server returns non-2xx.
	// We'll use a custom handler that always returns 401.
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": "unauthorized"}`))
	}))
	defer errSrv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "bad-key")
	t.Setenv("ANTHROPIC_BASE_URL", errSrv.URL)
	c, err := New()
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

func TestAnalyzeReviews(t *testing.T) {
	fake := fakes.NewFakeAnthropic(fixtures.AnalysisResponse)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	got, err := c.AnalyzeReviews(context.Background(), "some review data", "")
	assert.NilError(t, err)
	assert.Equal(t, got, fixtures.AnalysisResponse)

	// Verify default model was used.
	reqs := fake.Recorder.AllRequests()
	assert.Equal(t, len(reqs), 1)
	var body messagesRequest
	assert.NilError(t, json.Unmarshal(reqs[0].Body, &body))
	assert.Equal(t, body.Model, "claude-sonnet-4-5-20250929") // config.AnalyzeModel
	assert.Equal(t, body.MaxTokens, 16000)
}

func TestAnalyzeReviewsCustomModel(t *testing.T) {
	fake := fakes.NewFakeAnthropic(fixtures.AnalysisResponse)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	got, err := c.AnalyzeReviews(context.Background(), "data", "custom-model")
	assert.NilError(t, err)
	assert.Equal(t, got, fixtures.AnalysisResponse)

	var body messagesRequest
	assert.NilError(t, json.Unmarshal(fake.Recorder.AllRequests()[0].Body, &body))
	assert.Equal(t, body.Model, "custom-model")
}

func TestGenerateReviewPrompt(t *testing.T) {
	fake := fakes.NewFakeAnthropic(fixtures.PromptResponse)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	got, err := c.GenerateReviewPrompt(context.Background(), "analysis text", "", false)
	assert.NilError(t, err)
	assert.Equal(t, got, fixtures.PromptResponse)

	reqs := fake.Recorder.AllRequests()
	assert.Equal(t, len(reqs), 1)
	var body messagesRequest
	assert.NilError(t, json.Unmarshal(reqs[0].Body, &body))
	assert.Equal(t, body.Model, "claude-opus-4-5-20251101") // config.PromptModel
	assert.Equal(t, body.MaxTokens, 8000)

	// Verify the prompt includes the analysis and no-attribution instruction.
	assert.Assert(t, strings.Contains(body.Messages[0].Content, "analysis text"))
	assert.Assert(t, strings.Contains(body.Messages[0].Content, "Do not include reviewer attribution"))
}

func TestGenerateReviewPromptCustomModel(t *testing.T) {
	fake := fakes.NewFakeAnthropic(fixtures.PromptResponse)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.GenerateReviewPrompt(context.Background(), "analysis", "my-model", false)
	assert.NilError(t, err)

	var body messagesRequest
	assert.NilError(t, json.Unmarshal(fake.Recorder.AllRequests()[0].Body, &body))
	assert.Equal(t, body.Model, "my-model")
}

func TestGenerateReviewPromptWithAttribution(t *testing.T) {
	fake := fakes.NewFakeAnthropic(fixtures.PromptResponse)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.GenerateReviewPrompt(context.Background(), "analysis", "", true)
	assert.NilError(t, err)

	var body messagesRequest
	assert.NilError(t, json.Unmarshal(fake.Recorder.AllRequests()[0].Body, &body))
	assert.Assert(t, strings.Contains(body.Messages[0].Content, "Include which reviewers emphasize each rule"))
	assert.Assert(t, !strings.Contains(body.Messages[0].Content, "Do not include reviewer attribution"))
}

func TestBuildPromptGenerationPrompt(t *testing.T) {
	t.Run("without attribution", func(t *testing.T) {
		got := buildPromptGenerationPrompt("my analysis", false)
		assert.Assert(t, strings.Contains(got, "my analysis"))
		assert.Assert(t, strings.Contains(got, "Do not include reviewer attribution"))
		assert.Assert(t, strings.Contains(got, "transform a code review analysis report"))
	})

	t.Run("with attribution", func(t *testing.T) {
		got := buildPromptGenerationPrompt("my analysis", true)
		assert.Assert(t, strings.Contains(got, "my analysis"))
		assert.Assert(t, strings.Contains(got, "Include which reviewers emphasize each rule"))
		assert.Assert(t, !strings.Contains(got, "Do not include reviewer attribution"))
	})
}

func TestFullWorkflow(t *testing.T) {
	// Simulates the two-step workflow: analyze then generate prompt.
	fake := fakes.NewFakeAnthropic(fixtures.AnalysisResponse, fixtures.PromptResponse)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx := context.Background()

	analysis, err := c.AnalyzeReviews(ctx, "review data", "")
	assert.NilError(t, err)
	assert.Equal(t, analysis, fixtures.AnalysisResponse)

	prompt, err := c.GenerateReviewPrompt(ctx, analysis, "", false)
	assert.NilError(t, err)
	assert.Equal(t, prompt, fixtures.PromptResponse)

	// Verify two requests were made in order.
	reqs := fake.Recorder.AllRequests()
	assert.Equal(t, len(reqs), 2)
	assert.Equal(t, reqs[0].URL.Path, "/v1/messages")
	assert.Equal(t, reqs[1].URL.Path, "/v1/messages")

	// First request is the analysis (raw review data as prompt).
	var body0 messagesRequest
	assert.NilError(t, json.Unmarshal(reqs[0].Body, &body0))
	assert.Equal(t, body0.Messages[0].Content, "review data")

	// Second request is the prompt generation (contains the analysis report).
	var body1 messagesRequest
	assert.NilError(t, json.Unmarshal(reqs[1].Body, &body1))
	assert.Assert(t, strings.Contains(body1.Messages[0].Content, fixtures.AnalysisResponse))
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
