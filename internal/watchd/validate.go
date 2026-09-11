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
	// ProjectRoot is the repo being validated. It is what a result is filed
	// under, measured against, and remembered by, so anything the daemon decides
	// on a project's behalf needs it — a run that arrives without one is simply
	// run.
	ProjectRoot string `json:"project_root,omitempty"`
	// AllowAsync says the caller will accept being released before the answer
	// exists. It is an offer, not an instruction: the daemon weighs the change
	// and may hold the caller anyway.
	//
	// Only a caller that has somewhere to hear the answer later should set it.
	// A hook does — the next turn collects background results — while a developer
	// watching a terminal does not, and releasing them would leave the run's
	// output going nowhere they are looking.
	AllowAsync bool `json:"allow_async,omitempty"`
}

// ValidateResponse is the response from POST /validate.
type ValidateResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	// TaskID is set when the daemon took the run into the background rather than
	// running it here. Nothing has run yet: ExitCode is zero because there is no
	// exit code, and the result is collected on a later turn.
	TaskID string `json:"task_id,omitempty"`
	// Risk is what the daemon made of the change: a score, the facts behind it,
	// and any advice. Nil when the caller never offered to be released, since
	// then no change was judged.
	Risk *RiskSummary `json:"risk,omitempty"`
	// Reason says why the run was released or held, in words a caller can print
	// as one line. Empty when the caller never offered to be released, since
	// then there was no decision to explain.
	Reason string `json:"reason,omitempty"`
}

// AsyncValidateRequest starts a validate run the caller will not wait for.
// ProjectRoot is required: an async result that cannot be attributed to a
// project can never be collected.
type AsyncValidateRequest struct {
	ValidateRequest
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
// task ID immediately. This is the explicit path — `chunk validate --async` —
// so no risk assessment is made: the caller has already decided.
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

	// No risk summary: this is the explicit --async path, where the caller has
	// already decided and nothing was judged. Nothing is recorded in history
	// either, which keeps that record to the runs the daemon actually judged.
	taskID, err := d.startValidateTask(req.ValidateRequest, nil)
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

// startValidateTask launches req in the background and returns its task ID.
//
// Unlike a synchronous run this does not hold validateMu while it starts — that
// is the point, since the caller is released rather than waiting. The run itself
// still takes the lock, so two background runs queue behind each other exactly
// as two synchronous ones do; what changes is who waits, not how many run at
// once.
func (d *daemon) startValidateTask(req ValidateRequest, risk *RiskSummary) (string, error) {
	// Detached from the request: the caller is about to disconnect, and an async
	// run that died with the connection that started it would be pointless. The
	// store's parent context bounds it instead, so it ends with the daemon.
	env := req.Env
	if req.CircleCIToken != "" {
		env = append(append([]string(nil), env...), "CIRCLE_TOKEN="+req.CircleCIToken)
	}
	sessionID := session.IDFromSlice(req.Env)
	args := req.Args
	// Taken before the run, because this is the state the run is about to
	// validate. A tree that cannot be captured at all just means the next change
	// is measured against HEAD instead.
	before := d.snapshotState(req.ProjectRoot)

	return d.tasks.start(req.ProjectRoot, func(ctx context.Context) (int, string) {
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
		if exitCode == 0 {
			// Recorded even if the tree has moved on since. Staleness decides
			// whether this *result* can be reported, which is a different
			// question from which state is known to be good.
			d.risk.recordGreen(req.ProjectRoot, before)
		}
		if risk != nil {
			d.hist.record(req.ProjectRoot, *risk, exitCode == 0)
		}
		// stderr carries the progress lines and the tally; stdout is usually
		// empty for a validate run. Both are kept so whoever collects the result
		// sees what the developer would have seen.
		return exitCode, stdout.String() + stderr.String()
	})
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

// handleValidate runs a validate request, either here while the caller waits or
// in the background if the caller offered to be released and the change is one
// worth releasing them for.
func (d *daemon) handleValidate(w http.ResponseWriter, r *http.Request) {
	var req ValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if d.runner == nil {
		http.Error(w, "no validate runner configured", http.StatusServiceUnavailable)
		return
	}

	// Decided before validateMu is taken, and deliberately so: a caller that has
	// offered to be released must not first queue behind whatever run is already
	// in flight, since that wait is the whole thing being avoided.
	reason := ""
	var risk *RiskSummary
	if req.AllowAsync && req.ProjectRoot != "" {
		decision := d.assessRisk(req.ProjectRoot)
		reason = decision.reason
		summary := decision.risk
		risk = &summary
		if decision.async {
			if taskID, err := d.startValidateTask(req, risk); err == nil {
				writeValidateJSON(w, ValidateResponse{TaskID: taskID, Reason: reason, Risk: risk})
				return
			}
			// The tree cannot be fingerprinted, so a background result could not be
			// told apart from one that went out of date while it ran. Held here
			// instead, where the answer reaches the caller while it is still true.
			reason = "change cannot be fingerprinted, so this run blocks"
		}
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

	resp := d.runValidateNow(ctx, req, risk)
	resp.Reason = reason
	resp.Risk = risk
	writeValidateJSON(w, resp)
}

// runValidateNow runs req to completion while the caller waits.
func (d *daemon) runValidateNow(ctx context.Context, req ValidateRequest, risk *RiskSummary) ValidateResponse {
	before := d.snapshotState(req.ProjectRoot)

	d.validateMu.Lock()
	defer d.validateMu.Unlock()

	var stdout, stderr bytes.Buffer
	exitCode := d.runner(ctx, req.Args, req.Env, &stdout, &stderr)

	// Recorded from synchronous runs too, not just background ones. This is what
	// clears a failure debt: a project that owes a blocking run gets one, and if
	// it passes there is no longer anything to block for. Only the background
	// path could set the debt, so only recording there would leave it set for the
	// life of the daemon.
	//
	// The same goes for the baseline: a blocking run that passes is as good a
	// known-good state as a background one, and the change after it should be
	// measured from here rather than from whenever a run last happened to be
	// released.
	if req.ProjectRoot != "" {
		d.risk.record(req.ProjectRoot, exitCode == 0)
		if exitCode == 0 {
			d.risk.recordGreen(req.ProjectRoot, before)
		}
		if risk != nil {
			d.hist.record(req.ProjectRoot, *risk, exitCode == 0)
		}
	}

	return ValidateResponse{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}
}

func writeValidateJSON(w http.ResponseWriter, resp ValidateResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
