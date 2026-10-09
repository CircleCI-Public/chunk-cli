package claudecode

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

var entry = &sidecar.PoolEntry{ID: "sb-1", RepoPath: "/home/user/my repo"}

var promptRe = regexp.MustCompile(`echo (\S+) \| base64 -d`)

// promptOf decodes the prompt a script pipes to claude.
func promptOf(t *testing.T, script string) string {
	t.Helper()
	m := promptRe.FindStringSubmatch(script)
	assert.Assert(t, m != nil, "no prompt in script: %s", script)
	b, err := base64.StdEncoding.DecodeString(strings.Trim(m[1], "'"))
	assert.NilError(t, err)
	return string(b)
}

// fakeExec answers every run with the given streams and exit code, and records
// the script and environment it was given.
type fakeExec struct {
	stdout, stderr string
	code           int
	err            error

	script string
	env    map[string]string
}

func (f *fakeExec) exec(_ context.Context, _ *sidecar.PoolEntry, script string, env map[string]string, out circleci.OutputFn, onSubmitted func(string)) (int, error) {
	f.script, f.env = script, env
	if onSubmitted != nil {
		onSubmitted("cmd-1")
	}
	if f.stderr != "" {
		out(circleci.StreamStderr, []byte(f.stderr))
	}
	if f.stdout != "" {
		out(circleci.StreamStdout, []byte(f.stdout))
	}
	return f.code, f.err
}

var _ sidecar.Execer = (&fakeExec{}).exec

func TestRunAsksForTextWithoutASchema(t *testing.T) {
	f := &fakeExec{stdout: "  the answer\n"}

	turn, err := Run(context.Background(), f.exec, entry, `it's "quoted" $(rm -rf /)`, Options{
		Model: "claude-sonnet-5",
		Tools: []string{"Read", "Bash(git diff:*)"},
	})

	assert.NilError(t, err)
	assert.Equal(t, turn.Output, "the answer")
	assert.Assert(t, strings.Contains(f.script, "cd '/home/user/my repo'"), f.script)
	assert.Assert(t, strings.Contains(f.script, "'claude' '-p' '--output-format' 'text'"), f.script)
	assert.Assert(t, strings.Contains(f.script, "'--allowedTools' 'Read,Bash(git diff:*)'"), f.script)
	assert.Assert(t, strings.Contains(f.script, "'--model' 'claude-sonnet-5'"), f.script)
	assert.Assert(t, !strings.Contains(f.script, "--json-schema"), f.script)
	assert.Equal(t, promptOf(t, f.script), `it's "quoted" $(rm -rf /)`)
}

func TestRunWithASchemaAsksForJSON(t *testing.T) {
	f := &fakeExec{stdout: `{"type":"result"}`}
	schema := `{"type":"object"}`

	_, err := Run(context.Background(), f.exec, entry, "hi", Options{Schema: schema})

	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(f.script, "'claude' '-p' '--output-format' 'json'"), f.script)
	assert.Assert(t, strings.Contains(f.script, "'--json-schema' "+sidecar.ShellEscape(schema)), f.script)
	assert.Assert(t, !strings.Contains(f.script, "--allowedTools"), "no tools means no allowlist flag: %s", f.script)
	assert.Assert(t, !strings.Contains(f.script, "--model"), f.script)
}

func TestRunSendsOnlyTheCredential(t *testing.T) {
	f := &fakeExec{}
	cred := Credential{EnvVar: "CLAUDE_CODE_OAUTH_TOKEN", Value: "tok"}

	_, err := Run(context.Background(), f.exec, entry, "hi", Options{Credential: cred, BaseURL: "https://api.anthropic.com/"})

	assert.NilError(t, err)
	assert.DeepEqual(t, f.env, map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "tok"})
}

func TestRunForwardsACustomBaseURL(t *testing.T) {
	f := &fakeExec{}

	_, err := Run(context.Background(), f.exec, entry, "hi", Options{
		Credential: Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "sk"},
		BaseURL:    "https://llm-gateway.example",
	})

	assert.NilError(t, err)
	assert.Equal(t, f.env["ANTHROPIC_BASE_URL"], "https://llm-gateway.example")
}

func TestRunReportsTheSubmittedCommand(t *testing.T) {
	var got string

	_, err := Run(context.Background(), (&fakeExec{}).exec, entry, "hi", Options{
		OnSubmitted: func(id string) { got = id },
	})

	assert.NilError(t, err)
	assert.Equal(t, got, "cmd-1")
}

func TestRunExplainsHowItFailed(t *testing.T) {
	for name, tc := range map[string]struct {
		exec fakeExec
		is   error
		want string
	}{
		"missing claude":      {exec: fakeExec{code: ExitMissing}, is: ErrMissing},
		"rejected credential": {exec: fakeExec{code: 1, stdout: "Failed to authenticate. API Error: 401"}, is: ErrCredentialRejected},
		"exit with stderr":    {exec: fakeExec{code: 2, stderr: "rate limited\n"}, want: "claude exited 2: rate limited"},
		"exit without stderr": {exec: fakeExec{code: 2}, want: "claude exited 2"},
		"exec failure":        {exec: fakeExec{err: errors.New("boom")}, want: "exec: boom"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Run(context.Background(), tc.exec.exec, entry, "hi", Options{})
			if tc.is != nil {
				assert.Assert(t, errors.Is(err, tc.is), "got %v", err)
				return
			}
			assert.Error(t, err, tc.want)
		})
	}
}

