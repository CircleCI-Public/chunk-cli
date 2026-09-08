package watchd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
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
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
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
