package chunkd

import (
	"context"
	"net"
	"net/http"
	"time"
)

// bearerTransport is an http.RoundTripper that adds an Authorization header
// to every request before delegating to the inner transport.
type bearerTransport struct {
	inner http.RoundTripper
	token string
}

func (bt *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+bt.token)
	return bt.inner.RoundTrip(req)
}

// unixClient returns an *http.Client that dials the given Unix socket path.
func unixClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
}

// longUnixClient is like unixClient but with no total-request timeout, suitable
// for long-running operations like /validate.
func longUnixClient(sockPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
}

// tcpClient returns an *http.Client that dials addr over TCP. The custom
// DialContext ignores the URL hostname (kept as "chunkd" for consistency with
// the unix variants) and always connects to addr.
func tcpClient(addr string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		},
	}
	var rt http.RoundTripper = transport
	if token := TCPToken(); token != "" {
		rt = &bearerTransport{inner: transport, token: token}
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: rt}
}

// longTCPClient is like tcpClient but with no total-request timeout, suitable
// for long-running operations like /validate. A 10 s dial timeout bounds how
// long an unreachable sandbox can stall the caller before ErrDaemonUnavailable
// triggers a fallback to inline execution.
//
// ResponseHeaderTimeout is intentionally omitted: handleValidate buffers all
// output before writing the response, so the header arrives only after the run
// completes, and capping it would silently abort long validate runs.
func longTCPClient(addr string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
		},
	}
	var rt http.RoundTripper = transport
	if token := TCPToken(); token != "" {
		rt = &bearerTransport{inner: transport, token: token}
	}
	return &http.Client{Transport: rt}
}
