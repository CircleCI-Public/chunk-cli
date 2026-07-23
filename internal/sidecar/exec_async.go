package sidecar

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

const maxReconnects = 5

// ExecAsync executes a shell script on a remote sidecar via the async HTTP API.
// Output lines are written to streams in real time as they arrive.
// On dropped connections the stream reconnects automatically up to maxReconnects times.
func ExecAsync(ctx context.Context, client *circleci.Client, sidecarID, script string, envVars map[string]string, statusFn iostream.StatusFunc, streams iostream.Streams) (int, error) {
	commandID, err := client.ExecAsync(ctx, sidecarID, "sh", []string{"-c", script}, envVars, "")
	if err != nil {
		return 0, fmt.Errorf("start remote command: %w", err)
	}
	return streamWithReconnect(ctx, client, commandID, statusFn, streams)
}

func streamWithReconnect(ctx context.Context, client *circleci.Client, commandID string, statusFn iostream.StatusFunc, streams iostream.Streams) (int, error) {
	offset := 0
	reconnects := 0

	for {
		exitCode, done, newOffset, err := readStream(ctx, client, commandID, offset, streams)
		offset = newOffset
		if done {
			return exitCode, nil
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		// A clean EOF (err == nil) means the server closed the stream without a
		// terminal event. If the command has already ended, finish via a status
		// poll rather than burning reconnect attempts on a stream that will not
		// resume. Otherwise the command is still running and we reconnect to
		// resume streaming from the current offset.
		if err == nil {
			if code, ok := endedExitCode(ctx, client, commandID); ok {
				return code, nil
			}
		}

		reconnects++
		if reconnects > maxReconnects {
			// Fall back to status poll when we can no longer stream.
			if code, ok := endedExitCode(ctx, client, commandID); ok {
				return code, nil
			}
			return 0, fmt.Errorf("output stream disconnected: %w", err)
		}
		if err != nil {
			statusFn(iostream.LevelWarn, "connection interrupted, reconnecting...")
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(reconnectBackoff(reconnects)):
		}
	}
}

// endedExitCode polls the command status and returns its exit code if the
// command has ended. ok is false when the command is still running or its
// status could not be retrieved.
func endedExitCode(ctx context.Context, client *circleci.Client, commandID string) (code int, ok bool) {
	cmd, err := client.GetCommand(ctx, commandID)
	if err != nil || cmd.Phase != "ended" || cmd.ExitCode == nil {
		return 0, false
	}
	return *cmd.ExitCode, true
}

func readStream(ctx context.Context, client *circleci.Client, commandID string, offset int, streams iostream.Streams) (exitCode int, done bool, newOffset int, err error) {
	body, err := client.StreamCommandOutput(ctx, commandID, offset)
	if err != nil {
		return 0, false, offset, err
	}
	defer func() { _ = body.Close() }()

	newOffset = offset
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		var line circleci.CommandOutputLine
		if jsonErr := json.Unmarshal(scanner.Bytes(), &line); jsonErr != nil {
			continue
		}
		if line.CommandID != "" {
			code := 0
			if line.ExitCode != nil {
				code = *line.ExitCode
			}
			return code, true, newOffset, nil
		}
		newOffset = line.Index + 1
		switch line.Stream {
		case "stdout":
			_, _ = fmt.Fprintf(streams.Out, "%s\n", line.Line)
		case "stderr":
			_, _ = fmt.Fprintf(streams.Err, "%s\n", line.Line)
		}
	}
	return 0, false, newOffset, scanner.Err()
}

func reconnectBackoff(attempt int) time.Duration {
	d := time.Duration(attempt) * 500 * time.Millisecond
	if d > 4*time.Second {
		return 4 * time.Second
	}
	return d
}
