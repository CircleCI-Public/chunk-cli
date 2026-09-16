package watchd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

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
	// OrgID is the CircleCI org UUID for this project. When set and the daemon
	// has credentials, the daemon provisions a fresh sidecar for the run rather
	// than expecting one to already be registered on its filesystem.
	OrgID string `json:"org_id,omitempty"`
}

// ValidateResponse is the response from POST /validate.
type ValidateResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
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

	args := req.Args
	if d.prov != nil && req.OrgID != "" {
		name := fmt.Sprintf("validate-%x", time.Now().UnixNano())
		id, err := d.prov.create(ctx, req.OrgID, name, "")
		if err != nil {
			http.Error(w, "provision sidecar: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = d.prov.delete(context.Background(), id) }()
		args = append(append([]string(nil), args...), "--sidecar-id", id)
	}

	var stdout, stderr bytes.Buffer
	exitCode := d.runner(ctx, args, req.Env, &stdout, &stderr)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ValidateResponse{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	})
}
