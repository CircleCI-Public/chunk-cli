package sidecar

import (
	"context"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

// Execer runs a shell script on a sidecar and streams its output. onSubmitted,
// when non-nil, is called with the command ID between submission and streaming.
type Execer func(ctx context.Context, entry *PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(commandID string)) (exitCode int, err error)

// ClientExec runs scripts through the pool entry's CircleCI client. Submit and
// stream are kept apart so onSubmitted sees the command ID: the caller needs it
// before the command ends, not after.
func ClientExec(ctx context.Context, entry *PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(string)) (int, error) {
	commandID, err := entry.Client.SubmitExec(ctx, entry.ID, "sh", []string{"-c", script}, env)
	if err != nil {
		return 0, fmt.Errorf("submit: %w", err)
	}
	if onSubmitted != nil {
		onSubmitted(commandID)
	}
	res, err := entry.Client.StreamOutput(ctx, commandID, "", onOutput)
	if err != nil {
		return 0, fmt.Errorf("stream output: %w", err)
	}
	return res.ExitCode, nil
}
