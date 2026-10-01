package review

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// safePool is a concurrency-safe pool for RunPass, which releases from
// goroutines.
type safePool struct {
	free chan *sidecar.PoolEntry
}

func newSafePool(ids ...string) *safePool {
	p := &safePool{free: make(chan *sidecar.PoolEntry, len(ids))}
	for _, id := range ids {
		p.free <- &sidecar.PoolEntry{ID: id, RepoPath: "/home/user/repo"}
	}
	return p
}

func (p *safePool) Acquire(ctx context.Context) (*sidecar.PoolEntry, error) {
	// A released entry and a cancelled context can both be ready; select
	// would pick either, so a cancelled context must win deterministically.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case e := <-p.free:
		return e, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *safePool) Release(e *sidecar.PoolEntry) { p.free <- e }

var promptRe = regexp.MustCompile(`echo (\S+) \| base64 -d`)

// promptOf decodes the prompt a claudeScript pipes to claude.
func promptOf(t *testing.T, script string) string {
	t.Helper()
	m := promptRe.FindStringSubmatch(script)
	assert.Assert(t, m != nil, "no prompt in script: %s", script)
	b, err := base64.StdEncoding.DecodeString(strings.Trim(m[1], "'"))
	assert.NilError(t, err)
	return string(b)
}

// claudeResult is claude's --output-format json result for a review that
// answered with summary and findings.
func claudeResult(t *testing.T, summary string, findings ...Finding) string {
	t.Helper()
	if findings == nil {
		findings = []Finding{}
	}
	out, err := json.Marshal(report{Summary: summary, Findings: findings})
	assert.NilError(t, err)
	env, err := json.Marshal(map[string]any{
		"type":              "result",
		"subtype":           "success",
		"is_error":          false,
		"result":            string(out),
		"structured_output": json.RawMessage(out),
	})
	assert.NilError(t, err)
	return string(env)
}

func TestRunPass(t *testing.T) {
	t.Parallel()
	pool := newSafePool("sb-1")
	finding := Finding{File: "main.go", Line: 12, Severity: SeverityHigh, Confidence: 90, Claim: "nil map write", FailureScenario: "empty config panics"}
	var gotEnv map[string]string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, script string, env map[string]string, out circleci.OutputFn) (int, error) {
		gotEnv = env
		if promptOf(t, script) == "fail please" {
			out(circleci.StreamStdout, []byte("partial"))
			out(circleci.StreamStderr, []byte("rate limited\n"))
			return 1, nil
		}
		out(circleci.StreamStderr, []byte("thinking...\n"))
		out(circleci.StreamStdout, []byte(claudeResult(t, "reviewed: "+promptOf(t, script), finding)+"\n"))
		return 0, nil
	}

	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec, []Prompt{
		{Name: "a", Body: "it's \"quoted\" $(rm -rf /)"},
		{Name: "b", Body: "fail please"},
	}, Options{Credential: Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "sk-test"}})
	assert.NilError(t, err)
	assert.Equal(t, len(gotEnv), 1)
	assert.Equal(t, gotEnv["ANTHROPIC_API_KEY"], "sk-test")
	assert.Equal(t, len(results), 2)

	assert.Equal(t, results[0].Prompt, "a")
	assert.Equal(t, results[0].SidecarID, "sb-1")
	assert.Equal(t, results[0].Summary, `reviewed: it's "quoted" $(rm -rf /)`)
	assert.DeepEqual(t, results[0].Findings, []Finding{finding})
	assert.Equal(t, results[0].Output, "", "a successful review keeps findings, not raw output")
	assert.Equal(t, results[0].Error, "")

	assert.Equal(t, results[1].Prompt, "b")
	assert.Equal(t, results[1].Output, "partial")
	assert.Equal(t, results[1].Error, "claude exited 1: rate limited")
}

func TestRunPassClaudeMissingStopsPass(t *testing.T) {
	t.Parallel()
	exec := func(context.Context, *sidecar.PoolEntry, string, map[string]string, circleci.OutputFn) (int, error) {
		return exitClaudeMissing, nil
	}

	pool := newSafePool("sb-1", "sb-2")
	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}, {Name: "c", Body: "z"}}, Options{})
	assert.Assert(t, errors.Is(err, ErrClaudeMissing), "got %v", err)
}

