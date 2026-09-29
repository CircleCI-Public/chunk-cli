// Package claudeauth mints the long-lived Claude subscription token that
// headless claude reads from CLAUDE_CODE_OAUTH_TOKEN.
package claudeauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// setupTimeout bounds the whole flow. It is a browser round trip with a human
// in it, not a request.
const setupTimeout = 5 * time.Minute

// tokenRe matches the token `claude setup-token` prints.
var tokenRe = regexp.MustCompile(`sk-ant-oat[0-9]{2}-[A-Za-z0-9_-]{20,}`)

// ErrNotInstalled reports that claude is not on PATH, which needs different
// advice from a flow that ran and produced no token.
var ErrNotInstalled = errors.New("claude is not installed")

// SetupToken runs `claude setup-token` and returns the token it prints.
//
// stdin and stderr stay on the terminal so the browser flow and its prompts
// work. Stdout is captured to scrape the token, and echoed back with any token
// redacted so the secret never reaches a scrollback or a CI log. If stdout
// holds no token, prompter collects it instead.
func SetupToken(ctx context.Context, streams iostream.Streams, prompter func(string) (string, error)) (string, error) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotInstalled, err)
	}

	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()

	var captured bytes.Buffer
	redact := &redactWriter{w: streams.Err}

	cmd := exec.CommandContext(ctx, claude, "setup-token")
	// A child that needs a terminal needs a real *os.File, so stdin bypasses
	// Streams. Stderr does too, to keep the flow on one terminal.
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	cmd.Stdout = io.MultiWriter(&captured, redact)

	runErr := cmd.Run()
	if err := redact.Flush(); err != nil {
		return "", fmt.Errorf("echo setup-token output: %w", err)
	}

	if token := tokenRe.FindString(captured.String()); token != "" {
		return token, nil
	}
	if runErr != nil {
		return "", fmt.Errorf("claude setup-token: %w", runErr)
	}
	return prompter("Token")
}

// redactWriter forwards whole lines to w with any token blanked, so a captured
// setup-token flow still shows its prompts and authorize URL.
type redactWriter struct {
	w    io.Writer
	line bytes.Buffer
}

func (r *redactWriter) Write(p []byte) (int, error) {
	n := len(p)
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			r.line.Write(p)
			break
		}
		r.line.Write(p[:i+1])
		if err := r.emit(); err != nil {
			return 0, err
		}
		p = p[i+1:]
	}
	return n, nil
}

// Flush writes whatever trailing text arrived without a newline, such as a
// prompt claude leaves the cursor on.
func (r *redactWriter) Flush() error {
	if r.line.Len() == 0 {
		return nil
	}
	return r.emit()
}

func (r *redactWriter) emit() error {
	line := r.line.String()
	r.line.Reset()
	_, err := io.WriteString(r.w, tokenRe.ReplaceAllString(line, "[token redacted]"))
	return err
}
