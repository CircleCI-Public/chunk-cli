package anthropic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// Client is an Anthropic Messages API client.
type Client struct {
	http *httpcl.Client
}

// New creates an Anthropic API client.
// It resolves the API key via config (env > config file) and reads
// ANTHROPIC_BASE_URL from the environment.
func New() (*Client, error) {
	rc := config.Resolve("", "")
	key := rc.APIKey
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable or config file is required")
	}

	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	c := httpcl.New(httpcl.Config{
		BaseURL:    baseURL,
		AuthToken:  key,
		AuthHeader: "x-api-key",
		UserAgent:  "chunk-cli",
		Timeout:    120 * time.Second,
	})

	return &Client{http: c}, nil
}

// messagesRequest is the JSON body for POST /v1/messages.
type messagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// messagesResponse is the JSON response from POST /v1/messages.
type messagesResponse struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Ask sends a single user message and returns the assistant text.
// An optional system prompt can be passed as the last argument.
func (c *Client) Ask(ctx context.Context, model string, maxTokens int, prompt string, system ...string) (string, error) {
	var resp messagesResponse
	body := messagesRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  []message{{Role: "user", Content: prompt}},
	}
	if len(system) > 0 {
		body.System = system[0]
	}
	req := httpcl.NewRequest("POST", "/v1/messages",
		httpcl.Body(body),
		httpcl.Header("anthropic-version", "2023-06-01"),
		httpcl.JSONDecoder(&resp),
	)

	_, err := c.http.Call(ctx, req)
	if err != nil {
		return "", fmt.Errorf("anthropic messages: %w", err)
	}

	for _, block := range resp.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("no text content in Anthropic response")
}

// IsTokenLimitError reports whether err is an Anthropic API error indicating
// that the prompt exceeds the model's context window.
func IsTokenLimitError(err error) bool {
	var he *httpcl.HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return strings.Contains(string(he.Body), "prompt is too long")
}
