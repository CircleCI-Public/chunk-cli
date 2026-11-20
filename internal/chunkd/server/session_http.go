package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// maxRequestBytes bounds a session request body. A request names a project and
// a few options; anything larger is not one.
const maxRequestBytes = 64 * 1024

// registerSessionRoutes adds the session API to mux. It sits behind the
// transport's normal auth: the Unix socket is user-local, and the TCP listener
// demands the bearer token.
//
//	POST /chat                                  send a message to a repository's chat, starting or resuming it
//	GET  /chat                                  a repository's chat's log (?root=<path>&after=N)
func registerSessionRoutes(mux *http.ServeMux, d *daemon) {
	mux.HandleFunc("POST /chat", d.handleChatSend)
	mux.HandleFunc("GET /chat", d.handleChatLog)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAPIError answers a refusal with the status it carries.
func writeAPIError(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		http.Error(w, ae.msg, ae.status)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// handleChatSend sends a message to a repository's chat and
// answers with its log once the session has it. A failure to reach the
// session is a problem in the log, not the status, so every window shows it.
func (d *daemon) handleChatSend(w http.ResponseWriter, r *http.Request) {
	var msg chunkd.ChatMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&msg); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(msg.Text) == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	root, err := chatRoot(msg.Root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The session runs in root, so root must be a repository on this machine,
	// spelled as its top level.
	top := gitutil.TopLevelCtx(r.Context(), root)
	if top == "" || config.CanonicalProjectRoot(top) != root {
		http.Error(w, fmt.Sprintf("%q is not the top of a git repository", msg.Root), http.StatusBadRequest)
		return
	}
	msg.Root = root
	c := d.chats.get(root)
	if c == nil {
		http.Error(w, "the chunk daemon is shutting down", http.StatusServiceUnavailable)
		return
	}
	log, err := c.send(msg)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, log)
}

// handleChatLog answers with a repository's chat's log from
// ?after=N, without making a chat for a repository that has none.
// A missing after reads only where the log ends.
func (d *daemon) handleChatLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	root, err := chatRoot(q.Get("root"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	after := -1
	if s := q.Get("after"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			http.Error(w, "after must be an integer", http.StatusBadRequest)
			return
		}
		after = n
	}
	c := d.chats.lookup(root)
	if c == nil {
		writeJSON(w, http.StatusOK, chunkd.SessionLog{Events: []chunkd.SessionLogEvent{}})
		return
	}
	writeJSON(w, http.StatusOK, c.run.view(after))
}
