package secrets

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

const opTimeout = 30 * time.Second

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
		return "", fmt.Errorf("op CLI not found — install it from https://developer.1password.com/docs/cli/get-started/: %w", r.lookErr)
	}

	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.opPath, "read", "--no-newline", ref)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("op read: %s", stderr.String())
		}
		return "", fmt.Errorf("op read: %w", err)
	}
	return stdout.String(), nil
}
