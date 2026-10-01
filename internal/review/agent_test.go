package review

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// localExec runs scripts with sh on this machine, as a sidecar would, with
// HOME pointed at home so a fake claude can be installed where the scripts
// look for it.
func localExec(home string) Execer {
	return func(ctx context.Context, _ *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, _ func(string)) (int, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Env = append(os.Environ(), "HOME="+home)
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		// The exec API streams one chunk at a time; serialise so the callback
		// sees the same.
		var mu sync.Mutex
		emit := func(stream string) writerFn {
			return func(b []byte) {
				mu.Lock()
				defer mu.Unlock()
				onOutput(stream, b)
			}
		}
		cmd.Stdout = emit(circleci.StreamStdout)
		cmd.Stderr = emit(circleci.StreamStderr)
		// A killed sh can leave a child holding the output pipes open.
		cmd.WaitDelay = 100 * time.Millisecond
		err := cmd.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 0, err
	}
}

type writerFn func([]byte)

func (w writerFn) Write(b []byte) (int, error) {
	w(append([]byte(nil), b...))
	return len(b), nil
}

// installFakeClaude puts a claude on the scripts' PATH that records its
// arguments, stdin and environment, then runs body.
func installFakeClaude(t *testing.T, home, body string) {
	t.Helper()
	bin := filepath.Join(home, ".local", "bin")
	assert.NilError(t, os.MkdirAll(bin, 0o755))
	script := "#!/bin/sh\nprintf '%s\\n--end--\\n' \"$*\" >> \"$HOME/args\"\ncat >> \"$HOME/stdin\"\necho \"IS_SANDBOX=$IS_SANDBOX\" >> \"$HOME/env\"\n" + body + "\n"
	assert.NilError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755))
}

const streamOK = `printf '%s\n' '{"type":"system","subtype":"init","session_id":"s-1"}' \
 '{"type":"assistant","message":{"content":[{"type":"text","text":"Adding the flag."},{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}' \
 '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}' \
 '{"type":"result","subtype":"success","is_error":false,"result":"Added --verbose.","total_cost_usd":0.42}'`

func runImplement(t *testing.T, claudeBody string, a Agent) (AgentResult, string, []Activity) {
	t.Helper()
	home := t.TempDir()
	installFakeClaude(t, home, claudeBody)
	var acts []Activity
	res := RunAgent(context.Background(), localExec(home), &sidecar.PoolEntry{ID: "impl", RepoPath: t.TempDir()}, a,
		Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "k"}, "",
		AgentHooks{OnActivity: func(act Activity) { acts = append(acts, act) }})
	return res, home, acts
}

func TestRunAgentStreamsActivityAndResult(t *testing.T) {
	res, home, acts := runImplement(t, streamOK, ImplementAgent("implement", "add a --verbose flag"))

	assert.NilError(t, res.Err)
	assert.Equal(t, res.Output, "Added --verbose.")
	assert.Equal(t, res.SessionID, "s-1")
	assert.DeepEqual(t, acts, []Activity{
		{Detail: "Adding the flag."},
		{Tool: "Edit", Detail: "main.go"},
		{Tool: "Bash", Detail: "go test ./..."},
	})

	stdin, err := os.ReadFile(filepath.Join(home, "stdin"))
	assert.NilError(t, err)
	assert.Equal(t, string(stdin), "add a --verbose flag")
	env, err := os.ReadFile(filepath.Join(home, "env"))
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(string(env)), "IS_SANDBOX=1")
}

func TestImplementAgentScript(t *testing.T) {
	t.Parallel()
	a := ImplementAgent("implement", "p")
	a.SessionID = "abc"
	script := a.script("/repo")
	assert.Assert(t, strings.Contains(script, "'--output-format' 'stream-json' '--verbose'"), script)
	assert.Assert(t, strings.Contains(script, "'--dangerously-skip-permissions'"), script)
	assert.Assert(t, strings.Contains(script, "Bash(git commit:*)"), "git commits are disallowed: %s", script)
	assert.Assert(t, !strings.Contains(script, "--allowedTools"), script)
	assert.Assert(t, strings.Contains(script, "'--session-id' 'abc'"), script)

	// A later turn continues the conversation rather than starting a new one
	// without the context of the work it is fixing.
	a.Resume = true
	script = a.script("/repo")
	assert.Assert(t, strings.Contains(script, "'--resume' 'abc'"), script)
	assert.Assert(t, !strings.Contains(script, "--session-id"), script)
}

