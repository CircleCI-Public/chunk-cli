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

func (d *daemon) handleValidate(w http.ResponseWriter, r *http.Request) {
	d.validateMu.Lock()
	defer d.validateMu.Unlock()

	var req ValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Seed the context with the caller's env and session ID so the in-process
	// validate run behaves as if launched in the caller's environment.
	ctx := r.Context()
	if id := session.IDFromSlice(req.Env); id != "" {
		ctx = session.WithID(ctx, id)
	}
	if req.CircleCIToken != "" {
		req.Env = append(append([]string(nil), req.Env...), "CIRCLE_TOKEN="+req.CircleCIToken)
	}
	ctx = envctx.WithEnv(ctx, req.Env)

	var stdout, stderr bytes.Buffer
	exitCode := d.runner(ctx, req.Args, req.Env, &stdout, &stderr)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ValidateResponse{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	})
}
