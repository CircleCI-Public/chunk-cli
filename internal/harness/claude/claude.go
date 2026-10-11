// Package claude drives Claude Code (claude -p) as a harness.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/harness"
)

// ErrMissing is returned when a sidecar has no claude binary.
var ErrMissing = errors.New("claude is not installed on the sidecar")

// ErrCredentialRejected is returned when Anthropic rejects the credential.
var ErrCredentialRejected = errors.New("anthropic rejected the credential")

// credentialRejectedRe matches claude's own authentication failure, which reads
// the same for a revoked API key and a stale subscription token.
var credentialRejectedRe = regexp.MustCompile(`(?i)failed to authenticate.*\b401\b`)

// defaultBaseURL is where claude sends requests when no base URL is set.
const defaultBaseURL = "https://api.anthropic.com"

// stderrTail is how much of stderr a failed command reports.
const stderrTail = 2000

// disallowedGit are the git commands that would move the implementer's work
// out of the working tree the loop collects it from.
var disallowedGit = []string{
	"Bash(git commit:*)", "Bash(git stash:*)", "Bash(git reset:*)",
	"Bash(git checkout:*)", "Bash(git switch:*)", "Bash(git rebase:*)",
}

// binary is the executable name.
const binary = "claude"

// Harness is Claude Code.
type Harness struct{}

// Name implements harness.Harness.
func (Harness) Name() harness.Name { return harness.Claude }

// Binary implements harness.Harness.
func (Harness) Binary() string { return binary }

// ImplementScript implements harness.Harness. The sidecar is a sandbox, which
// is what lets claude skip permission prompts there even when the image runs it
// as root.
func (Harness) ImplementScript(in harness.ImplementInput) string {
	args := []string{
		binary, "-p", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions",
		"--disallowedTools", strings.Join(disallowedGit, ","),
		"--append-system-prompt", in.SystemPrompt,
	}
	switch {
	case in.Resume:
		args = append(args, "--resume", in.SessionID)
	case in.SessionID != "":
		args = append(args, "--session-id", in.SessionID)
	}
	if in.Model != "" {
		args = append(args, "--model", in.Model)
	}
	return "export IS_SANDBOX=1\n" + Script(in.RepoPath, in.Prompt, args)
}

// ReviewScript implements harness.Harness.
func (Harness) ReviewScript(in harness.ReviewInput) string {
	format := "text"
	if in.Schema != "" {
		format = "json"
	}
	args := []string{binary, "-p", "--output-format", format, "--allowedTools", strings.Join(in.Tools, ",")}
	if in.Schema != "" {
		args = append(args, "--json-schema", in.Schema)
	}
	if in.Model != "" {
		args = append(args, "--model", in.Model)
	}
	return Script(in.RepoPath, in.Prompt, args)
}

// Script builds the shell script that runs claude in repoPath on a sidecar with
// prompt on stdin. args is the whole command line, starting with "claude".
func Script(repoPath, prompt string, args []string) string {
	return harness.PipeScript(binary, "", repoPath, prompt, args)
}

// result is the part of claude's --output-format json result that a review
// needs. structured_output is set only when the answer satisfied the schema.
type result struct {
	IsError          bool   `json:"is_error"`
	Subtype          string `json:"subtype"`
	Result           string `json:"result"`
	StructuredOutput *struct {
		Review   string          `json:"review"`
		Findings json.RawMessage `json:"findings"`
	} `json:"structured_output"`
}

// maxResultInError caps how much of claude's own result text an error quotes.
const maxResultInError = 500

// ParseReview implements harness.Harness. A result with no structured output is
// an error, not a prose-only review.
func (Harness) ParseReview(stdout string) (harness.ReviewPayload, error) {
	var res result
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		return harness.ReviewPayload{}, fmt.Errorf("read claude's result: %w", err)
	}
	if res.IsError || res.StructuredOutput == nil {
		text := []rune(strings.TrimSpace(res.Result))
		if len(text) > maxResultInError {
			text = append(text[:maxResultInError], '…')
		}
		return harness.ReviewPayload{}, fmt.Errorf("claude gave no structured findings (%s): %s", res.Subtype, string(text))
	}
	return harness.ReviewPayload{Review: res.StructuredOutput.Review, Findings: res.StructuredOutput.Findings}, nil
}

// Env implements harness.Harness: only the credential, and the base URL when it
// points somewhere other than Anthropic.
func (Harness) Env(cred harness.Credential, baseURL string) map[string]string {
	env := map[string]string{cred.EnvVar: cred.Value}
	if baseURL != "" && strings.TrimRight(baseURL, "/") != defaultBaseURL {
		env["ANTHROPIC_BASE_URL"] = baseURL
	}
	return env
}

// Prepare implements harness.Harness: claude needs no setup.
func (Harness) Prepare(harness.Credential) (string, map[string]string) { return "", nil }

// RunError implements harness.Harness. Every caller running claude classifies
// its end here, so a missing binary or a rejected credential reads the same
// whatever ran.
func (Harness) RunError(ctx context.Context, timeout time.Duration, code int, execErr error, stdout, stderr string) error {
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("timed out after %s", timeout)
	case execErr != nil:
		return fmt.Errorf("exec: %w", execErr)
	case code == harness.MissingExitCode:
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

func exitError(code int, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return fmt.Errorf("claude exited %d", code)
	}
	return fmt.Errorf("claude exited %d: %s", code, harness.Tail(stderr, stderrTail))
}
