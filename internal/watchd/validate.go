package watchd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/CircleCI-Public/chunk-cli/internal/envctx"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
)

// ValidateRunner runs a validate command in-process. args is os.Args[1:] from
// the caller (e.g. ["validate", "test", "--remote"]); env is the caller's
// os.Environ(), which may differ from the daemon's own environment.
// stdout and stderr capture the command output. Returns the exit code.
type ValidateRunner func(ctx context.Context, args []string, env []string, stdout, stderr io.Writer) int

// ValidateRequest is the payload sent to POST /validate.
type ValidateRequest struct {
	// Args is os.Args[1:] from the caller, e.g. ["validate", "test", "--remote"].
	Args []string `json:"args"`
	// CircleCIToken is forwarded to the subprocess as CIRCLE_TOKEN.
	CircleCIToken string `json:"circleci_token,omitempty"`
	// Env is the caller's os.Environ(), forwarded verbatim to the subprocess so
	// session-identity variables (e.g. CLAUDE_CODE_SESSION_ID) reach it intact.
	Env []string `json:"env,omitempty"`
}

// ValidateResponse is the response from POST /validate.
type ValidateResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// AsyncValidateRequest starts a validate run the caller will not wait for.
type AsyncValidateRequest struct {
	ValidateRequest
	// ProjectRoot is the repo the run validates. The daemon files the task under
	// it and fingerprints it, so it is required — an async result that cannot be
	// attributed to a project can never be collected.
	ProjectRoot string `json:"project_root"`
}

// AsyncValidateResponse acknowledges an accepted async run.
type AsyncValidateResponse struct {
	TaskID string `json:"task_id"`
}

// CollectResponse carries the finished results for a project.
type CollectResponse struct {
	Tasks []TaskState `json:"tasks"`
}

// handleAsyncValidate starts a validate run in the background and returns its
// task ID immediately.
//
// Unlike handleValidate this does not hold validateMu across the run — that is
// the point, since the caller is released rather than waiting. The run itself
// still takes the lock, so two async runs queue behind each other exactly as
// two synchronous ones do; what changes is who waits, not how many run at once.
func (d *daemon) handleAsyncValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req AsyncValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ProjectRoot == "" {
		http.Error(w, "project_root required", http.StatusBadRequest)
		return
	}
	if d.runner == nil {
		http.Error(w, "no validate runner configured", http.StatusServiceUnavailable)
		return
	}

	// Detached from the request: the caller is about to disconnect, and an async
	// run that died with the connection that started it would be pointless.
	// The store's parent context bounds it instead, so it ends with the daemon.
	env := req.Env
	if req.CircleCIToken != "" {
		env = append(append([]string(nil), env...), "CIRCLE_TOKEN="+req.CircleCIToken)
	}
	sessionID := session.IDFromSlice(req.Env)
	args := req.Args

	taskID, err := d.tasks.start(req.ProjectRoot, func(ctx context.Context) (int, string) {
		// Serialised against every other validate run, async or not: two runs of
		// the same commands in one tree would race over whatever they build.
		d.validateMu.Lock()
		defer d.validateMu.Unlock()

		if sessionID != "" {
			ctx = session.WithID(ctx, sessionID)
		}
		ctx = envctx.WithEnv(ctx, env)

		var stdout, stderr bytes.Buffer
		exitCode := d.runner(ctx, args, env, &stdout, &stderr)
		// stderr carries the progress lines and the tally; stdout is usually
		// empty for a validate run. Both are kept so whoever collects the result
		// sees what the developer would have seen.
		return exitCode, stdout.String() + stderr.String()
	})
	if err != nil {
		// The tree could not be fingerprinted, so staleness would be undetectable.
		// Reported as a conflict rather than a server error: nothing is broken,
		// this tree just cannot be validated asynchronously, and the caller is
		// expected to run inline instead.
		http.Error(w, "cannot validate asynchronously: "+err.Error(), http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(AsyncValidateResponse{TaskID: taskID})
}

// handleCollect returns the finished results for a project and forgets them.
func (d *daemon) handleCollect(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("root")
	if root == "" {
		http.Error(w, "root required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(CollectResponse{Tasks: d.tasks.collect(root)})
}

func (d *daemon) handleValidate(w http.ResponseWriter, r *http.Request) {
	d.validateMu.Lock()
	defer d.validateMu.Unlock()

	var req ValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Use WithoutCancel so the validate run completes even if the HTTP client
	// disconnects mid-run. The result is buffered; partial runs under
	// validateMu are worse than completing after the client is gone.
	ctx := context.WithoutCancel(r.Context())
	if id := session.IDFromSlice(req.Env); id != "" {
		ctx = session.WithID(ctx, id)
	}
	if req.CircleCIToken != "" {
		req.Env = append(append([]string(nil), req.Env...), "CIRCLE_TOKEN="+req.CircleCIToken)
	}
	ctx = envctx.WithEnv(ctx, req.Env)

	if d.runner == nil {
		http.Error(w, "no validate runner configured", http.StatusServiceUnavailable)
		return
	}

	var stdout, stderr bytes.Buffer
	exitCode := d.runner(ctx, req.Args, req.Env, &stdout, &stderr)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ValidateResponse{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	})
}