func TestRunPassShellNotFoundIsOneFailedReview(t *testing.T) {
	t.Parallel()
	exec := func(_ context.Context, _ *sidecar.PoolEntry, script string, _ map[string]string, out circleci.OutputFn) (int, error) {
		if promptOf(t, script) == "x" {
			return 127, nil
		}
		out(circleci.StreamStdout, []byte(claudeResult(t, "clean")))
		return 0, nil
	}

	pool := newSafePool("sb-1", "sb-2")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}}, Options{})
	assert.NilError(t, err)
	assert.Equal(t, len(results), 2)
	assert.Equal(t, results[0].Error, "claude exited 127")
	assert.Equal(t, results[1].Error, "")
}

func TestRunPassSendsOnlyTheGivenCredential(t *testing.T) {
	t.Parallel()
	var gotEnv map[string]string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, env map[string]string, out circleci.OutputFn) (int, error) {
		gotEnv = env
		out(circleci.StreamStdout, []byte(claudeResult(t, "clean")))
		return 0, nil
	}

	pool := newSafePool("sb-1")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}},
		Options{Credential: Credential{EnvVar: "CLAUDE_CODE_OAUTH_TOKEN", Value: "sk-ant-oat01-tok"}})
	assert.NilError(t, err)
	assert.Equal(t, results[0].Error, "")
	assert.Equal(t, len(gotEnv), 1)
	assert.Equal(t, gotEnv["CLAUDE_CODE_OAUTH_TOKEN"], "sk-ant-oat01-tok")
	_, hasKey := gotEnv["ANTHROPIC_API_KEY"]
	assert.Assert(t, !hasKey, "env: %v", gotEnv)
}

func TestRunPassCredentialRejectedStopsPass(t *testing.T) {
	t.Parallel()
	// The 401 lands on stdout, while stderr carries an unrelated warning.
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, out circleci.OutputFn) (int, error) {
		out(circleci.StreamStderr, []byte("workspace has not been trusted\n"))
		out(circleci.StreamStdout, []byte("Failed to authenticate. API Error: 401 OAuth access token is invalid.\n"))
		return 1, nil
	}

	pool := newSafePool("sb-1", "sb-2")
	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}, {Name: "c", Body: "z"}}, Options{})
	assert.Assert(t, errors.Is(err, ErrCredentialRejected), "got %v", err)
}

// A review that merely quotes a 401 is not an authentication failure, so the
// pass must not be cut short by it.
func TestRunPassSuccessfulReviewQuotingA401(t *testing.T) {
	t.Parallel()
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, out circleci.OutputFn) (int, error) {
		out(circleci.StreamStdout, []byte(claudeResult(t, "the handler failed to authenticate and returns 401")))
		return 0, nil
	}

	pool := newSafePool("sb-1")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}}, Options{})
	assert.NilError(t, err)
	assert.Equal(t, results[0].Error, "")
}

func TestRunPassClaudeMissingNoSpuriousFailures(t *testing.T) {
	t.Parallel()
	// One goroutine returns exitClaudeMissing immediately; the rest block until the
	// context is canceled. The context-canceled results must be attributed to
	// ErrClaudeMissing, not shown as generic "exec: context canceled" failures.
	var first atomic.Bool
	exec := func(ctx context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, _ circleci.OutputFn) (int, error) {
		if first.CompareAndSwap(false, true) {
			return exitClaudeMissing, nil
		}
		<-ctx.Done()
		return 0, ctx.Err()
	}

	pool := newSafePool("sb-1", "sb-2", "sb-3")
	var mu sync.Mutex
	var failedEvents []string
	record := func(e ProgressEvent) {
		if e.State == StateFailed {
			mu.Lock()
			failedEvents = append(failedEvents, e.Prompt+": "+e.Error)
			mu.Unlock()
		}
	}

	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}, {Name: "c", Body: "z"}},
		Options{ProgressFn: record})
	assert.Assert(t, errors.Is(err, ErrClaudeMissing), "got %v", err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, len(failedEvents), 0, "expected no StateFailed events when claude is missing, got: %v", failedEvents)
}

func TestRunPassTimeout(t *testing.T) {
	t.Parallel()
	exec := func(ctx context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, _ circleci.OutputFn) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}

	pool := newSafePool("sb-1")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "slow", Body: "x"}}, Options{Timeout: 10 * time.Millisecond})
	assert.NilError(t, err)
	assert.Equal(t, results[0].Error, "timed out after 10ms")
}

