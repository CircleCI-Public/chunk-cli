// Package harness is the seam between smelt and the coding-agent CLIs it can
// drive on a sidecar. A Harness builds the command line for one CLI and reads
// what it prints; the factory and the reviewers do everything else.
//
// This package holds only the interface and what the implementations share, so
// internal/harness/claude and internal/harness/codex can import it. Look a
// harness up by name with internal/harness/all.
package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/sidecar"
)

// Name identifies a harness.
type Name string

// The harnesses smelt can drive.
const (
	Claude Name = config.HarnessClaude
	Codex  Name = config.HarnessCodex
)

// MissingExitCode is the exit code a script uses for its CLI not being on PATH,
// so every caller reports it the same way. Not the shell's own 127, which the
// CLI also exits with when something it shelled out to is missing.
const MissingExitCode = 97

// Credential is what a harness authenticates with. EnvVar is the variable the
// CLI reads it from, so only one kind is ever sent and a stale one cannot
// shadow another.
type Credential struct {
	EnvVar string
	Value  string
}

// Harness is one coding-agent CLI the factory can drive on a sidecar.
type Harness interface {
	Name() Name
	// Binary is the executable the sidecar must have.
	Binary() string

	// ImplementScript is one implementer turn: prompt on stdin, streamed output.
	ImplementScript(in ImplementInput) string
	// NewImplementParser consumes the streamed output of an implementer turn.
	NewImplementParser(onActivity func(Activity)) StreamParser

	// ReviewScript is one read-only review. With a Schema the answer is
	// structured findings, read back by ParseReview.
	ReviewScript(in ReviewInput) string
	// ParseReview reads structured findings out of a review script's stdout.
	ParseReview(stdout string) (ReviewPayload, error)

	// Env is the environment a turn runs with: the credential the CLI reads
	// directly, and a base URL when it points somewhere unusual.
	Env(cred Credential, baseURL string) map[string]string
	// Prepare returns a script to run once per sidecar before the first turn,
	// and the environment it needs, or an empty script when there is nothing
	// to set up. Secrets that only setup needs go in that environment, so later
	// turns never get them.
	Prepare(cred Credential) (script string, env map[string]string)

	// RunError explains how a run ended, or returns nil when it exited 0.
	RunError(ctx context.Context, timeout time.Duration, code int, execErr error, stdout, stderr string) error
}

// ImplementInput is one implementer turn.
type ImplementInput struct {
	RepoPath     string
	Prompt       string
	SystemPrompt string
	Model        string
	// SessionID is the session to continue when Resume is set.
	SessionID string
	Resume    bool
}

// ReviewInput is one review.
type ReviewInput struct {
	RepoPath string
	Prompt   string
	Model    string
	// Schema, when set, is the JSON schema the answer must satisfy.
	Schema string
	// Tools limits what a reviewer may use. Claude only: Codex reviews are held
	// to read-only by checking the working tree afterwards.
	Tools []string
}

// ReviewPayload is the structured answer of a review before it is checked.
type ReviewPayload struct {
	Review   string
	Findings json.RawMessage
}

// Activity is one thing the implementer did, for display.
type Activity struct {
	// Tool is the tool used, or "" for text the implementer wrote.
	Tool string
	// Detail is the tool's target (a file, a command) or the text.
	Detail string
}

// Turn is what one implementer turn reports, whichever harness ran it.
type Turn struct {
	Summary      string
	CostUSD      float64 // 0 when the CLI reports tokens only
	InputTokens  int
	CachedTokens int
	OutputTokens int
	// RateLimit is the last rate-limit signal seen, if any.
	RateLimit *RateLimit
	SessionID string
}

// RateLimit is a rate-limit signal an agent emitted during a turn.
type RateLimit struct {
	Status      string     `json:"status"`                // "allowed", "warning", "limited"
	Bucket      string     `json:"bucket,omitempty"`      // e.g. "5h", "7d"; "" when unknown
	Utilization *int       `json:"utilization,omitempty"` // percent, when the CLI reports one
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
}

// StreamParser reads the streamed output of an implementer turn as it arrives.
type StreamParser interface {
	Write(data []byte)
	// Flush handles a last line that had no trailing newline.
	Flush()
	Outcome() Outcome
}

