package oauth

import (
	"context"
	"net"
	"net/http"
	"time"
)

type CallbackResult struct {
	Code  string
	State string
	Error string
}

func writeBrowserResponse(w http.ResponseWriter, status int, state browserState) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	renderPage(w, state)
}

func ListenForCallback(ctx context.Context) (port int, result <-chan CallbackResult, cleanup func(), err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, nil, err
	}
	port = listener.Addr().(*net.TCPAddr).Port

	ch := make(chan CallbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := CallbackResult{
			Code:  q.Get("code"),
			State: q.Get("state"),
			Error: q.Get("error"),
		}

		if res.Error != "" {
			writeBrowserResponse(w, http.StatusBadRequest, browserState{
				Title: "Authorization failed",
				Body:  "Login was denied. You can close this tab.",
			})
		} else {
			writeBrowserResponse(w, http.StatusOK, browserState{
				Title:   "Authorization successful",
				Body:    "You can close this window and return to your terminal.",
				Success: true,
			})
		}

		select {
		case ch <- res:
		default:
		}
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	go func() {
		_ = srv.Serve(listener)
	}()

	cleanup = func() {
		_ = srv.Shutdown(context.Background())
	}

	return port, ch, cleanup, nil
}
