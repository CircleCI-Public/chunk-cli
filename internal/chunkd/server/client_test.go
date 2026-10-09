package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// The tests talk to a daemon the way a client would, but over clients of their
// own: chunkd's are unexported, and these need to aim at a test daemon's socket
// or address directly rather than at whatever the environment configures.

// unixClient talks to the daemon listening on sockPath.
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

// tcpClient talks to the daemon listening on addr, with the environment's
// token when one is set.
func tcpClient(addr string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		},
	}
	var rt http.RoundTripper = transport
	if token := chunkd.TCPToken(); token != "" {
		rt = &bearerTransport{inner: transport, token: token}
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: rt}
}

// bearerTransport adds a bearer token to every request.
type bearerTransport struct {
	inner http.RoundTripper
	token string
}

func (bt *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+bt.token)
	return bt.inner.RoundTrip(req)
}

// ping reports whether the daemon on sockPath answers, and its build.
func ping(sockPath string) (bool, string) {
	return doPing(unixClient(sockPath))
}

// doPing reports whether the daemon behind client answers /ping, and its build.
func doPing(client *http.Client) (bool, string) {
	resp, err := client.Get("http://chunkd/ping")
	if err != nil {
		return false, ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return true, ""
	}
	return true, strings.TrimSpace(string(body))
}
