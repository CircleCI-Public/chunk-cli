package review

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// Agent is one run of Claude Code on a sidecar. Reviewing, fixing and
// implementing are all agents: they differ in what they are asked, which tools
// they may use and what shape their answer takes, not in how they are run.
type Agent struct {
	// Name labels the run in errors and progress.
	Name   string
	Prompt string
	// SystemPrompt is appended to Claude Code's own.
	SystemPrompt string
	// AllowedTools pre-approves tools. Empty, with SkipPermissions unset, means
	// ReviewTools: an agent edits only when it is given the tools to.
	AllowedTools []string
	// DisallowedTools are refused even when SkipPermissions allows everything.
	DisallowedTools []string
	// SkipPermissions lets the agent use every tool not disallowed, running
	// commands included. It is only for a sidecar, which is a disposable sandbox.
	SkipPermissions bool
	// Schema constrains the answer with --json-schema; Output is then claude's
	// JSON result. It cannot be combined with Stream.
	Schema string
	// Stream runs claude with stream-json, so what the agent does is reported as
	// it does it and the session it ran in is known. Output is then the agent's
	// final message.
	Stream  bool
	Model   string
	Timeout time.Duration // DefaultTimeout when zero
	// SessionID names the claude session the run starts or, with Resume,
	// continues. Empty means claude picks one.
	SessionID string
	Resume    bool
}

// AgentHooks are optional callbacks for one run.
type AgentHooks struct {
	// OnActivity reports each tool use and message of a Stream run.
	OnActivity func(Activity)
	// OnSubmitted receives the remote command ID before output is streamed.
	OnSubmitted func(commandID string)
}

// AgentResult is how one run went. Output is kept when the run fails partway.
type AgentResult struct {
	Output string
	// SessionID is the claude session a Stream run reported, which a later run
	// can resume. It is set even when the run failed after starting.
	SessionID string
	Duration  time.Duration
	// Err is why the run failed. ErrClaudeMissing and ErrCredentialRejected
	// would fail every run on the pool the same way.
	Err error
}

// Activity is one thing a streaming agent did, for display.
type Activity struct {
	// Tool is the tool used, or "" for text the agent wrote.
	Tool string
	// Detail is the tool's target (a file, a command) or the text.
	Detail string
}

// ReviewTools limits an agent to reading the repository. A review reports
// findings; it has no business editing the tree the next pass reviews.
var ReviewTools = []string{
	"Read", "Grep", "Glob",
	"Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git status:*)",
}

// implementSystemPrompt keeps an implementer's work where it is collected from:
// the working tree. A stash, reset or checkout would move it out of sight.
const implementSystemPrompt = `You are working in a disposable copy of the repository on a remote machine.
Make the requested change by editing files. You may run builds, tests and linters to check your work.
Leave your changes in the working tree: do not run git commit, git stash, git reset, git checkout, git switch or git rebase. Your changes are collected from the working tree and reviewed.
When you finish, reply with a short summary of what you changed.`

// implementDisallowed are the git commands that would move an implementer's
// work out of the working tree it is collected from.
var implementDisallowed = []string{
	"Bash(git commit:*)", "Bash(git stash:*)", "Bash(git reset:*)",
	"Bash(git checkout:*)", "Bash(git switch:*)", "Bash(git rebase:*)",
}

// DefaultImplementTimeout bounds one implementer run. It writes code and may run
// the tests several times, so it gets longer than a review.
const DefaultImplementTimeout = 30 * time.Minute

// ImplementAgent is an agent that writes code on its sidecar and may run any
// command there to check it, git commands that hide its work excepted.
func ImplementAgent(name, prompt string) Agent {
	return Agent{
		Name:            name,
		Prompt:          prompt,
		SystemPrompt:    implementSystemPrompt,
		DisallowedTools: implementDisallowed,
		SkipPermissions: true,
		Stream:          true,
		Timeout:         DefaultImplementTimeout,
	}
}

