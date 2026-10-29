package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// maxRequestBytes bounds a session request body. A request names a project and
// a few options; anything larger is not one.
const maxRequestBytes = 64 * 1024

// registerSessionRoutes adds the session API to mux. It sits behind the
// transport's normal auth: the Unix socket is user-local, and the TCP listener
// demands the bearer token.
//
//	POST /factory/plans        build an approved factory plan
//	POST /factory/{id}/cancel  stop a session
//	DELETE /factory/{id}       forget a finished session (409 while it is running)
func registerSessionRoutes(mux *http.ServeMux, d *daemon) {
	mux.HandleFunc("POST /factory/plans", d.handlePlanBuild)
	mux.HandleFunc("POST /factory/{id}/cancel", d.handleSessionCancel)
	mux.HandleFunc("DELETE /factory/{id}", d.handleSessionDelete)
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

func (d *daemon) handlePlanBuild(w http.ResponseWriter, r *http.Request) {
	var req chunkd.PlanBuildRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ProjectRoot == "" || req.PlanID == "" {
		http.Error(w, "project_root and plan_id required", http.StatusBadRequest)
		return
	}
	sess, err := d.startPlanBuild(req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, chunkd.SessionStartResponse{ID: sess.ID})
}

func (d *daemon) handleSessionCancel(w http.ResponseWriter, r *http.Request) {
	if !d.sessions.cancelSession(r.PathValue("id")) {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	// Cancelling is idempotent: a session that already ended is not an error, so
	// a viewer that raced the session's own end gets the same answer.
	w.WriteHeader(http.StatusAccepted)
}

func (d *daemon) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	found, removed := d.sessions.deleteSession(r.PathValue("id"))
	switch {
	case !found:
		http.Error(w, "no such session", http.StatusNotFound)
	case !removed:
		http.Error(w, "the session is still running: cancel it first", http.StatusConflict)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
