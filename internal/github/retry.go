package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

const (
	maxRetries         = 3
	initialRetryDelay  = 2 * time.Second
	rateLimitThreshold = 500
)

// isHTMLError checks if an error message looks like an HTML response
// (GitHub 500/503 error page).
func isHTMLError(msg string) bool {
	return strings.Contains(msg, "<!DOCTYPE") ||
		strings.Contains(msg, "<html") ||
		strings.Contains(msg, "Unicorn")
}

// isRetryable returns true for transient errors worth retrying:
// timeouts, 5xx HTTP errors, and HTML error responses.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}

	// HTTP 5xx errors
	var httpErr *hc.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode >= 500 {
		return true
	}

	msg := err.Error()

	// Context deadline / timeout
	if errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "ETIMEDOUT") {
		return true
	}

	// GitHub sometimes returns HTML error pages instead of JSON,
	// which causes decode errors containing the HTML content.
	if isHTMLError(msg) {
		return true
	}

	// JSON decode errors from non-JSON responses (e.g. HTML error pages)
	// show up as "invalid character '<'" from Go's JSON decoder.
	if strings.Contains(msg, "invalid character '<'") {
		return true
	}

	return false
}

// retryDelay returns the backoff delay for a given attempt.
// Tests can override this via Client.retryDelayOverride.
func (c *Client) retryDelay(attempt int) time.Duration {
	if c.retryDelayOverride > 0 {
		return c.retryDelayOverride
	}
	return initialRetryDelay * (1 << attempt)
}

// doWithRetry wraps Client.do with retry logic for transient errors.
// It retries up to maxRetries times with exponential backoff.
func (c *Client) doWithRetry(ctx context.Context, query string, vars map[string]any, dest any) error {
	var lastErr error

	for attempt := range maxRetries {
		err := c.do(ctx, query, vars, dest)
		if err == nil {
			return nil
		}
		if !isRetryable(err) {
			return mapErr("", err)
		}

		lastErr = err
		delay := c.retryDelay(attempt)
		c.status(fmt.Sprintf("GitHub API error on attempt %d/%d, retrying in %s...", attempt+1, maxRetries, delay))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}

	if lastErr != nil && isHTMLError(lastErr.Error()) {
		return &RetryError{Retries: maxRetries, ServerError: true}
	}
	return &RetryError{Retries: maxRetries, Err: mapErr("", lastErr)}
}

// waitForRateLimit sleeps until the rate limit resets if remaining is below the threshold.
func (c *Client) waitForRateLimit(ctx context.Context, rl RateLimit) error {
	if rl.Remaining > rateLimitThreshold {
		return nil
	}

	resetTime, err := time.Parse(time.RFC3339, rl.ResetAt)
	if err != nil {
		return nil // can't parse, skip waiting
	}

	wait := time.Until(resetTime) + time.Second // +1s buffer
	if wait <= 0 {
		return nil
	}

	c.status(fmt.Sprintf("Rate limit low (%d remaining). Waiting %s until reset...", rl.Remaining, wait.Truncate(time.Second)))

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// status emits a progress message via the logStatus callback, if set.
func (c *Client) status(msg string) {
	if c.logStatus != nil {
		c.logStatus(msg)
	}
}
