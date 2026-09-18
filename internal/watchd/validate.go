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

// ValidateRunner runs a validate command in-process. projectRoot is the repo the
// run applies to; args is os.Args[1:] from the caller (e.g. ["validate", "test",
// "--remote"]); env is the caller's os.Environ(), which may differ from the
// daemon's own environment. stdout and stderr capture the command output.
// Returns the exit code.
//
// projectRoot is a parameter rather than something the runner works out for
// itself because the daemon has no working directory worth trusting. One daemon
// serves every repo on the machine — the socket is user-global — and it was
// launched with whatever cwd the developer happened to be in at the time. An
// implementation that resolves the project from its own cwd validates that repo
// no matter which one the request was about.
type ValidateRunner func(ctx context.Context, projectRoot string, args []string, env []string, stdout, stderr io.Writer) int

// ValidateRequest is the payload sent to POST /validate and POST
// /validate/async.
type ValidateRequest struct {
	// Args is os.Args[1:] from the caller, e.g. ["validate", "test", "--remote"].
	Args []string `json:"args"`
	// CircleCIToken is forwarded to the subprocess as CIRCLE_TOKEN.
	CircleCIToken string `json:"circleci_token,omitempty"`
	// Env is the caller's os.Environ(), forwarded verbatim to the subprocess so
	// session-identity variables (e.g. CLAUDE_CODE_SESSION_ID) reach it intact.
	Env []string `json:"env,omitempty"`
	// ProjectRoot is the repo the run applies to, already resolved by the client
	// (so it reflects the caller's --project, or its cwd). It decides what gets
	// validated, and for an async run it is also what the task is filed and
	// fingerprinted under.
	//
	// It cannot be left to the daemon to infer. The daemon's own cwd is wherever
	// it was launched from, which is one arbitrary repo out of all the repos it
	// serves, so a run that resolved the project itself would validate that one
	// and report the answer under whichever project asked. Empty is accepted on
	// the synchronous path only, for a client too old to send it.
	ProjectRoot string `json:"project_root,omitempty"`
}

// ValidateResponse is the response from POST /validate.
type ValidateResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
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
	var req ValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Required here, unlike on the synchronous path: it decides both what gets
	// validated and where the answer is filed, and a result that cannot be
	// attributed to a project can never be collected.
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
	root := req.ProjectRoot

	taskID, err := d.tasks.start(root, func(ctx context.Context) (int, string) {
		// Serialised against every other validate run, async or not: two runs of
		// the same commands in one tree would race over whatever they build.
		d.validateMu.Lock()
		defer d.validateMu.Unlock()

		if sessionID != "" {
			ctx = session.WithID(ctx, sessionID)
		}
		ctx = envctx.WithEnv(ctx, env)

		var stdout, stderr bytes.Buffer
		// root, not the daemon's cwd: the task is filed and fingerprinted against
		// this project, so validating any other one would report an answer about
		// the wrong repo — and the staleness check, comparing this project against
		// itself, would confirm it.
		exitCode := d.runner(ctx, root, args, env, &stdout, &stderr)
		// stderr carries the progress lines and the tally; stdout is usually
		// empty for a validate run. Both are kept so whoever collects the result
		// sees what the developer would have seen, up to the tail the store
		// retains (see maxTaskOutput).
		return exitCode, stdout.String() + stderr.String()
	})
	if err != nil {
		// Either the tree could not be fingerprinted, so staleness would be
		// undetectable, or the project already has its cap of runs in flight.
		// Reported as a conflict rather than a server error: nothing is broken,
		// this run just cannot be taken asynchronously, and the caller is
		// expected to run inline instead. The reason travels as the body so the
		// caller can say which it was.
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(AsyncValidateResponse{TaskID: taskID})
}

// handleCollect returns the finished results for a project and records them
// delivered once the response is on its way.
//
// Read, encode, write, then acknowledge — in that order, and not as one step.
// Encoding into the ResponseWriter while the store is being mutated would mark
// results delivered before any byte left the process, so a marshal error or a
// client that has already hung up would take the result with it. Nothing would
// show it had happened either: the client turns a transport failure into
// ErrDaemonUnavailable, and the results hook reads that as "no daemon running,
// nothing to report" and prints nothing at all. A failed validation would
// disappear into what looks like an ordinary quiet turn.
//
// Acknowledging after the write leaves one window this cannot close: a client
// that dies between the daemon's successful write and printing what it read.
// Shutting that too needs the client to acknowledge in a second call, which is a
// round trip on every prompt for a case an unacknowledged result already
// survives — it is simply reported again next turn.
func (d *daemon) handleCollect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	root := r.URL.Query().Get("root")
	if root == "" {
		http.Error(w, "root required", http.StatusBadRequest)
		return
	}

	tasks := d.tasks.peek(root)
	body, err := json.Marshal(CollectResponse{Tasks: tasks})
	if err != nil {
		// Nothing acknowledged, so the results are still owed to somebody and
		// come back on the next request.
		http.Error(w, "encode collect response: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		// The caller is gone. Left unacknowledged on purpose.
		return
	}
	d.tasks.acknowledge(tasks)
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
	exitCode := d.runner(ctx, req.ProjectRoot, req.Args, req.Env, &stdout, &stderr)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ValidateResponse{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	})
}
