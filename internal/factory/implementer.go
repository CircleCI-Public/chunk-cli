package factory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// DefaultImplementTimeout bounds one implementer turn. A turn writes code and
// may run the tests several times, so it gets longer than a review.
const DefaultImplementTimeout = 30 * time.Minute

// implementerSystemPrompt keeps the implementer's work where the loop collects
// it. Reviewers are shown the uncommitted changes against the baseline commit,
// so a commit, reset or branch switch would hide the work from them.
const implementerSystemPrompt = `You are working in a disposable copy of the repository on a remote machine.
Make the requested change by editing files. Leave your changes uncommitted: do not run git commit, git stash, git reset, git checkout, git switch or git rebase. Your changes are collected from the working tree and reviewed.
When you finish, reply with a short summary of what you changed.`

// disallowedGit are the git commands that would move the implementer's work
// out of the working tree the loop collects it from.
var disallowedGit = []string{
	"Bash(git commit:*)", "Bash(git stash:*)", "Bash(git reset:*)",
	"Bash(git checkout:*)", "Bash(git switch:*)", "Bash(git rebase:*)",
}

// Activity is one thing the implementer did, for display.
type Activity struct {
	// Tool is the tool used, or "" for text the implementer wrote.
	Tool string
	// Detail is the tool's target (a file, a command) or the text.
	Detail string
}

// Turn is the outcome of one implementer turn.
type Turn struct {
	Summary  string
	Duration time.Duration
	CostUSD  float64
}

// Implementer runs Claude Code on a sidecar to write code. Every turn resumes
// the same session, so feedback arrives with the context of the work so far.
type Implementer struct {
	Exec       review.Execer
	Entry      *sidecar.PoolEntry
	Credential review.Credential
	BaseURL    string
	Model      string
	Timeout    time.Duration
	OnActivity func(Activity)

	sessionID string
	started   bool
}

// Run sends prompt to the implementer and waits for its turn to end.
func (im *Implementer) Run(ctx context.Context, prompt string) (Turn, error) {
	if im.sessionID == "" {
		im.sessionID = uuid.NewString()
	}
	timeout := im.Timeout
	if timeout <= 0 {
		timeout = DefaultImplementTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	s := &streamParser{onActivity: im.OnActivity}
	var stderr bytes.Buffer
	env := review.Env(im.Credential, im.BaseURL)
	// The sidecar is a sandbox, which is what lets claude skip permission
	// prompts there even when the image runs it as root.
	env["IS_SANDBOX"] = "1"
	code, err := im.Exec(ctx, im.Entry, im.script(prompt), env, func(stream string, data []byte) {
		if stream == circleci.StreamStderr {
			if stderr.Len() < outputTail {
				stderr.Write(data)
			}
			return
		}
		s.write(data)
	}, nil)
	s.flush()
	// The session exists once claude has started it, even if this turn failed,
	// so the next turn must resume it rather than try to create it again.
	if s.sessionID != "" {
		im.started = true
	}
	turn := Turn{Summary: s.result.Result, Duration: time.Since(start), CostUSD: s.result.TotalCostUSD}

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return turn, fmt.Errorf("implementer timed out after %s", timeout)
	case err != nil:
		return turn, fmt.Errorf("implementer exec: %w", err)
	case code == review.ExitClaudeMissing:
		return turn, review.ErrClaudeMissing
	case code != 0 && review.CredentialRejected(s.result.Result, stderr.String()):
		return turn, review.ErrCredentialRejected
	case code != 0 || s.result.IsError:
		msg := strings.TrimSpace(s.result.Result)
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		return turn, fmt.Errorf("implementer exited %d: %s", code, tailText(msg, 2000))
	case !s.sawResult:
		return turn, errors.New("implementer ended without a result")
	}
	return turn, nil
}

// script builds the shell script for one turn. The prompt is piped in
// base64-encoded so no quoting in it reaches the shell.
func (im *Implementer) script(prompt string) string {
	args := []string{
		"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions",
		"--disallowedTools", strings.Join(disallowedGit, ","),
		"--append-system-prompt", implementerSystemPrompt,
	}
	if im.started {
		args = append(args, "--resume", im.sessionID)
	} else {
		args = append(args, "--session-id", im.sessionID)
	}
	if im.Model != "" {
		args = append(args, "--model", im.Model)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(prompt))
	return fmt.Sprintf(`export PATH="$HOME/.local/bin:$PATH"
command -v claude >/dev/null 2>&1 || exit %d
cd %s && echo %s | base64 -d | %s`,
		review.ExitClaudeMissing, sidecar.ShellEscape(im.Entry.RepoPath), encoded, sidecar.ShellJoin(args))
}

// streamEvent is the part of one stream-json line the implementer reads.
type streamEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Message   struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

// maxLineBytes caps one buffered stream-json line. A tool result echoing a
// large file can be long; a line past this is dropped rather than buffered.
const maxLineBytes = 1 << 20

// streamParser splits claude's stream-json output into lines as it arrives,
// which can be mid-line, and reports what the implementer does.
type streamParser struct {
	onActivity func(Activity)
	buf        []byte
	overflow   bool
	sessionID  string
	result     streamEvent
	sawResult  bool
}

func (s *streamParser) write(data []byte) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			s.appendPartial(data)
			return
		}
		s.appendPartial(data[:i])
		if !s.overflow {
			s.line(s.buf)
		}
		s.buf, s.overflow = s.buf[:0], false
		data = data[i+1:]
	}
}

func (s *streamParser) appendPartial(data []byte) {
	if s.overflow || len(s.buf)+len(data) > maxLineBytes {
		s.overflow = true
		return
	}
	s.buf = append(s.buf, data...)
}

// flush handles a last line that had no trailing newline.
func (s *streamParser) flush() {
	if len(s.buf) > 0 && !s.overflow {
		s.line(s.buf)
	}
	s.buf, s.overflow = nil, false
}

func (s *streamParser) line(b []byte) {
	var e streamEvent
	if json.Unmarshal(b, &e) != nil {
		return
	}
	if e.SessionID != "" {
		s.sessionID = e.SessionID
	}
	switch e.Type {
	case "result":
		s.result, s.sawResult = e, true
	case "assistant":
		if s.onActivity == nil {
			return
		}
		for _, c := range e.Message.Content {
			switch c.Type {
			case "text":
				if t := strings.TrimSpace(c.Text); t != "" {
					s.onActivity(Activity{Detail: t})
				}
			case "tool_use":
				s.onActivity(Activity{Tool: c.Name, Detail: toolDetail(c.Input)})
			}
		}
	}
}

// toolDetail picks the field of a tool's input that says what it acted on.
func toolDetail(input json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	for _, key := range []string{"file_path", "command", "pattern", "path", "description"} {
		if v, ok := in[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
