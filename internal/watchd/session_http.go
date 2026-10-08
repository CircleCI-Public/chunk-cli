package watchd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// maxRequestBytes bounds a session request body. A request names a project and
// a few options; anything larger is not one.
const maxRequestBytes = 64 * 1024

// registerSessionRoutes adds the session API to mux. It sits behind the
// transport's normal auth: the Unix socket is user-local, and the TCP listener
// demands the bearer token.
//
//	POST /factory              start a factory session
//	GET  /factory[?root=<path>] list sessions (newest first), optionally one project's
//	GET  /factory/{id}         one session with its review findings
//	POST /factory/{id}/cancel  stop a session
func registerSessionRoutes(mux *http.ServeMux, d *daemon) {
	mux.HandleFunc("POST /factory", d.handleFactoryStart)
	mux.HandleFunc("GET /factory", d.handleSessionList)
	mux.HandleFunc("GET /factory/{id}", d.handleSessionGet)
	mux.HandleFunc("POST /factory/{id}/cancel", d.handleSessionCancel)
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

func (d *daemon) handleFactoryStart(w http.ResponseWriter, r *http.Request) {
	var req FactoryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ProjectRoot == "" {
		http.Error(w, "project_root required", http.StatusBadRequest)
		return
	}
	sess, err := d.startFactory(req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, SessionStartResponse{ID: sess.ID})
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