func TestRunPassAcquireFailureKeepsStartedResults(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, out circleci.OutputFn) (int, error) {
		out(circleci.StreamStdout, []byte(claudeResult(t, "clean")))
		return 0, nil
	}
	// One sidecar; the first review cancels the context, so the second
	// prompt can never acquire one.
	pool := newSafePool("sb-1")
	blocking := func(ctx context.Context, e *sidecar.PoolEntry, s string, env map[string]string, out circleci.OutputFn) (int, error) {
		cancel()
		return exec(ctx, e, s, env, out)
	}

	results, err := RunPass(ctx, pool.Acquire, pool.Release, blocking, []Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}}, Options{})
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
	assert.Equal(t, len(results), 1)
	assert.Equal(t, results[0].Prompt, "a")
	assert.Equal(t, results[0].Error, "")
}

func TestRunPassProgressFn(t *testing.T) {
	t.Parallel()
	pool := newSafePool("sb-1", "sb-2")
	exec := func(_ context.Context, _ *sidecar.PoolEntry, script string, _ map[string]string, out circleci.OutputFn) (int, error) {
		if promptOf(t, script) == "fail" {
			return 1, nil
		}
		out(circleci.StreamStdout, []byte(claudeResult(t, "ok")))
		return 0, nil
	}

	type ev struct {
		prompt string
		state  PromptState
	}
	var mu sync.Mutex
	var events []ev
	record := func(e ProgressEvent) {
		mu.Lock()
		events = append(events, ev{e.Prompt, e.State})
		mu.Unlock()
	}

	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "pass", Body: "x"}, {Name: "fail", Body: "fail"}},
		Options{ProgressFn: record})
	assert.NilError(t, err)

	mu.Lock()
	defer mu.Unlock()

	// Both prompts must appear as queued before any running event.
	queued := map[string]bool{}
	for _, e := range events {
		if e.state == StateQueued {
			queued[e.prompt] = true
		}
	}
	assert.Assert(t, queued["pass"] && queued["fail"], "want both prompts queued, got %v", events)

	// Each prompt must end in a terminal state.
	terminal := map[string]PromptState{}
	for _, e := range events {
		if e.state == StateDone || e.state == StateFailed {
			terminal[e.prompt] = e.state
		}
	}
	assert.Equal(t, terminal["pass"], StateDone)
	assert.Equal(t, terminal["fail"], StateFailed)
}

func TestClaudeScript(t *testing.T) {
	t.Parallel()
	script := claudeScript("/home/user/my repo", "hi", "claude-sonnet-5")
	assert.Assert(t, strings.Contains(script, "cd '/home/user/my repo'"), script)
	assert.Assert(t, strings.Contains(script, "'claude' '-p' '--output-format' 'json' '--json-schema' '{"), script)
	assert.Assert(t, strings.Contains(script, "'--model' 'claude-sonnet-5'"), script)
	assert.Assert(t, strings.Contains(script, "Bash(git diff:*)"), script)
	assert.Assert(t, !strings.Contains(script, "Edit"), script)
	assert.Equal(t, promptOf(t, script), "hi")
}

func TestRunPassForwardsACustomBaseURL(t *testing.T) {
	t.Parallel()
	var gotEnv map[string]string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, env map[string]string, out circleci.OutputFn) (int, error) {
		gotEnv = env
		out(circleci.StreamStdout, []byte(claudeResult(t, "clean")))
		return 0, nil
	}

	pool := newSafePool("sb-1")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}},
		Options{Credential: Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "sk-test"}, BaseURL: "https://llm-gateway.example"})
	assert.NilError(t, err)
	assert.Equal(t, results[0].Error, "")
	assert.Equal(t, gotEnv["ANTHROPIC_BASE_URL"], "https://llm-gateway.example")
}

func TestRunPassOmitsTheDefaultBaseURL(t *testing.T) {
	t.Parallel()
	var gotEnv map[string]string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, env map[string]string, out circleci.OutputFn) (int, error) {
		gotEnv = env
		out(circleci.StreamStdout, []byte(claudeResult(t, "clean")))
		return 0, nil
	}

	pool := newSafePool("sb-1")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}},
		Options{Credential: Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "sk-test"}, BaseURL: "https://api.anthropic.com/"})
	assert.NilError(t, err)
	assert.Equal(t, results[0].Error, "")
	assert.Equal(t, len(gotEnv), 1)
}

