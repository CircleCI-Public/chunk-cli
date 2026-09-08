package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const opTimeout = 30 * time.Second

// ErrOpNotFound reports that the 1Password CLI is not on PATH. Callers use it to
// tell "you need to install op" apart from "op ran and could not read the ref",
// which need different advice.
var ErrOpNotFound = errors.New("op CLI not found")

// OpResolver resolves references via `op read <ref>`.
type OpResolver struct {
	once    sync.Once
	opPath  string
	lookErr error
}

func (r *OpResolver) Resolve(ctx context.Context, ref string) (string, error) {
	r.once.Do(func() {
		r.opPath, r.lookErr = exec.LookPath("op")
	})
	if r.lookErr != nil {
		return "", fmt.Errorf("%w: %w", ErrOpNotFound, r.lookErr)
	}

	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.opPath, "read", "--no-newline", ref)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			// op's stderr ends in a newline; keeping it doubles the blank line
			// the error printer already adds around the detail.
			return "", fmt.Errorf("op read: %s", msg)
		}
		return "", fmt.Errorf("op read: %w", err)
	}
	return stdout.String(), nil
}
