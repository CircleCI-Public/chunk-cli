// Package httpcl provides a minimal HTTP client with JSON defaults and retries.
// Inspired by backplane-go/httpcl but stripped to essentials, using
// hashicorp/go-retryablehttp for retry logic.
package httpcl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

// checkDeprecation calls onWarn when the response carries Deprecation or Sunset
// headers, signalling that the endpoint will be removed. The message passed to
// onWarn is plain text — no prefix or newline — so the caller controls formatting.
func checkDeprecation(onWarn func(string), h http.Header) {
	dep := h.Get("Deprecation")
	sunset := h.Get("Sunset")
	if dep == "" && sunset == "" {
		return
	}
	if sunset != "" {
		if t, err := http.ParseTime(sunset); err == nil {
			days := int(time.Until(t).Hours() / 24)
			if days > 0 {
				onWarn(fmt.Sprintf("this API endpoint is deprecated and will be removed in %d days — upgrade chunk CLI", days))
			} else {
				onWarn("this API endpoint is deprecated and removal is imminent — upgrade chunk CLI")
			}
		} else {
			onWarn(fmt.Sprintf("this API endpoint is deprecated and will be removed on %s — upgrade chunk CLI", sunset))
		}
	} else {
		onWarn("this API endpoint is deprecated and will be removed — upgrade chunk CLI")
	}
}

// retryCtxKey is the context key for the per-call retry state.
type retryCtxKey struct{}

// retryState tracks per-call retry counters stored in the request context.
// Using a pointer allows mutation across CheckRetry invocations for the same call.
type retryState struct {
	start                time.Time
	nonRateLimitAttempts int
}

const jsonContentType = "application/json; charset=utf-8"

// Config configures a Client.
type Config struct {
	// BaseURL is prepended to every request route.
	BaseURL string
	// AuthToken is sent as a Bearer token unless AuthHeader is set.
	AuthToken string
	// AuthHeader overrides the header name for AuthToken (e.g. "Circle-Token", "x-api-key").
	// When set, the token is sent as the raw header value (not "Bearer ...").
	AuthHeader string
	// UserAgent sets the User-Agent header on every request.
	UserAgent string
	// Timeout is the per-request timeout. Defaults to 30s.
	Timeout time.Duration
	// DisableRetries disables automatic retries. By default requests are
	// retried up to 3 times with exponential backoff.
	DisableRetries bool
	// RetryOn429Budget, when non-zero, enables retrying HTTP 429 responses by
	// honouring the Retry-After response header. Retries stop when the
	// cumulative wait time would exceed this budget, or when a single
	// Retry-After value exceeds it, and a RateLimitError is returned.
	RetryOn429Budget time.Duration
	// Transport overrides the HTTP transport (useful for testing).
	Transport http.RoundTripper
	// OnWarn, when non-nil, is called with a plain-text warning message when the
	// server signals endpoint removal via Deprecation or Sunset response headers.
	// The caller is responsible for formatting (prefix, newline, colour).
	OnWarn func(msg string)
}

// Client is a simple HTTP client with JSON defaults and automatic retries.
type Client struct {
	baseURL          string
	authToken        string
	authHeader       string
	userAgent        string
	timeout          time.Duration
	retryOn429Budget time.Duration
	onWarn           func(string)
	http             *retryablehttp.Client
}

