package watchd

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// newServer returns an http.Server whose handler serves the daemon's HTTP API.
func newServer(d *daemon) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		// The body carries this daemon's build identity. A daemon predating the
		// identity answers with an empty body, which reads as a mismatch — which
		// is right, since that is exactly the daemon a client needs to replace.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, BuildID())
	})
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		var roots []string
		if err := json.NewDecoder(r.Body).Decode(&roots); err != nil && r.ContentLength != 0 {
			http.Error(w, fmt.Sprintf("decode roots: %v", err), http.StatusBadRequest)
			return
		}
		snap := d.snapshot(roots)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})

	// Registration of a command whose output the daemon should stream and buffer.
	// The submitting process may exit immediately after this call, so the daemon
	// takes ownership of the stream rather than borrowing the caller's.
	mux.HandleFunc("/command", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var reg CommandReg
		if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
			http.Error(w, fmt.Sprintf("decode command: %v", err), http.StatusBadRequest)
			return
		}
		if reg.CommandID == "" {
			http.Error(w, "command_id required", http.StatusBadRequest)
			return
		}
		if reg.SubmittedAt.IsZero() {
			reg.SubmittedAt = time.Now()
		}
		d.out.register(reg, streamFor(d.client))
		w.WriteHeader(http.StatusAccepted)
	})

	// Conflict state for one project root. Its own endpoint rather than a field
	// read off /snapshot: the caller is a hook, it wants one project, and
	// serving it the whole snapshot means every registered project's event
	// backlog crosses the socket to answer a yes-or-no question.
	mux.HandleFunc("/conflicts", func(w http.ResponseWriter, r *http.Request) {
		root := r.URL.Query().Get("root")
		if root == "" {
			http.Error(w, "root required", http.StatusBadRequest)
			return
		}
		report := d.conflictReport(root)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(report)
	})

	// Buffered output for one command, from a byte offset. The offset is into the
	// daemon's buffer, not the API's opaque SSE cursor, so a reader never has to
	// reason about reconnects.
	mux.HandleFunc("/output", func(w http.ResponseWriter, r *http.Request) {
		commandID := r.URL.Query().Get("command_id")
		if commandID == "" {
			http.Error(w, "command_id required", http.StatusBadRequest)
			return
		}
		var offset int64
		if raw := r.URL.Query().Get("offset"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 0 {
				http.Error(w, "offset must be a non-negative integer", http.StatusBadRequest)
				return
			}
			offset = parsed
		}
		chunk := d.out.read(commandID, offset)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chunk)
	})
	mux.HandleFunc("/validate", d.handleValidate)
	mux.HandleFunc("/validate/async", d.handleAsyncValidate)
	mux.HandleFunc("/validate/collect", d.handleCollect)
	mux.HandleFunc("/sidecar", d.handleSidecar)
	mux.HandleFunc("/sidecar/", d.handleSidecarByID)
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// withBearerAuth wraps h so that every request must carry a matching
// Authorization: Bearer <token> header. When token is empty the handler is
// returned unchanged (auth disabled).
func withBearerAuth(h http.Handler, token string) http.Handler {
	if token == "" {
		return h
	}
	want := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

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
// DialContext ignores the URL hostname (kept as "watchd" for consistency with
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

// handleSidecar serves POST /sidecar: create a new sidecar and return its ID.
func (d *daemon) handleSidecar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if d.prov == nil {
		http.Error(w, "daemon has no credentials; cannot provision sidecars", http.StatusServiceUnavailable)
		return
	}
	var req ProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.OrgID == "" {
		http.Error(w, "org_id required", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	id, err := d.prov.create(r.Context(), req.OrgID, req.Name, req.Image)
	if err != nil {
		http.Error(w, "provision sidecar: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ProvisionResponse{SidecarID: id})
}

// handleSidecarByID serves DELETE /sidecar/{id}: delete a daemon-provisioned sidecar.
func (d *daemon) handleSidecarByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if d.prov == nil {
		http.Error(w, "daemon has no credentials", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/sidecar/")
	if id == "" {
		http.Error(w, "sidecar id required", http.StatusBadRequest)
		return
	}
	if err := d.prov.delete(r.Context(), id); err != nil {
		http.Error(w, "delete sidecar: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
