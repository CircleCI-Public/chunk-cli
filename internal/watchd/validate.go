package watchd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/CircleCI-Public/chunk-cli/internal/envctx"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
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
	// validated, and it is also what a task is filed and fingerprinted under,
	// what a change is measured against, and what a verdict is remembered by —
	// so anything the daemon decides on a project's behalf needs it.
	//
	// It cannot be left to the daemon to infer. The daemon's own cwd is wherever
	// it was launched from, which is one arbitrary repo out of all the repos it
	// serves, so a run that resolved the project itself would validate that one
	// and report the answer under whichever project asked. Empty is accepted on
	// the synchronous path only, for a client too old to send it; such a run is
	// simply run, with nothing judged or recorded.
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

	// No risk summary: this is the explicit --async path, where the caller has
	// already decided and nothing was judged. Nothing is recorded in history
	// either, which keeps that record to the runs the daemon actually judged.
	taskID, err := d.startValidateTask(req, nil)
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

	// A project that has asked for it gets its checks run in a checked-out copy
	// of that state, so editing while they run cannot make the answer describe
	// code that has moved. Materialising can fail — no snapshot, no worktree
	// support, no disk — and then the run happens in the live tree as usual,
	// which is slower to be sure of but never wrong about what it ran.
	shadow, cleanup := "", func() {}
	if policyFor(req.ProjectRoot).worktree && before.tree != "" {
		if path, remove, err := gitutil.MaterializeTree(req.ProjectRoot, before.tree); err == nil {
			shadow, cleanup = path, remove
			// The commands run in the copy; the results still belong to the real
			// project, whose data directory and event log the developer is watching.
			// Where they run is the runner's projectRoot argument below, not a
			// --project here: the runner appends its own --project last, and cobra
			// takes the final value, so a --project in args would be overridden and
			// the run would happen in the live tree while claiming otherwise.
			args = append(append([]string(nil), args...), "--attribute-to", req.ProjectRoot)
		}
	}

	// Whatever was already running for this project is validating a tree that
	// has since moved, which is why this run exists at all.
	if stopped := d.tasks.supersede(req.ProjectRoot); stopped > 0 {
		log.Printf("watchd: superseded %d in-flight validate run(s) for %s", stopped, req.ProjectRoot)
	}

	taskID, err := d.tasks.start(req.ProjectRoot, shadow != "", func(ctx context.Context) (int, string) {
		defer cleanup()
		// Serialised against every other validate run, async or not: two runs of
		// the same commands in one tree would race over whatever they build.
		d.validateMu.Lock()
		defer d.validateMu.Unlock()

		if sessionID != "" {
			ctx = session.WithID(ctx, sessionID)
		}
		ctx = envctx.WithEnv(ctx, env)

		var stdout, stderr bytes.Buffer
		// Where the commands run: the snapshot copy when there is one, and the
		// request's project root otherwise. Never the daemon's cwd — the task is
		// filed and fingerprinted against this project, so validating any other one
		// would report an answer about the wrong repo, and the staleness check,
		// comparing this project against itself, would confirm it.
		runRoot := req.ProjectRoot
		if shadow != "" {
			runRoot = shadow
		}
		exitCode := d.runner(ctx, runRoot, args, env, &stdout, &stderr)
		if ctx.Err() != nil {
			// Superseded, or the daemon is shutting down. The run concluded
			// nothing, so nothing is recorded: a cancelled run is not a failed one,
			// and filing it as either a failure or a known-good state would be a
			// verdict on work that never finished.
			return exitCode, stdout.String() + stderr.String()
		}
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
		// sees what the developer would have seen, up to the tail the store
		// retains (see maxTaskOutput).
		return exitCode, stdout.String() + stderr.String()
	})
	if err != nil {
		// The run never started, so nothing will ever reach the deferred cleanup
		// above. A shadow left behind would be a temp directory and a worktree
		// entry per refused run.
		cleanup()
		return "", err
	}
	return taskID, nil
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

// handleValidate runs a validate request, either here while the caller waits or
// in the background if the caller offered to be released and the change is one
// worth releasing them for.
func (d *daemon) handleValidate(w http.ResponseWriter, r *http.Request) {
	// Bound body size before decoding; an attacker-controlled Content-Length
	// could otherwise grow the heap by hundreds of MiB.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
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
	exitCode := d.runner(ctx, req.ProjectRoot, req.Args, req.Env, &stdout, &stderr)

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
