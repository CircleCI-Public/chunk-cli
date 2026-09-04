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
	"sync"
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
	// ReloadToken, when non-nil, re-reads the caller's stored credential after a
	// 401. When it returns a token different from the one that was just
	// rejected, the request is retried once with the new token; otherwise the
	// 401 is returned as-is.
	//
	// This is deliberately a reload and not a refresh: there is no refresh grant
	// to call, so the only way a token can improve is if something else stored a
	// new one. It exists for long-lived processes — a watch daemon holds one
	// client for its whole life, and without this a `chunk auth login` in
	// another terminal never reaches it.
	ReloadToken func() (string, error)
}

// Client is a simple HTTP client with JSON defaults and automatic retries.
type Client struct {
	baseURL string
	// mu guards authToken, which ReloadToken can replace mid-life. Everything
	// else here is immutable after New.
	mu               sync.RWMutex
	authToken        string
	reloadToken      func() (string, error)
	authHeader       string
	userAgent        string
	timeout          time.Duration
	retryOn429Budget time.Duration
	onWarn           func(string)
	http             *retryablehttp.Client
}

// token returns the credential to send on the next attempt.
func (c *Client) token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.authToken
}

// reload re-reads the stored credential after used was rejected, and reports
// whether the client now holds a different one — i.e. whether a retry is worth
// making.
//
// The lock is held across the reload so that concurrent 401s produce one read
// rather than one per caller: reading can mean a keychain round trip, and a
// daemon streaming several commands can hit this from several goroutines at
// once. Whoever arrives second sees the token has already moved and retries
// without reading again.
func (c *Client) reload(used string) bool {
	if c.reloadToken == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.authToken != used {
		return true
	}
	tok, err := c.reloadToken()
	// A failed reload is not worth surfacing: the caller already has a 401,
	// which is the more useful error of the two.
	if err != nil || tok == "" || tok == used {
		return false
	}
	c.authToken = tok
	return true
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
		reloadToken:      cfg.ReloadToken,
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

	var bodyBytes []byte
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return 0, fmt.Errorf("httpcl: marshal body: %w", err)
		}
		// Retained rather than wrapped in a reader once: a 401 retry needs to
		// send the same body again, and the first attempt will have drained it.
		bodyBytes = b
	}

	cancel := func() {}
	if !r.noTimeout {
		ctxTimeout := c.timeout
		if c.retryOn429Budget > 0 {
			ctx = context.WithValue(ctx, retryCtxKey{}, &retryState{start: time.Now()})
			ctxTimeout = c.retryOn429Budget + c.timeout // extend deadline to cover retry waits
		}
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, ctxTimeout)
		cancel = timeoutCancel
	}
	defer cancel()

	status, tok, err := c.attempt(ctx, r, u, bodyBytes)
	// One retry, and only when the credential actually changed. Retrying with
	// the same token would just buy a second 401, and looping on reload would
	// turn a revoked token into a keychain read per request.
	if status == http.StatusUnauthorized && c.reload(tok) {
		status, _, err = c.attempt(ctx, r, u, bodyBytes)
	}
	return status, err
}

// attempt performs one request and reports the token it authenticated with, so
// the caller can tell whether a reload has since superseded it.
func (c *Client) attempt(ctx context.Context, r Request, u *url.URL, bodyBytes []byte) (int, string, error) {
	var bodyReader io.Reader
	if bodyBytes != nil {
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := retryablehttp.NewRequestWithContext(ctx, r.method, u.String(), bodyReader)
	if err != nil {
		return 0, "", fmt.Errorf("httpcl: new request: %w", err)
	}

	// Set headers
	if r.body != nil {
		req.Header.Set("Content-Type", jsonContentType)
	}
	req.Header.Set("Accept", "application/json")

	tok := c.token()
	if tok != "" {
		if c.authHeader != "" {
			req.Header.Set(c.authHeader, tok)
		} else {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	// Per-request headers override the defaults set above rather than appending
	// to them, so a caller can replace Accept (e.g. to request a stream).
	for k, vals := range r.headers {
		req.Header.Del(k)
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
		return 0, tok, err
	}

	status := resp.StatusCode

	if status >= 200 && status < 300 {
		if c.onWarn != nil {
			checkDeprecation(c.onWarn, resp.Header)
		}
		if r.decoder != nil {
			if err := r.decoder(resp.Body); err != nil {
				return status, tok, fmt.Errorf("httpcl: decode response: %w", err)
			}
		}
		return status, tok, nil
	}

	body, _ := io.ReadAll(resp.Body)
	return status, tok, &HTTPError{
		Method:     r.method,
		Route:      r.route,
		StatusCode: status,
		Body:       body,
	}
}
