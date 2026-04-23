package anthropic

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

var (
	// ErrKeyNotFound indicates no Anthropic API key was found in env or config.
	ErrKeyNotFound = errors.New("api key not found")

	// ErrTokenLimit indicates the prompt exceeds the model's context window.
	ErrTokenLimit = errors.New("prompt exceeds context window")
)

// StatusError is an alias for the shared httpcl.StatusError type.
type StatusError = hc.StatusError

type Config struct {
	APIKey  string
	BaseURL string
}

// Client is an Anthropic Messages API client.
type Client struct {
	http *hc.Client
}

func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, ErrKeyNotFound
	}
	c := hc.New(hc.Config{
		BaseURL:        cfg.BaseURL,
		AuthToken:      cfg.APIKey,
		AuthHeader:     "x-api-key",
		UserAgent:      "chunk-cli",
		Timeout:        5 * time.Minute,
		DisableRetries: true,
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
	req := hc.NewRequest("POST", "/v1/messages",
		hc.Body(body),
		hc.Header("anthropic-version", "2023-06-01"),
		hc.JSONDecoder(&resp),
	)

	_, err := c.http.Call(ctx, req)
	if err != nil {
		if isTokenLimitErr(err) {
			return "", fmt.Errorf("anthropic messages: %w", ErrTokenLimit)
		}
		return "", mapErr("anthropic messages", err)
	}

	for _, block := range resp.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("no text content in Anthropic response")
}

func isTokenLimitErr(err error) bool {
	var he *hc.HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return strings.Contains(string(he.Body), "prompt is too long")
}

func mapErr(op string, err error) error {
	var he *hc.HTTPError
	if !errors.As(err, &he) {
		return fmt.Errorf("%s: %w", op, err)
	}
	return &StatusError{Op: op, StatusCode: he.StatusCode}
}