func TestRunKeepsOutputWhenItFails(t *testing.T) {
	f := &fakeExec{code: 1, stdout: "partial\n"}

	turn, err := Run(context.Background(), f.exec, entry, "hi", Options{})

	assert.Assert(t, err != nil)
	assert.Equal(t, turn.Output, "partial")
}

func TestRunTimesOut(t *testing.T) {
	exec := func(ctx context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, _ circleci.OutputFn, _ func(string)) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}

	_, err := Run(context.Background(), exec, entry, "hi", Options{Timeout: 10 * time.Millisecond})

	assert.Error(t, err, "timed out after 10ms")
}

func TestRunTimesOutOnTheCallersDeadline(t *testing.T) {
	exec := func(ctx context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, _ circleci.OutputFn, _ func(string)) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := Run(ctx, exec, entry, "hi", Options{})

	assert.Assert(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
	assert.Error(t, err, "timed out: context deadline exceeded")
}

func TestRunStructuredResultOverTheCapFailsWithAClearError(t *testing.T) {
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, out circleci.OutputFn, _ func(string)) (int, error) {
		out(circleci.StreamStdout, []byte(`{"type":"result","result":"`))
		out(circleci.StreamStdout, []byte(strings.Repeat("x", maxStructuredOutputBytes)))
		return 0, nil
	}

	_, err := Run(context.Background(), exec, entry, "hi", Options{Schema: `{}`})

	// The whole message, not a substring: the cap has to be what is reported.
	// Truncated JSON parses as a syntax error too, and that error says nothing
	// about why the output stopped.
	assert.Error(t, err, fmt.Sprintf("claude's result is over %d bytes", maxStructuredOutputBytes))
}

func TestParseResult(t *testing.T) {
	res, err := ParseResult(`{"type":"result","subtype":"success","is_error":false,"result":"done","structured_output":{"a":1}}`)

	assert.NilError(t, err)
	assert.Equal(t, res.Subtype, "success")
	assert.Equal(t, res.Result, "done")
	assert.Equal(t, string(res.StructuredOutput), `{"a":1}`)
}

func TestParseResultReadsANullStructuredOutputAsNone(t *testing.T) {
	for name, out := range map[string]string{
		"null":    `{"type":"result","structured_output":null}`,
		"missing": `{"type":"result"}`,
	} {
		t.Run(name, func(t *testing.T) {
			res, err := ParseResult(out)
			assert.NilError(t, err)
			assert.Assert(t, res.StructuredOutput == nil)
		})
	}
}

func TestParseResultFailsOnProse(t *testing.T) {
	_, err := ParseResult("Just prose.")

	assert.ErrorContains(t, err, "read claude's result")
}

func TestRunWithOnActivityStreamsWhatClaudeDoes(t *testing.T) {
	result := `{"type":"result","subtype":"success","is_error":false,"result":"done","total_cost_usd":1.5,"structured_output":{"a":1}}`
	f := &fakeExec{stdout: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"AGENTS.md"}}]}}` + "\n" +
		result + "\n" + `{"type":"system","subtype":"session_state_changed"}` + "\n"}
	var acts []Activity

	turn, err := Run(context.Background(), f.exec, entry, "hi", Options{
		Schema:     `{"type":"object"}`,
		OnActivity: func(a Activity) { acts = append(acts, a) },
	})

	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(f.script, "'claude' '-p' '--output-format' 'stream-json' '--verbose'"), f.script)
	assert.Assert(t, strings.Contains(f.script, "'--json-schema'"), "the schema still applies: %s", f.script)
	assert.DeepEqual(t, acts, []Activity{{Tool: "Read", Detail: "AGENTS.md"}})
	assert.Equal(t, turn.Output, result, "the result event reads like --output-format json's result")
	assert.Equal(t, turn.CostUSD, 1.5)
	parsed, err := ParseResult(turn.Output)
	assert.NilError(t, err)
	assert.Equal(t, string(parsed.StructuredOutput), `{"a":1}`)
}

func TestRunWithOnActivityWithoutASchemaReturnsTheAnswer(t *testing.T) {
	f := &fakeExec{stdout: `{"type":"result","is_error":false,"result":"  the answer  "}` + "\n"}

	turn, err := Run(context.Background(), f.exec, entry, "hi", Options{OnActivity: func(Activity) {}})

	assert.NilError(t, err)
	assert.Equal(t, turn.Output, "the answer")
}

func TestRunWithOnActivityExplainsHowItFailed(t *testing.T) {
	for name, tc := range map[string]struct {
		exec fakeExec
		is   error
		want string
	}{
		"rejected credential": {
			exec: fakeExec{code: 1, stdout: `{"type":"result","is_error":true,"result":"Failed to authenticate. API Error: 401"}` + "\n"},
			is:   ErrCredentialRejected,
		},
		"no result":    {exec: fakeExec{stdout: `{"type":"system"}` + "\n"}, want: "claude ended without a result"},
		"error result": {exec: fakeExec{stdout: `{"type":"result","is_error":true,"result":"gave up"}` + "\n"}, want: "claude: gave up"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Run(context.Background(), tc.exec.exec, entry, "hi", Options{OnActivity: func(Activity) {}})
			if tc.is != nil {
				assert.Assert(t, errors.Is(err, tc.is), "got %v", err)
				return
			}
			assert.ErrorContains(t, err, tc.want)
		})
	}
}
