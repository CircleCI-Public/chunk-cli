// Package claudecode is the Claude Code harness: it runs the claude CLI on a
// sidecar and explains how the run ended.
package claudecode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/harness"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// maxOutputBytes caps what is kept of one run's output. A run's answer is
// prose, so hitting this means something went wrong, not that it was thorough.
const maxOutputBytes = 256 * 1024

// maxStructuredOutputBytes caps claude's JSON result when a schema is given. It
// is larger than maxOutputBytes because the result carries the answer twice, as
// text and as structured_output; a truncated result would not decode at all.
const maxStructuredOutputBytes = 8 << 20

// ExitMissing is the exit code a script running claude on a sidecar uses for
// claude not being on PATH, so every caller reports it the same way. Not the
// shell's own 127, which claude also exits with when something it shelled out
// to is missing: that is one broken run, not a missing binary.
const ExitMissing = 97

// ErrMissing is returned when a sidecar has no claude binary.
var ErrMissing = errors.New("claude is not installed on the sidecar")

// ErrCredentialRejected is returned when Anthropic rejects the credential.
var ErrCredentialRejected = errors.New("anthropic rejected the credential")

// credentialRejectedRe matches claude's own authentication failure, which reads
// the same for a revoked API key and a stale subscription token.
var credentialRejectedRe = regexp.MustCompile(`(?i)failed to authenticate.*\b401\b`)

// Credential is the Claude credential a run authenticates with. EnvVar is the
// variable claude reads it from, so only one of the two is ever sent and a
// stale one cannot shadow the other.
type Credential struct {
	EnvVar string
	Value  string
}

// Options configures one run of claude -p.
type Options struct {
	Credential Credential
	// BaseURL is forwarded to claude when it is not Anthropic's own, so a
	// credential issued by a gateway is sent to that gateway.
	BaseURL string
	Model   string        // optional; claude's default when empty
	Timeout time.Duration // optional; no limit beyond ctx when zero
	// Tools is claude's --allowedTools list.
	Tools []string
	// Schema, when set, is passed as --json-schema and the output is claude's
	// JSON result, which ParseResult reads. Empty, the output is the answer as
	// plain text.
	Schema string
	// OnSubmitted is called with the remote command ID as soon as the exec is
	// accepted and before its output is streamed.
	OnSubmitted func(commandID string)
}

// Turn is the outcome of one run. Output is kept when the run fails, so a
// caller can show what it produced.
type Turn struct {
	// Output is claude's stdout, trimmed: the answer, or the JSON result when
	// Options.Schema is set.
	Output   string
	Duration time.Duration
}

// Run runs claude -p once in the entry's repository with prompt on stdin and
// returns its output. A failed run is explained by RunError.
func Run(ctx context.Context, exec harness.Execer, entry *sidecar.PoolEntry, prompt string, opts Options) (Turn, error) {
	start := time.Now()
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	// Stdout is the answer. Stderr is kept only to explain a failure, so
	// claude's progress noise never lands in the answer.
	var stdout, stderr strings.Builder
	// Only stdout carries the JSON result, so only it gets the larger cap; stderr
	// is read for a short tail and stays small.
	stdoutLimit := maxOutputBytes
	if opts.Schema != "" {
		stdoutLimit = maxStructuredOutputBytes
	}
	stdoutCut := false
	onOutput := func(stream string, data []byte) {
		buf, limit := &stdout, stdoutLimit
		if stream == circleci.StreamStderr {
			buf, limit = &stderr, maxOutputBytes
		}
		room := max(limit-buf.Len(), 0)
		if len(data) > room && buf == &stdout {
			stdoutCut = true
		}
		buf.Write(data[:min(len(data), room)])
	}
	code, err := exec(ctx, entry, script(entry.RepoPath, prompt, opts), Env(opts.Credential, opts.BaseURL), onOutput, opts.OnSubmitted)
	turn := Turn{Output: strings.TrimSpace(stdout.String()), Duration: time.Since(start)}
	if err := RunError(ctx, opts.Timeout, code, err, turn.Output, stderr.String()); err != nil {
		return turn, err
	}
	// A result cut at the cap cannot decode; say why instead of reporting
	// malformed JSON.
	if opts.Schema != "" && stdoutCut {
		return turn, fmt.Errorf("claude's result is over %d bytes", stdoutLimit)
	}
	return turn, nil
}

