package watchd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// newServer returns an http.Server whose handler serves the daemon's HTTP API.
func newServer(d *daemon) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
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
