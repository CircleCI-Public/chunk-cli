package chunkd

import (
	"context"
	"net"
	"net/http"
	"sync"
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

// sharedIdleTimeout is how long a pooled connection to the daemon stays open
// unused.
const sharedIdleTimeout = 90 * time.Second

var (
	transportsMu sync.Mutex
	// transports are the connection pools to each daemon address, keyed by
	// network and address, so every client of one daemon reuses connections.
	transports = map[string]*http.Transport{}
)

// sharedTransport is the one transport this process uses to reach addr over
// network ("unix" or "tcp").
func sharedTransport(network, addr string) *http.Transport {
	key := network + ":" + addr
	transportsMu.Lock()
	defer transportsMu.Unlock()
	if t, ok := transports[key]; ok {
		return t
	}
	d := &net.Dialer{}
	if network == "tcp" {
		// The dial timeout bounds how long an unreachable sandbox can stall a
		// long client, which has no total timeout, before
		// ErrDaemonUnavailable triggers the inline fallback.
		d.Timeout = 10 * time.Second
	}
	// No ResponseHeaderTimeout: a long request such as /validate may send its
	// headers only once it finishes, so capping it would abort long runs.
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, addr)
		},
		IdleConnTimeout:     sharedIdleTimeout,
		MaxIdleConnsPerHost: 4,
	}
	transports[key] = t
	return t
}

// closeIdle drops the pooled idle connections to addr over network, if any.
func closeIdle(network, addr string) {
	transportsMu.Lock()
	t := transports[network+":"+addr]
	transportsMu.Unlock()
	if t != nil {
		t.CloseIdleConnections()
	}
}

// unixClient returns an *http.Client that dials the given Unix socket path.
func unixClient(sockPath string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: sharedTransport("unix", sockPath)}
}

// longUnixClient is like unixClient but with no total-request timeout, suitable
// for long-running operations like /validate.
func longUnixClient(sockPath string) *http.Client {
	return &http.Client{Transport: sharedTransport("unix", sockPath)}
}

// tcpClient returns an *http.Client that dials addr over TCP. The custom
// DialContext ignores the URL hostname (kept as "chunkd" for consistency with
// the unix variants) and always connects to addr.
func tcpClient(addr string) *http.Client {
	var rt http.RoundTripper = sharedTransport("tcp", addr)
	if token := TCPToken(); token != "" {
		rt = &bearerTransport{inner: rt, token: token}
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: rt}
}

// longTCPClient is like tcpClient but with no total-request timeout, suitable
// for long-running operations like /validate.
func longTCPClient(addr string) *http.Client {
	var rt http.RoundTripper = sharedTransport("tcp", addr)
	if token := TCPToken(); token != "" {
		rt = &bearerTransport{inner: rt, token: token}
	}
	return &http.Client{Transport: rt}
}