func TestAgentScriptWithItsOwnTools(t *testing.T) {
	t.Parallel()
	script := Agent{Prompt: "p", AllowedTools: []string{"Read", "Edit", "Write"}}.script("/repo")
	assert.Assert(t, strings.Contains(script, "'--allowedTools' 'Read,Edit,Write'"), script)
	assert.Assert(t, !strings.Contains(script, "--dangerously-skip-permissions"), script)
}

func TestRunAgentStreamFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		is   error
	}{
		{
			name: "error result",
			body: `echo '{"type":"result","is_error":true,"result":"API overloaded"}'; exit 1`,
			want: "claude exited 1: API overloaded",
		},
		{
			name: "error result exiting zero",
			body: `echo '{"type":"result","is_error":true,"result":"max turns"}'`,
			want: "claude reported an error: max turns",
		},
		{
			name: "credential rejected",
			body: `echo '{"type":"result","is_error":true,"result":"Failed to authenticate. API Error: 401"}'; exit 1`,
			is:   ErrCredentialRejected,
		},
		{
			name: "no result",
			body: `echo '{"type":"system","session_id":"s"}'`,
			want: "without a result",
		},
		{
			name: "stderr only",
			body: `echo boom >&2; exit 2`,
			want: "claude exited 2: boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _, _ := runImplement(t, tc.body, ImplementAgent("implement", "p"))
			if tc.is != nil {
				assert.Assert(t, errors.Is(res.Err, tc.is), "got %v", res.Err)
				return
			}
			assert.ErrorContains(t, res.Err, tc.want)
		})
	}
}

// TestRunAgentKeepsTheSessionOfAFailedRun guards resuming after a failure: the
// session exists once claude has started it, so the next turn must continue it.
func TestRunAgentKeepsTheSessionOfAFailedRun(t *testing.T) {
	res, _, _ := runImplement(t, `echo '{"type":"system","session_id":"s-9"}'; exit 1`, ImplementAgent("implement", "p"))
	assert.Assert(t, res.Err != nil)
	assert.Equal(t, res.SessionID, "s-9")
}

func TestRunAgentReportsMissingClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", "/usr/bin:/bin")
	res := RunAgent(context.Background(), localExec(home), &sidecar.PoolEntry{RepoPath: t.TempDir()}, ImplementAgent("implement", "p"), Credential{EnvVar: "K", Value: "v"}, "", AgentHooks{})
	assert.Assert(t, errors.Is(res.Err, ErrClaudeMissing), "got %v", res.Err)
}

func TestRunAgentRefusesStreamWithSchema(t *testing.T) {
	t.Parallel()
	res := RunAgent(context.Background(), nil, &sidecar.PoolEntry{}, Agent{Stream: true, Schema: "{}"}, Credential{}, "", AgentHooks{})
	assert.ErrorContains(t, res.Err, "cannot both stream")
}

// TestStreamParserReassemblesSplitLines covers output arriving in chunks that
// cut lines anywhere, as the exec stream delivers it.
func TestStreamParserReassemblesSplitLines(t *testing.T) {
	t.Parallel()
	var acts []Activity
	p := &streamParser{onActivity: func(a Activity) { acts = append(acts, a) }}
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n" +
		`{"type":"result","result":"ok"}`
	for i := 0; i < len(stream); i += 7 {
		p.write([]byte(stream[i:min(i+7, len(stream))]))
	}
	p.flush()
	assert.DeepEqual(t, acts, []Activity{{Tool: "Read", Detail: "a.go"}})
	assert.Assert(t, p.sawResult)
	assert.Equal(t, p.result.Result, "ok")
}

func TestStreamParserDropsOversizedLines(t *testing.T) {
	t.Parallel()
	p := &streamParser{}
	p.write([]byte(`{"type":"user","x":"` + strings.Repeat("a", maxLineBytes) + `"}` + "\n"))
	p.write([]byte(`{"type":"result","result":"ok"}` + "\n"))
	assert.Assert(t, p.sawResult)
	assert.Assert(t, len(p.buf) < maxLineBytes)
}