// runOneWith runs a single review whose claude run prints stdout and exits
// with code.
func runOneWith(t *testing.T, stdout string, code int) Result {
	t.Helper()
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, out circleci.OutputFn) (int, error) {
		out(circleci.StreamStdout, []byte(stdout))
		return code, nil
	}
	pool := newSafePool("sb-1")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec, []Prompt{{Name: "a", Body: "x"}}, Options{})
	assert.NilError(t, err)
	assert.Equal(t, len(results), 1)
	return results[0]
}

func TestRunPassNoFindings(t *testing.T) {
	t.Parallel()
	r := runOneWith(t, claudeResult(t, "No issues found."), 0)
	assert.Equal(t, r.Error, "")
	assert.Equal(t, r.Summary, "No issues found.")
	assert.Assert(t, r.Findings != nil, "a clean review has empty findings, not nil")
	assert.Equal(t, len(r.Findings), 0)
}

// Claude can finish with exit 0 and still report an error, such as running
// out of turns before answering.
func TestRunPassClaudeReportedError(t *testing.T) {
	t.Parallel()
	r := runOneWith(t, `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"reached max turns"}`, 0)
	assert.Equal(t, r.Error, "claude reported error_max_turns: reached max turns")
	assert.Assert(t, r.Findings == nil)
}

func TestRunPassNoStructuredOutput(t *testing.T) {
	t.Parallel()
	stdout := `{"type":"result","subtype":"success","is_error":false,"result":"Looks fine to me."}`
	r := runOneWith(t, stdout, 0)
	assert.Equal(t, r.Error, errNoStructuredOutput.Error())
	assert.Equal(t, r.Output, stdout, "raw output is kept to diagnose the failure")
}

// Claude can emit a null structured_output rather than omitting it.
func TestRunPassNullStructuredOutput(t *testing.T) {
	t.Parallel()
	r := runOneWith(t, `{"type":"result","subtype":"success","is_error":false,"structured_output":null}`, 0)
	assert.Equal(t, r.Error, errNoStructuredOutput.Error())
}

func TestRunPassStructuredOutputOfTheWrongShape(t *testing.T) {
	t.Parallel()
	stdout := `{"type":"result","subtype":"success","is_error":false,"structured_output":{"findings":"none"}}`
	r := runOneWith(t, stdout, 0)
	assert.Assert(t, strings.HasPrefix(r.Error, "decode review: "), r.Error)
	assert.Equal(t, r.Output, stdout)
}

// Output from a claude too old for JSON output is not a result.
func TestRunPassUnparseableOutput(t *testing.T) {
	t.Parallel()
	r := runOneWith(t, "plain text review", 0)
	assert.Assert(t, strings.HasPrefix(r.Error, "decode claude result: "), r.Error)
	assert.Equal(t, r.Output, "plain text review")
}

// Claude's JSON output reports API errors in the result on stdout, leaving
// stderr empty.
func TestRunPassExitErrorFromResult(t *testing.T) {
	t.Parallel()
	r := runOneWith(t, `{"type":"result","subtype":"success","is_error":true,"result":"API Error: 529 Overloaded"}`, 1)
	assert.Equal(t, r.Error, "claude exited 1: API Error: 529 Overloaded")
}

// Stderr can carry warnings unrelated to why claude failed; the error claude
// reports in its result is the one that explains it.
func TestRunPassExitErrorPrefersResultOverStderr(t *testing.T) {
	t.Parallel()
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, out circleci.OutputFn) (int, error) {
		out(circleci.StreamStderr, []byte("workspace has not been trusted\n"))
		out(circleci.StreamStdout, []byte(`{"type":"result","subtype":"success","is_error":true,"result":"API Error: 529 Overloaded"}`))
		return 1, nil
	}
	pool := newSafePool("sb-1")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec, []Prompt{{Name: "a", Body: "x"}}, Options{})
	assert.NilError(t, err)
	assert.Equal(t, results[0].Error, "claude exited 1: API Error: 529 Overloaded")
}

