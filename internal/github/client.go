package github

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// Client is a GitHub GraphQL API client.
type Client struct {
	http *httpcl.Client

	// retryDelayOverride, if non-zero, replaces the exponential backoff
	// delay in doWithRetry. Intended for tests only.
	retryDelayOverride time.Duration

	// logStatus, if non-nil, is called with informational progress
	// messages (e.g. retry/rate-limit waits). Callers typically wire
	// this to stderr output.
	logStatus func(string)
}

// SetLogStatus sets an optional callback for progress/status messages.
func (c *Client) SetLogStatus(fn func(string)) {
	c.logStatus = fn
}

// New creates a GitHub GraphQL client.
// It resolves the token via config (GITHUB_TOKEN env > config file) and reads
// GITHUB_API_URL from the environment.
func New() (*Client, error) {
	rc, err := config.Resolve("", "")
	if rc.GitHubToken == "" {
		if err != nil {
			return nil, fmt.Errorf("resolve config: %w", err)
		}
		return nil, fmt.Errorf("GitHub token not found: set GITHUB_TOKEN or run 'chunk auth set github'")
	}

	baseURL := os.Getenv("GITHUB_API_URL")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}

	c := httpcl.New(httpcl.Config{
		BaseURL:    baseURL,
		AuthToken:  "token " + rc.GitHubToken,
		AuthHeader: "Authorization",
		UserAgent:  "chunk-cli",
	})

	return &Client{http: c}, nil
}

// graphQLRequest is the JSON body sent to /graphql.
type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// do executes a GraphQL query and decodes the response into dest.
func (c *Client) do(ctx context.Context, query string, vars map[string]any, dest any) error {
	req := httpcl.NewRequest("POST", "/graphql",
		httpcl.Body(graphQLRequest{Query: query, Variables: vars}),
		httpcl.JSONDecoder(dest),
	)

	_, err := c.http.Call(ctx, req)
	return err
}

// ValidateOrg checks that the org is accessible.
func (c *Client) ValidateOrg(ctx context.Context, org string) error {
	var resp graphQLResponse[struct {
		Organization struct {
			Login string `json:"login"`
		} `json:"organization"`
	}]

	err := c.do(ctx, `query($org: String!) { organization(login: $org) { login } }`, map[string]any{"org": org}, &resp)
	if err != nil {
		return fmt.Errorf("validate org: %w", err)
	}
	if hasResolutionError(resp.Errors) {
		return fmt.Errorf("organization %q not found or not accessible", org)
	}
	return nil
}

// CheckRateLimit queries the current rate limit status.
func (c *Client) CheckRateLimit(ctx context.Context) error {
	var resp graphQLResponse[struct {
		RateLimit RateLimit `json:"rateLimit"`
	}]
	return c.do(ctx, `{ rateLimit { remaining resetAt } }`, nil, &resp)
}
