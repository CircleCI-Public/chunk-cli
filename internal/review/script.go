package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// RunScript runs a bookkeeping script on a sidecar, such as reading a diff
// back, and returns its stdout. No credential is sent: the script does not run
// claude. It fails on a non-zero exit, carrying the tail of stderr, and on
// stdout larger than limit bytes, which is never truncated: a cut diff or
// object name is worse than none.
func RunScript(ctx context.Context, exec sidecar.Execer, entry *sidecar.PoolEntry, script string, limit int) (string, error) {
	var stdout, stderr strings.Builder
	tooBig := false
	code, err := exec(ctx, entry, script, nil, func(stream string, data []byte) {
		if stream == circleci.StreamStderr {
			if stderr.Len() < maxStderrBytes {
				stderr.Write(data)
			}
			return
		}
		if stdout.Len()+len(data) > limit {
			tooBig = true
			return
		}
		stdout.Write(data)
	}, nil)
	switch {
	case err != nil:
		return "", err
	case tooBig:
		return "", fmt.Errorf("output is larger than %d bytes", limit)
	case code != 0:
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("exited %d: %s", code, Tail(msg, stderrTail))
		}
		return "", fmt.Errorf("exited %d", code)
	}
	return stdout.String(), nil
}
