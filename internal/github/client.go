package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

type Config struct {
	Token     string
	BaseURL   string
	LogStatus func(string)
}

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

func New(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, ErrTokenNotFound
	}
	c := httpcl.New(httpcl.Config{
		BaseURL:    cfg.BaseURL,
		AuthToken:  "token " + cfg.Token,
		AuthHeader: "Authorization",
		UserAgent:  "chunk-cli",
	})
	return &Client{http: c, logStatus: cfg.LogStatus}, nil
}

// graphQLRequest is the JSON body sent to /graphql.
type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// StatusError represents an HTTP error from the GitHub API without exposing httpcl internals.
type StatusError struct {
	Op         string
	StatusCode int
}

func (e *StatusError) Error() string {
	if e.Op != "" {
		return fmt.Sprintf("%s: %d %s", e.Op, e.StatusCode, http.StatusText(e.StatusCode))
	}
	return fmt.Sprintf("%d %s", e.StatusCode, http.StatusText(e.StatusCode))
}

func mapErr(op string, err error) error {
	var he *httpcl.HTTPError
	if !errors.As(err, &he) {
		if op == "" {
			return err
		}
		return fmt.Errorf("%s: %w", op, err)
	}
	return &StatusError{Op: op, StatusCode: he.StatusCode}
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
		return mapErr("validate org", err)
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
	if err := c.do(ctx, `{ rateLimit { remaining resetAt } }`, nil, &resp); err != nil {
		return mapErr("check rate limit", err)
	}
	return nil
}