func TestRunPassClaudeReportedErrorWithoutMessage(t *testing.T) {
	t.Parallel()
	r := runOneWith(t, `{"type":"result","subtype":"error_during_execution","is_error":true}`, 0)
	assert.Equal(t, r.Error, "claude reported error_during_execution")
}

// Claude can report a rejected credential in a result that exits 0. It must
// still stop the pass, and "success" is not a reason worth printing.
func TestRunPassCredentialRejectedWithExitZero(t *testing.T) {
	t.Parallel()
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, out circleci.OutputFn) (int, error) {
		out(circleci.StreamStdout, []byte(`{"type":"result","subtype":"success","is_error":true,"result":"Failed to authenticate. API Error: 401 invalid x-api-key"}`))
		return 0, nil
	}
	pool := newSafePool("sb-1", "sb-2")
	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}, {Name: "c", Body: "z"}}, Options{})
	assert.Assert(t, errors.Is(err, ErrCredentialRejected), "got %v", err)
}

func TestRunPassClaudeReportedErrorWithSuccessSubtype(t *testing.T) {
	t.Parallel()
	r := runOneWith(t, `{"type":"result","subtype":"success","is_error":true,"result":"API Error: 529 Overloaded"}`, 0)
	assert.Equal(t, r.Error, "claude reported an error: API Error: 529 Overloaded")
}

func TestRunPassRejectsFindingsOutsideTheSchema(t *testing.T) {
	t.Parallel()
	good := Finding{File: "a.go", Line: 1, Severity: SeverityHigh, Confidence: 80, Claim: "c", FailureScenario: "f"}
	for name, mutate := range map[string]func(*Finding){
		"severity":        func(f *Finding) { f.Severity = "urgent" },
		"high confidence": func(f *Finding) { f.Confidence = 150 },
		"low confidence":  func(f *Finding) { f.Confidence = -1 },
		"line":            func(f *Finding) { f.Line = -3 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bad := good
			mutate(&bad)
			r := runOneWith(t, claudeResult(t, "s", good, bad), 0)
			assert.Assert(t, strings.HasPrefix(r.Error, "finding 2: "), r.Error)
			assert.Assert(t, r.Findings == nil)
		})
	}
}

func TestRunPassOutputTooLarge(t *testing.T) {
	t.Parallel()
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, out circleci.OutputFn) (int, error) {
		chunk := []byte(strings.Repeat("x", maxOutputBytes/2+1))
		out(circleci.StreamStdout, chunk)
		out(circleci.StreamStdout, chunk)
		return 0, nil
	}
	pool := newSafePool("sb-1")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec, []Prompt{{Name: "a", Body: "x"}}, Options{})
	assert.NilError(t, err)
	assert.Equal(t, results[0].Error, "output exceeded 262144 bytes")
	assert.Equal(t, len(results[0].Output), maxOutputBytes)
}

// Output exactly at the cap is complete, not cut off.
func TestRunPassOutputAtTheCap(t *testing.T) {
	t.Parallel()
	result := claudeResult(t, "clean")
	r := runOneWith(t, result+strings.Repeat(" ", maxOutputBytes-len(result)), 0)
	assert.Equal(t, r.Error, "")
}

// The schema is what reviewers answer with and Finding is what it decodes
// into, so a field added to one and not the other would be silently dropped.
func TestFindingsSchemaMatchesFinding(t *testing.T) {
	t.Parallel()
	var schema struct {
		Properties struct {
			Findings struct {
				Items struct {
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				} `json:"items"`
			} `json:"findings"`
		} `json:"properties"`
	}
	assert.NilError(t, json.Unmarshal([]byte(findingsSchema), &schema))
	items := schema.Properties.Findings.Items

	b, err := json.Marshal(Finding{})
	assert.NilError(t, err)
	var fields map[string]any
	assert.NilError(t, json.Unmarshal(b, &fields))

	assert.Equal(t, len(items.Properties), len(fields))
	for name := range fields {
		_, ok := items.Properties[name]
		assert.Assert(t, ok, "Finding field %q is not in the schema", name)
	}
	required := map[string]bool{}
	for _, name := range items.Required {
		required[name] = true
	}
	for name := range fields {
		assert.Assert(t, required[name], "Finding field %q is not required by the schema", name)
	}
	assert.Equal(t, len(items.Required), len(fields))
}
