package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

var (
	// ErrKeyNotFound indicates no Anthropic API key was found in env or config.
	ErrKeyNotFound = errors.New("api key not found")

	// ErrTokenLimit indicates the prompt exceeds the model's context window.
	ErrTokenLimit = errors.New("prompt exceeds context window")
)

func isTokenLimitErr(err error) bool {
	var he *httpcl.HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return strings.Contains(string(he.Body), "prompt is too long")
}

// StatusError represents an HTTP error from the Anthropic API without exposing httpcl internals.
type StatusError struct {
	Op         string
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: %d %s", e.Op, e.StatusCode, http.StatusText(e.StatusCode))
}

func mapErr(op string, err error) error {
	var he *httpcl.HTTPError
	if !errors.As(err, &he) {
		return fmt.Errorf("%s: %w", op, err)
	}
	return &StatusError{Op: op, StatusCode: he.StatusCode}
}

// Client is an Anthropic Messages API client.
type Client struct {
	http *httpcl.Client
}

// New creates an Anthropic API client.
// It resolves the API key via config (env > config file) and reads
// ANTHROPIC_BASE_URL from the environment.
func New() (*Client, error) {
	rc, err := config.Resolve("", "")
	key := rc.AnthropicAPIKey
	if key == "" {
		if err != nil {
			return nil, fmt.Errorf("resolve config: %w", err)
		}
		return nil, ErrKeyNotFound
	}

	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	c := httpcl.New(httpcl.Config{
		BaseURL:        baseURL,
		AuthToken:      key,
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
	req := httpcl.NewRequest("POST", "/v1/messages",
		httpcl.Body(body),
		httpcl.Header("anthropic-version", "2023-06-01"),
		httpcl.JSONDecoder(&resp),
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