// script builds the shell script for one run.
func script(repoPath, prompt string, opts Options) string {
	format := "text"
	if opts.Schema != "" {
		format = "json"
	}
	args := []string{"claude", "-p", "--output-format", format}
	if len(opts.Tools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.Tools, ","))
	}
	if opts.Schema != "" {
		args = append(args, "--json-schema", opts.Schema)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	return Script(repoPath, prompt, args)
}

// Result is the part of claude's --output-format json result a caller reads.
// StructuredOutput is set only when the answer satisfied the schema.
type Result struct {
	IsError          bool            `json:"is_error"`
	Subtype          string          `json:"subtype"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// ParseResult reads claude's JSON result. A null structured_output reads as
// none.
func ParseResult(output string) (Result, error) {
	var res Result
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		return Result{}, fmt.Errorf("read claude's result: %w", err)
	}
	if string(res.StructuredOutput) == "null" {
		res.StructuredOutput = nil
	}
	return res, nil
}

// RunError explains how a claude run on a sidecar ended, or returns nil when it
// exited 0. ctx is the run's own context, bounded by timeout; code and execErr
// are what the Execer returned, and stdout and stderr what claude wrote. Every
// caller running claude classifies its end here, so a missing binary or a
// rejected credential reads the same whatever ran.
func RunError(ctx context.Context, timeout time.Duration, code int, execErr error, stdout, stderr string) error {
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("timed out after %s", timeout)
	case execErr != nil:
		return fmt.Errorf("exec: %w", execErr)
	case code == ExitMissing:
		return ErrMissing
	case code != 0 && CredentialRejected(stdout, stderr):
		return ErrCredentialRejected
	case code != 0:
		return exitError(code, stderr)
	}
	return nil
}

// CredentialRejected reports whether claude failed to authenticate. Both
// streams are checked: the 401 lands on stdout, while stderr can carry
// unrelated warnings.
func CredentialRejected(stdout, stderr string) bool {
	return credentialRejectedRe.MatchString(stdout) || credentialRejectedRe.MatchString(stderr)
}

// stderrTail is how much of stderr a failed run reports.
const stderrTail = 2000

func exitError(code int, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return fmt.Errorf("claude exited %d", code)
	}
	return fmt.Errorf("claude exited %d: %s", code, tail(stderr, stderrTail))
}

// tail keeps the last n bytes of s, marking the cut. The end is kept because
// that is where claude says what went wrong.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// Script builds the shell script that runs claude in repoPath on a sidecar with
// prompt on stdin. args is the whole command line, starting with "claude". The
// prompt is piped in base64-encoded, so no quoting in it can reach the shell.
// Claude Code's native installer puts claude in ~/.local/bin, which a non-login
// sh does not have on PATH.
func Script(repoPath, prompt string, args []string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(prompt))
	return fmt.Sprintf(`export PATH="$HOME/.local/bin:$PATH"
command -v claude >/dev/null 2>&1 || exit %d
cd %s && echo %s | base64 -d | %s`,
		ExitMissing, sidecar.ShellEscape(repoPath), encoded, sidecar.ShellJoin(args))
}

// defaultBaseURL is where claude sends requests when no base URL is set.
const defaultBaseURL = "https://api.anthropic.com"

// Env is the environment claude runs with on a sidecar: only the credential,
// and the base URL when it points somewhere other than Anthropic.
func Env(cred Credential, baseURL string) map[string]string {
	env := map[string]string{cred.EnvVar: cred.Value}
	if baseURL != "" && strings.TrimRight(baseURL, "/") != defaultBaseURL {
		env["ANTHROPIC_BASE_URL"] = baseURL
	}
	return env
}
