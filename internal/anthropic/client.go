package anthropic

import (
	"context"
	"fmt"
	"os"

	"github.com/CircleCI-Public/chunk-cli/httpcl"
)

// Client is an Anthropic Messages API client.
type Client struct {
	http *httpcl.Client
}

// New creates an Anthropic API client.
// It reads ANTHROPIC_API_KEY and ANTHROPIC_BASE_URL from the environment.
func New() (*Client, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable is required")
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
	})

	return &Client{http: c}, nil
}

// messagesRequest is the JSON body for POST /v1/messages.
type messagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
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

// sendMessage sends a single user message and returns the assistant text.
func (c *Client) sendMessage(ctx context.Context, model string, maxTokens int, prompt string) (string, error) {
	var resp messagesResponse
	req := httpcl.NewRequest("POST", "/v1/messages",
		httpcl.Body(messagesRequest{
			Model:     model,
			MaxTokens: maxTokens,
			Messages:  []message{{Role: "user", Content: prompt}},
		}),
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
