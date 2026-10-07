package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/envctx"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// ValidateRunner runs a validate command in-process. projectRoot is the repo
// the daemon uses for task tracking and state keying; workDir is the caller's
// working directory, passed to the subprocess as --project so it can load
// .chunk/config.json from the right location (workDir equals projectRoot unless
// the .chunk directory sits below the git root, in which case workDir is the
// subdirectory and projectRoot is the git top-level). When workDir is empty the
// runner falls back to projectRoot. args is os.Args[1:] from the caller;
// env is the caller's os.Environ(). stdout and stderr capture command output.
// Returns the exit code.
type ValidateRunner func(ctx context.Context, projectRoot, workDir string, args []string, env []string, stdout, stderr io.Writer) int

// validateRunArgs is the command line the run is given: the caller's args, plus
// whatever the request carries as fields that the run takes as flags.
func validateRunArgs(req watchd.ValidateRequest) []string {
	if !req.HookCodex {
		return req.Args
	}
	return append(append([]string(nil), req.Args...), "--hook-codex")
}

// handleAsyncValidate starts a validate run in the background and returns its
// task ID immediately. This is the explicit path — `chunk validate --async` —
// so no risk assessment is made: the caller has already decided.
func (d *daemon) handleAsyncValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req watchd.ValidateRequest
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

	// Claim registration for cross-agent advisory. Nil paths: no assessRisk on
	// the explicit --async path, so file-level granularity is unavailable here.
	sessionID := session.IDFromSlice(req.Env)
	var claimWarning string
	if sessionID != "" {
		overlaps := d.claims.overlapping(sessionID, req.ProjectRoot, nil)
		d.claims.register(sessionID, req.ProjectRoot, nil)
		claimWarning = watchd.ClaimNotice(overlaps)
	}

	// No risk summary: this is the explicit --async path, where the caller has
	// already decided and nothing was judged. Nothing is recorded in history
	// either, which keeps that record to the runs the daemon actually judged.
	onDone := func() { d.claims.release(sessionID, req.ProjectRoot) }
	taskID, err := d.startValidateTask(req, nil, onDone)
	if err != nil {
		// Either the tree could not be fingerprinted, so staleness would be
		// undetectable, or the project already has its cap of runs in flight.
		// Reported as a conflict rather than a server error: nothing is broken,
		// this run just cannot be taken asynchronously, and the caller is
		// expected to run inline instead. The reason travels as the body so the
		// caller can say which it was.
		d.claims.release(sessionID, req.ProjectRoot)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(watchd.AsyncValidateResponse{TaskID: taskID, ClaimWarning: claimWarning})
}