// Outcome is what a parser has seen so far.
type Outcome struct {
	Turn Turn
	// Done is set once the CLI reported the end of the turn.
	Done bool
	// Failed is set when the CLI reported that the turn failed.
	Failed bool
	// Message is the CLI's own account of the turn: the final result, or why it
	// failed. It stands in for stderr when explaining a failed exit.
	Message string
	// Err is a failure the parser recognised and classified, such as a sentinel
	// a caller can test for. It ends the run as a harness error even when the
	// CLI itself exited 0 and called the turn done.
	Err error
}

// Tail keeps the last n bytes of s, marking the cut. The end is kept because
// that is where compilers, test runners and agents say what went wrong.
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// PipeScript builds the shell script that runs a CLI in repoPath on a sidecar
// with prompt on stdin. args is the whole command line, starting with binary.
// pathPrefix goes in front of PATH; see ScriptHead.
func PipeScript(binary, pathPrefix, repoPath, prompt string, args []string) string {
	return ScriptHead(binary, pathPrefix) + PipeLine(repoPath, prompt, args)
}

// ScriptHead puts the CLI's directory on PATH and exits MissingExitCode when
// the CLI is not there. Native installers put the CLI in ~/.local/bin, which a
// non-login sh does not have on PATH.
func ScriptHead(binary, pathPrefix string) string {
	return fmt.Sprintf(`export PATH="%s$HOME/.local/bin:$PATH"
command -v %s >/dev/null 2>&1 || exit %d
`, pathPrefix, sidecar.ShellEscape(binary), MissingExitCode)
}

// PipeLine is the line of a script that runs args in repoPath with prompt on
// stdin. The prompt is piped in base64-encoded, so no quoting in it can reach
// the shell.
func PipeLine(repoPath, prompt string, args []string) string {
	return PipeCommand(repoPath, prompt, sidecar.ShellJoin(args))
}

// PipeCommand is PipeLine for a command that is already shell text, such as
// one that needs the shell to expand a variable.
func PipeCommand(repoPath, prompt, command string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(prompt))
	return fmt.Sprintf("cd %s && echo %s | base64 -d | %s", sidecar.ShellEscape(repoPath), encoded, command)
}

// Lines splits streamed output into lines as it arrives, which can be
// mid-line, and hands each to Handle. A line past MaxLineBytes is dropped
// rather than buffered: a tool result echoing a large file can be long.
type Lines struct {
	Handle   func([]byte)
	buf      []byte
	overflow bool
}

// MaxLineBytes caps one buffered line.
const MaxLineBytes = 1 << 20

// Write feeds data to the splitter.
func (l *Lines) Write(data []byte) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			l.appendPartial(data)
			return
		}
		l.appendPartial(data[:i])
		if !l.overflow {
			l.Handle(l.buf)
		}
		l.buf, l.overflow = l.buf[:0], false
		data = data[i+1:]
	}
}

func (l *Lines) appendPartial(data []byte) {
	if l.overflow || len(l.buf)+len(data) > MaxLineBytes {
		l.overflow = true
		return
	}
	l.buf = append(l.buf, data...)
}

// Flush handles a last line that had no trailing newline.
func (l *Lines) Flush() {
	if len(l.buf) > 0 && !l.overflow {
		l.Handle(l.buf)
	}
	l.buf, l.overflow = nil, false
}

// Window is one usage window of a harness, as `smelt harness status` reports it.
type Window struct {
	// Window names the span: "5h", "7d", "30d", "7d:<model>", or "<n>m".
	Window      string     `json:"window"`
	UsedPercent *float64   `json:"used_percent"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
	// Status is "allowed", "warning" or "limited" when the figure came from a
	// run's rate-limit signal.
	Status string `json:"status,omitempty"`
	// Source is where the figure came from: "app-server", "oauth-usage" or "run".
	Source string     `json:"source"`
	SeenAt *time.Time `json:"seen_at,omitempty"`
}

// WindowName names a window by its length, so a plan's windows are named by
// what they are rather than by which slot the CLI put them in.
func WindowName(minutes int) string {
	switch minutes {
	case 300:
		return "5h"
	case 10080:
		return "7d"
	case 43200:
		return "30d"
	}
	return fmt.Sprintf("%dm", minutes)
}
