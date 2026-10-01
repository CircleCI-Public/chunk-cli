package watchd

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// registerSessionRoutes adds the session API to mux. It sits behind the
// transport's normal auth: the Unix socket is user-local, and the TCP listener
// demands the bearer token.
//
//	GET  /session[?root=<path>] list sessions (newest first), optionally one project's
//	GET  /session/{id}         one session with its review text
//	POST /session/{id}/cancel  stop a session
func registerSessionRoutes(mux *http.ServeMux, d *daemon) {
	mux.HandleFunc("GET /session", d.handleSessionList)
	mux.HandleFunc("GET /session/{id}", d.handleSessionGet)
	mux.HandleFunc("POST /session/{id}/cancel", d.handleSessionCancel)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (d *daemon) handleSessionList(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("root")
	if root == "" {
		var all []Session
		for _, p := range d.snapshot(nil).Projects {
			all = append(all, p.Sessions...)
		}
		writeJSON(w, http.StatusOK, SessionList{Sessions: all})
		return
	}
	ps := d.lookupProject(root)
	if ps == nil {
		http.Error(w, fmt.Sprintf("the watch daemon is not tracking %q", root), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, SessionList{Sessions: d.sessions.forProject(ps.root)})
}

func (d *daemon) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	entry := d.sessions.get(r.PathValue("id"))
	if entry == nil {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, entry.detail())
}

func (d *daemon) handleSessionCancel(w http.ResponseWriter, r *http.Request) {
	found, _ := d.sessions.cancelSession(r.PathValue("id"))
	if !found {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	// Cancelling is idempotent: a session that already ended is not an error, so
	// a viewer that raced the session's own end gets the same answer.
	w.WriteHeader(http.StatusAccepted)
}