// RunAgent runs a on entry's sidecar and waits for it to finish.
func RunAgent(ctx context.Context, exec Execer, entry *sidecar.PoolEntry, a Agent, cred Credential, baseURL string, hooks AgentHooks) AgentResult {
	if a.Timeout <= 0 {
		a.Timeout = DefaultTimeout
	}
	if a.Schema != "" && a.Stream {
		return AgentResult{Err: errors.New("an agent cannot both stream and answer to a schema")}
	}
	ctx, cancel := context.WithTimeout(ctx, a.Timeout)
	defer cancel()

	start := time.Now()
	// Stdout is the answer. Stderr is kept only to explain a failure, so
	// claude's progress noise never lands in the answer.
	var stdout, stderr strings.Builder
	limit := maxOutputBytes
	if a.Schema != "" {
		// The JSON result carries the answer twice, as text and as
		// structured_output; a truncated result would not decode at all.
		limit = maxStructuredOutputBytes
	}
	cut := false
	stream := &streamParser{onActivity: hooks.OnActivity}
	onOutput := func(name string, data []byte) {
		switch {
		case name == circleci.StreamStderr:
			stderr.Write(data[:min(len(data), max(maxOutputBytes-stderr.Len(), 0))])
		case a.Stream:
			stream.write(data)
		default:
			room := max(limit-stdout.Len(), 0)
			if len(data) > room {
				cut = true
			}
			stdout.Write(data[:min(len(data), room)])
		}
	}

	env := Env(cred, baseURL)
	if a.SkipPermissions {
		// The sidecar is a sandbox, which is what lets claude skip permission
		// prompts there even when the image runs it as root.
		env["IS_SANDBOX"] = "1"
	}
	code, err := exec(ctx, entry, a.script(entry.RepoPath), env, onOutput, hooks.OnSubmitted)
	stream.flush()

	res := AgentResult{Output: strings.TrimSpace(stdout.String()), Duration: time.Since(start)}
	if a.Stream {
		res.Output = strings.TrimSpace(stream.result.Result)
		res.SessionID = stream.sessionID
	}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.Err = fmt.Errorf("timed out after %s", a.Timeout)
	case err != nil:
		res.Err = fmt.Errorf("exec: %w", err)
	case code == ExitClaudeMissing:
		res.Err = ErrClaudeMissing
	case code != 0 && CredentialRejected(res.Output, stderr.String()):
		res.Err = ErrCredentialRejected
	case code != 0 && a.Stream && strings.TrimSpace(stderr.String()) == "" && res.Output != "":
		res.Err = exitError(code, res.Output)
	case code != 0:
		res.Err = exitError(code, stderr.String())
	case a.Stream && stream.result.IsError:
		res.Err = fmt.Errorf("claude reported an error: %s", truncateRunes(res.Output, maxResultInError))
	case a.Stream && !stream.sawResult:
		res.Err = errors.New("claude ended without a result")
	case cut && a.Schema != "":
		res.Err = fmt.Errorf("claude's result is over %d bytes", limit)
	}
	return res
}

// script builds the shell script for one run. The prompt is piped in
// base64-encoded, so no quoting in it can reach the shell. Claude Code's native
// installer puts claude in ~/.local/bin, which a non-login sh does not have on
// PATH.
func (a Agent) script(repoPath string) string {
	format := "text"
	switch {
	case a.Stream:
		format = "stream-json"
	case a.Schema != "":
		format = "json"
	}
	args := []string{"claude", "-p", "--output-format", format}
	if a.Stream {
		args = append(args, "--verbose")
	}
	if a.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		tools := a.AllowedTools
		if len(tools) == 0 {
			tools = ReviewTools
		}
		args = append(args, "--allowedTools", strings.Join(tools, ","))
	}
	if len(a.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(a.DisallowedTools, ","))
	}
	if a.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", a.SystemPrompt)
	}
	if a.Schema != "" {
		args = append(args, "--json-schema", a.Schema)
	}
	switch {
	case a.SessionID != "" && a.Resume:
		args = append(args, "--resume", a.SessionID)
	case a.SessionID != "":
		args = append(args, "--session-id", a.SessionID)
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(a.Prompt))
	return fmt.Sprintf(`export PATH="$HOME/.local/bin:$PATH"
command -v claude >/dev/null 2>&1 || exit %d
cd %s && echo %s | base64 -d | %s`,
		ExitClaudeMissing, sidecar.ShellEscape(repoPath), encoded, sidecar.ShellJoin(args))
}

// streamEvent is the part of one stream-json line an agent run reads.
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
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// maxLineBytes caps one buffered stream-json line. A tool result echoing a
// large file can be long; a line past this is dropped rather than buffered.
const maxLineBytes = 1 << 20

// streamParser splits claude's stream-json output into lines as it arrives,
// which can be mid-line, and reports what the agent does.
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