// New creates a Client from the given config.
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	rc := retryablehttp.NewClient()
	rc.RetryMax = 3
	if cfg.DisableRetries {
		rc.RetryMax = 0
	}
	rc.RetryWaitMin = 50 * time.Millisecond
	rc.RetryWaitMax = 2 * time.Second
	rc.Logger = nil // suppress default log output

	if cfg.RetryOn429Budget > 0 {
		budget := cfg.RetryOn429Budget
		origMax := 3
		if cfg.DisableRetries {
			origMax = 0
		}
		// Raise RetryMax so it never binds before the budget does.
		// Each 429 retry consumes ≥1s (Retry-After floor), so budget/s + origMax is sufficient.
		rc.RetryMax = int(budget/time.Second) + origMax
		rc.CheckRetry = func(ctx context.Context, resp *http.Response, err error) (bool, error) {
			state, _ := ctx.Value(retryCtxKey{}).(*retryState)
			if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
				retryAfter := parseRetryAfter(resp)
				elapsed := time.Duration(0)
				if state != nil {
					elapsed = time.Since(state.start)
				}
				if elapsed+retryAfter > budget {
					return false, &RateLimitError{RetryAfter: retryAfter, Budget: budget}
				}
				return true, nil
			}
			// Cap non-429 retries at the original limit.
			if state != nil {
				state.nonRateLimitAttempts++
				if state.nonRateLimitAttempts > origMax {
					return false, nil
				}
			}
			return retryablehttp.DefaultRetryPolicy(ctx, resp, err)
		}
		// DefaultBackoff already honours Retry-After; keep it.
	}

	if cfg.Transport != nil {
		rc.HTTPClient.Transport = cfg.Transport
	}

	return &Client{
		baseURL:          cfg.BaseURL,
		authToken:        cfg.AuthToken,
		authHeader:       cfg.AuthHeader,
		userAgent:        cfg.UserAgent,
		timeout:          timeout,
		retryOn429Budget: cfg.RetryOn429Budget,
		onWarn:           cfg.OnWarn,
		http:             rc,
	}
}

// parseRetryAfter parses the Retry-After header as seconds or an HTTP date.
func parseRetryAfter(resp *http.Response) time.Duration {
	ra := resp.Header.Get("Retry-After")
	if ra == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(ra, 10, 64); err == nil {
		if secs > 0 {
			return time.Duration(secs) * time.Second
		}
		return 0
	}
	if t, err := http.ParseTime(ra); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// Call executes the request and returns the HTTP status code.
// Non-2xx responses return an *HTTPError. If a decoder is set and the
// response is 2xx, the response body is decoded.
func (c *Client) Call(ctx context.Context, r Request) (int, error) {
	u, err := url.Parse(c.baseURL + r.URL())
	if err != nil {
		return 0, fmt.Errorf("httpcl: bad url: %w", err)
	}
	if len(r.query) > 0 {
		u.RawQuery = r.query.Encode()
	}

	var bodyReader io.Reader
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return 0, fmt.Errorf("httpcl: marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	ctxTimeout := c.timeout
	if c.retryOn429Budget > 0 {
		ctx = context.WithValue(ctx, retryCtxKey{}, &retryState{start: time.Now()})
		ctxTimeout = c.retryOn429Budget + c.timeout // extend deadline to cover retry waits
	}
	ctx, cancel := context.WithTimeout(ctx, ctxTimeout)
	defer cancel()

	req, err := retryablehttp.NewRequestWithContext(ctx, r.method, u.String(), bodyReader)
	if err != nil {
		return 0, fmt.Errorf("httpcl: new request: %w", err)
	}

	// Set headers
	if r.body != nil {
		req.Header.Set("Content-Type", jsonContentType)
	}
	req.Header.Set("Accept", "application/json")

	if c.authToken != "" {
		if c.authHeader != "" {
			req.Header.Set(c.authHeader, c.authToken)
		} else {
			req.Header.Set("Authorization", "Bearer "+c.authToken)
		}
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	for k, vals := range r.headers {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.http.Do(req)
	if resp != nil {
		defer func() {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	if err != nil {
		return 0, err
	}

	status := resp.StatusCode

	if status >= 200 && status < 300 {
		if c.onWarn != nil {
			checkDeprecation(c.onWarn, resp.Header)
		}
		if r.decoder != nil {
			if err := r.decoder(resp.Body); err != nil {
				return status, fmt.Errorf("httpcl: decode response: %w", err)
			}
		}
		return status, nil
	}

	body, _ := io.ReadAll(resp.Body)
	return status, &HTTPError{
		Method:     r.method,
		Route:      r.route,
		StatusCode: status,
		Body:       body,
	}
}
