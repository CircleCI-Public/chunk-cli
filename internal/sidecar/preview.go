package sidecar

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// PreviewLogPath is where StartPreviewServer redirects the app's stdout and
// stderr on the sidecar.
const PreviewLogPath = "/tmp/chunk-preview.log"

const (
	previewPortTimeout    = 30 * time.Second
	previewPollInterval   = 2 * time.Second
	previewInsecureScheme = "http"
)

// BuildPreviewURL rewrites a sidecar's e2b connection URL to point at a
// different port on the same sandbox, e.g.
// https://8000-abc123.e2b.app + 3000 -> https://3000-abc123.e2b.app
func BuildPreviewURL(rawURL string, port int) (string, error) {
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse sidecar URL: %w", err)
	}

	host := u.Hostname()
	dash := strings.Index(host, "-")
	if dash <= 0 {
		return "", fmt.Errorf("sidecar URL host %q does not look like an e2b sandbox host (expected <port>-<sandbox-id>)", host)
	}
	if _, err := strconv.Atoi(host[:dash]); err != nil {
		return "", fmt.Errorf("sidecar URL host %q does not look like an e2b sandbox host (expected <port>-<sandbox-id>)", host)
	}

	scheme := "https"
	if u.Scheme == previewInsecureScheme || u.Scheme == "ws" {
		scheme = previewInsecureScheme
	}
	return fmt.Sprintf("%s://%d-%s", scheme, port, host[dash+1:]), nil
}

// StartPreviewServer runs command in the background on the sidecar, detached
// via nohup and a backgrounded subshell so it keeps running after the SSH
// session closes, with output redirected to PreviewLogPath. It then polls
// port until it accepts connections or the timeout elapses.
func StartPreviewServer(ctx context.Context, session *Session, workspace, command string,
	port int, envVars map[string]string, status iostream.StatusFunc) error {

	startCmd := command
	if workspace != "" {
		startCmd = "cd " + ShellEscape(workspace) + " && " + startCmd
	}
	detached := "bash -l -c " + ShellEscape(
		"(nohup sh -c "+ShellEscape(startCmd)+" >"+PreviewLogPath+" 2>&1 &)",
	)

	result, err := ExecOverSSH(ctx, session, detached, nil, envVars)
	if err != nil {
		return fmt.Errorf("start preview server: %w", err)
	}
	if result.ExitCode != 0 {
		detail := result.Stderr
		if detail == "" {
			detail = "command exited with a non-zero status"
		}
		return fmt.Errorf("start preview server: %s", detail)
	}

	status(iostream.LevelInfo, fmt.Sprintf("Waiting for port %d...", port))
	return waitForPort(ctx, session, port, previewPortTimeout, previewPollInterval)
}

// waitForPort polls the sidecar over SSH until a process is listening on
// port, or returns an error once timeout has elapsed.
func waitForPort(ctx context.Context, session *Session, port int, timeout, interval time.Duration) error {
	checkCmd := fmt.Sprintf(`timeout 1 bash -c "exec 3<>/dev/tcp/localhost/%d"`, port)
	deadline := time.Now().Add(timeout)
	for {
		result, err := ExecOverSSH(ctx, session, checkCmd, nil, nil)
		if err == nil && result.ExitCode == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for port %d to open after %s", port, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
