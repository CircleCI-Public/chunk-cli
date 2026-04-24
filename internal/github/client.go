package github

import (
	"context"
	"errors"
	"fmt"
	"time"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

type Config struct {
	Token     string
	BaseURL   string
	LogStatus func(string)
}

// Client is a GitHub GraphQL API client.
type Client struct {
	http *hc.Client

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
	c := hc.New(hc.Config{
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

// StatusError is an alias for the shared httpcl.StatusError type.
type StatusError = hc.StatusError

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

func (c *Client) do(ctx context.Context, query string, vars map[string]any, dest any) error {
	req := hc.NewRequest("POST", "/graphql",
		hc.Body(graphQLRequest{Query: query, Variables: vars}),
		hc.JSONDecoder(dest),
	)

	_, err := c.http.Call(ctx, req)
	return err
}

func mapErr(op string, err error) error {
	var he *hc.HTTPError
	if !errors.As(err, &he) {
		if op == "" {
			return err
		}
		return fmt.Errorf("%s: %w", op, err)
	}
	return &StatusError{Op: op, StatusCode: he.StatusCode}
}
