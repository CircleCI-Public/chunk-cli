package server

import (
	"context"
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
//	POST /factory/plans                         start a build of a factory plan no session holds
//	GET  /factory/plans/{plan}/build            a plan's build session and its feed (?after=N)
//	POST /factory/plans/{plan}/approve          approve a plan, stop its planner and build it
//	POST /factory/plans/{plan}/planner/request  start a new plan's planner on its request
//	POST /factory/plans/{plan}/planner/resume   have a plan's planner claim it again
//	GET  /factory/plans/{plan}/planner          read a plan's planner's log (?after=N)
//	POST /factory/{id}/cancel                   stop a session
//	DELETE /factory/{id}                        forget a finished session (409 while it is running)
//	POST /chat                                  send a message to a repository's chat, starting or resuming it
//	GET  /chat                                  a repository's chat's log (?root=<path>&after=N)
//	POST /factory/setup                         start a project's Setup run, or retry a stuck or failed one
//	GET  /factory/setup                         a project's Setup run and its feed (?root=<path>&after=N)
//	POST /factory/setup/continue                continue a stuck Setup run without an image
func registerSessionRoutes(mux *http.ServeMux, d *daemon) {
	mux.HandleFunc("POST /factory/plans", d.handlePlanBuild)
	mux.HandleFunc("GET /factory/plans/{plan}/build", d.handlePlanBuildActivity)
	mux.HandleFunc("POST /factory/plans/{plan}/approve", d.handlePlanApprove)
	mux.HandleFunc("POST /factory/plans/{plan}/planner/request", d.handlePlannerRequest)
	mux.HandleFunc("POST /factory/plans/{plan}/planner/resume", d.handlePlannerResume)
	mux.HandleFunc("GET /factory/plans/{plan}/planner", d.handlePlannerLog)
	mux.HandleFunc("POST /factory/{id}/cancel", d.handleSessionCancel)
	mux.HandleFunc("DELETE /factory/{id}", d.handleSessionDelete)
	mux.HandleFunc("POST /chat", d.handleChatSend)
	mux.HandleFunc("GET /chat", d.handleChatLog)
	mux.HandleFunc("POST /factory/setup", d.handleSetupStart)
	mux.HandleFunc("GET /factory/setup", d.handleSetupActivity)
	mux.HandleFunc("POST /factory/setup/continue", d.handleSetupContinue)
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
	sess, err := d.resumePlanBuild(req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, chunkd.SessionStartResponse{ID: sess.ID})
}

// handlePlanBuildActivity answers a plan's build session and its feed lines
// past ?after=N. A missing or unreadable after is 0.
func (d *daemon) handlePlanBuildActivity(w http.ResponseWriter, r *http.Request) {
	planID := r.PathValue("plan")
	entry := d.latestPlanBuild(planID)
	if entry == nil {
		http.Error(w, "no build of plan "+planID, http.StatusNotFound)
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	writeJSON(w, http.StatusOK, planBuildActivity(entry.snapshot(), after))
}

func (d *daemon) handlePlanApprove(w http.ResponseWriter, r *http.Request) {
	var req chunkd.PlanApproveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ProjectRoot == "" {
		http.Error(w, "project_root required", http.StatusBadRequest)
		return
	}
	// Detached from the request: once accepted, a client that goes away does
	// not leave the plan approved and unbuilt.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), planApproveTimeout)
	defer cancel()
	sess, err := d.approvePlan(ctx, r.PathValue("plan"), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, chunkd.SessionStartResponse{ID: sess.ID})
}

func (d *daemon) handlePlannerRequest(w http.ResponseWriter, r *http.Request) {
	var req chunkd.PlannerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ProjectRoot == "" || req.Text == "" {
		http.Error(w, "project_root and text required", http.StatusBadRequest)
		return
	}
	log, err := d.requestPlanner(r.PathValue("plan"), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, log)
}

func (d *daemon) handlePlannerResume(w http.ResponseWriter, r *http.Request) {
	var req chunkd.PlannerResume
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ProjectRoot == "" {
		http.Error(w, "project_root required", http.StatusBadRequest)
		return
	}
	log, err := d.resumePlanner(r.PathValue("plan"), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, log)
}

func (d *daemon) handlePlannerLog(w http.ResponseWriter, r *http.Request) {
	after := -1
	if s := r.URL.Query().Get("after"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			http.Error(w, "after must be an integer", http.StatusBadRequest)
			return
		}
		after = n
	}
	writeJSON(w, http.StatusOK, d.plannerLog(r.PathValue("plan"), after))
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

// handleSetupStart starts a project's Setup run, or answers with the one
// already under way; a stuck or failed run starts again.
func (d *daemon) handleSetupStart(w http.ResponseWriter, r *http.Request) {
	var req chunkd.SetupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	root, err := chatRoot(req.ProjectRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Setup writes root's config and syncs root to a sidecar, so root must be
	// a repository on this machine, spelled as its top level.
	top := gitutil.TopLevelCtx(r.Context(), root)
	if top == "" || config.CanonicalProjectRoot(top) != root {
		http.Error(w, fmt.Sprintf("%q is not the top of a git repository", req.ProjectRoot), http.StatusBadRequest)
		return
	}
	if req.OrgID == "" {
		http.Error(w, "org_id required", http.StatusBadRequest)
		return
	}
	if d.client == nil {
		msg := d.factoryAuthError()
		if msg == "" {
			msg = "not authenticated to CircleCI (run: chunk auth login)"
		}
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}
	run, err := d.setups.start(root, req.OrgID, d.client)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run.view(0))
}

// handleSetupActivity answers a project's Setup run and its feed lines past
// ?after=N, parsed as handleChatLog parses it.
func (d *daemon) handleSetupActivity(w http.ResponseWriter, r *http.Request) {
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
	run := d.setups.get(root)
	if run == nil {
		http.Error(w, "no setup for this project", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, run.view(after))
}

// handleSetupContinue records Continue without an image on a project's stuck
// Setup run; any other run is a 409.
func (d *daemon) handleSetupContinue(w http.ResponseWriter, r *http.Request) {
	var req chunkd.SetupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	root, err := chatRoot(req.ProjectRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.setups.cont(root); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d.setups.get(root).view(0))
}
