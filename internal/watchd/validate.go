package watchd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
)

// ValidateRequest is the payload sent to POST /validate.
type ValidateRequest struct {
	// Args is os.Args[1:] from the caller, e.g. ["validate", "test", "--remote"].
	Args []string `json:"args"`
	// CircleCIToken is forwarded to the subprocess as CIRCLE_TOKEN.
	CircleCIToken string `json:"circleci_token,omitempty"`
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

	exe, err := os.Executable()
	if err != nil {
		http.Error(w, "executable: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Append --sync so the subprocess skips daemon delegation and runs inline.
	// The socket is owner-only (0700) and exe is the current binary, so the
	// args are not an injection risk.
	args := append(append([]string(nil), req.Args...), "--sync")
	cmd := exec.CommandContext(r.Context(), exe, args...) //nolint:gosec

	env := os.Environ()
	if req.CircleCIToken != "" {
		env = append(env, "CIRCLE_TOKEN="+req.CircleCIToken)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			http.Error(w, "run: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ValidateResponse{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	})
}