// startValidateTask launches req in the background and returns its task ID.
//
// Unlike a synchronous run this does not hold validateMu while it starts — that
// is the point, since the caller is released rather than waiting. The run itself
// still takes the lock, so two background runs queue behind each other exactly
// as two synchronous ones do; what changes is who waits, not how many run at
// once.
//
// onDone is called when the goroutine finishes its run. Pass a claim release
// function to transfer claim ownership from the HTTP handler to the goroutine.
// Nil is safe.
func (d *daemon) startValidateTask(req watchd.ValidateRequest, risk *watchd.RiskSummary, onDone func()) (string, error) {
	// Detached from the request: the caller is about to disconnect, and an async
	// run that died with the connection that started it would be pointless. The
	// store's parent context bounds it instead, so it ends with the daemon.
	env := req.Env
	if req.CircleCIToken != "" {
		env = append(append([]string(nil), env...), "CIRCLE_TOKEN="+req.CircleCIToken)
	}
	sessionID := session.IDFromSlice(req.Env)
	args := validateRunArgs(req)
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
		if onDone != nil {
			defer onDone()
		}
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
		exitCode := d.runner(ctx, runRoot, req.WorkDir, args, env, &stdout, &stderr)
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
	body, err := json.Marshal(watchd.CollectResponse{Tasks: tasks})
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
	var req watchd.ValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if d.runner == nil {
		http.Error(w, "no validate runner configured", http.StatusServiceUnavailable)
		return
	}

	sessionID := session.IDFromSlice(req.Env)

	// Claim registration: check for overlap, register this session's claim.
	// Release is handled explicitly — not via defer — so ownership can be
	// transferred to the goroutine on the async path without an early release.
	var claimWarning string
	if sessionID != "" && req.ProjectRoot != "" {
		overlaps := d.claims.overlapping(sessionID, req.ProjectRoot, nil)
		d.claims.register(sessionID, req.ProjectRoot, nil)
		claimWarning = watchd.ClaimNotice(overlaps)
	}

	// Decided before validateMu is taken, and deliberately so: a caller that has
	// offered to be released must not first queue behind whatever run is already
	// in flight, since that wait is the whole thing being avoided.
	reason := ""
	var risk *watchd.RiskSummary
	if req.AllowAsync && req.ProjectRoot != "" {
		decision := d.assessRisk(req.ProjectRoot)
		reason = decision.reason
		summary := decision.risk
		risk = &summary
		// Re-register with real paths now that assessRisk has measured the tree,
		// and re-check overlaps with those paths.
		if sessionID != "" && len(decision.paths) > 0 {
			d.claims.register(sessionID, req.ProjectRoot, decision.paths)
			claimWarning = watchd.ClaimNotice(d.claims.overlapping(sessionID, req.ProjectRoot, decision.paths))
		}
		if decision.async {
			// Transfer claim ownership to the goroutine: the handler returns
			// immediately and must not release the claim before the run finishes.
			onDone := func() { d.claims.release(sessionID, req.ProjectRoot) }
			if taskID, err := d.startValidateTask(req, risk, onDone); err == nil {
				writeValidateJSON(w, watchd.ValidateResponse{TaskID: taskID, Reason: reason, Risk: risk, ClaimWarning: claimWarning})
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
	if sessionID != "" {
		ctx = session.WithID(ctx, sessionID)
	}
	if req.CircleCIToken != "" {
		req.Env = append(append([]string(nil), req.Env...), "CIRCLE_TOKEN="+req.CircleCIToken)
	}
	ctx = envctx.WithEnv(ctx, req.Env)

	resp := d.runValidateNow(ctx, req, risk)
	d.claims.release(sessionID, req.ProjectRoot)
	resp.Reason = reason
	resp.Risk = risk
	resp.ClaimWarning = claimWarning
	writeValidateJSON(w, resp)
}

// validateSidecarImage returns the image to boot the run's sidecar from: the one the
// caller sent, or else the project's configured snapshot, or "" when there is
// neither. The sidecar the daemon creates is handed to the subprocess by ID, so
// the subprocess never gets to pick an image itself: without this, a daemon run
// boots the bare default image and none of the snapshot's toolchain is there.
//
// A project with no config file has no snapshot. A config that exists but
// cannot be loaded is an error: booting the bare image instead would fail later
// with a missing toolchain and nothing to explain why.
func validateSidecarImage(req watchd.ValidateRequest) (string, error) {
	if req.SidecarImage != "" {
		return req.SidecarImage, nil
	}
	dir := req.WorkDir
	if dir == "" {
		dir = req.ProjectRoot
	}
	// With no directory the load would read the daemon's own working
	// directory, which is some other project's config.
	if dir == "" {
		return "", nil
	}
	cfg, err := config.LoadProjectConfig(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load project config: %w", err)
	}
	if !cfg.HasSidecarImage() {
		return "", nil
	}
	return cfg.Validation.SidecarImage, nil
}

// provisionFailure is the message for a sidecar that could not be created. A
// 400 or 404 with a configured snapshot usually means the snapshot is gone or
// belongs to another org, which the API's own message does not say.
func provisionFailure(err error, orgID, image string) string {
	msg := err.Error()
	var se *circleci.StatusError
	if image == "" || !errors.As(err, &se) || (se.StatusCode != http.StatusBadRequest && se.StatusCode != http.StatusNotFound) {
		return msg
	}
	return fmt.Sprintf("%s\nsnapshot %s (validation.sidecarImage) may not exist in org %s; "+
		"'chunk sidecar snapshot list' shows the ones that do, and "+
		"'chunk config set validation.sidecarImage <id>' records one", msg, image, orgID)
}

// runValidateNow runs req to completion while the caller waits.
func (d *daemon) runValidateNow(ctx context.Context, req watchd.ValidateRequest, risk *watchd.RiskSummary) watchd.ValidateResponse {
	before := d.snapshotState(req.ProjectRoot)

	d.validateMu.Lock()
	defer d.validateMu.Unlock()

	args := validateRunArgs(req)
	if d.prov != nil && req.OrgID != "" {
		name := fmt.Sprintf("validate-%x", time.Now().UnixNano())
		image, err := validateSidecarImage(req)
		if err != nil {
			return watchd.ValidateResponse{ExitCode: 1, Stderr: err.Error()}
		}
		id, err := d.prov.create(ctx, req.OrgID, name, image)
		if err != nil {
			return watchd.ValidateResponse{ExitCode: 1, Stderr: provisionFailure(err, req.OrgID, image)}
		}
		defer func() { _ = d.prov.delete(context.Background(), id) }()
		args = append(append([]string(nil), args...), "--sidecar-id", id)
	}

	var stdout, stderr bytes.Buffer
	exitCode := d.runner(ctx, req.ProjectRoot, req.WorkDir, args, req.Env, &stdout, &stderr)

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

	return watchd.ValidateResponse{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}
}

func writeValidateJSON(w http.ResponseWriter, resp watchd.ValidateResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
