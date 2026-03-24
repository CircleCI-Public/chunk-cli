package github

import (
	"context"
	"fmt"
	"os"

	"github.com/CircleCI-Public/chunk-cli/httpcl"
)

// Client is a GitHub GraphQL API client.
type Client struct {
	http *httpcl.Client
}

// New creates a GitHub GraphQL client.
// It reads GITHUB_TOKEN and GITHUB_API_URL from the environment.
func New() (*Client, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}

	baseURL := os.Getenv("GITHUB_API_URL")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}

	c := httpcl.New(httpcl.Config{
		BaseURL:    baseURL,
		AuthToken:  "token " + token,
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
